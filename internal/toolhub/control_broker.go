package toolhub

import (
	"context"
	"fmt"
	brokerv1 "github.com/letya999/credential-broker/api/v1"
	brokerclient "github.com/letya999/credential-broker/client"
	"github.com/letya999/hermes-hub/internal/identity"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (c *ControlPlane) ensureBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *Onboarding, definition ToolDefinition) error {
	if c == nil || c.Broker == nil || !c.Broker.Enabled() || len(definition.Credentials) == 0 {
		return nil
	}
	// Bounded-cli credentials are owner-declared environment values, not
	// provider sessions: without a reviewed broker contract the protected
	// loopback form carries them. A bound contract keeps the broker path.
	if definition.Transport == BoundedCLI && definition.CredentialContractID == "" {
		return nil
	}
	if definition.CredentialContractID == "" || definition.CredentialContractRevision < 1 || len(definition.CredentialContractEnv) == 0 {
		return fmt.Errorf("%w: reviewed credential broker contract is required for %s", ErrUnauthorized, definition.DefinitionID)
	}
	control, err := c.Broker.New(auth, "broker:control")
	if err != nil {
		return err
	}
	if onboarding.BrokerRequestID == "" && onboarding.BrokerRotateCredentialID == "" {
		if onboarding.BrokerCredentialID != "" {
			return nil
		}
		c.Store.mu.RLock()
		matches := c.Store.matchingOwnerConnectionsLocked(auth, definition)
		c.Store.mu.RUnlock()
		if len(matches) > 1 {
			return fmt.Errorf("%w: ambiguous owner connection", ErrUnauthorized)
		}
		if len(matches) == 1 {
			ref := matches[0].credential
			if ref.Backend == "credential-broker" && ref.BrokerContractID == definition.CredentialContractID && ref.BrokerContractRevision == definition.CredentialContractRevision && brokerCredentialActive(ctx, control, ref.Locator) {
				onboarding.Locator, onboarding.BrokerCredentialID = ref.Locator, ref.Locator
				onboarding.BrokerContractID, onboarding.BrokerContractRevision = ref.BrokerContractID, ref.BrokerContractRevision
				onboarding.Phase = PhaseAwaitingConfirm
				onboarding.FormNonce = ""
				onboarding.FormExpires = time.Time{}
				onboarding.ConfirmationNonce = randomNonce()
				onboarding.ConfirmationExpires = c.now().Add(c.ttl())
				onboarding.Revision++
				if err := c.Store.PutOnboarding(*onboarding); err != nil {
					return err
				}
				return c.refreshBrokerRequest(ctx, auth, onboarding)
			}
		}
	}
	if onboarding.BrokerRequestID != "" {
		// A dead link must not pin the onboarding forever: expired or canceled
		// requests are replaced by a fresh one under a new idempotency key.
		request, err := control.Request(ctx, onboarding.BrokerRequestID)
		if err != nil {
			return err
		}
		if request.Status == "expired" || request.Status == "canceled" {
			onboarding.BrokerRequestID = ""
			onboarding.BrokerAuthorizationURL = ""
			onboarding.BrokerAttempts++
		}
	}
	if onboarding.BrokerRequestID == "" {
		ownerKind := "user"
		if definition.Workload.Class == Shared {
			ownerKind = "context"
		}
		connectionID := deterministicID("conn", auth.PrincipalID, definition.DefinitionID, onboarding.OnboardingID)
		consumerID := definition.DefinitionID
		if onboarding.BrokerRotateCredentialID != "" {
			var credential brokerv1.Credential
			if err := control.Do(ctx, http.MethodGet, "/v1/credentials/"+url.PathEscape(onboarding.BrokerRotateCredentialID), nil, &credential); err != nil {
				return err
			}
			connectionID = credential.ConnectionID
			consumerID = credential.ConsumerID
		}
		request, err := control.CreateRequest(ctx, brokerv1.CreateRequest{
			ContractID: definition.CredentialContractID, ContractRevision: definition.CredentialContractRevision,
			ConnectionID: connectionID, ConsumerID: consumerID, RotateCredentialID: onboarding.BrokerRotateCredentialID,
			OnboardingID: onboarding.OnboardingID, IdempotencyKey: fmt.Sprintf("%s/broker-%d", onboarding.OnboardingID, onboarding.BrokerAttempts),
			OwnerKind: ownerKind,
		})
		if err != nil {
			return err
		}
		onboarding.BrokerRequestID = request.ID
		onboarding.BrokerAuthorizationURL = request.AuthorizationURL
		onboarding.BrokerContractID = request.ContractID
		onboarding.BrokerContractRevision = request.ContractRevision
		onboarding.FormNonce = ""
		onboarding.FormExpires = time.Time{}
		onboarding.Revision++
		if err := c.Store.PutOnboarding(*onboarding); err != nil {
			return err
		}
	}
	return c.refreshBrokerRequest(ctx, auth, onboarding)
}

