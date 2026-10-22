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
	Store          *Store
	Secrets        credstore.Backend
	Reviewer       api.SourceReviewer
	SourceResolver api.SourceResolver
	RecipeCatalogs []api.RecipeCatalog
	OAuth          *oauth.Broker
	Injector       api.CredentialInjector
	admissionMu    sync.Mutex
	oauthFlows     oauthFlowStore
	Now            func() time.Time
	Listen         string
	FormOrigin     string
	WorkloadRoot   string
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
	return 10 * time.Minute
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
		return c.setPhase(auth, args, api.DisabledStatus, api.PhaseDisabled)
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
			if candidate.PrincipalID == auth.PrincipalID && candidate.ContextID == auth.ContextID && candidate.RuntimeID == auth.RuntimeID && candidate.PolicyVersion == auth.PolicyVersion && candidate.Phase != api.PhaseRemoved && (latest.OnboardingID == "" || candidate.CreatedAt.After(latest.CreatedAt)) {
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
	if onboarding.Phase == api.PhaseAwaitingOAuth {
		definition, err := c.definitionOf(onboarding)
		if err != nil {
			return nil, err
		}
		resend, _ := args["resend"].(bool)
		return c.ensurePreparedOAuth(context.Background(), auth, onboarding, definition, resend)
	}
	if err := c.regenerateBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		return nil, err
	}
	if err := c.refreshBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		return nil, err
	}
	if err := c.refreshConfirmation(ctx, auth, &onboarding); err != nil {
		return nil, err
	}
	if resend, _ := args["resend"].(bool); resend && onboarding.Phase == api.PhaseAwaitingConfirm {
		// A resend asks for a fresh message carrying the live confirm link.
		api.DeliverPrepareEvent(ctx, auth, onboarding, "link-resent")
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
	if onboarding.Phase == api.PhaseAwaitingOAuth {
		resend, _ := args["resend"].(bool)
		return c.ensurePreparedOAuth(ctx, auth, onboarding, definition, resend)
	}
	nonce := api.ArgString(args, "nonce")
	if onboarding.ConfirmationUsed {
		return nil, fmt.Errorf("%w: replayed nonce", api.ErrUnauthorized)
	}
	if onboarding.ConfirmationNonce == "" || nonce != onboarding.ConfirmationNonce {
		return nil, fmt.Errorf("%w: confirmation nonce", api.ErrUnauthorized)
	}
	if onboarding.ConfirmationExpires.IsZero() || c.now().After(onboarding.ConfirmationExpires) {
		return nil, fmt.Errorf("%w: expired confirmation", api.ErrUnauthorized)
	}
	if len(onboarding.Required) > 0 && onboarding.Locator == "" {
		return nil, fmt.Errorf("%w: credentials required", api.ErrUnauthorized)
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
	onboarding.ConfirmationUsed = true
	onboarding.BindingID = binding.ToolBindingID
	onboarding.ConnectionID = binding.ConnectionID
	onboarding.CredentialRefID = binding.CredentialRefID
	onboarding.BrokerRotateCredentialID = ""
	onboarding.Error = ""
	onboarding.ErrorCode = ""
	onboarding.Phase = api.PhaseConfirmed
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
		if onboarding.Phase != api.PhaseConfirmed && onboarding.Phase != api.PhaseEnabled && onboarding.Phase != api.PhaseDisabled {
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
	onboarding.Phase = api.PhaseEnabled
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) setPhase(auth identity.Envelope, args map[string]any, status api.Status, phase string) (map[string]any, error) {
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
	onboarding.Phase = phase
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
	onboarding.Phase = api.PhaseRevoked
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
	if onboarding.Phase == api.PhaseRemoved {
		return c.statusBody(onboarding, false), nil
	}
	if !c.confirmedRemoveInProgress(onboarding) {
		// Destructive step: a bare call previews what goes away and arms a
		// nonce; only the nonce-carrying follow-up deletes credentials and
		// state. A cleanup_incomplete retry already passed that gate once.
		nonce := api.ArgString(args, "remove_nonce")
		if onboarding.RemoveNonce == "" || nonce != onboarding.RemoveNonce {
			if nonce != "" {
				return nil, fmt.Errorf("%w: remove confirmation", api.ErrUnauthorized)
			}
			onboarding.RemoveNonce = api.RandomNonce()
			onboarding.RemoveExpires = c.now().Add(c.ttl())
			onboarding.Revision++
			if err := c.Store.PutOnboarding(onboarding); err != nil {
				return nil, err
			}
			// The user confirms through the channel link, not a tool argument.
			api.PostPrepareNotice(ctx, auth, onboarding, "", 0)
			body := c.statusBody(onboarding, false)
			body["requires_confirmation"] = true
			body["next_action"] = "confirm_remove"
			return body, nil
		}
		if onboarding.RemoveExpires.IsZero() || c.now().After(onboarding.RemoveExpires) {
			return nil, fmt.Errorf("%w: expired remove confirmation", api.ErrUnauthorized)
		}
	}
	// Keep the spent nonce as a tombstone: a replayed link matches it and gets
	// a 410 "fresh link" page instead of an opaque 401.
	if len(onboarding.Finalizers) == 0 {
		// Immediate cut before the destructive ladder: the binding leaves the
		// projection and the connection drops to deleting (non-active ⇒
		// Authorize denies) while finalizers purge the footprint.
		if err := c.revokeAccess(onboarding, api.DeletingStatus); err != nil {
			return nil, err
		}
		onboarding.Finalizers = newRemoveLadder()
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
			onboarding.Phase = api.PhaseCleanupIncomplete
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
	onboarding.Phase = api.PhaseRemoved
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
	body := map[string]any{
		"onboarding_id":   onboarding.OnboardingID,
		"phase":           onboarding.Phase,
		"status":          onboarding.Phase,
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
		"form_ready":      onboarding.Phase == api.PhaseAwaitingCreds && (onboarding.BrokerAuthorizationURL != "" || onboarding.ChannelAuthURL != "" || onboarding.FormNonce != ""),
		"artifact_ready":  false,
		"tools_confirmed": false,
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
	switch onboarding.Phase {
	case api.PhasePreparing:
		body["next_action"] = "poll_status"
	case api.PhaseFailed:
		body["next_action"] = "retry"
	case api.PhaseAwaitingCreds:
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
	case api.PhaseAwaitingConfirm:
		body["next_action"] = "confirm"
	case api.PhaseAwaitingOAuth:
		body["next_action"] = "authorize"
	case api.PhaseConfirmed:
		body["next_action"] = "enable"
	case api.PhaseEnabled:
		body["next_action"] = "ready"
	}
	if onboarding.Error != "" {
		body["error"] = onboarding.Error
	}
	if onboarding.ErrorCode != "" {
		body["error_code"] = onboarding.ErrorCode
	}
	if onboarding.Phase == api.PhaseAwaitingConfirm && onboarding.ConfirmationNonce != "" && !onboarding.ConfirmationUsed {
		body["confirmation_sent"] = true
	}
	if onboarding.Phase == api.PhaseEnabled {
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
