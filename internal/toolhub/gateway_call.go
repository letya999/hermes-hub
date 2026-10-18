package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log/slog"
	"time"
)

func (g *Gateway) call(ctx context.Context, auth identity.Envelope, projectedName string, arguments map[string]any) (*mcp.CallToolResult, error) {
	corr := api.CallCorrelationFrom(ctx)
	if corr.ToolCallID == "" {
		corr.ToolCallID = api.NewToolCallID()
	}
	ctx = api.WithCallCorrelation(ctx, corr)
	var out *mcp.CallToolResult
	err := g.Store.AuthorizeProjectedCall(auth, projectedName, arguments, g.admitGoverned(auth, func(projected api.ProjectedTool, effective api.EffectiveBinding) error {
		if auth.CapabilityProfile != "" && g.AuditWrite == nil {
			return fmt.Errorf("%w: managed dispatch requires durable audit", api.ErrUnauthorized)
		}
		if err := api.RejectToolAuthorityArguments(projected.Tool, arguments); err != nil {
			return err
		}
		if len(projected.Tool.InputSchema) > 0 {
			var schema jsonschema.Schema
			if json.Unmarshal(projected.Tool.InputSchema, &schema) != nil {
				return api.ErrInvalid
			}
			resolved, err := schema.Resolve(nil)
			if err != nil || resolved.Validate(arguments) != nil {
				return fmt.Errorf("%w: MCP arguments do not match admitted schema", api.ErrInvalid)
			}
		}
		if err := g.audit("admit", mergeAudit(auditFields(auth, projected, effective, "admit"), corr)); err != nil {
			return err
		}
		injection := api.CredentialInjection{Environment: map[string]string{}}
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
		effective.CredentialMounts = append([]api.Mount(nil), injection.Mounts...)
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(effective.Definition.Execution.TimeoutSeconds)*time.Second)
		defer cancel()
		var result api.BackendResult
		var callErr error
		caller, envOK := g.Backend.(api.EnvBackend)
		if envOK {
			result, callErr = caller.CallEnv(callCtx, effective, projected.Tool, arguments, injection.Environment)
		} else {
			result, callErr = g.Backend.Call(callCtx, effective, projected.Tool, arguments)
		}
		// An OAuth-managed remote connection whose token was just rejected gets
		// one refresh-and-retry: a renewed bearer re-injects from the locator.
		// insufficient_scope is a granted-set shortfall — only re-consent fixes
		// it, so the refresh path is skipped.
		if callErr != nil && envOK && !errors.Is(callErr, api.ErrReconsent) && g.OAuthRefresh != nil && effective.Connection != nil && effective.Connection.Metadata["oauth_provider"] != "" {
			if refreshed, refreshErr := g.OAuthRefresh(callCtx, effective); refreshErr == nil && refreshed && g.Injector != nil {
				if fresh, injErr := g.Injector(ctx, effective); injErr == nil {
					if fresh.Cleanup != nil {
						defer fresh.Cleanup()
					}
					result, callErr = caller.CallEnv(callCtx, effective, projected.Tool, arguments, fresh.Environment)
				}
			}
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
				return fmt.Errorf("%w: backend structured output", api.ErrInvalid)
			}
		}
		if len(result.Text) > effective.Definition.Execution.OutputBytes {
			return fmt.Errorf("%w: backend text output", api.ErrInvalid)
		}
		if len(result.Content) > 0 {
			encoded, err := json.Marshal(result.Content)
			if err != nil || len(encoded) > effective.Definition.Execution.OutputBytes {
				return fmt.Errorf("%w: backend content output", api.ErrInvalid)
			}
		}
		out = &mcp.CallToolResult{IsError: result.IsError, StructuredContent: result.Structured, Content: result.Content}
		if result.Text != "" && len(out.Content) == 0 {
			out.Content = []mcp.Content{&mcp.TextContent{Text: result.Text}}
		}
		fields := auditFields(auth, projected, effective, "allow")
		if receipt := api.SafeReceipt(result.Receipt); receipt != "" {
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

func auditFields(auth identity.Envelope, projected api.ProjectedTool, effective api.EffectiveBinding, outcome string) map[string]string {
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

func mergeAudit(fields map[string]string, corr api.CallCorrelation) map[string]string {
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
