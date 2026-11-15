package toolhub

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"log"
	"strings"
	"sync"
	"time"
)

type ControlPlane struct {
	Store *Store
	// Cursors is the sidecar for hot at-least-once bookkeeping (broker event
	// cursors) that must not pay the registry's whole-snapshot commit cost.
	// Nil means lazily bind on first use via cursorStore() — constructed
	// planes that never reconcile broker events never create the file.
	Cursors           *CursorStore
	Secrets           credstore.Backend
	Reviewer          api.SourceReviewer
	SourceResolver    api.SourceResolver
	RecipeCatalogs    []api.RecipeCatalog
	OAuth             *oauth.Broker
	Injector          api.CredentialInjector
	admissionMu       sync.Mutex
	admissionInFlight map[string]chan struct{}
	lifecycleMu       sync.Mutex
	lifecycleTasks    map[string]bool
	oauthFlows        oauthFlowStore
	Now               func() time.Time
	Listen            string
	FormOrigin        string
	WorkloadRoot      string
	// CredentialStagingRoot is the tmpfs root where per-call credential
	// material stages (HUB_CRED_STAGING). When set and different from
	// WorkloadRoot, binding cleanup removes the staging workspace too.
	CredentialStagingRoot string
	// DiagnosticsDir points at the bounded operator diagnostics directory; the
	// diagnostics control op reads only the collected log file inside it.
	DiagnosticsDir  string
	ConfirmationTTL time.Duration
	Ready           func(context.Context, api.EffectiveBinding) error
	Broker          *credentialbroker.Config
	BrokerRuntime   *credentialbroker.Config
	discovery       discoverySelections
	// Release asks the workload controller to stop a running workload. Revoke
	// and remove use it so a cut connector does not keep a materialized
	// credential alive in a running container until the idle TTL fires.
	// Purge additionally drops the workload's named state volumes.
	Release func(context.Context, api.ControllerRelease) error
	// CLIRelease drops bounded-cli sandbox cells by principal/binding/job
	// selector: disable and revoke kill warm cells, the /v1 job-end route
	// kills task cells. Nil leaves cells to the controller's own ladder.
	CLIRelease func(context.Context, api.CliReleaseRequest) error
	// LabelQuery lists Docker objects (container/volume/network/image) whose
	// labels match the query; remove's verify-zero and the reconciler read the
	// connection footprint through it. Nil means no controller seam — verify
	// passes vacuously.
	LabelQuery func(context.Context, map[string]string) (map[string][]string, error)
	// LabelPurge deletes one labelled Docker object by kind
	// (container/volume/network/image); the volume/network/image-ref
	// finalizers sweep leftovers through it. Nil skips the sweep — the
	// controller's own release path already removed them.
	LabelPurge func(context.Context, string, string, map[string]string) error
	// LabelScan lists hub-labelled Docker objects with their full label
	// sets — the production Scan seam the reconciler's orphan pass diffs
	// against the store. Nil leaves orphan detection unwired.
	LabelScan func(context.Context, map[string]string) ([]api.LabeledResource, error)
	// SurfaceProbe re-lists a live connection's tool surface — a workload
	// tools/list or remote re-probe — for the reconciler's rug-pull check.
	// Nil falls back to the recorded definition surface, which still
	// detects pin-versus-definition drift but cannot see a live workload.
	SurfaceProbe func(context.Context, api.Connection, api.ToolDefinition) ([]api.ToolSpec, error)
	// PrepareSyncWindow bounds how long prepare_source waits for review+build
	// before returning the durable "preparing" record; the work then continues
	// on a detached context and its result is read via status. Zero uses the
	// default; negative runs the whole prepare synchronously (tests).
	PrepareSyncWindow time.Duration
	// PrepareDone, when set, runs after a backgrounded prepare finishes —
	// whatever the outcome — so transports can wake open sessions.
	PrepareDone func(context.Context, identity.Envelope, api.Onboarding)
	// AdmitWithCredentials re-runs tools/list with owner-submitted secrets
	// after a server refused MCP until those secrets existed. Nil means a
	// deferred admission cannot be completed.
	AdmitWithCredentials func(context.Context, api.ToolDefinition, map[string]string) (api.ToolDefinition, error)
	// CLIArtifacts is the owner bounded-cli artifact pipeline (immutable
	// source → restricted build → digest-pinned extracted binary). Nil fails
	// the cli artifact source path closed.
	CLIArtifacts *CLIArtifactPipeline
	// governance is the host-owned tool policy document (issue 139). The
	// grant_request control op stamps the in-conversation request onto a
	// host-created pending grant; nil disables the op entirely.
	Governance *GovernanceStore
	// FinalizerStepDelay stalls each remove-ladder step — a live
	// restart-resume chaos knob set only through HUB_DEBUG_FINALIZER_DELAY_MS.
	// Zero (the default) runs the ladder at full speed.
	FinalizerStepDelay time.Duration
}

