package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"time"
)

// prepareRemote registers an owner-scoped remote MCP endpoint for a granted
// principal. The URL is validated as a real HTTPS MCP destination before any
// store write. A credential-free endpoint is probed immediately; one that
// declares credentials defers the probe to the protected form submit, so the
// definition is published only after the endpoint proves itself under the
// owner's own credential.
func (c *ControlPlane) prepareRemote(ctx context.Context, auth identity.Envelope, endpoint string, args map[string]any) (map[string]any, error) {
	if err := c.Store.RequireSelfInstall(auth); err != nil {
		return nil, err
	}
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		return nil, err
	}
	credentials, err := RemoteCredentialInputs(args)
	if err != nil {
		return nil, err
	}
	name := argString(args, "name")
	allowlist := argStrings(args, "tools")
	for _, tool := range allowlist {
		if !mcpToolNamePattern.MatchString(tool) {
			return nil, fmt.Errorf("%w: invalid tool allowlist entry %q", ErrInvalid, tool)
		}
	}
	draft, err := remoteDefinitionBase(endpoint, name, credentials)
	if err != nil {
		return nil, err
	}
	requestKey := argString(args, "request_key")
	seed := requestKey
	if seed == "" {
		credKey := make([]string, 0, len(credentials))
		for _, input := range credentials {
			credKey = append(credKey, input.Name+">"+input.Target)
		}
		seed = "remote-mcp:" + draft.DefinitionID + "@" + draft.Version + ":" + draft.Source.URL + ":" + strings.Join(credKey, ",") + ":" + strings.Join(allowlist, ",")
	}
	onboardingID := deterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, seed)
	if existing, err := c.Store.onboarding(onboardingID); err == nil && existing.Phase != PhaseFailed && existing.Phase != PhaseRemoved {
		if err := c.refreshCredentialForm(&existing); err != nil {
			return nil, err
		}
		if err := c.refreshConfirmation(&existing); err != nil {
			return nil, err
		}
		return c.statusBody(existing, false), nil
	}
	if len(credentials) > 0 {
		draft.Tools = remotePlaceholders(allowlist)
		onboarding := Onboarding{
			Schema: SchemaVersion, OnboardingID: onboardingID,
			PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
			Mode: OnboardingRemote, Phase: PhaseAwaitingCreds, SourceURL: draft.Source.URL,
			DefinitionID: draft.DefinitionID, DefinitionVersion: draft.Version,
			Required: remoteCredentialHints(credentials), AdmissionPending: true,
			Error:     "The remote endpoint needs credentials before tools/list. Submit them through the protected form; it is checked against the endpoint and the tool list is recorded.",
			FormNonce: randomNonce(), FormExpires: c.now().Add(c.ttl()),
			IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
			Definition: &draft,
		}
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return nil, err
		}
		return c.statusBody(onboarding, false), nil
	}
	probe, err := probeRemoteMCP(ctx, draft, nil)
	if err != nil {
		if probe.AuthNeeded {
			return nil, fmt.Errorf("%w: remote endpoint requires credentials; resubmit prepare_source with a credentials list", ErrUnauthorized)
		}
		return nil, err
	}
	definition, err := RemoteMCPDefinition(endpoint, name, nil, probe.Tools, allowlist)
	if err != nil {
		return nil, err
	}
	if err := c.registerAdmittedDefinition(&definition, auth.PrincipalID); err != nil {
		return nil, err
	}
	onboarding, err := c.newOnboarding(auth, OnboardingRemote, requestKey, definition, definition.Source.URL, "")
	if err != nil {
		return nil, err
	}
	if onboarding.OnboardingID != onboardingID {
		onboarding.OnboardingID = onboardingID
	}
	copyDef := definition
	onboarding.Definition = &copyDef
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

// admitRemoteDefinition re-probes a credentialed remote endpoint with the
// owner-submitted secrets mapped onto the declared HTTP headers. The
// definition's placeholder tools become the allowlist; the advertised set may
// narrow but never widen it.
func admitRemoteDefinition(ctx context.Context, definition ToolDefinition, secrets map[string]string) (ToolDefinition, error) {
	if definition.Transport != RemoteMCP {
		return ToolDefinition{}, fmt.Errorf("%w: remote admission needs a remote-mcp draft", ErrInvalid)
	}
	allowlist := make([]string, 0, len(definition.Tools))
	for _, tool := range definition.Tools {
		allowlist = append(allowlist, tool.Name)
	}
	draft := definition
	draft.Tools = nil
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probe, err := probeRemoteMCP(probeCtx, draft, secrets)
	if err != nil {
		return ToolDefinition{}, err
	}
	admitted := definition
	admitted.Tools, err = selectRemoteTools(probe.Tools, allowlist)
	if err != nil {
		return ToolDefinition{}, err
	}
	if len(admitted.Tools) == 0 {
		return ToolDefinition{}, fmt.Errorf("%w: remote MCP tool allowlist is empty", ErrInvalid)
	}
	return admitted, admitted.Validate()
}

// remoteReady proves the endpoint is still an MCP server and still advertises
// every bound tool before a binding goes active.
func (b MCPBackend) remoteReady(ctx context.Context, effective EffectiveBinding, environment map[string]string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probe, err := probeRemoteMCP(probeCtx, effective.Definition, environment)
	if err != nil {
		return fmt.Errorf("%w: remote MCP readiness: %v", ErrIsolation, sanitizeRemoteError(err))
	}
	advertised := map[string]bool{}
	for _, tool := range probe.Tools {
		advertised[tool.Name] = true
	}
	for _, tool := range effective.Definition.Tools {
		if !advertised[tool.Name] {
			return fmt.Errorf("%w: remote endpoint no longer serves %q", ErrStale, tool.Name)
		}
	}
	return nil
}

// callRemoteMCP projects one tools/call onto the owner's remote endpoint. The
// endpoint is re-validated at call time and credentials arrive only as the
// declared HTTP headers — no ToolHive admission, no credential files.
func (b MCPBackend) callRemoteMCP(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, environment map[string]string) (BackendResult, error) {
	endpoint := effective.Definition.Source.URL
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		return BackendResult{}, err
	}
	headers, err := CredentialHeaders(effective.Definition, environment)
	if err != nil {
		return BackendResult{}, err
	}
	session, err := connectRemoteMCP(ctx, endpoint, remoteHTTPClient(endpoint, headers))
	if err != nil {
		return BackendResult{}, fmt.Errorf("%w: remote MCP session: %v", ErrIsolation, sanitizeRemoteError(err))
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: arguments})
	closeErr := session.Close()
	if err != nil {
		return BackendResult{}, fmt.Errorf("%w: remote MCP call: %v", ErrIsolation, sanitizeRemoteError(err))
	}
	if closeErr != nil {
		return BackendResult{}, fmt.Errorf("%w: remote MCP close: %v", ErrIsolation, sanitizeRemoteError(closeErr))
	}
	var text strings.Builder
	for _, content := range result.Content {
		if value, ok := content.(*mcp.TextContent); ok {
			text.WriteString(value.Text)
		}
	}
	return BackendResult{Text: text.String(), Content: result.Content, Structured: result.StructuredContent, IsError: result.IsError}, nil
}
