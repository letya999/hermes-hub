package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// prepareTelegramSend reviews an owner-local version upgrade, retaining the
// Broker credential. The normal confirm/enable path replaces the old grant;
// a failed probe never cuts the working read connection or its session.
func (c *ControlPlane) prepareTelegramSend(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	requested, valid := args["telegram_send"].(bool)
	if !valid || !requested || api.ArgString(args, "onboarding_id") == "" {
		return nil, fmt.Errorf("%w: telegram_send=true requires an enabled onboarding_id", api.ErrInvalid)
	}
	for _, key := range []string{"source", "candidate_id", "definition_id", "version", "request_key"} {
		if api.ArgString(args, key) != "" {
			return nil, fmt.Errorf("%w: telegram_send selects only onboarding_id", api.ErrInvalid)
		}
	}
	if err := c.Store.RequireSelfInstall(auth); err != nil {
		return nil, err
	}
	c.admissionMu.Lock()
	defer c.admissionMu.Unlock()
	original, err := c.Store.OnboardingFor(auth, api.ArgString(args, "onboarding_id"))
	if err != nil {
		return nil, err
	}
	key := "telegram-send:" + original.OnboardingID
	if existing, ok := c.Store.FindOnboardingByKey(auth, key); ok {
		if existing.Phase == api.PhaseRemoved || existing.Phase == api.PhaseFailed {
			return nil, api.ErrRevoked
		}
		if err := c.refreshConfirmation(&existing); err != nil {
			return nil, err
		}
		return c.statusBody(existing, false), nil
	}
	if original.Phase != api.PhaseEnabled || original.BrokerCredentialID == "" {
		return nil, fmt.Errorf("%w: enabled Broker Telegram connection required", api.ErrUnauthorized)
	}
	// This is a profile-authorized control operation, not a provider tool
	// dispatch. Resolve the owned binding as EnableReady does; public Resolve
	// deliberately refuses managed callers without a capability selection.
	c.Store.mu.RLock()
	effective, err := c.Store.resolveLocked(auth, original.BindingID)
	c.Store.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	definition := effective.Definition
	entry, matched, err := api.PreparedForSource(api.ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA, Subfolder: definition.Source.Subfolder})
	if err != nil || !matched || entry.ID != "telegram" || effective.Credential == nil || effective.Credential.Backend != "credential-broker" || effective.Credential.Locator != original.BrokerCredentialID {
		return nil, fmt.Errorf("%w: reviewed owner Telegram session required", api.ErrUnauthorized)
	}
	if definition.RuntimeEnvironment["TELEGRAM_EXPOSED_TOOLS"] == "read-only+send_message" {
		return c.statusBody(original, false), nil
	}
	if !c.selfInstallUsable(definition) || api.VerifyDefinitionToolContract(definition) != nil {
		return nil, fmt.Errorf("%w: reviewed read-only Telegram definition required", api.ErrUnauthorized)
	}
	definition.RuntimeEnvironment = api.CloneMap(definition.RuntimeEnvironment)
	definition.RuntimeEnvironment["TELEGRAM_EXPOSED_TOOLS"] = "read-only+send_message"
	probe := original
	probe.OnboardingID = api.DeterministicID("send-probe", original.OnboardingID)
	admitted, err := c.admitWithBrokerCredential(ctx, auth, probe, definition, original.BrokerCredentialID)
	if err != nil {
		return nil, err
	}
	if err := validateTelegramSendUpgrade(definition, admitted); err != nil {
		return nil, err
	}
	if err := c.registerAdmittedDefinition(&admitted, auth.PrincipalID); err != nil {
		return nil, err
	}
	onboarding, err := c.newOnboarding(auth, api.OnboardingSelfInstall, key, admitted, original.SourceURL, original.CommitSHA)
	if err != nil {
		return nil, err
	}
	onboarding.Definition = &admitted
	onboarding.BrokerCredentialID, onboarding.Locator = original.BrokerCredentialID, original.BrokerCredentialID
	onboarding.ReviewDigest = admitted.Source.ReviewDigest
	onboarding.Phase = api.PhaseAwaitingConfirm
	onboarding.FormNonce, onboarding.FormExpires = "", time.Time{}
	onboarding.ConfirmationNonce = api.RandomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func validateTelegramSendUpgrade(expected, admitted api.ToolDefinition) error {
	if err := api.VerifyDefinitionToolContract(admitted); err != nil {
		return err
	}
	reads := make(map[string]api.ToolSpec, len(expected.Tools))
	for _, tool := range expected.Tools {
		if tool.Effect != api.ReadEffect {
			return api.ErrUnauthorized
		}
		reads[tool.Name] = tool
	}
	send := false
	for _, tool := range admitted.Tools {
		if tool.Name == "send_message" && tool.Effect == api.WriteEffect && len(tool.InputSchema) > 0 && !send {
			send = true
		} else if previous, ok := reads[tool.Name]; ok && api.RecordsEqual(previous, tool) {
			delete(reads, tool.Name)
		} else {
			return fmt.Errorf("%w: Telegram upgrade may add only send_message", api.ErrUnauthorized)
		}
	}
	expected.Tools = admitted.Tools
	expected.Source.ReviewDigest = admitted.Source.ReviewDigest
	expected.Source.ToolContractDigest = admitted.Source.ToolContractDigest
	expected.Source.ToolContractSource = admitted.Source.ToolContractSource
	if !send || len(reads) != 0 || !api.DefinitionsEqual(expected, admitted) {
		return fmt.Errorf("%w: Telegram upgrade contract drift", api.ErrUnauthorized)
	}
	return nil
}