func (c *ControlPlane) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *ControlPlane) ttl() time.Duration {
	if c != nil && c.ConfirmationTTL > 0 {
		return c.ConfirmationTTL
	}
	// Protected links are loopback-only and travel through chat: ten minutes
	// expired before the owner could read, click and return — a removal
	// nonce is consent, not a credential form.
	return time.Hour
}

func (c *ControlPlane) origin() string {
	if c != nil && c.FormOrigin != "" {
		return strings.TrimRight(c.FormOrigin, "/")
	}
	if c != nil && c.Listen != "" {
		if strings.Contains(c.Listen, "://") {
			return strings.TrimRight(c.Listen, "/")
		}
		return "http://" + c.Listen
	}
	return "http://127.0.0.1"
}

func (c *ControlPlane) Invoke(ctx context.Context, auth identity.Envelope, op string, args map[string]any) (map[string]any, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("%w: control plane", api.ErrInvalid)
	}
	if err := api.RejectAuthorityArguments(args); err != nil {
		return nil, err
	}
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if err := c.Store.RequireControlOperation(auth, op); err != nil {
		return nil, err
	}
	switch op {
	case "discover":
		return c.discover(ctx, auth, api.ArgString(args, "query"))
	case "prepare_source":
		return c.prepareSource(ctx, auth, args)
	case "status":
		return c.status(ctx, auth, args)
	case "required_credentials":
		return c.requiredCredentials(ctx, auth, args)
	case "confirm":
		return c.confirm(ctx, auth, args)
	case "enable":
		return c.enable(ctx, auth, args)
	case "rotate":
		return c.Rotate(auth, args)
	case "disable":
		return c.setPhase(auth, args, api.DisabledStatus)
	case "revoke":
		return c.revoke(auth, args)
	case "remove":
		return c.remove(ctx, auth, args)
	case "diagnostics":
		return c.diagnostics(ctx, auth, args)
	case "grant_request":
		return c.grantRequest(auth, args)
	default:
		return nil, fmt.Errorf("%w: unknown control operation", api.ErrInvalid)
	}
}

