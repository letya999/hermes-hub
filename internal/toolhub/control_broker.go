package toolhub

import (
	"context"
	"errors"
	"fmt"
	brokerv1 "github.com/letya999/credential-broker/api/v1"
	brokerclient "github.com/letya999/credential-broker/client"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/url"
	"time"
)

// onboardingShared reports whether the onboarding's definition is a shared
// workload; an unreadable definition falls back to the per-user signer and
// lets the broker's own authorization decide.
func (c *ControlPlane) onboardingShared(onboarding api.Onboarding) bool {
	definition, err := c.definitionOf(onboarding)
	return err == nil && definition.Workload.Class == api.Shared
}

// brokerControl signs control calls; shared (context-owned) credentials
// require the context_manager assertion or the broker rejects them.
func (c *ControlPlane) brokerControl(auth identity.Envelope, shared bool) (*brokerclient.Client, error) {
	if shared {
		return c.Broker.NewContextManaged(auth, "broker:control")
	}
	return c.Broker.New(auth, "broker:control")
}

func (c *ControlPlane) ensureBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding, definition api.ToolDefinition) error {
	return c.ensureBrokerIntake(ctx, auth, onboarding, definition, false)
}

func (c *ControlPlane) ensureBrokerIntake(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding, definition api.ToolDefinition, explicitReauth bool) error {
	if c == nil || c.Broker == nil || !c.Broker.Enabled() || len(definition.Credentials) == 0 {
		return nil
	}
	// Broker intake belongs to the pending stage — a live record already owns
	// its credential and a slot attach must never reopen confirmation on it.
	// Reauth is the deliberate live→pending edge and passes explicitReauth.
	if onboarding.Stage == api.StageLive && !explicitReauth {
		return nil
	}
	// Bounded-cli credentials are owner-declared environment values, not
	// provider sessions: without a reviewed broker contract the protected
	// loopback form carries them. A bound contract keeps the broker path.
	if definition.Transport == api.BoundedCLI && definition.CredentialContractID == "" {
		return nil
	}
	if definition.CredentialContractID == "" || definition.CredentialContractRevision < 1 || len(definition.CredentialContractEnv) == 0 {
		return fmt.Errorf("%w: reviewed credential broker contract is required for %s", api.ErrUnauthorized, definition.DefinitionID)
	}
	control, err := c.brokerControl(auth, definition.Workload.Class == api.Shared)
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
			return fmt.Errorf("%w: ambiguous owner connection", api.ErrUnauthorized)
		}
		if len(matches) == 1 {
			ref := matches[0].credential
			if ref.Backend == "credential-broker" && ref.BrokerContractID == definition.CredentialContractID && ref.BrokerContractRevision == definition.CredentialContractRevision {
				active, err := brokerCredentialActive(ctx, control, ref.Locator)
				if err != nil {
					return err
				}
				if active {
					onboarding.Locator, onboarding.BrokerCredentialID = ref.Locator, ref.Locator
					onboarding.BrokerContractID, onboarding.BrokerContractRevision = ref.BrokerContractID, ref.BrokerContractRevision
					onboarding.Stage = api.StagePending
					onboarding.ArmGate(api.GateConfirmSetup, api.RandomNonce(), c.now().Add(c.ttl()))
					if onboarding.AdmissionPending {
						// The broker probe still owns the record — no human
						// action is armed until admission settles.
						onboarding.ClearGate()
					}
					c.armWizard(onboarding)
					onboarding.Revision++
					if err := c.Store.PutOnboarding(*onboarding); err != nil {
						return err
					}
					return c.refreshBrokerRequest(ctx, auth, onboarding)
				}
			}
		}
	}
	deadLink := false
	if onboarding.BrokerRequestID != "" {
		// A dead link must not pin the onboarding forever: expired or canceled
		// requests are replaced by a fresh one under a new idempotency key. The
		// same applies when the broker dropped the request record entirely
		// (ledger reset, volume drift), or when a pending request has less life
		// left than the channel invite it would produce.
		request, err := control.Request(ctx, onboarding.BrokerRequestID)
		if err != nil && !brokerclient.IsNotFound(err) {
			return err
		}
		if err != nil || c.brokerRequestDead(request) {
			onboarding.BrokerRequestID = ""
			onboarding.BrokerAuthorizationURL = ""
			onboarding.BrokerAttempts++
			deadLink = true
		}
	}
	if onboarding.BrokerRequestID == "" {
		ownerKind := "user"
		if definition.Workload.Class == api.Shared {
			ownerKind = "context"
		}
		connectionID := api.DeterministicID("conn", auth.PrincipalID, definition.DefinitionID, onboarding.OnboardingID)
		consumerID := definition.DefinitionID
		rotateTarget := onboarding.BrokerRotateCredentialID
		if rotateTarget != "" {
			var credential brokerv1.Credential
			err := control.Do(ctx, http.MethodGet, "/v1/credentials/"+url.PathEscape(rotateTarget), nil, &credential)
			switch {
			case brokerclient.IsNotFound(err):
				if !explicitReauth {
					return fmt.Errorf("%w: %w", api.ErrCredentialVisibilityUnknown, err)
				}
				// A masked 404 proves nothing about the old secret. The owner
				// can still authorize a separate, caller-owned replacement.
				rotateTarget = ""
				connectionID = api.DeterministicID("conn", auth.PrincipalID, onboarding.OnboardingID, "reauth", fmt.Sprint(onboarding.BrokerAttempts))
			case err != nil:
				return err
			case credential.Status == "deleted":
				// A visible deletion permits a fresh request; a masked 404 above
				// does not prove that the rotation target was deleted.
				rotateTarget = ""
			default:
				connectionID = credential.ConnectionID
				consumerID = credential.ConsumerID
			}
		}
		// A pinned reviewed contract can be absent from the broker catalog —
		// fresh volumes, ledger resets, and the auth-gated prepare path reach
		// request creation without bindReviewedContract. Register the embedded
		// contract before asking for a request; RegisterContract is idempotent
		// for a digest-matching catalog entry.
		if reviewed, ok, rerr := api.PreparedContractFor(definition.CredentialContractID, definition.CredentialContractRevision); rerr == nil && ok {
			if _, rerr := control.RegisterContract(ctx, reviewed); rerr != nil {
				return fmt.Errorf("register credential broker contract %s@%d: %w", reviewed.ID, reviewed.Revision, rerr)
			}
		}
		request, err := control.CreateRequest(ctx, brokerv1.CreateRequest{
			ContractID: definition.CredentialContractID, ContractRevision: definition.CredentialContractRevision,
			ConnectionID: connectionID, ConsumerID: consumerID, RotateCredentialID: rotateTarget,
			OnboardingID: onboarding.OnboardingID, IdempotencyKey: fmt.Sprintf("%s/broker-%d", onboarding.OnboardingID, onboarding.BrokerAttempts),
			OwnerKind: ownerKind, Extra: brokerExtras(definition),
		})
		if err != nil {
			return fmt.Errorf("credential broker request for contract %s rev %d: %w", definition.CredentialContractID, definition.CredentialContractRevision, err)
		}
		onboarding.BrokerRequestID = request.ID
		onboarding.BrokerAuthorizationURL = request.AuthorizationURL
		onboarding.BrokerContractID = request.ContractID
		onboarding.BrokerContractRevision = request.ContractRevision
		// The broker connect URL carries the credential step now — the local
		// form gate stays armed but holds no token of its own.
		onboarding.ArmGate(api.GateCredentials, "", time.Time{})
		c.armWizard(onboarding)
		onboarding.Revision++
		if err := c.Store.PutOnboarding(*onboarding); err != nil {
			return err
		}
		if deadLink {
			// The QR invite or form link for the dead request never reaches
			// the channel on its own; deliver the outcome notice again.
			c.deliverOutcomeNotice(ctx, auth, onboarding)
		}
	}
	return c.refreshBrokerRequest(ctx, auth, onboarding)
}

