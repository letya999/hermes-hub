package toolhub

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The frozen install ladder, persisted on the operation at creation.
var connectorInstallSteps = []string{
	api.OpStepResolve, api.OpStepFetchArtifact, api.OpStepCollectCredentials,
	api.OpStepAdmitProbe, api.OpStepBind, api.OpStepProject, api.OpStepNotify,
}

func connectorSteps(names []string) []api.OperationStep {
	steps := make([]api.OperationStep, len(names))
	for i, name := range names {
		steps[i] = api.OperationStep{Name: name, State: api.OpStepPending}
	}
	return steps
}

func markStep(op *api.Operation, name, state string) {
	for i := range op.Steps {
		if op.Steps[i].Name == name {
			op.Steps[i].State = state
			return
		}
	}
}

// newConnectorOperation creates the durable journal record for one verb.
// The operation id is derived from the request key, so a caller retrying
// with the same intent+target converges on the same record.
func (c *ControlPlane) newConnectorOperation(auth identity.Envelope, intent, target string, steps []string) api.Operation {
	key := api.OperationRequestKey(auth, intent, target)
	return api.Operation{
		Schema: api.SchemaVersion, OperationID: api.DeterministicID("op", auth.PrincipalID, intent, key),
		Intent: intent, Target: target,
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		RequestKey: key, State: api.OpStateQueued, Steps: connectorSteps(steps), CreatedAt: c.now(),
	}
}

