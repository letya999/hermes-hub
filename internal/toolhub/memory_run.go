package toolhub

import (
	"context"
	"fmt"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// requireMemoryAdmission is the SPEC-0098 M2 gate on the shared call path:
// published memory enablement first, then the exact-run check against the
// lifecycle authority. It sits inside AuthorizeProjectedCall, so direct and
// nested calls share it and every denial lands before the backend or vault.
func (g *Gateway) requireMemoryAdmission(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation) error {
	if err := memoryEnrollment(g.PassportRoot, auth.PrincipalID, auth.Environment); err != nil {
		return err
	}
	return g.requireLivePrivateRun(ctx, auth, corr)
}

// requireLivePrivateRun verifies the claimed job/run pair against the
// supervisor's durable record: the record must be a live private run bound
// to the caller's principal, context, runtime, environment and current
// generation. The correlation claim selects the record only — authority
// comes from the record's stored fields, never from headers or arguments.
func (g *Gateway) requireLivePrivateRun(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation) error {
	deny := fmt.Errorf("%w: run context", api.ErrUnauthorized)
	if g.Runs == nil || corr.JobID == "" || corr.HermesRunID == "" {
		return deny
	}
	record, err := g.Runs.Lookup(ctx, corr.JobID)
	if err != nil {
		return deny
	}
	if record.JobID != corr.JobID || record.RunID == "" || record.RunID != corr.HermesRunID ||
		record.Status != "running" ||
		record.RuntimeGeneration == "" || record.RuntimeGeneration != record.CurrentGeneration ||
		record.Environment != auth.Environment || record.Kind() != api.RunKindPrivate ||
		record.Envelope.PrincipalID != auth.PrincipalID ||
		record.Envelope.ContextID != auth.ContextID ||
		record.Envelope.RuntimeID != auth.RuntimeID {
		return deny
	}
	return nil
}