func (c *ControlPlane) status(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	if api.ArgString(args, "onboarding_id") == "" && api.ArgString(args, "definition_id") == "" && api.ArgString(args, "selector") == "" && api.ArgString(args, "name") == "" && api.ArgString(args, "source") == "" && api.ArgString(args, "operation_id") == "" && api.ArgString(args, "request_key") == "" {
		c.Store.mu.RLock()
		var latest api.Onboarding
		for _, candidate := range c.Store.onboardings {
			if candidate.PrincipalID == auth.PrincipalID && candidate.ContextID == auth.ContextID && candidate.RuntimeID == auth.RuntimeID && candidate.PolicyVersion == auth.PolicyVersion && !candidate.DeadEnd() && (latest.OnboardingID == "" || candidate.CreatedAt.After(latest.CreatedAt)) {
				latest = candidate
			}
		}
		c.Store.mu.RUnlock()
		if latest.OnboardingID == "" {
			return nil, fmt.Errorf("%w: onboarding", api.ErrNotFound)
		}
		args = map[string]any{"onboarding_id": latest.OnboardingID}
	}
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}

	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) confirm(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if err := c.refreshBrokerRequest(ctx, auth, &onboarding); err != nil {
		return nil, err
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return nil, err
	}
	if err := api.RejectEscalation(definition, args); err != nil {
		return nil, err
	}
	if onboarding.Awaiting(api.GateOAuth) {
		resend, _ := args["resend"].(bool)
		return c.ensurePreparedOAuth(ctx, auth, onboarding, definition, resend)
	}
	nonce := api.ArgString(args, "nonce")
	if onboarding.ConfirmationUsed {
		return nil, fmt.Errorf("%w: replayed nonce", api.ErrUnauthorized)
	}
	if onboarding.GateNonce(api.GateConfirmSetup) == "" || nonce != onboarding.GateNonce(api.GateConfirmSetup) {
		return nil, fmt.Errorf("%w: confirmation nonce", api.ErrUnauthorized)
	}
	if onboarding.GateExpiry(api.GateConfirmSetup).IsZero() || c.now().After(onboarding.GateExpiry(api.GateConfirmSetup)) {
		return nil, fmt.Errorf("%w: expired confirmation", api.ErrUnauthorized)
	}
	if len(onboarding.Required) > 0 && onboarding.Locator == "" {
		return nil, fmt.Errorf("%w: credentials required", api.ErrUnauthorized)
	}
	if proof, ok := ctx.Value(humanConfirmationKey{}).(api.HumanConfirmation); ok {
		onboarding, err = c.Store.ConsumeHumanConfirmation(proof, c.now())
	} else {
		onboarding, err = c.Store.ConsumeConfirmation(onboarding)
	}
	if err != nil {
		return nil, err
	}
	binding, err := c.materializeBinding(ctx, auth, onboarding, definition)
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) || errors.Is(err, api.ErrCredentialDelivery) {
			// Only a missing token state may re-route to the reviewed OAuth
			// handoff: a delivered bundle that lacks required inputs (e.g. a
			// non-client field like the account email) is owner-fixable, so
			// the credential step reopens for a fresh Broker form instead.
			if entry, matched, lookupErr := api.PreparedForSource(api.ArtifactSource{Repository: definition.Source.Repository, Subfolder: definition.Source.Subfolder, CommitSHA: definition.Source.CommitSHA}); !errors.Is(err, api.ErrCredentialDelivery) && (definition.OAuth != nil || lookupErr == nil && matched && entry.OAuth != nil) {
				log.Printf("toolhub oauth re-route onboarding=%s materialize: %v", onboarding.OnboardingID, err)
				return c.ensurePreparedOAuth(ctx, auth, onboarding, definition, false)
			}
			c.resetSubmittedCredential(&onboarding)
		}
		return nil, c.persistFailure(onboarding, api.PublicPrepareError(err), err)
	}
	onboarding.BindingID = binding.ToolBindingID
	onboarding.ConnectionID = binding.ConnectionID
	onboarding.CredentialRefID = binding.CredentialRefID
	onboarding.BrokerRotateCredentialID = ""
	onboarding.Error = ""
	onboarding.ErrorCode = ""
	onboarding.Stage = api.StagePending
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) enable(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		definitionID := api.ArgString(args, "definition_id")
		version := api.ArgString(args, "version")
		if definitionID == "" || version == "" {
			return nil, err
		}
		definition, defErr := c.Store.Definition(definitionID, version)
		if defErr != nil {
			return nil, defErr
		}
		if err := api.RejectEscalation(definition, args); err != nil {
			return nil, err
		}
		if pub := c.Store.publicationFor(definition); pub.Visibility != api.PublicationUser {
			if err := c.Store.RequireCatalogAccess(auth, definition); err != nil {
				return nil, err
			}
		}
		var binding api.ToolBinding
		var enableErr error
		if c.Ready != nil {
			binding, enableErr = c.Store.EnableReady(ctx, auth, definitionID, version, c.Ready)
		} else {
			binding, enableErr = c.Store.Enable(auth, definitionID, version)
		}
		if enableErr != nil {
			return nil, enableErr
		}
		return map[string]any{"phase": api.PhaseEnabled, "status": string(binding.Status), "binding_id": binding.ToolBindingID, "definition_id": binding.DefinitionID, "version": binding.DefinitionVersion}, nil
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return nil, err
	}
	if err := api.RejectEscalation(definition, args); err != nil {
		return nil, err
	}
	if onboarding.BindingID == "" {
		consentedRetry := onboarding.Stage == api.StagePending && onboarding.ConfirmationUsed && onboarding.HumanConsent != nil && onboarding.HumanConsent.ActionDigest == api.ConfirmationActionDigest(onboarding)
		if !consentedRetry && !(onboarding.Stage == api.StagePending && onboarding.ConfirmationUsed) && onboarding.Stage != api.StageLive {
			return nil, fmt.Errorf("%w: confirm required", api.ErrUnauthorized)
		}
		binding, err := c.materializeBinding(ctx, auth, onboarding, definition)
		if err != nil {
			return nil, c.persistFailure(onboarding, api.PublicPrepareError(err), err)
		}
		onboarding.BindingID = binding.ToolBindingID
		onboarding.ConnectionID = binding.ConnectionID
		onboarding.CredentialRefID = binding.CredentialRefID
	}
	existing, err := c.Store.binding(onboarding.BindingID)
	if err != nil {
		return nil, err
	}
	if existing.Status == api.RevokedStatus {
		return nil, api.ErrRevoked
	}
	if existing.Status != api.ActiveStatus {
		var binding api.ToolBinding
		if c.Ready != nil {
			binding, err = c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
		} else {
			binding, err = c.Store.Enable(auth, definition.DefinitionID, definition.Version)
		}
		if err != nil {
			return nil, c.persistFailure(onboarding, api.PublicPrepareError(err), err)
		}
		onboarding.BindingID = binding.ToolBindingID
		onboarding.ConnectionID = binding.ConnectionID
		onboarding.CredentialRefID = binding.CredentialRefID
	} else if err := c.Store.SupersedeSiblings(auth, existing.DefinitionID, existing.ToolBindingID); err != nil {
		// An enable on an already-active binding short-circuits Store.Enable,
		// so it is the only place sibling versions can still be retired.
		return nil, err
	}
	onboarding.Error = ""
	onboarding.ErrorCode = ""
	onboarding.Stage = api.StageLive
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

