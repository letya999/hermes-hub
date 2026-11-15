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
	shared, err := sharedScope(args)
	if err != nil {
		return nil, err
	}
	if shared {
		if err := c.Store.RequireControlOperation(auth, "install_shared"); err != nil {
			return nil, err
		}
	} else if err := c.Store.RequireSelfInstall(auth); err != nil {
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
	if shared {
		draft.Workload.Class = api.Shared
		draft.Workload.Sharable = true
		draft.Workload.Rationale = "operator-shared remote MCP endpoint; catalog-visible, members bind one policy-owned connection and credential"
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
	onboardingID := api.DeterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion, seed)
	if holder, held := c.Store.OnboardingSlot(auth, draft.DefinitionID); held && !shared {
		if holder.CleanupIncomplete() {
			return nil, api.ErrRevoked
		}
		if holder.PreparingStub {
			return c.statusBody(holder, false), nil
		}
		if holder.Stage != api.StageFailed {
			if err := c.refreshCredentialForm(ctx, auth, &holder); err != nil {
				return nil, err
			}
			if err := c.refreshConfirmation(ctx, auth, &holder); err != nil {
				return nil, err
			}
			return c.statusBody(holder, false), nil
		}
		if holder.OnboardingID != onboardingID {
			// A failed holder under a different seed: retire it so the retry
			// minted here is the one record answering the slot.
			if err := c.Store.SupersedeOnboarding(holder, onboardingID); err != nil {
				return nil, err
			}
		}
	}
	if len(credentials) > 0 {
		draft.Tools = api.RemotePlaceholders(allowlist)
		onboarding := api.Onboarding{
			Schema: api.SchemaVersion, OnboardingID: onboardingID,
			PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Initiator: &auth,
			Mode: api.OnboardingRemote, Stage: api.StagePending, SourceURL: draft.Source.URL,
			DefinitionID: draft.DefinitionID, DefinitionVersion: draft.Version,
			Required: api.RemoteCredentialHints(credentials), AdmissionPending: true,
			Error:          "The remote endpoint needs credentials before tools/list. Submit them through the protected form; it is checked against the endpoint and the tool list is recorded.",
			IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
			Definition: &draft,
		}
		if previous, err := c.Store.onboarding(onboardingID); err == nil {
			onboarding.Revision = previous.Revision + 1
		}
		onboarding.ArmGate(api.GateCredentials, api.RandomNonce(), c.now().Add(c.ttl()))
		c.armWizard(&onboarding)
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return nil, err
		}
		return c.statusBody(onboarding, false), nil
	}
	probe, err := api.ProbeRemoteMCP(ctx, draft, nil)
	if err != nil {
		if probe.AuthNeeded {
			body, advertised, oauthErr := c.prepareRemoteOAuth(auth, draft, onboardingID, requestKey, allowlist, probe.Scopes)
			if advertised {
				return body, oauthErr
			}
			return nil, fmt.Errorf("%w: remote endpoint requires credentials; resubmit prepare_source with a credentials list", api.ErrUnauthorized)
		}
		return nil, err
	}
	definition, err := api.RemoteMCPDefinition(endpoint, name, nil, probe.Tools, allowlist)
	if err != nil {
		return nil, err
	}
	if shared {
		definition.Workload.Class = api.Shared
		definition.Workload.Sharable = true
		definition.Workload.Rationale = draft.Workload.Rationale
	}
	if err := c.registerAdmittedDefinition(&definition, auth.PrincipalID); err != nil {
		return nil, err
	}
	if shared {
		if err := c.Store.PromoteToCatalog(definition.DefinitionID, definition.Version, auth.PrincipalID); err != nil {
			return nil, err
		}
	}
	onboarding, attached, err := c.newOnboarding(auth, api.OnboardingRemote, requestKey, definition, definition.Source.URL, "", shared)
	if err != nil {
		return nil, err
	}
	if attached {
		return c.statusBody(onboarding, false), nil
	}
	// The fresh record keeps the id newOnboarding minted — the definition
	// slot, not the id seed, is what a repeat call converges on.
	copyDef := definition
	onboarding.Definition = &copyDef
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	// A credential-free endpoint asked for nothing else — the install
	// request is the consent, so the armed setup gate completes inline.
	if err := c.autoCompleteSetup(ctx, auth, &onboarding, "install_request"); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

// sharedScope admits "owner" (default) or "shared" — the same contract the
// cli spec uses. Shared connectors are catalog-visible and bind every member
// to the policy-owned connection and credential.
func sharedScope(args map[string]any) (bool, error) {
	switch api.ArgString(args, "scope") {
	case "", "owner":
		return false, nil
	case "shared":
		return true, nil
	default:
		return false, fmt.Errorf("%w: scope must be owner or shared", api.ErrInvalid)
	}
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
