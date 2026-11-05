package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log/slog"
	"maps"
	"os"
	"strconv"
	"strings"
	"time"
)

// chargeRunCall counts a call against its run (job) correlation;
// HUB_TOOL_CALLS_PER_RUN bounds per-run fan-out.
func (g *Gateway) chargeRunCall(corr api.CallCorrelation) error {
	if corr.JobID == "" {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runCap == 0 {
		g.runCap, _ = strconv.Atoi(os.Getenv("HUB_TOOL_CALLS_PER_RUN"))
		if g.runCap == 0 {
			g.runCap = 256
		}
	}
	if g.runCalls == nil {
		g.runCalls = map[string]int{}
	}
	if g.runCalls[corr.JobID] >= g.runCap {
		return fmt.Errorf("%w: run tool-call ceiling", api.ErrUnauthorized)
	}
	g.runCalls[corr.JobID]++
	return nil
}

func (g *Gateway) call(ctx context.Context, auth identity.Envelope, projectedName string, arguments map[string]any) (*mcp.CallToolResult, error) {
	corr := api.CallCorrelationFrom(ctx)
	if corr.ToolCallID == "" {
		corr.ToolCallID = api.NewToolCallID()
	}
	ctx = api.WithCallCorrelation(ctx, corr)
	if err := g.chargeRunCall(corr); err != nil {
		return nil, err
	}
	var out *mcp.CallToolResult
	admitted := false
	authorized := false
	err := g.Store.AuthorizeProjectedCall(auth, projectedName, arguments, g.admitGoverned(auth, func(projected api.ProjectedTool, effective api.EffectiveBinding) (callbackErr error) {
		authorized = true
		if strings.HasPrefix(projected.Tool.CapabilityID, "memory.") {
			if err := g.requireMemoryAdmission(ctx, auth, corr); err != nil {
				return err
			}
		}
		if g.AuditWrite == nil {
			return fmt.Errorf("%w: durable tool-call audit required", audit.ErrWrite)
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
		fields := mergeAudit(auditFields(auth, projected, effective, "admit"), corr)
		if err := g.audit("admit", fields); err != nil {
			return err
		}
		admitted = true
		effectStarted := false
		execution := "succeeded"
		outcome := "ok"
		defer func() {
			event := "allow"
			if callbackErr != nil {
				out = nil
				if effectStarted {
					event = "uncertain"
				} else {
					event = "not-started"
				}
			}
			fields["outcome"], fields["execution"] = outcome, execution
			auditErr := g.audit(event, fields)
			if event == "uncertain" || auditErr != nil {
				out = nil
				callbackErr = errors.Join(audit.ErrUncertain, auditErr)
			}
		}()
		injection := api.CredentialInjection{Environment: map[string]string{}}
		if g.Injector != nil {
			var injErr error
			injection, injErr = g.Injector(ctx, effective)
			if injErr != nil {
				outcome = "preflight-error"
				return injErr
			}
			if injection.Cleanup != nil {
				defer injection.Cleanup()
			}
		}
		effective.CredentialMounts = append([]api.Mount(nil), injection.Mounts...)
		effective.ProxyGrants = append([]api.ProxyGrant(nil), injection.Proxy...)
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(effective.Definition.Execution.TimeoutSeconds)*time.Second)
		defer cancel()
		var result api.BackendResult
		var callErr error
		if err := callCtx.Err(); err != nil {
			outcome = "cancelled"
			return err
		}
		if strings.HasPrefix(projected.Tool.CapabilityID, "memory.") {
			if err := g.requireMemoryAdmission(ctx, auth, corr); err != nil {
				outcome = "preflight-error"
				return err
			}
		}
		effectStarted = true
		outcome = "backend-error"
		caller, envOK := g.Backend.(api.EnvBackend)
		if api.RemoteExecutor(projected.Tool.Executor) {
			result, callErr = g.execRemote(callCtx, auth, projected.Tool, arguments)
		} else if envOK {
			result, callErr = caller.CallEnv(callCtx, effective, projected.Tool, arguments, injection.Environment)
		} else {
			result, callErr = g.Backend.Call(callCtx, effective, projected.Tool, arguments)
		}
		// Re-authentication may repeat setup only after the trusted remote backend
		// proved that it never dispatched tools/call. Post-dispatch failures never retry.
		if errors.Is(callErr, api.ErrBeforeEffect) {
			effectStarted = false
		}
		if errors.Is(callErr, api.ErrAuthBeforeEffect) && envOK && g.OAuthRefresh != nil && effective.Connection != nil && effective.Connection.Metadata["oauth_provider"] != "" {
			refreshed, refreshErr := g.OAuthRefresh(callCtx, effective)
			if refreshErr != nil {
				outcome = "preflight-error"
				return refreshErr
			}
			if refreshed && g.Injector != nil {
				fresh, injectErr := g.Injector(ctx, effective)
				if injectErr != nil {
					outcome = "preflight-error"
					return injectErr
				}
				if fresh.Cleanup != nil {
					defer fresh.Cleanup()
				}
				effectStarted = true
				result, callErr = caller.CallEnv(callCtx, effective, projected.Tool, arguments, fresh.Environment)
				if errors.Is(callErr, api.ErrBeforeEffect) {
					effectStarted = false
				}
			}
		}
		if callErr != nil {
			if !effectStarted {
				outcome = "preflight-error"
				return callErr
			}
			return audit.ErrUncertain
		}
		outcome = "result-error"
		if result.IsError {
			execution = "failed"
		}
		if injection.Checkpoint != nil && !result.IsError {
			if err := injection.Checkpoint(); err != nil {
				outcome = "checkpoint-error"
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
		// A backend's own interactive OAuth (desktop loopback like
		// http://localhost:8000/oauth2callback) is unreachable for the user
		// through any channel — managed sign-in runs through onboarding.
		for _, content := range result.Content {
			if tc, ok := content.(*mcp.TextContent); ok {
				tc.Text = scrubLoopbackOAuthURL(tc.Text)
			}
		}
		out = &mcp.CallToolResult{IsError: result.IsError, StructuredContent: result.Structured, Content: result.Content}
		if result.Text != "" && len(out.Content) == 0 {
			out.Content = []mcp.Content{&mcp.TextContent{Text: scrubLoopbackOAuthURL(result.Text)}}
		}
		outcome = "ok"
		if result.IsError {
			outcome = "backend-error"
		}
		if receipt := api.SafeReceipt(result.Receipt); receipt != "" {
			fields["receipt"] = receipt
		}
		return nil
	}))
	if err != nil && !admitted {
		fields := map[string]string{
			"principal_id": auth.PrincipalID, "context_id": auth.ContextID, "runtime_id": auth.RuntimeID,
			"policy_version": auth.PolicyVersion, "outcome": "deny",
		}
		if auth.CapabilityProfile != "" {
			fields["capability_profile"], fields["environment"], fields["generation"] = auth.CapabilityProfile, auth.Environment, fmt.Sprint(auth.Generation)
		}
		event := "deny"
		if authorized {
			event = "reject"
			fields["outcome"] = "preflight-error"
		}
		err = errors.Join(err, g.audit(event, mergeAudit(fields, corr)))
	}
	if errors.Is(err, audit.ErrUncertain) {
		return nil, &uncertainCallError{operationID: toolOperationID(auth.PrincipalID, auth.ContextID, auth.Environment, corr.JobID, corr.ToolCallID)}
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
		"sender_id":      auth.ExternalIdentityID,
		"context_id":     auth.ContextID,
		"runtime_id":     auth.RuntimeID,
		"policy_version": auth.PolicyVersion,
		"environment":    auth.Environment, "generation": fmt.Sprint(auth.Generation),
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
		if effective.Connection.Owner.Type == api.TeamOwner {
			fields["team_id"] = effective.Connection.Owner.ID
		}
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
	fields = maps.Clone(fields)
	if event == "admin.diagnostics" && (g == nil || g.AuditWrite == nil) {
		return errors.New("durable administrative audit required")
	}
	var err error
	if g != nil && g.AuditWrite != nil {
		err = g.AuditWrite(event, fields)
	}
	if g != nil && g.Audit != nil {
		g.Audit(event, fields)
	}
	attrs := make([]any, 0, 32)
	for _, key := range []string{"principal_id", "context_id", "runtime_id", "policy_version", "sender_id", "team_id", "binding_id", "backend", "projection_rev", "outcome", "execution", "connection_id", "connection_revision", "credential_revision", "capability_id", "capability_profile", "environment", "generation", "job_id", "hermes_run_id", "tool_call_id", "capability_policy_revision", "capability_profile_revision", "implementation_digest", "admin", "target"} {
		if value, ok := fields[key]; ok {
			attrs = append(attrs, key, value)
		}
	}
	if err != nil {
		attrs = append(attrs, "status", "audit-unavailable", "error_class", "uncertain")
	}
	slog.Info("toolhub "+event, attrs...)
	return err
}
