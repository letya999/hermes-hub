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