// deliverOutcomeNotice posts the outcome to the communication plane and, when
// it returns a channel sign-in link (QR invite and friends), records it on the
// onboarding so the next status result can hand it to the agent directly.
func (c *ControlPlane) deliverOutcomeNotice(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding) {
	if current, err := c.Store.OnboardingFor(auth, onboarding.OnboardingID); err == nil {
		*onboarding = current
	}
	if onboarding.Initiator == nil {
		onboarding.Initiator = &auth
		onboarding.Revision++
		if err := c.Store.PutOnboarding(*onboarding); err != nil {
			return
		}
	}
	c.deliverOnboardingNotice(ctx, auth, onboarding.OnboardingID)
	if current, err := c.Store.OnboardingFor(auth, onboarding.OnboardingID); err == nil {
		*onboarding = current
	}
}

// brokerRequestDead reports whether the request can still carry a credential
// submission. Keep a live form until expiry: Broker rejects a second pending
// request for the same connection, and the owner may still be filling it.
func (c *ControlPlane) brokerRequestDead(request brokerv1.Request) bool {
	switch request.Status {
	case "expired", "canceled":
		return true
	case "ready", "submitted", "approved":
		return false
	}
	return !request.ExpiresAt.IsZero() && !c.now().Before(request.ExpiresAt)
}