// brokerCredentialActive verifies a stored broker credential is still live
// before a new onboarding reuses it. Revocation happens at the broker, so the
// toolhub connection record alone cannot prove the credential works.
func brokerCredentialActive(ctx context.Context, control *brokerclient.Client, id string) bool {
	if id == "" {
		return false
	}
	var credential brokerv1.Credential
	if err := control.Do(ctx, http.MethodGet, "/v1/credentials/"+url.PathEscape(id), nil, &credential); err != nil {
		return false
	}
	return credential.Status == "active"
}

func (c *ControlPlane) refreshBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *Onboarding) error {
	if c == nil || c.Broker == nil || !c.Broker.Enabled() || onboarding == nil {
		return nil
	}
	if onboarding.BrokerRequestID == "" {
		if onboarding.AdmissionPending && onboarding.BrokerCredentialID != "" {
			return c.completeBrokerAdmission(ctx, auth, onboarding, onboarding.BrokerCredentialID)
		}
		return nil
	}
	control, err := c.Broker.New(auth, "broker:control")
	if err != nil {
		return err
	}
	request, err := control.Request(ctx, onboarding.BrokerRequestID)
	if err != nil {
		return err
	}
	onboarding.BrokerAuthorizationURL = request.AuthorizationURL
	onboarding.BrokerContractID = request.ContractID
	onboarding.BrokerContractRevision = request.ContractRevision
	changed := false
	if request.Status == "ready" && request.CredentialID != "" {
		if onboarding.AdmissionPending {
			return c.completeBrokerAdmission(ctx, auth, onboarding, request.CredentialID)
		}
		if onboarding.BrokerCredentialID != request.CredentialID || onboarding.Locator != request.CredentialID || onboarding.Phase == PhaseAwaitingCreds {
			onboarding.BrokerCredentialID = request.CredentialID
			onboarding.Locator = request.CredentialID
			onboarding.Phase = PhaseAwaitingConfirm
			onboarding.FormNonce = ""
			onboarding.FormExpires = time.Time{}
			onboarding.ConfirmationNonce = randomNonce()
			onboarding.ConfirmationExpires = c.now().Add(c.ttl())
			onboarding.ConfirmationUsed = false
			changed = true
		}
	}
	if changed {
		onboarding.Revision++
		return c.Store.PutOnboarding(*onboarding)
	}
	return nil
}