// completeSetup runs the bind+enable tail a protected confirm click used to
// drive. The consent it stood for already arrived — as a credential
// submission, an OAuth grant at the provider, or the bare install request —
// so the setup gate no longer stops the flow. HumanConsent keeps the audit
// shape the consumed gate recorded; Source names which signal counted.
// allow_writes keeps its own protected gate (PendingWrites): enabling writes
// is a separate privilege escalation, not install ceremony.
func (c *ControlPlane) completeSetup(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding, consentSource string) error {
	// markConsent records the already-received consent on a failed bind:
	// without it the owner would need a second ceremony just to retry
	// `enable`. A credential form re-armed by resetSubmittedCredential is
	// left unmarked — the fresh submission is the next consent.
	markConsent := func() {
		if onboarding.Awaiting(api.GateCredentials) {
			return
		}
		onboarding.ConfirmationUsed = true
		onboarding.HumanConsent = &api.HumanConsent{ActorID: auth.PrincipalID, ActionDigest: api.ConfirmationActionDigest(*onboarding), Revision: onboarding.Revision, ConfirmedAt: c.now(), Source: consentSource}
	}
	definition, err := c.definitionOf(*onboarding)
	if err != nil {
		return err
	}
	// Materialize unconditionally — same rule the confirm handler followed:
	// a kept BindingID still needs re-materialization when a rotation or a
	// fresh credential must replace the referenced credential revision.
	binding, err := c.materializeBinding(ctx, auth, *onboarding, definition)
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) || errors.Is(err, api.ErrCredentialDelivery) {
			// Same re-route rule as the confirm handler: a missing token
			// may fall back to the reviewed OAuth handoff; a delivered
			// bundle missing inputs reopens the credential step.
			if entry, matched, lookupErr := api.PreparedForSource(api.ArtifactSource{Repository: definition.Source.Repository, Subfolder: definition.Source.Subfolder, CommitSHA: definition.Source.CommitSHA}); !errors.Is(err, api.ErrCredentialDelivery) && (definition.OAuth != nil || lookupErr == nil && matched && entry.OAuth != nil) {
				log.Printf("toolhub oauth re-route onboarding=%s materialize: %v", onboarding.OnboardingID, err)
				_, rerouteErr := c.ensurePreparedOAuth(ctx, auth, *onboarding, definition, false)
				return rerouteErr
			}
			if onboarding.Locator == "" && onboarding.BrokerCredentialID == "" && len(definition.Credentials) > 0 {
				// Stored inputs were declared but nothing collected — the
				// credential form is where the owner fills or skips them;
				// failing here would dead-end a connector that may still
				// install through a submitted (or empty) form.
				onboarding.Stage = api.StagePending
				if c.brokerCredentialRequired(definition, *onboarding) {
					onboarding.ArmGate(api.GateCredentials, "", time.Time{})
				} else {
					onboarding.ArmGate(api.GateCredentials, api.RandomNonce(), c.now().Add(c.ttl()))
					c.armWizard(onboarding)
				}
				onboarding.Error, onboarding.ErrorCode = "", ""
				onboarding.Revision++
				if putErr := c.Store.PutOnboarding(*onboarding); putErr != nil {
					return putErr
				}
				if c.brokerCredentialRequired(definition, *onboarding) {
					if berr := c.ensureBrokerRequest(ctx, auth, onboarding, definition); berr != nil {
						return berr
					}
				}
				c.deliverOutcomeNotice(ctx, auth, onboarding)
				return nil
			}
			c.resetSubmittedCredential(onboarding)
		}
		markConsent()
		return c.persistFailure(*onboarding, api.PublicPrepareError(err), err)
	}
	onboarding.BindingID = binding.ToolBindingID
	onboarding.ConnectionID = binding.ConnectionID
	onboarding.CredentialRefID = binding.CredentialRefID
	onboarding.BrokerRotateCredentialID = ""
	onboarding.BrokerRequestID = ""
	onboarding.BrokerAuthorizationURL = ""
	onboarding.Error, onboarding.ErrorCode = "", ""
	existing, err := c.Store.binding(onboarding.BindingID)
	if err != nil {
		return err
	}
	if existing.Status == api.RevokedStatus {
		return api.ErrRevoked
	}
	if existing.Status != api.ActiveStatus {
		var binding api.ToolBinding
		if c.Ready != nil {
			binding, err = c.Store.EnableReady(ctx, auth, definition.DefinitionID, definition.Version, c.Ready)
		} else {
			binding, err = c.Store.Enable(auth, definition.DefinitionID, definition.Version)
		}
		if err != nil {
			markConsent()
			return c.persistFailure(*onboarding, api.PublicPrepareError(err), err)
		}
		onboarding.BindingID = binding.ToolBindingID
		onboarding.ConnectionID = binding.ConnectionID
		onboarding.CredentialRefID = binding.CredentialRefID
	} else if err := c.Store.SupersedeSiblings(auth, existing.DefinitionID, existing.ToolBindingID); err != nil {
		return err
	}
	onboarding.ClearGate()
	onboarding.ConfirmationUsed = true
	onboarding.HumanConsent = &api.HumanConsent{ActorID: auth.PrincipalID, ActionDigest: api.ConfirmationActionDigest(*onboarding), Revision: onboarding.Revision, ConfirmedAt: c.now(), Source: consentSource}
	onboarding.Stage = api.StageLive
	onboarding.Revision++
	if err := c.Store.PutOnboarding(*onboarding); err != nil {
		return err
	}
	c.deliverOutcomeNotice(ctx, auth, onboarding)
	return nil
}