// brokerCredentialActive verifies a stored broker credential is still live
// before a new onboarding reuses it. Revocation happens at the broker, so the
// toolhub connection record alone cannot prove the credential works.
func brokerCredentialActive(ctx context.Context, control *brokerclient.Client, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	gone, err := brokerCredentialGone(ctx, control, id)
	return !gone && err == nil, err
}

// brokerCredentialGone only accepts a visible credential's inactive state.
// A 404 also masks ownership mismatch and must not reset stored references.
func brokerCredentialGone(ctx context.Context, control *brokerclient.Client, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var credential brokerv1.Credential
	if err := control.Do(ctx, http.MethodGet, "/v1/credentials/"+url.PathEscape(id), nil, &credential); err != nil {
		if brokerclient.IsNotFound(err) {
			return false, fmt.Errorf("%w: %w", api.ErrCredentialVisibilityUnknown, err)
		}
		return false, err
	}
	switch credential.Status {
	case "active":
		return false, nil
	case "revoked", "deleted", "reauthorize_required":
		return true, nil
	default:
		return false, fmt.Errorf("%w: unknown credential broker status", api.ErrInvalid)
	}
}

func (c *ControlPlane) refreshBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding) error {
	if c == nil || c.Broker == nil || !c.Broker.Enabled() || onboarding == nil {
		return nil
	}
	if onboarding.BrokerRequestID == "" {
		if onboarding.AdmissionPending && onboarding.BrokerCredentialID != "" {
			return c.completeBrokerAdmission(ctx, auth, onboarding, onboarding.BrokerCredentialID)
		}
		return nil
	}
	control, err := c.brokerControl(auth, c.onboardingShared(*onboarding))
	if err != nil {
		return err
	}
	if onboarding.BrokerCredentialID != "" && (onboarding.GateKind() == api.GateConfirmSetup || onboarding.Awaiting(api.GateCredentials)) {
		// Reset only after observing an inactive credential. Unknown visibility
		// preserves the reference and confirmation for owner-context diagnosis.
		gone, err := brokerCredentialGone(ctx, control, onboarding.BrokerCredentialID)
		if err != nil {
			return err
		}
		if gone {
			onboarding.BrokerRotateCredentialID = onboarding.BrokerCredentialID
			onboarding.BrokerCredentialID, onboarding.Locator = "", ""
			onboarding.BrokerRequestID, onboarding.BrokerAuthorizationURL = "", ""
			onboarding.BrokerAttempts++
			onboarding.Stage = api.StagePending
			onboarding.ArmGate(api.GateCredentials, "", time.Time{})
			onboarding.ConfirmationUsed = false
			onboarding.Revision++
			if err := c.Store.PutOnboarding(*onboarding); err != nil {
				return err
			}
			definition, derr := c.definitionOf(*onboarding)
			if derr != nil {
				return derr
			}
			return c.ensureBrokerRequest(ctx, auth, onboarding, definition)
		}
	}
	request, err := control.Request(ctx, onboarding.BrokerRequestID)
	if err != nil {
		// The broker dropped this request record (ledger reset, volume drift):
		// mint a fresh link instead of pinning the onboarding on not_found.
		if !brokerclient.IsNotFound(err) {
			return err
		}
		onboarding.BrokerRequestID = ""
		onboarding.BrokerAuthorizationURL = ""
		onboarding.BrokerAttempts++
		definition, derr := c.definitionOf(*onboarding)
		if derr != nil {
			return derr
		}
		if err := c.ensureBrokerRequest(ctx, auth, onboarding, definition); err != nil {
			return err
		}
		// The old link is dead; the owner needs the new one in the channel.
		c.deliverOutcomeNotice(ctx, auth, onboarding)
		return nil
	}
	if c.brokerRequestDead(request) {
		// The broker still has the record but the connect link is dead:
		// mint a fresh request instead of resending an unusable URL.
		onboarding.BrokerRequestID = ""
		onboarding.BrokerAuthorizationURL = ""
		onboarding.BrokerAttempts++
		definition, derr := c.definitionOf(*onboarding)
		if derr != nil {
			return derr
		}
		if err := c.ensureBrokerRequest(ctx, auth, onboarding, definition); err != nil {
			return err
		}
		c.deliverOutcomeNotice(ctx, auth, onboarding)
		return nil
	}
	onboarding.BrokerAuthorizationURL = request.AuthorizationURL
	onboarding.BrokerContractID = request.ContractID
	onboarding.BrokerContractRevision = request.ContractRevision
	if request.Status == "pending" && onboarding.Awaiting(api.GateCredentials) {
		// The QR invite minted for this request dies before the request does;
		// every credentials ask re-delivers the notice so a fresh link lands
		// in the channel. The gateway dedupes while the invite is alive.
		c.deliverOutcomeNotice(ctx, auth, onboarding)
	}
	changed := false
	if request.Status == "ready" && request.CredentialID != "" {
		if onboarding.AdmissionPending {
			return c.completeBrokerAdmission(ctx, auth, onboarding, request.CredentialID)
		}
		if onboarding.BrokerCredentialID != request.CredentialID || onboarding.Locator != request.CredentialID || onboarding.Awaiting(api.GateCredentials) {
			onboarding.BrokerCredentialID = request.CredentialID
			onboarding.Locator = request.CredentialID
			onboarding.Stage = api.StagePending
			onboarding.ArmGate(api.GateConfirmSetup, api.RandomNonce(), c.now().Add(c.ttl()))
			onboarding.ConfirmationUsed = false
			c.armWizard(onboarding)
			changed = true
		}
	}
	if changed {
		onboarding.ObservedAt, onboarding.ObservationCode = c.now(), ""
		expected := onboarding.Revision
		onboarding.Revision++
		if err := c.Store.PutOnboardingIfRevision(*onboarding, expected); err != nil {
			if errors.Is(err, api.ErrStale) {
				current, readErr := c.Store.OnboardingFor(auth, onboarding.OnboardingID)
				if readErr == nil && current.GateKind() == api.GateConfirmSetup && current.BrokerCredentialID == request.CredentialID {
					*onboarding = current
					return nil
				}
			}
			return err
		}
		c.deliverOutcomeNotice(ctx, auth, onboarding)
	}
	return nil
}

