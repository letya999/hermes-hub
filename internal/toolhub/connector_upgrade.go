package toolhub

import (
	"context"
	"fmt"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// prepareConnectorUpgrade admits a reviewed capability change against an
// existing owner-bound Broker session. The old binding remains live until the
// ordinary confirmation and enable path replaces it.
func (c *ControlPlane) prepareConnectorUpgrade(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	mode, onboardingID := api.ArgString(args, "upgrade_mode"), api.ArgString(args, "onboarding_id")
	if mode == "" || onboardingID == "" {
		return nil, fmt.Errorf("%w: upgrade_mode and onboarding_id required", api.ErrInvalid)
	}
	for key := range args {
		if key != "upgrade_mode" && key != "onboarding_id" {
			return nil, fmt.Errorf("%w: upgrade selects only onboarding_id and upgrade_mode", api.ErrInvalid)
		}
	}
	if err := c.Store.RequireSelfInstall(auth); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		c.admissionMu.Lock()
		if c.admissionInFlight == nil {
			c.admissionInFlight = make(map[string]chan struct{})
		}
		pending := c.admissionInFlight[onboardingID]
		if pending == nil {
			c.admissionInFlight[onboardingID] = make(chan struct{})
			c.admissionMu.Unlock()
			break
		}
		c.admissionMu.Unlock()
		select {
		case <-pending:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	defer func() {
		c.admissionMu.Lock()
		close(c.admissionInFlight[onboardingID])
		delete(c.admissionInFlight, onboardingID)
		c.admissionMu.Unlock()
	}()
	original, err := c.Store.OnboardingFor(auth, onboardingID)
	if err != nil {
		return nil, err
	}
	key := "upgrade:" + original.OnboardingID + ":" + mode
	if existing, ok := c.Store.FindOnboardingByKey(auth, key); ok {
		if existing.Phase == api.PhaseRemoved || existing.Phase == api.PhaseFailed {
			return nil, api.ErrRevoked
		}
		if err := c.refreshConfirmation(ctx, auth, &existing); err != nil {
			return nil, err
		}
		return c.statusBody(existing, false), nil
	}
	if original.Phase != api.PhaseEnabled || original.BrokerCredentialID == "" {
		return nil, fmt.Errorf("%w: enabled Broker connection required", api.ErrUnauthorized)
	}
	c.Store.mu.RLock()
	effective, err := c.Store.resolveLocked(auth, original.BindingID)
	c.Store.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	definition := effective.Definition
	entry, matched, err := api.PreparedForSource(api.ArtifactSource{Repository: definition.Source.Repository, CommitSHA: definition.Source.CommitSHA, Subfolder: definition.Source.Subfolder})
	upgrade, supported := entry.UpgradeModes[mode]
	if err != nil || !matched || !supported || effective.Credential == nil || effective.Credential.Backend != "credential-broker" || effective.Credential.Locator != original.BrokerCredentialID {
		return nil, fmt.Errorf("%w: reviewed owner connection and upgrade mode required", api.ErrUnauthorized)
	}
	if !c.selfInstallUsable(definition) || api.VerifyDefinitionToolContract(definition) != nil {
		return nil, fmt.Errorf("%w: reviewed connector definition required", api.ErrUnauthorized)
	}
	definition.RuntimeEnvironment = api.CloneMap(definition.RuntimeEnvironment)
	for key, value := range upgrade.RuntimeEnvironment {
		if definition.RuntimeEnvironment[key] != entry.RuntimeEnvironment[key] {
			return nil, fmt.Errorf("%w: upgrade already applied or environment drifted", api.ErrUnauthorized)
		}
		definition.RuntimeEnvironment[key] = value
	}
	probe := original
	probe.OnboardingID = api.DeterministicID("upgrade-probe", original.OnboardingID, mode)
	admitted, err := c.admitWithBrokerCredential(ctx, auth, probe, definition, original.BrokerCredentialID)
	if err != nil {
		return nil, err
	}
	if err := validateConnectorUpgrade(definition, admitted, upgrade); err != nil {
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

func validateConnectorUpgrade(expected, admitted api.ToolDefinition, upgrade api.PreparedUpgrade) error {
	if err := api.VerifyDefinitionToolContract(admitted); err != nil {
		return err
	}
	previous := make(map[string]api.ToolSpec, len(expected.Tools))
	for _, tool := range expected.Tools {
		previous[tool.Name] = tool
	}
	added := make(map[string]bool, len(upgrade.AddedTools))
	for _, name := range upgrade.AddedTools {
		added[name] = true
	}
	for _, tool := range admitted.Tools {
		if original, ok := previous[tool.Name]; ok && api.RecordsEqual(api.ProjectConnectorTool(expected.DefinitionID, original), tool) {
			delete(previous, tool.Name)
		} else if added[tool.Name] && tool.Effect == api.WriteEffect && len(tool.InputSchema) > 0 {
			delete(added, tool.Name)
		} else {
			return fmt.Errorf("%w: connector upgrade tool drift", api.ErrUnauthorized)
		}
	}
	expected.Tools = admitted.Tools
	expected.Source.ReviewDigest = admitted.Source.ReviewDigest
	expected.Source.ToolContractDigest = admitted.Source.ToolContractDigest
	expected.Source.ToolContractSource = admitted.Source.ToolContractSource
	if len(added) != 0 || len(previous) != 0 || !api.DefinitionsEqual(expected, admitted) {
		return fmt.Errorf("%w: connector upgrade contract drift", api.ErrUnauthorized)
	}
	return nil
}