func (c *ControlPlane) completeBrokerAdmission(ctx context.Context, auth identity.Envelope, onboarding *Onboarding, credentialID string) error {
	c.admissionMu.Lock()
	defer c.admissionMu.Unlock()
	current, err := c.Store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		return err
	}
	if !current.AdmissionPending {
		*onboarding = current
		return nil
	}
	if current.BrokerRequestID != onboarding.BrokerRequestID || current.BrokerCredentialID != "" && current.BrokerCredentialID != credentialID && current.BrokerRotateCredentialID != current.BrokerCredentialID {
		return ErrStale
	}
	if strings.HasPrefix(current.Error, "Проверка не прошла") {
		*onboarding = current
		return nil
	}
	definition, err := c.definitionOf(current)
	if err != nil {
		return err
	}
	admitted, err := c.admitWithBrokerCredential(ctx, auth, current, definition, credentialID)
	if err == nil && !c.selfInstallUsable(admitted) {
		err = fmt.Errorf("%w: admitted Broker definition differs from reviewed entry", ErrUnauthorized)
	}
	if err == nil {
		err = c.registerAdmittedDefinition(&admitted, current.PrincipalID)
	}
	latest, readErr := c.Store.OnboardingFor(auth, current.OnboardingID)
	if readErr != nil {
		return readErr
	}
	if !latest.AdmissionPending || latest.Phase != current.Phase || latest.BrokerRequestID != current.BrokerRequestID || latest.BrokerRotateCredentialID != current.BrokerRotateCredentialID {
		return ErrStale
	}
	latest.BrokerCredentialID, latest.Locator = credentialID, credentialID
	latest.FormNonce, latest.FormExpires = "", time.Time{}
	if err != nil {
		latest.Phase = PhaseAwaitingCreds
		latest.Error = "Проверка не прошла. " + credentialProbeFailure(err)
	} else {
		latest.Definition = &admitted
		latest.DefinitionID, latest.DefinitionVersion = admitted.DefinitionID, admitted.Version
		latest.Permissions, latest.Effects = toolNames(admitted), effectNames(admitted)
		latest.ReviewDigest = admitted.Source.ReviewDigest
		latest.AdmissionPending = false
		latest.Error = ""
		latest.Phase = PhaseAwaitingConfirm
		latest.ConfirmationNonce = randomNonce()
		latest.ConfirmationExpires = c.now().Add(c.ttl())
		latest.ConfirmationUsed = false
	}
	latest.Revision++
	if err := c.Store.PutOnboarding(latest); err != nil {
		return err
	}
	*onboarding = latest
	return nil
}

// regenerateBrokerRequest replaces an expired or canceled broker request while
// the onboarding still waits for credentials, so a dead connect link heals on
// the next status poll instead of pinning the onboarding forever.
func (c *ControlPlane) regenerateBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *Onboarding) error {
	if onboarding == nil || onboarding.Phase != PhaseAwaitingCreds {
		return nil
	}
	definition, err := c.definitionOf(*onboarding)
	if err != nil {
		return err
	}
	// Unprepared servers with no reviewed contract still use the protected
	// loopback form. A prepared authenticated source resumes through Broker.
	if onboarding.AdmissionPending && definition.CredentialContractID == "" {
		return nil
	}
	return c.ensureBrokerRequest(ctx, auth, onboarding, definition)
}

// brokerCredentialRequired is true when the definition is bound to a reviewed
// broker contract or the onboarding already holds a broker credential. A
// credential-gate admission has neither, so its loopback secret stays local.
func (c *ControlPlane) brokerCredentialRequired(definition ToolDefinition, onboarding Onboarding) bool {
	return c != nil && c.Broker != nil && c.Broker.Enabled() && (definition.CredentialContractID != "" || onboarding.BrokerCredentialID != "")
}

func (c *ControlPlane) ensureBrokerGrant(ctx context.Context, auth identity.Envelope, definition ToolDefinition, onboarding Onboarding, binding ToolBinding) (string, error) {
	if c.Broker == nil || !c.Broker.Enabled() || onboarding.BrokerCredentialID == "" {
		return "", fmt.Errorf("%w: credential broker credential", ErrUnauthorized)
	}
	if binding.WorkloadClass == PerJob {
		return "", fmt.Errorf("%w: credential broker per-job grants are not supported yet", ErrUnauthorized)
	}
	owner := (*OwnerRef)(nil)
	if binding.WorkloadClass != Shared {
		owner = &OwnerRef{Type: ContextOwner, ID: auth.ContextID}
	}
	workload, err := NewWorkloadInstance(binding, owner, "", 1, c.now(), time.Time{})
	if err != nil {
		return "", err
	}
	control, err := c.Broker.New(auth, "broker:control")
	if err != nil {
		return "", err
	}
	execution := "dedicated"
	if binding.WorkloadClass == Shared {
		execution = "shared"
	}
	grant, err := control.Grant(ctx, onboarding.BrokerCredentialID, brokerv1.GrantRequest{
		ContractID: definition.CredentialContractID, ContractRevision: definition.CredentialContractRevision,
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		BindingID: binding.ToolBindingID, WorkloadID: workload.WorkloadID, Execution: execution,
		IdempotencyKey: deterministicID("grant", onboarding.OnboardingID, binding.ToolBindingID),
	})
	if err != nil {
		return "", err
	}
	return grant.ID, nil
}