// autoCompleteSetup finishes a setup that needs no further owner action. It
// fires only on an armed setup gate with everything required already
// collected — the credential submit, OAuth callback, or a credential-free
// install reached this point — and never consumes the allow_writes gate:
// PendingWrites records stay for the explicit protected confirmation.
func (c *ControlPlane) autoCompleteSetup(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding, consentSource string) error {
	o := *onboarding
	if o.Stage != api.StagePending || o.BindingID != "" || !o.Awaiting(api.GateConfirmSetup) || o.PendingWrites || o.WritesEnabled {
		return nil
	}
	if len(o.Required) > 0 && o.Locator == "" && o.BrokerCredentialID == "" {
		return nil
	}
	return c.completeSetup(ctx, auth, onboarding, consentSource)
}

// setPhase flips the reversible binding switch. Enabled/disabled lives on
// the binding alone — the onboarding stage stays live; the revision bump
// only journals the lifecycle event on the owning record.
func (c *ControlPlane) setPhase(auth identity.Envelope, args map[string]any, status api.Status) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if onboarding.BindingID == "" {
		return nil, fmt.Errorf("%w: binding", api.ErrNotFound)
	}
	if err := c.Store.SetBindingStatus(onboarding.BindingID, status); err != nil {
		return nil, err
	}
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) revoke(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if err := c.cutAuthorization(onboarding, false); err != nil {
		return nil, err
	}
	onboarding.Retire(api.TombstoneRevoked)
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) remove(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if onboarding.Removed() {
		return c.statusBody(onboarding, false), nil
	}
	if !c.confirmedRemoveInProgress(onboarding) {
		// Destructive step: a bare call previews what goes away and arms a
		// nonce; only the nonce-carrying follow-up deletes credentials and
		// state. A cleanup_incomplete retry already passed that gate once.
		nonce := api.ArgString(args, "remove_nonce")
		if onboarding.GateNonce(api.GateRemove) == "" || nonce != onboarding.GateNonce(api.GateRemove) {
			if nonce != "" {
				return nil, fmt.Errorf("%w: remove confirmation", api.ErrUnauthorized)
			}
			onboarding.ArmGate(api.GateRemove, api.RandomNonce(), c.now().Add(c.ttl()))
			onboarding.Revision++
			if err := c.Store.PutOnboarding(onboarding); err != nil {
				return nil, err
			}
			// The user confirms through the channel link, not a tool argument.
			c.deliverOutcomeNotice(ctx, auth, &onboarding)
			body := c.statusBody(onboarding, false)
			body["requires_confirmation"] = true
			body["next_action"] = "confirm_remove"
			return body, nil
		}
		if onboarding.GateExpiry(api.GateRemove).IsZero() || c.now().After(onboarding.GateExpiry(api.GateRemove)) {
			return nil, fmt.Errorf("%w: expired remove confirmation", api.ErrUnauthorized)
		}
	}
	// Keep the spent nonce as a tombstone: a replayed link matches it and gets
	// a 410 "fresh link" page instead of an opaque 401.
	if len(onboarding.Finalizers) > 0 && !onboarding.CleanupIncomplete() && onboarding.Stage != api.StageGone {
		// Earlier versions marked the connection deleting before recording the
		// cleanup phase. Resume those durable ladders through the same fence.
		onboarding.Stage = api.StageFailed
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return nil, err
		}
	}
	if len(onboarding.Finalizers) == 0 {
		// Fence late callbacks durably before external cleanup starts. A gone
		// (revoked) record is already terminal — the ladder runs in place on
		// it; bouncing it through failed would violate the absorbing edge.
		if onboarding.Stage != api.StageGone {
			onboarding.Stage = api.StageFailed
		}
		onboarding.Finalizers = newRemoveLadder()
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return nil, err
		}
	}
	// Repeat the local authorization cut after a crash or storage failure;
	// the canonical cleanup phase already denies dispatch in resolveLocked.
	if err := c.revokeAccess(onboarding, api.DeletingStatus); err != nil {
		return nil, err
	}
	if onboarding.RemoveFootprint == nil && c.LabelQuery != nil {
		onboarding.RemoveFootprint = c.labelFootprint(ctx, onboarding)
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return nil, err
		}
	}
	for i := range onboarding.Finalizers {
		step := &onboarding.Finalizers[i]
		if step.State == api.FinalizerDone {
			continue
		}
		if c.FinalizerStepDelay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.FinalizerStepDelay):
			}
		}
		step.Attempts++
		step.Error = ""
		if err := c.runRemoveFinalizer(ctx, onboarding, step.Name); err != nil {
			step.State = api.FinalizerFailed
			step.Error = api.PublicPrepareError(err)
			onboarding.Stage = api.StageFailed
			onboarding.ErrorCode = "cleanup_incomplete"
			onboarding.Revision++
			if err := c.Store.PutOnboarding(onboarding); err != nil {
				return nil, err
			}
			body := c.statusBody(onboarding, false)
			body["retryable"] = true
			body["next_action"] = "remove"
			body["finalizer"] = step.Name
			return body, nil
		}
		step.State = api.FinalizerDone
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return nil, err
		}
	}
	if onboarding.ConnectionID != "" && !c.Store.sharedForeignConnection(onboarding.PrincipalID, onboarding.ConnectionID) {
		_ = c.Store.SetConnectionStatus(onboarding.ConnectionID, api.RevokedStatus)
		// Sibling install attempts bound to the same connection are zombies:
		// the resource they provisioned is gone, so they retire with it.
		_ = c.Store.SweepConnectionOnboardings(onboarding.ConnectionID, onboarding.OnboardingID)
	}
	onboarding.Retire(api.TombstoneRemoved)
	onboarding.RemoveReceipt = removeReceipt(onboarding.RemoveFootprint)
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