func (c *ControlPlane) completeBrokerAdmission(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding, credentialID string) error {
	current, err := c.Store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		return err
	}
	if !current.AdmissionPending || current.Terminal() || len(current.Finalizers) > 0 {
		*onboarding = current
		return nil
	}
	if current.BrokerRequestID != onboarding.BrokerRequestID || current.BrokerCredentialID != "" && current.BrokerCredentialID != credentialID && current.BrokerRotateCredentialID != current.BrokerCredentialID {
		return api.ErrStale
	}
	if current.Stage == api.StageFailed || current.ErrorCode == api.OpCodeProbeFailed || c.now().Before(current.AdmissionNextAttempt) || c.now().Before(current.AdmissionLeaseUntil) {
		*onboarding = current
		return nil
	}
	definition, err := c.definitionOf(current)
	if err != nil {
		return err
	}
	expected := current.Revision
	current.BrokerCredentialID, current.Locator = credentialID, credentialID
	current.Stage = api.StagePending
	current.ClearGate()
	current.AdmissionLeaseUntil = c.now().Add(90 * time.Second)
	current.AdmissionAttempts++
	current.Revision++
	if err := c.Store.PutOnboardingIfRevision(current, expected); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	admitted, err := c.admitWithBrokerCredential(ctx, auth, current, definition, credentialID)
	if err == nil && !c.selfInstallUsable(admitted) {
		err = fmt.Errorf("%w: admitted Broker definition differs from reviewed entry", api.ErrUnauthorized)
	}
	if err == nil {
		err = c.registerAdmittedDefinition(&admitted, current.PrincipalID)
	}
	latest, readErr := c.Store.OnboardingFor(auth, current.OnboardingID)
	if readErr != nil {
		return readErr
	}
	if !latest.AdmissionPending || latest.Stage != current.Stage || latest.GateKind() != current.GateKind() || len(latest.Finalizers) > 0 || !latest.AdmissionLeaseUntil.Equal(current.AdmissionLeaseUntil) || latest.BrokerRequestID != current.BrokerRequestID || latest.BrokerRotateCredentialID != current.BrokerRotateCredentialID {
		return api.ErrStale
	}
	latest.BrokerCredentialID, latest.Locator = credentialID, credentialID
	latest.ObservedAt, latest.ObservationCode = c.now(), ""
	latest.ClearGateKind(api.GateCredentials)
	latest.AdmissionLeaseUntil = time.Time{}
	if err != nil {
		latest.Stage = api.StagePending
		latest.AdmissionNextAttempt = c.now().Add(time.Duration(1<<min(latest.AdmissionAttempts, 6)) * time.Second)
		if latest.AdmissionAttempts >= 5 {
			latest.Stage = api.StageFailed
		}
		if errors.Is(err, api.ErrAuthBeforeEffect) {
			latest.Stage = api.StagePending
			latest.ArmGate(api.GateCredentials, "", time.Time{})
		}
		latest.Error = locale.T(locale.Fallback(), "cred.failed", credentialProbeFailure(err, locale.Fallback()))
		latest.ErrorCode = api.OpCodeAdmissionUnavailable
		if errors.Is(err, api.ErrAuthBeforeEffect) {
			latest.ErrorCode = api.OpCodeProbeFailed
		}
		if admitted.Source.ArchiveDigest != "" {
			// Built evidence survives a rejected credential so rotation only
			// re-runs the tools/list probe instead of rebuilding the artifact.
			// Tools and tool-contract digests stay empty — confirmation still
			// waits for a real credentialed tools/list.
			admitted.Tools = nil
			admitted.Source.ToolContractDigest = ""
			admitted.Source.ToolContractSource = ""
			latest.Definition = &admitted
			latest.DefinitionID, latest.DefinitionVersion = admitted.DefinitionID, admitted.Version
		}
	} else {
		latest.Definition = &admitted
		latest.DefinitionID, latest.DefinitionVersion = admitted.DefinitionID, admitted.Version
		latest.Permissions, latest.Effects = api.ToolNames(admitted), api.EffectNames(admitted)
		latest.ReviewDigest = admitted.Source.ReviewDigest
		latest.AdmissionPending = false
		latest.Error, latest.ErrorCode = "", ""
		latest.AdmissionNextAttempt, latest.AdmissionLeaseUntil = time.Time{}, time.Time{}
		latest.Stage = api.StagePending
		latest.ArmGate(api.GateConfirmSetup, api.RandomNonce(), c.now().Add(c.ttl()))
		latest.ConfirmationUsed = false
		c.armWizard(&latest)
	}
	expected = latest.Revision
	latest.Revision++
	if err := c.Store.PutOnboardingIfRevision(latest, expected); err != nil {
		return err
	}
	*onboarding = latest
	if err == nil {
		c.deliverOutcomeNotice(ctx, auth, onboarding)
	}
	return nil
}

