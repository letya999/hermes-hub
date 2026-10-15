package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
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
	if err := api.ValidateRemoteMCPEndpoint(endpoint); err != nil {
		return nil, err
	}
	credentials, err := api.RemoteCredentialInputs(args)
	if err != nil {
		return nil, err
	}
	name := api.ArgString(args, "name")
	allowlist := api.ArgStrings(args, "tools")
	for _, tool := range allowlist {
		if !api.McpToolNamePattern.MatchString(tool) {
			return nil, fmt.Errorf("%w: invalid tool allowlist entry %q", api.ErrInvalid, tool)
		}
	}
	draft, err := api.RemoteDefinitionBase(endpoint, name, credentials)
	if err != nil {
		return nil, err
	}
	requestKey := api.ArgString(args, "request_key")
	seed := requestKey
	if seed == "" {
		credKey := make([]string, 0, len(credentials))
		for _, input := range credentials {
			credKey = append(credKey, input.Name+">"+input.Target)
		}
		seed = "remote-mcp:" + draft.DefinitionID + "@" + draft.Version + ":" + draft.Source.URL + ":" + strings.Join(credKey, ",") + ":" + strings.Join(allowlist, ",")
	}
	onboardingID := api.DeterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, seed)
	if existing, err := c.Store.onboarding(onboardingID); err == nil && existing.Phase != api.PhaseFailed && existing.Phase != api.PhaseRemoved {
		if err := c.refreshCredentialForm(&existing); err != nil {
			return nil, err
		}
		if err := c.refreshConfirmation(&existing); err != nil {
			return nil, err
		}
		return c.statusBody(existing, false), nil
	}
	if len(credentials) > 0 {
		draft.Tools = api.RemotePlaceholders(allowlist)
		onboarding := api.Onboarding{
			Schema: api.SchemaVersion, OnboardingID: onboardingID,
			PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
			Mode: api.OnboardingRemote, Phase: api.PhaseAwaitingCreds, SourceURL: draft.Source.URL,
			DefinitionID: draft.DefinitionID, DefinitionVersion: draft.Version,
			Required: api.RemoteCredentialHints(credentials), AdmissionPending: true,
			Error:     "The remote endpoint needs credentials before tools/list. Submit them through the protected form; it is checked against the endpoint and the tool list is recorded.",
			FormNonce: api.RandomNonce(), FormExpires: c.now().Add(c.ttl()),
			IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
			Definition: &draft,
		}
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return nil, err
		}
		return c.statusBody(onboarding, false), nil
	}
	probe, err := api.ProbeRemoteMCP(ctx, draft, nil)
	if err != nil {
		if probe.AuthNeeded {
			return nil, fmt.Errorf("%w: remote endpoint requires credentials; resubmit prepare_source with a credentials list", api.ErrUnauthorized)
		}
		return nil, err
	}
	definition, err := api.RemoteMCPDefinition(endpoint, name, nil, probe.Tools, allowlist)
	if err != nil {
		return nil, err
	}
	if err := c.registerAdmittedDefinition(&definition, auth.PrincipalID); err != nil {
		return nil, err
	}
	onboarding, err := c.newOnboarding(auth, api.OnboardingRemote, requestKey, definition, definition.Source.URL, "")
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
func admitRemoteDefinition(ctx context.Context, definition api.ToolDefinition, secrets map[string]string) (api.ToolDefinition, error) {
	if definition.Transport != api.RemoteMCP {
		return api.ToolDefinition{}, fmt.Errorf("%w: remote admission needs a remote-mcp draft", api.ErrInvalid)
	}
	allowlist := make([]string, 0, len(definition.Tools))
	for _, tool := range definition.Tools {
		allowlist = append(allowlist, tool.Name)
	}
	draft := definition
	draft.Tools = nil
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probe, err := api.ProbeRemoteMCP(probeCtx, draft, secrets)
	if err != nil {
		return api.ToolDefinition{}, err
	}
	admitted := definition
	admitted.Tools, err = api.SelectRemoteTools(probe.Tools, allowlist)
	if err != nil {
		return api.ToolDefinition{}, err
	}
	if len(admitted.Tools) == 0 {
		return api.ToolDefinition{}, fmt.Errorf("%w: remote MCP tool allowlist is empty", api.ErrInvalid)
	}
	return admitted, admitted.Validate()
}
