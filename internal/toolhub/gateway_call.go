package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log/slog"
	"strings"
	"time"
	"unicode"
)

func (g *Gateway) call(ctx context.Context, auth identity.Envelope, projectedName string, arguments map[string]any) (*mcp.CallToolResult, error) {
	corr := callCorrelationFrom(ctx)
	if corr.ToolCallID == "" {
		corr.ToolCallID = newToolCallID()
	}
	ctx = withCallCorrelation(ctx, corr)
	var out *mcp.CallToolResult
	err := g.Store.AuthorizeProjectedCall(auth, projectedName, arguments, g.admitGoverned(auth, func(projected ProjectedTool, effective EffectiveBinding) error {
		if auth.CapabilityProfile != "" && g.AuditWrite == nil {
			return fmt.Errorf("%w: managed dispatch requires durable audit", ErrUnauthorized)
		}
		if err := rejectToolAuthorityArguments(projected.Tool, arguments); err != nil {
			return err
		}
		if len(projected.Tool.InputSchema) > 0 {
			var schema jsonschema.Schema
			if json.Unmarshal(projected.Tool.InputSchema, &schema) != nil {
				return ErrInvalid
			}
			resolved, err := schema.Resolve(nil)
			if err != nil || resolved.Validate(arguments) != nil {
				return fmt.Errorf("%w: MCP arguments do not match admitted schema", ErrInvalid)
			}
		}
		if err := g.audit("admit", mergeAudit(auditFields(auth, projected, effective, "admit"), corr)); err != nil {
			return err
		}
		injection := CredentialInjection{Environment: map[string]string{}}
		if g.Injector != nil {
			var injErr error
			injection, injErr = g.Injector(ctx, effective)
			if injErr != nil {
				return injErr
			}
			if injection.Cleanup != nil {
				defer injection.Cleanup()
			}
		}
		effective.CredentialMounts = append([]Mount(nil), injection.Mounts...)
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(effective.Definition.Execution.TimeoutSeconds)*time.Second)
		defer cancel()
		var result BackendResult
		var callErr error
		if caller, ok := g.Backend.(envBackend); ok {
			result, callErr = caller.CallEnv(callCtx, effective, projected.Tool, arguments, injection.Environment)
		} else {
			result, callErr = g.Backend.Call(callCtx, effective, projected.Tool, arguments)
		}
		if callErr != nil {
			_ = g.audit("deny", mergeAudit(auditFields(auth, projected, effective, "backend-error"), corr))
			return callErr
		}
		if injection.Checkpoint != nil && !result.IsError {
			if err := injection.Checkpoint(); err != nil {
				return err
			}
		}
		if result.Structured != nil {
			encoded, err := json.Marshal(result.Structured)
			if err != nil {
				return err
			}
			if len(encoded) > effective.Definition.Execution.OutputBytes {
				return fmt.Errorf("%w: backend structured output", ErrInvalid)
			}
		}
		if len(result.Text) > effective.Definition.Execution.OutputBytes {
			return fmt.Errorf("%w: backend text output", ErrInvalid)
		}
		if len(result.Content) > 0 {
			encoded, err := json.Marshal(result.Content)
			if err != nil || len(encoded) > effective.Definition.Execution.OutputBytes {
				return fmt.Errorf("%w: backend content output", ErrInvalid)
			}
		}
		out = &mcp.CallToolResult{IsError: result.IsError, StructuredContent: result.Structured, Content: result.Content}
		if result.Text != "" && len(out.Content) == 0 {
			out.Content = []mcp.Content{&mcp.TextContent{Text: result.Text}}
		}
		fields := auditFields(auth, projected, effective, "allow")
		if receipt := SafeReceipt(result.Receipt); receipt != "" {
			fields["receipt"] = receipt
		}
		return g.audit("allow", mergeAudit(fields, corr))
	}))
	if err != nil && out == nil {
		fields := map[string]string{
			"principal_id": auth.PrincipalID, "context_id": auth.ContextID, "runtime_id": auth.RuntimeID,
			"policy_version": auth.PolicyVersion, "outcome": "deny",
		}
		if auth.CapabilityProfile != "" {
			fields["capability_profile"], fields["environment"], fields["generation"] = auth.CapabilityProfile, auth.Environment, fmt.Sprint(auth.Generation)
		}
		_ = g.audit("deny", mergeAudit(fields, corr))
	}
	return out, err
}