// parrotInstructions tells the agent how to hand a protected link to the
// user: verbatim, alone, without reconstructing alternatives.
func parrotInstructions(field string) string {
	return "Send " + field + " to the user verbatim as the only link — it is the protected sign-in page for this request. Do not offer, construct, or mention any other link. Never ask for credentials in chat."
}

func (c *ControlPlane) statusBody(onboarding api.Onboarding, withHints bool) map[string]any {
	phase := onboarding.NoticePhase()
	disabled := c.Store.bindingStatus(onboarding.BindingID) == api.DisabledStatus
	if disabled && onboarding.Stage == api.StageLive {
		phase = api.PhaseDisabled
	}
	body := map[string]any{
		"onboarding_id":   onboarding.OnboardingID,
		"phase":           phase,
		"status":          phase,
		"mode":            onboarding.Mode,
		"definition_id":   onboarding.DefinitionID,
		"version":         onboarding.DefinitionVersion,
		"repository":      onboarding.SourceURL,
		"commit_sha":      onboarding.CommitSHA,
		"subfolder":       onboarding.Subfolder,
		"binding_id":      onboarding.BindingID,
		"permissions":     onboarding.Permissions,
		"effects":         onboarding.Effects,
		"principal_from":  "request",
		"form_ready":      onboarding.Awaiting(api.GateCredentials) && (onboarding.BrokerAuthorizationURL != "" || onboarding.ChannelAuthURL != "" || onboarding.GateNonce(api.GateCredentials) != ""),
		"artifact_ready":  false,
		"tools_confirmed": false,
	}
	if connection, ok := c.Store.connectionFor(onboarding.ConnectionID); ok && connection.Owner.Matches(identity.Envelope{
		PrincipalID: onboarding.PrincipalID, ContextID: onboarding.ContextID,
	}) {
		body["connection_status"] = string(connection.Status)
		if connection.CredentialVisibility != "" {
			body["credential_visibility"] = connection.CredentialVisibility
			body["credential_checked_at"] = connection.CredentialCheckedAt
			if connection.HealthCause != "" {
				body["health_cause"] = connection.HealthCause
			}
		}
	}
	if definition, err := c.definitionOf(onboarding); err == nil {
		body["artifact_ready"] = api.DigestPattern.MatchString(definition.Source.Digest)
		body["tools_confirmed"] = !onboarding.AdmissionPending && len(definition.Tools) > 0
	}
	if recipe := c.recipeOf(onboarding); recipe != nil {
		body["recipe"] = recipe
	}
	if len(onboarding.CredentialSources) > 0 {
		body["credential_sources"] = onboarding.CredentialSources
	}
	if len(onboarding.Finalizers) > 0 {
		body["finalizers"] = onboarding.Finalizers
	}
	if onboarding.RemoveReceipt != "" {
		body["remove_receipt"] = onboarding.RemoveReceipt
	}
	if onboarding.BrokerRequestID != "" {
		body["broker_request_id"] = onboarding.BrokerRequestID
	}
	if withHints {
		hints := make([]map[string]string, 0, len(onboarding.Required))
		for _, hint := range onboarding.Required {
			hints = append(hints, map[string]string{"name": hint.Name, "type": hint.Type, "hint": hint.Hint})
		}
		body["credentials"] = hints
	}
	switch {
	case onboarding.Awaiting(api.GateCredentials):
		body["next_action"] = "submit_credentials"
		if onboarding.AdmissionPending {
			body["tools_confirmed"] = false
			if onboarding.BrokerContractID != "" && onboarding.ErrorCode == api.OpCodeProbeFailed {
				body["next_action"] = "rotate"
			}
		}
		if onboarding.ChannelAuthURL != "" {
			// The communication plane minted a sign-in link for this request
			// (QR page and friends); it outranks the Broker form, which stays
			// as the manual fallback. Parroting authorization_url as the
			// primary path makes users submit the form and never see the
			// channel link. A stale informational error (e.g. the
			// admission-pending detail) must not suppress the link either:
			// with no URL in the result the agent reconstructs the Broker
			// connect URL itself.
			// The Broker form stays a stored fallback but must not exist for
			// the agent at all: mentioning an "authorization_url fallback"
			// makes the model reconstruct and parrot that URL anyway.
			body["signin_url"] = onboarding.ChannelAuthURL
			body["instructions"] = parrotInstructions("signin_url")
		}
	case onboarding.Awaiting(api.GateConfirmSetup):
		body["next_action"] = "await_user_confirmation"
	case onboarding.Awaiting(api.GateOAuth):
		body["next_action"] = "authorize"
	case onboarding.Stage == api.StagePending && onboarding.ConfirmationUsed:
		body["next_action"] = "enable"
	case onboarding.Stage == api.StageFailed && !onboarding.CleanupIncomplete():
		body["next_action"] = "retry"
	case onboarding.Stage == api.StagePending:
		body["next_action"] = "poll_status"
	case onboarding.Stage == api.StageLive && !disabled:
		body["next_action"] = "diagnostics"
	}
	if onboarding.Error != "" {
		body["error"] = onboarding.Error
	}
	if onboarding.ErrorCode != "" {
		body["error_code"] = onboarding.ErrorCode
	}
	if onboarding.Awaiting(api.GateConfirmSetup) && onboarding.NoticeVersion > 0 && onboarding.NoticeQueuedVersion == onboarding.NoticeVersion {
		body["confirmation_sent"] = true
	}
	if onboarding.Stage == api.StageLive && !disabled {
		if definition, err := c.definitionOf(onboarding); err == nil {
			tools := make([]string, 0, len(definition.Tools))
			for _, tool := range definition.Tools {
				tools = append(tools, api.ProjectedToolName(definition.DefinitionID, definition.Version, tool.Name))
			}
			body["projected_tools"] = tools
		}
	}
	return body
}
