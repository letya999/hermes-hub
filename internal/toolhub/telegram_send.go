package toolhub

import (
	"context"
	"fmt"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// prepareTelegramSend reviews an owner-local version upgrade, retaining the
// Broker credential. The normal confirm/enable path replaces the old grant;
// a failed probe never cuts the working read connection or its session.
func (c *ControlPlane) prepareTelegramSend(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	requested, valid := args["telegram_send"].(bool)
	if !valid || !requested || argString(args, "onboarding_id") == "" {
		return nil, fmt.Errorf("%w: telegram_send=true requires an enabled onboarding_id", ErrInvalid)
	}
	for _, key := range []string{"source", "candidate_id", "definition_id", "version", "request_key"} {
		if argString(args, key) != "" {
			return nil, fmt.Errorf("%w: telegram_send selects only onboarding_id", ErrInvalid)
		}
	}
	if err := c.Store.RequireSelfInstall(auth); err != nil {
		return nil, err
	}
	c.admissionMu.Lock()
	defer c.admissionMu.Unlock()
	original, err := c.Store.OnboardingFor(auth, argString(args, "onboarding_id"))
	if err != nil {
		return nil, err
	}
	key := "telegram-send:" + original.OnboardingID
	if existing, ok := c.Store.FindOnboardingByKey(auth, key); ok {
		if existing.Phase == PhaseRemoved || existing.Phase == PhaseFailed {
			return nil, ErrRevoked
		}
		if err := c.refreshConfirmation(&existing); err != nil {
			return nil, err
		}
		return c.statusBody(existing, false), nil
	}
	if original.Phase != PhaseEnabled || original.BrokerCredentialID == "" {
		return nil, fmt.Errorf("%w: enabled Broker Telegram connection required", ErrUnauthorized)
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
	entry, matched, err := preparedForSource(ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA, Subfolder: definition.Source.Subfolder})
	if err != nil || !matched || entry.ID != "telegram" || effective.Credential == nil || effective.Credential.Backend != "credential-broker" || effective.Credential.Locator != original.BrokerCredentialID {
		return nil, fmt.Errorf("%w: reviewed owner Telegram session required", ErrUnauthorized)
	}
	if definition.RuntimeEnvironment["TELEGRAM_EXPOSED_TOOLS"] == "read-only+send_message" {
		return c.statusBody(original, false), nil
	}
	if !c.selfInstallUsable(definition) || verifyDefinitionToolContract(definition) != nil {
		return nil, fmt.Errorf("%w: reviewed read-only Telegram definition required", ErrUnauthorized)
	}
	definition.RuntimeEnvironment = cloneMap(definition.RuntimeEnvironment)
	definition.RuntimeEnvironment["TELEGRAM_EXPOSED_TOOLS"] = "read-only+send_message"
	probe := original
	probe.OnboardingID = deterministicID("send-probe", original.OnboardingID)
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
	onboarding, err := c.newOnboarding(auth, OnboardingSelfInstall, key, admitted, original.SourceURL, original.CommitSHA)
	if err != nil {
		return nil, err
	}
	onboarding.Definition = &admitted
	onboarding.BrokerCredentialID, onboarding.Locator = original.BrokerCredentialID, original.BrokerCredentialID
	onboarding.ReviewDigest = admitted.Source.ReviewDigest
	onboarding.Phase = PhaseAwaitingConfirm
	onboarding.FormNonce, onboarding.FormExpires = "", time.Time{}
	onboarding.ConfirmationNonce = randomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func validateTelegramSendUpgrade(expected, admitted ToolDefinition) error {
	if err := verifyDefinitionToolContract(admitted); err != nil {
		return err
	}
	reads := make(map[string]ToolSpec, len(expected.Tools))
	for _, tool := range expected.Tools {
		if tool.Effect != ReadEffect {
			return ErrUnauthorized
		}
		reads[tool.Name] = tool
	}
	send := false
	for _, tool := range admitted.Tools {
		if tool.Name == "send_message" && tool.Effect == WriteEffect && len(tool.InputSchema) > 0 && !send {
			send = true
		} else if previous, ok := reads[tool.Name]; ok && recordsEqual(previous, tool) {
			delete(reads, tool.Name)
		} else {
			return fmt.Errorf("%w: Telegram upgrade may add only send_message", ErrUnauthorized)
		}
	}
	expected.Tools = admitted.Tools
	expected.Source.ReviewDigest = admitted.Source.ReviewDigest
	expected.Source.ToolContractDigest = admitted.Source.ToolContractDigest
	expected.Source.ToolContractSource = admitted.Source.ToolContractSource
	if !send || len(reads) != 0 || !definitionsEqual(expected, admitted) {
		return fmt.Errorf("%w: Telegram upgrade contract drift", ErrUnauthorized)
	}
	return nil
}