// CallAuthorized is the protected host CLI path; it uses the identical
// authorization, injection, output and audit fences as the MCP handler.
func (g *Gateway) CallAuthorized(ctx context.Context, auth identity.Envelope, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	return g.call(ctx, auth, name, arguments)
}

func auditFields(auth identity.Envelope, projected ProjectedTool, effective EffectiveBinding, outcome string) map[string]string {
	fields := map[string]string{
		"principal_id":   auth.PrincipalID,
		"context_id":     auth.ContextID,
		"runtime_id":     auth.RuntimeID,
		"policy_version": auth.PolicyVersion,
		"binding_id":     projected.BindingID,
		"backend":        string(effective.Definition.Transport),
		"projection_rev": fmt.Sprint(effective.Binding.ProjectionRevision),
		"outcome":        outcome,
	}
	if effective.CapabilityID != "" {
		fields["capability_id"] = effective.CapabilityID
		fields["implementation_digest"] = effective.ImplementationDigest
		fields["capability_profile"] = auth.CapabilityProfile
		fields["capability_policy_revision"] = fmt.Sprint(effective.CapabilityPolicyRevision)
		fields["capability_profile_revision"] = fmt.Sprint(effective.CapabilityProfileRevision)
		fields["environment"] = auth.Environment
		fields["generation"] = fmt.Sprint(auth.Generation)
	}
	if effective.Connection != nil {
		fields["connection_id"] = effective.Connection.ConnectionID
		fields["connection_revision"] = fmt.Sprint(effective.Connection.Revision)
	}
	if effective.Credential != nil {
		fields["credential_revision"] = fmt.Sprint(effective.Credential.Revision)
	}
	return fields
}

// SafeReceipt bounds a provider mutation receipt before it reaches the audit
// ledger. An oversized or control-character receipt is dropped rather than
// truncated, so a provider cannot smuggle content into control-plane records.
func SafeReceipt(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ""
	}
	return value
}

func mergeAudit(fields map[string]string, corr callCorrelation) map[string]string {
	if fields == nil {
		fields = map[string]string{}
	}
	if corr.JobID != "" {
		fields["job_id"] = corr.JobID
	}
	if corr.HermesRunID != "" {
		fields["hermes_run_id"] = corr.HermesRunID
	}
	if corr.ToolCallID != "" {
		fields["tool_call_id"] = corr.ToolCallID
	}
	return fields
}

func (g *Gateway) audit(event string, fields map[string]string) error {
	var err error
	if g != nil && g.AuditWrite != nil {
		err = g.AuditWrite(event, fields)
	} else if g != nil && g.Audit != nil {
		g.Audit(event, fields)
	}
	attrs := make([]any, 0, len(fields)*2)
	for key, value := range fields {
		attrs = append(attrs, key, value)
	}
	slog.Info("toolhub "+event, attrs...)
	return err
}

// RejectAuthorityArguments denies model-supplied identity, binding or
// credential selectors. Authorization comes only from the authenticated
// runtime and the resolved binding.
func RejectAuthorityArguments(arguments map[string]any) error {
	for key := range arguments {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "principal_id", "context_id", "runtime_id", "policy_version", "binding_id", "tool_binding_id", "connection_id", "credential_ref", "credential_ref_id", "owner", "owner_id", "user", "user_id", "account_id", "locator", "credential_locator", "backend", "backend_url", "mcp_endpoint", "policy", "grant", "grant_id", "issued_by", "store_owner":
			return fmt.Errorf("%w: authority argument %q", ErrUnauthorized, key)
		}
		if credentialPattern.MatchString(key) {
			return fmt.Errorf("%w: credential value argument %q", ErrUnauthorized, key)
		}
	}
	return nil
}

// Provider resource owners are data only when explicitly declared by the
// admitted tool. Runtime identity and credential selectors remain reserved.
func rejectToolAuthorityArguments(tool ToolSpec, arguments map[string]any) error {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(tool.InputSchema, &schema)
	for key, value := range arguments {
		switch key {
		case "owner", "owner_id", "user", "user_id", "account_id":
			if _, declared := schema.Properties[key]; declared {
				continue
			}
		}
		if err := RejectAuthorityArguments(map[string]any{key: value}); err != nil {
			return err
		}
	}
	return nil
}