// connectorInstall opens (or resumes) the install operation for a target.
// The request key makes an identical repeat call converge on the same
// operation and the same onboarding — no link is reminted.
func (c *ControlPlane) connectorInstall(ctx context.Context, auth identity.Envelope, args map[string]any) (api.OperationResult, error) {
	target := strings.TrimSpace(api.ArgString(args, "target"))
	if target == "" {
		return api.OperationResult{}, fmt.Errorf("%w: install target", api.ErrInvalid)
	}
	key := api.OperationRequestKey(auth, api.OpIntentInstall, target)
	op, exists := c.Store.OperationForRequestKey(auth, api.OpIntentInstall, target)
	if !exists {
		op = c.newConnectorOperation(auth, api.OpIntentInstall, target, connectorInstallSteps)
		body, err := c.prepareInstallTarget(ctx, auth, target, strings.TrimSpace(api.ArgString(args, "version")), key)
		if err != nil {
			// The pipeline may have created the durable preparing record before
			// failing — the operation keeps pointing at it for status/resume.
			if onboarding, ok := c.Store.FindOnboardingByKey(auth, key); ok {
				op.OnboardingID = onboarding.OnboardingID
			}
			op.State = api.OpStateFailed
			if persistErr := c.Store.PutOperation(op); persistErr != nil {
				return api.OperationResult{}, persistErr
			}
			return api.OperationResult{OperationID: op.OperationID, State: api.OpStateFailed, Summary: "install " + target}, nil
		}
		markStep(&op, api.OpStepResolve, api.OpStepDone)
		op.OnboardingID = api.ArgString(body, "onboarding_id")
		markStep(&op, api.OpStepFetchArtifact, api.OpStepDone)
	}
	// Progress is re-observed from the onboarding phase on every call, so a
	// repeat never remints links or creates a second onboarding.
	onboarding, err := c.Store.OnboardingFor(auth, op.OnboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	markStep(&op, api.OpStepCollectCredentials, api.OpStepDone)
	result := api.OperationResult{OperationID: op.OperationID, Summary: "install " + op.Target}
	if onboarding.Phase == api.PhaseAwaitingCreds {
		op.State = api.OpStateAwaitingUser
		result.LinkSent = onboarding.FormNonce != ""
	} else if onboarding.Phase == api.PhaseAwaitingConfirm {
		op.State = api.OpStateAwaitingConfirm
		result.LinkSent = onboarding.ConfirmationNonce != ""
	} else if onboarding.Phase == api.PhaseEnabled {
		// The human gates are past: bind and projection already happened on
		// the primitive path; the journal records the remaining steps done.
		markStep(&op, api.OpStepAdmitProbe, api.OpStepDone)
		markStep(&op, api.OpStepBind, api.OpStepDone)
		markStep(&op, api.OpStepProject, api.OpStepDone)
		markStep(&op, api.OpStepNotify, api.OpStepDone)
		op.State = api.OpStateDone
	} else {
		op.State = api.OpStateRunning
	}
	result.State = op.State
	if err := c.Store.PutOperation(op); err != nil {
		return api.OperationResult{}, err
	}
	return result, nil
}

// connectorPhase runs the reversible enable/disable verbs through the
// journal: resolve -> apply -> notify, credentials untouched.
func (c *ControlPlane) connectorPhase(ctx context.Context, auth identity.Envelope, args map[string]any, intent string) (api.OperationResult, error) {
	target := strings.TrimSpace(api.ArgString(args, "target"))
	if target == "" {
		return api.OperationResult{}, fmt.Errorf("%w: connector target", api.ErrInvalid)
	}
	op, exists := c.Store.OperationForRequestKey(auth, intent, target)
	if !exists {
		op = c.newConnectorOperation(auth, intent, target, []string{api.OpStepApply, api.OpStepNotify})
	}
	resolved, err := c.Store.ResolveConnectorTarget(auth, target)
	if err != nil {
		return api.OperationResult{}, err
	}
	op.OnboardingID = resolved.OnboardingID
	var phaseErr error
	if intent == api.OpIntentEnable {
		_, phaseErr = c.enable(ctx, auth, map[string]any{"onboarding_id": resolved.OnboardingID})
	} else {
		_, phaseErr = c.setPhase(auth, map[string]any{"onboarding_id": resolved.OnboardingID}, api.DisabledStatus, api.PhaseDisabled)
	}
	if phaseErr != nil {
		op.State = api.OpStateFailed
		op.ErrorCode = api.OpCodeNotPermitted
		if persistErr := c.Store.PutOperation(op); persistErr != nil {
			return api.OperationResult{}, persistErr
		}
		return api.OperationResult{OperationID: op.OperationID, State: api.OpStateFailed, Code: op.ErrorCode, Summary: intent + " " + target}, nil
	}
	if intent == api.OpIntentEnable {
		// connector_enable … toolsets=[issues] widens the projected surface
		// beyond the install-time default read set.
		if raw, ok := args["toolsets"].([]any); ok && len(raw) > 0 {
			sets := make([]string, 0, len(raw))
			for _, item := range raw {
				if name, ok := item.(string); ok {
					sets = append(sets, name)
				}
			}
			if len(sets) > 0 {
				onboarding, err := c.Store.OnboardingFor(auth, resolved.OnboardingID)
				if err != nil {
					return api.OperationResult{}, err
				}
				if onboarding.BindingID != "" {
					if err := c.Store.EnableToolsets(auth, onboarding.BindingID, sets); err != nil {
						return api.OperationResult{}, err
					}
				}
			}
		}
	}
	markStep(&op, api.OpStepApply, api.OpStepDone)
	markStep(&op, api.OpStepNotify, api.OpStepDone)
	op.State = api.OpStateDone
	if err := c.Store.PutOperation(op); err != nil {
		return api.OperationResult{}, err
	}
	return api.OperationResult{OperationID: op.OperationID, State: op.State, Summary: intent + " " + target}, nil
}

// The remove ladder: one link-confirm gate, then the purge steps the
// primitive remove owns. Resume re-observes the onboarding phase — a
// removed record means the purge converged.
var connectorRemoveSteps = []string{
	api.OpStepConfirm, api.OpStepCutAuthorization, api.OpStepPurge, api.OpStepVerifyZero, api.OpStepNotify,
}

// connectorRemove arms the one-time remove link and journals the operation.
// The model call never carries the nonce; the human link executes the purge.
func (c *ControlPlane) connectorRemove(ctx context.Context, auth identity.Envelope, args map[string]any) (api.OperationResult, error) {
	target := strings.TrimSpace(api.ArgString(args, "target"))
	if target == "" {
		return api.OperationResult{}, fmt.Errorf("%w: connector target", api.ErrInvalid)
	}
	op, exists := c.Store.OperationForRequestKey(auth, api.OpIntentRemove, target)
	if !exists {
		op = c.newConnectorOperation(auth, api.OpIntentRemove, target, connectorRemoveSteps)
	}
	if op.OnboardingID == "" {
		resolved, err := c.Store.ResolveConnectorTarget(auth, target)
		if err != nil {
			return api.OperationResult{}, err
		}
		op.OnboardingID = resolved.OnboardingID
	}
	onboarding, err := c.Store.OnboardingFor(auth, op.OnboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	if onboarding.Phase == api.PhaseRemoved {
		for _, step := range connectorRemoveSteps {
			markStep(&op, step, api.OpStepDone)
		}
		op.State = api.OpStateDone
		if err := c.Store.PutOperation(op); err != nil {
			return api.OperationResult{}, err
		}
		return api.OperationResult{OperationID: op.OperationID, State: api.OpStateDone, Summary: "remove " + op.Target + " — " + onboarding.RemoveReceipt}, nil
	}
	if onboarding.RemoveNonce == "" {
		// Bare remove mints the one-time link and halts for the human.
		if _, err := c.remove(ctx, auth, map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil {
			op.State = api.OpStateFailed
			op.ErrorCode = api.OpCodeNotPermitted
			if persistErr := c.Store.PutOperation(op); persistErr != nil {
				return api.OperationResult{}, persistErr
			}
			return api.OperationResult{OperationID: op.OperationID, State: api.OpStateFailed, Code: op.ErrorCode, Summary: "remove " + target}, nil
		}
	}
	markStep(&op, api.OpStepConfirm, api.OpStepDone)
	op.State = api.OpStateAwaitingConfirm
	if err := c.Store.PutOperation(op); err != nil {
		return api.OperationResult{}, err
	}
	return api.OperationResult{OperationID: op.OperationID, State: op.State, LinkSent: true, Summary: "remove " + op.Target}, nil
}

// connectorReauth reopens credential collection on a live connector: a
// fresh protected form link is dispatched while the binding stays put, and
// the operation completes once the connector is enabled again.
func (c *ControlPlane) connectorReauth(ctx context.Context, auth identity.Envelope, args map[string]any) (api.OperationResult, error) {
	target := strings.TrimSpace(api.ArgString(args, "target"))
	if target == "" {
		return api.OperationResult{}, fmt.Errorf("%w: connector target", api.ErrInvalid)
	}
	op, exists := c.Store.OperationForRequestKey(auth, api.OpIntentReauth, target)
	if !exists {
		op = c.newConnectorOperation(auth, api.OpIntentReauth, target, []string{api.OpStepCollectCredentials, api.OpStepNotify})
	}
	if op.OnboardingID == "" {
		resolved, err := c.Store.ResolveConnectorTarget(auth, target)
		if err != nil {
			return api.OperationResult{}, err
		}
		op.OnboardingID = resolved.OnboardingID
	}
	onboarding, err := c.Store.OnboardingFor(auth, op.OnboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	result := api.OperationResult{OperationID: op.OperationID, Summary: "reauth " + op.Target}
	if onboarding.Phase == api.PhaseEnabled && (op.State == api.OpStateQueued || op.State == api.OpStateRunning) {
		// Reopen credential collection: the protected form rotates in the
		// new values; the binding stays until re-confirmation.
		onboarding.Phase = api.PhaseAwaitingCreds
		onboarding.FormNonce = ""
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return api.OperationResult{}, err
		}
	}
	if onboarding.Phase == api.PhaseAwaitingCreds {
		if onboarding.FormNonce == "" || !c.now().Before(onboarding.FormExpires) {
			onboarding.FormNonce = api.RandomNonce()
			onboarding.FormExpires = c.now().Add(c.ttl())
			onboarding.Revision++
			if err := c.Store.PutOnboarding(onboarding); err != nil {
				return api.OperationResult{}, err
			}
			api.PostPrepareNotice(ctx, auth, onboarding, "", 0)
		}
		op.State = api.OpStateAwaitingUser
		result.State = op.State
		result.LinkSent = true
	} else if onboarding.Phase == api.PhaseEnabled {
		markStep(&op, api.OpStepCollectCredentials, api.OpStepDone)
		markStep(&op, api.OpStepNotify, api.OpStepDone)
		op.State = api.OpStateDone
		result.State = op.State
	} else {
		op.State = api.OpStateRunning
		result.State = op.State
	}
	if err := c.Store.PutOperation(op); err != nil {
		return api.OperationResult{}, err
	}
	return result, nil
}

// connectorAllowWrites routes write-enable through the human confirm link;
// switching writes off is immediate. The operation resumes done once the
// onboarding's WritesEnabled reflects the request.
func (c *ControlPlane) connectorAllowWrites(ctx context.Context, auth identity.Envelope, args map[string]any) (api.OperationResult, error) {
	target := strings.TrimSpace(api.ArgString(args, "target"))
	if target == "" {
		return api.OperationResult{}, fmt.Errorf("%w: connector target", api.ErrInvalid)
	}
	on, _ := args["on"].(bool)
	op, exists := c.Store.OperationForRequestKey(auth, api.OpIntentAllowWrites, target)
	if !exists {
		op = c.newConnectorOperation(auth, api.OpIntentAllowWrites, target, []string{api.OpStepConfirm, api.OpStepApply, api.OpStepNotify})
	}
	if op.OnboardingID == "" {
		resolved, err := c.Store.ResolveConnectorTarget(auth, target)
		if err != nil {
			return api.OperationResult{}, err
		}
		op.OnboardingID = resolved.OnboardingID
	}
	onboarding, err := c.Store.OnboardingFor(auth, op.OnboardingID)
	if err != nil {
		return api.OperationResult{}, err
	}
	result := api.OperationResult{OperationID: op.OperationID, Summary: "allow_writes " + op.Target}
	if onboarding.WritesEnabled == on {
		markStep(&op, api.OpStepConfirm, api.OpStepDone)
		markStep(&op, api.OpStepApply, api.OpStepDone)
		markStep(&op, api.OpStepNotify, api.OpStepDone)
		op.State = api.OpStateDone
		result.State = api.OpStateDone
	} else if !on {
		onboarding.WritesEnabled = false
		onboarding.PendingWrites = false
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return api.OperationResult{}, err
		}
		markStep(&op, api.OpStepApply, api.OpStepDone)
		markStep(&op, api.OpStepNotify, api.OpStepDone)
		op.State = api.OpStateDone
		result.State = api.OpStateDone
	} else {
		// on: arm the same one-time /confirm link the enablement flow uses —
		// the page branches on PendingWrites.
		onboarding.PendingWrites = true
		onboarding.Phase = api.PhaseAwaitingConfirm
		onboarding.ConfirmationNonce = api.RandomNonce()
		onboarding.ConfirmationExpires = c.now().Add(c.ttl())
		onboarding.ConfirmationUsed = false
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return api.OperationResult{}, err
		}
		api.PostPrepareNotice(ctx, auth, onboarding, "", 0)
		markStep(&op, api.OpStepConfirm, api.OpStepDone)
		op.State = api.OpStateAwaitingConfirm
		result.State = api.OpStateAwaitingConfirm
		result.LinkSent = true
	}
	if err := c.Store.PutOperation(op); err != nil {
		return api.OperationResult{}, err
	}
	return result, nil
}

// applyConfirmedWrites is the human-side completion of an allow_writes link:
// called from serveConfirm once the one-time token checks out.
func (c *ControlPlane) applyConfirmedWrites(ctx context.Context, onboardingID string) error {
	onboarding, err := c.Store.onboarding(onboardingID)
	if err != nil {
		return err
	}
	if !onboarding.PendingWrites {
		return fmt.Errorf("%w: writes confirm", api.ErrInvalid)
	}
	onboarding.WritesEnabled = true
	onboarding.PendingWrites = false
	onboarding.Phase = api.PhaseEnabled
	onboarding.ConfirmationUsed = true
	onboarding.Revision++
	return c.Store.PutOnboarding(onboarding)
}

// prepareInstallTarget infers the connector kind from the target itself —
// the agent never picks between definition_id/source/remote_url. A
// registered catalog definition goes to the catalog path; a prepared id or
// alias resolves to its reviewed pinned source and runs the self-install
// pipeline.
func (c *ControlPlane) prepareInstallTarget(ctx context.Context, auth identity.Envelope, target, version, requestKey string) (map[string]any, error) {
	if definition, err := c.Store.Definition(target, version); err == nil {
		return c.prepareCatalog(ctx, auth, definition.DefinitionID, definition.Version, requestKey)
	}
	entries, err := api.PreparedCatalog()
	if err != nil {
		return nil, err
	}
	normalized := strings.ToLower(target)
	for _, entry := range entries {
		if strings.ToLower(entry.ID) != normalized && !slices.ContainsFunc(entry.Aliases, func(a string) bool { return strings.ToLower(a) == normalized }) {
			continue
		}
		source := entry.Source.Repository
		if entry.Source.CommitSHA != "" {
			source += "/tree/" + entry.Source.CommitSHA
			if entry.Source.Subfolder != "" {
				source += "/" + entry.Source.Subfolder
			}
		}
		return c.prepareSelfInstallAsync(ctx, auth, source, requestKey, nil, false, "")
	}
	return nil, fmt.Errorf("%w: connector target %s", api.ErrNotFound, target)
}