// resetSubmittedCredential reopens the credential step when a readiness probe
// rejects the minted credential: submitted values are wrong and the spent
// connect link cannot take corrections, so the dead credential becomes the
// rotate target and the next status poll mints a fresh request.
func (c *ControlPlane) resetSubmittedCredential(onboarding *api.Onboarding) {
	if onboarding == nil || onboarding.BrokerCredentialID == "" || onboarding.GateKind() != api.GateConfirmSetup {
		return
	}
	onboarding.BrokerRotateCredentialID = onboarding.BrokerCredentialID
	onboarding.BrokerCredentialID, onboarding.Locator = "", ""
	onboarding.BrokerRequestID, onboarding.BrokerAuthorizationURL = "", ""
	onboarding.BrokerAttempts++
	onboarding.Stage = api.StagePending
	onboarding.ArmGate(api.GateCredentials, "", time.Time{})
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
}

// regenerateBrokerRequest replaces an expired or canceled broker request while
// the onboarding still waits for credentials, so a dead connect link heals on
// the next status poll instead of pinning the onboarding forever.
func (c *ControlPlane) regenerateBrokerRequest(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding) error {
	if onboarding == nil || !onboarding.Awaiting(api.GateCredentials) {
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
func (c *ControlPlane) brokerCredentialRequired(definition api.ToolDefinition, onboarding api.Onboarding) bool {
	return c != nil && c.Broker != nil && c.Broker.Enabled() && (definition.CredentialContractID != "" || onboarding.BrokerCredentialID != "")
}

func (c *ControlPlane) brokerGrantActive(ctx context.Context, auth identity.Envelope, definition api.ToolDefinition, match ownerConnectionMatch) (bool, error) {
	if !c.Store.brokerGrantReusable(auth, definition, match) {
		return false, nil
	}
	control, err := c.brokerControl(auth, definition.Workload.Class == api.Shared)
	if err != nil {
		return false, err
	}
	grant, err := control.GetGrant(ctx, match.credential.BrokerGrantID)
	if err != nil {
		return false, err
	}
	return grant.Active, nil
}

func (c *ControlPlane) ensureBrokerGrant(ctx context.Context, auth identity.Envelope, definition api.ToolDefinition, onboarding api.Onboarding, binding api.ToolBinding) (string, error) {
	if c.Broker == nil || !c.Broker.Enabled() || onboarding.BrokerCredentialID == "" {
		return "", fmt.Errorf("%w: credential broker credential", api.ErrUnauthorized)
	}
	if binding.WorkloadClass == api.PerJob {
		return "", fmt.Errorf("%w: credential broker per-job grants are not supported yet", api.ErrUnauthorized)
	}
	owner := (*api.OwnerRef)(nil)
	if binding.WorkloadClass != api.Shared {
		owner = &api.OwnerRef{Type: api.ContextOwner, ID: auth.ContextID}
	}
	workload, err := api.NewWorkloadInstance(binding, owner, "", 1, c.now(), time.Time{})
	if err != nil {
		return "", err
	}
	control, err := c.brokerControl(auth, binding.WorkloadClass == api.Shared)
	if err != nil {
		return "", err
	}
	execution := "dedicated"
	if binding.WorkloadClass == api.Shared {
		execution = "shared"
	}
	request := brokerv1.GrantRequest{
		ContractID: definition.CredentialContractID, ContractRevision: definition.CredentialContractRevision,
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		BindingID: binding.ToolBindingID, WorkloadID: workload.WorkloadID, Execution: execution,
		IdempotencyKey: api.DeterministicID("grant", onboarding.OnboardingID, binding.ToolBindingID),
	}
	for attempt := 0; attempt < 2; attempt++ {
		grant, err := control.Grant(ctx, onboarding.BrokerCredentialID, request)
		if err != nil {
			return "", err
		}
		if grant.Active {
			return grant.ID, nil
		}
		request.IdempotencyKey = api.DeterministicID("grant-retry", request.IdempotencyKey, grant.ID)
	}
	return "", fmt.Errorf("%w: broker grant remained revoked", api.ErrUnauthorized)
}
