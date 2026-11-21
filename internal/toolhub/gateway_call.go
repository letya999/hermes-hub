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

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
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
	activation, err := g.requireActivated(ctx, auth)
	if err != nil {
		return nil, err
	}
	corr := api.CallCorrelationFrom(ctx)
	if corr.ToolCallID == "" {
		corr.ToolCallID = api.NewToolCallID()
	}
	ctx = api.WithCallCorrelation(ctx, corr)
	// Run-scoped surfaces require a verifiable job/run pair; any non-empty
	// correlation anywhere is a claim that must prove itself against the
	// supervisor's durable record before it can charge or attribute a call.
	if corr.Scoped && (corr.JobID == "" || corr.HermesRunID == "") {
		return nil, fmt.Errorf("%w: run correlation required", api.ErrUnauthorized)
	}
	runScoped := corr.Scoped || corr.JobID != "" || corr.HermesRunID != "" || corr.RunToken != "" || corr.MemoryToken != "" || corr.Delegation != nil
	var runPolicy *contract.PolicyBundle
	if runScoped {
		record, policy, err := g.checkedRun(ctx, auth, corr)
		if err != nil {
			return nil, errors.Join(err, g.audit("deny", map[string]string{
				"principal_id": auth.PrincipalID, "context_id": auth.ContextID, "environment": auth.Environment,
				"reason": "run-authorization", "outcome": "deny",
			}))
		}
		if policy != nil && activation.revision != "" && policy.PassportRevision != activation.revision {
			return nil, api.ErrUnauthorized
		}
		runPolicy = policy
		// The assignment owner gate consults the live PDP outside the tool
		// registry's admission lock.
		if record.Assignment && (record.AssignmentID == "" || record.AssignmentPlanRevision == 0 ||
			record.UserID != auth.PrincipalID || g.authorizeAssignmentRun == nil ||
			g.authorizeAssignmentRun(auth, record.AssignmentID, record.AssignmentPlanRevision) != nil) {
			return nil, fmt.Errorf("%w: assignment run authority", api.ErrUnauthorized)
		}
	}
	if err := g.chargeRunCall(corr); err != nil {
		return nil, err
	}
	arguments = normalizeTranscriptionPath(projectedName, arguments)
	var out *mcp.CallToolResult
	admitted := false
	authorized := false
	execute := g.admitGoverned(auth, func(projected api.ProjectedTool, effective api.EffectiveBinding) (callbackErr error) {
		if activation.revision != "" && effective.CapabilityProfileRevision != activation.profileRevision {
			return fmt.Errorf("%w: passport activation mismatch", api.ErrUnauthorized)
		}
		if err := g.checkActivePassport(ctx, auth, activation.revision); err != nil {
			return err
		}
		authorized = true
		if runPolicy != nil {
			if err := authorizeToolPolicy(runPolicy, projected.Tool); err != nil {
				return err
			}
		}
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
		inputSchema := projected.Tool.InputSchema
		if len(inputSchema) == 0 && effective.Definition.Transport == api.AgentTools && len(projected.Tool.Arguments) > 0 {
			inputSchema, _ = json.Marshal(api.CliInputSchema(projected.Tool))
		}
		if len(inputSchema) > 0 {
			var schema jsonschema.Schema
			if json.Unmarshal(inputSchema, &schema) != nil {
				return api.ErrInvalid
			}
			resolved, err := schema.Resolve(nil)
			if err != nil || resolved.Validate(arguments) != nil {
				return fmt.Errorf("%w: MCP arguments do not match admitted schema; use tool_search with the exact tool name", api.ErrInvalid)
			}
		}
		fields := mergeAudit(auditFields(auth, projected, effective, "admit"), corr)
		if projected.Tool.Effect == api.WriteEffect {
			// Write calls carry a stable logical correlation derived from the
			// admitted arguments: a retry after a lost response replays the
			// same operation and the audit fence holds it, while a changed
			// argument list is a different operation. ToolCallID stays the
			// unique per-attempt identity.
			canonical, err := json.Marshal(arguments)
			if err != nil {
				return fmt.Errorf("%w: canonical arguments", api.ErrInvalid)
			}
			fields["correlation_id"] = writeCorrelationID(auth, corr, effective, projected, canonical)
		}
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
		if strings.HasPrefix(projected.Tool.Name, "goal_") {
			conversationID, err := g.liveRunConversation(callCtx, auth, corr)
			if err != nil {
				outcome = "preflight-error"
				return err
			}
			callCtx = api.WithAgentConversation(callCtx, conversationID)
		}
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
		} else if runScoped {
			if _, err := g.lookupLiveRun(callCtx, auth, corr); err != nil {
				outcome = "preflight-error"
				return err
			}
		}
		if err := g.checkActivePassport(ctx, auth, activation.revision); err != nil {
			outcome = "preflight-error"
			return err
		}
		effectStarted = true
		outcome = "backend-error"
		caller, envOK := g.Backend.(api.EnvBackend)
		if effective.Definition.Transport == api.AgentTools && api.RemoteExecutor(projected.Tool.Executor) {
			result, callErr = g.execRemote(callCtx, auth, effective, projected.Tool, arguments)
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
				if runScoped {
					if _, err := g.lookupLiveRun(callCtx, auth, corr); err != nil {
						outcome = "preflight-error"
						return err
					}
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
		if result.CleanupError != "" {
			outcome = "cleanup-error"
		}
		if receipt := api.SafeReceipt(result.Receipt); receipt != "" {
			fields["receipt"] = receipt
		}
		return nil
	})
	var afterAdmission func() error
	err = g.Store.AuthorizeProjectedCall(auth, projectedName, arguments, func(projected api.ProjectedTool, effective api.EffectiveBinding) error {
		if effective.Definition.DefinitionID == "hub-agent-tools" && projected.Tool.Executor == "runtime:toolhub" && projected.Tool.Name == "tool_call" {
			// The delegate performs a full admission for its target call.
			afterAdmission = func() error { return execute(projected, effective) }
			return nil
		}
		if projected.Tool.Executor == "runtime:connectors" {
			// The connector facade reads this registry; do not call it under its admission lock.
			afterAdmission = func() error {
				authorized = true
				if err := g.Store.Reload(); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				if err := g.Store.ReverifyEffective(effective); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				return execute(projected, effective)
			}
			return nil
		}
		if g.Vaults != nil && effective.Definition.DefinitionID == "hub-agent-tools" && projected.Tool.Executor == "runtime:toolhub" &&
			(projected.Tool.Name == "memory_search" || projected.Tool.Name == "memory_read" || projected.Tool.Name == "memory_card") {
			// Waiting for dreaming must not pin shared registry authority.
			afterAdmission = func() error {
				authorized = true
				if err := g.requireMemoryAdmission(ctx, auth, corr); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				u, err := g.Vaults.Resolve(auth.PrincipalID, auth.Environment)
				if err != nil {
					if projected.Tool.Name == "memory_search" || projected.Tool.Name == "memory_read" {
						// Read-only tools report unavailable after admission; the handler cannot fall back to another vault.
						return g.Store.AuthorizeProjectedCall(auth, projectedName, arguments, execute)
					}
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				return u.Locked(func(root string) error {
					ctx = context.WithValue(ctx, memoryVaultKey{}, memoryVaultPin{gateway: g, auth: auth, root: root})
					return g.Store.AuthorizeProjectedCall(auth, projectedName, arguments, execute)
				})
			}
			return nil
		}
		if effective.Definition.DefinitionID == "hub-agent-tools" && effective.Definition.Transport == api.AgentTools &&
			projected.Tool.Name == "settings_edit" && projected.Tool.Executor == "handler:settings" {
			// The authenticated publisher rechecks edit authority and writes this registry.
			// Holding its admission fence through that write would deadlock both processes.
			afterAdmission = func() error { return execute(projected, effective) }
			return nil
		}
		if effective.Definition.DefinitionID == "hub-agent-tools" && effective.Definition.Transport == api.AgentTools &&
			(projected.Tool.Executor == "handler:assignments" || projected.Tool.Executor == "handler:routines" || projected.Tool.Executor == "handler:watches" || projected.Tool.Executor == "handler:goals" || projected.Tool.Executor == "handler:context") {
			// These handlers can call back through communication, supervisor or
			// model relay for rights; admission cannot span that callback.
			afterAdmission = func() error {
				authorized = true
				if g.acquireRuntime != nil {
					release, err := g.acquireRuntime(ctx, effective)
					if err != nil {
						return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
					}
					if release == nil {
						return fmt.Errorf("%w: %w", api.ErrBeforeEffect, api.ErrIsolation)
					}
					defer release()
				}
				if err := g.Store.Reload(); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				if err := g.Store.ReverifyEffective(effective); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				return execute(projected, effective)
			}
			return nil
		}
		if effective.Definition.DefinitionID == "hub-agent-tools" && projected.Tool.Executor == "runtime:channels" {
			// Speech delivery asks this ToolHub for send_voice; do not hold the
			// admission lock across that nested rights decision.
			afterAdmission = func() error {
				authorized = true
				if err := g.Store.Reload(); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				if err := g.Store.ReverifyEffective(effective); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				return execute(projected, effective)
			}
			return nil
		}
		if effective.Definition.DefinitionID == "hub-agent-tools" && api.RemoteExecutor(projected.Tool.Executor) {
			// Hub and service reads may re-enter this registry while checking rights.
			afterAdmission = func() error {
				authorized = true
				if err := g.Store.Reload(); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				if err := g.Store.ReverifyEffective(effective); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				return execute(projected, effective)
			}
			return nil
		}
		if effective.Definition.DefinitionID == "hub-agent-tools" && projected.Tool.Executor == "handler:terminal" {
			// A long command must not pin the registry read lock against a revoke.
			afterAdmission = func() error {
				authorized = true
				if projected.Tool.Name == "terminal_exec" && g.acquireRuntime != nil {
					release, err := g.acquireRuntime(ctx, effective)
					if err != nil || release == nil {
						return fmt.Errorf("%w: terminal runtime unavailable", api.ErrBeforeEffect)
					}
					defer release()
				}
				if err := g.Store.ReverifyEffective(effective); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				return execute(projected, effective)
			}
			return nil
		}
		if g.acquireRuntime != nil && effective.Definition.Transport == api.AgentTools && !api.RemoteExecutor(projected.Tool.Executor) && projected.Tool.Executor != "handler:settings" {
			// Cold startup may call back into ToolHub or update registry state.
			// Recheck after startup without pinning the registry through a long tool call.
			afterAdmission = func() error {
				authorized = true
				release, err := g.acquireRuntime(ctx, effective)
				if err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				if release == nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, api.ErrIsolation)
				}
				defer release()
				if err := g.Store.Reload(); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				if err := g.Store.ReverifyEffective(effective); err != nil {
					return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
				}
				return execute(projected, effective)
			}
			return nil
		}
		// Every remaining executor runs the backend after admission returns:
		// the registry fence is never held across a provider call, so a slow
		// tool cannot starve revocations or other principals' admissions. A
		// stale decision between admission and dispatch fails closed on the
		// same recheck the special branches already use.
		afterAdmission = func() error {
			authorized = true
			if err := g.Store.Reload(); err != nil {
				return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
			}
			if err := g.Store.ReverifyEffective(effective); err != nil {
				return fmt.Errorf("%w: %w", api.ErrBeforeEffect, err)
			}
			return execute(projected, effective)
		}
		return nil
	})
	if err == nil && afterAdmission != nil {
		err = afterAdmission()
	}
	if err != nil && !admitted {
		fields := map[string]string{
			"principal_id": auth.PrincipalID, "context_id": auth.ContextID, "runtime_id": auth.RuntimeID,
			"policy_version": auth.PolicyVersion, "outcome": "deny",
		}
		// reason carries only the sentinel class — argument values and tool
		// internals never reach the audit record.
		switch {
		case errors.Is(err, api.ErrNotFound):
			fields["reason"] = "not-found"
		case errors.Is(err, api.ErrInvalid):
			fields["reason"] = "invalid"
		case errors.Is(err, api.ErrUnauthorized):
			fields["reason"] = "denied"
		default:
			fields["reason"] = "error"
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

func authorizeToolPolicy(policy *contract.PolicyBundle, tool api.ToolSpec) error {
	rules := policy.Identity.Restrictions
	if tool.Effect != api.ReadEffect && !rules.AllowsUnattendedWrite() {
		return api.ErrUnauthorized
	}
	if rules.Grants != nil && len(tool.Requires) == 0 {
		return api.ErrUnauthorized
	}
	for _, right := range tool.Requires {
		domain, operation, ok := strings.Cut(right, ".")
		if !ok || !policy.AllowsRight(domain, operation) {
			return api.ErrUnauthorized
		}
	}
	return nil
}

// The agent sees /workspace while ToolHub scopes every path to that root.
func normalizeTranscriptionPath(name string, arguments map[string]any) map[string]any {
	if name != "media_transcribe" {
		return arguments
	}
	value, ok := arguments["path"].(string)
	if !ok || !strings.HasPrefix(value, "/workspace/") {
		return arguments
	}
	relative := strings.TrimPrefix(value, "/workspace/")
	if !api.ValidCapabilityPath(relative) || relative == "" {
		return arguments
	}
	copy := maps.Clone(arguments)
	copy["path"] = relative
	return copy
}

// requireActivated binds a managed profile to the one immutable passport
// bundle visible through current. A prepared or mismatched generation denies
// before credential injection or backend dispatch.
type activationPin struct {
	revision        string
	profileRevision uint64
}

func (g *Gateway) requireActivated(ctx context.Context, auth identity.Envelope) (activationPin, error) {
	if g.Spawn == nil {
		return activationPin{}, nil
	}
	if auth.CapabilityProfile == "" {
		return activationPin{}, fmt.Errorf("%w: passport activation unavailable", api.ErrUnauthorized)
	}
	if err := g.Store.Reload(); err != nil {
		return activationPin{}, err
	}
	g.Store.mu.RLock()
	profile, _, err := g.Store.capabilityProfileLocked(auth)
	g.Store.mu.RUnlock()
	if err != nil || profile.PassportRevision == "" {
		return activationPin{}, fmt.Errorf("%w: passport activation unavailable", api.ErrUnauthorized)
	}
	return activationPin{revision: profile.PassportRevision, profileRevision: profile.Revision}, g.checkActivePassport(ctx, auth, profile.PassportRevision)
}

func (g *Gateway) checkActivePassport(ctx context.Context, auth identity.Envelope, revision string) error {
	if revision == "" {
		return nil
	}
	eff, err := effectiveFor(ctx, g.Spawn, auth.PrincipalID)
	if err != nil || eff.Header.Metadata.Revision != revision || eff.Header.Metadata.Space != auth.PrincipalID || eff.Header.Metadata.Environment != auth.Environment {
		return fmt.Errorf("%w: passport activation mismatch", api.ErrUnauthorized)
	}
	return nil
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
	for _, key := range []string{"principal_id", "context_id", "runtime_id", "policy_version", "sender_id", "team_id", "binding_id", "backend", "projection_rev", "outcome", "execution", "connection_id", "connection_revision", "credential_revision", "capability_id", "capability_profile", "environment", "generation", "job_id", "hermes_run_id", "tool_call_id", "capability_policy_revision", "capability_profile_revision", "implementation_digest", "admin", "target", "reason"} {
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
