package toolhub

import (
	"context"
	"fmt"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// requireMemoryAdmission is the SPEC-0098 M2 gate on the shared call path:
// published memory enablement first, then the exact-run check against the
// lifecycle authority. It runs before waiting for the vault and again inside
// AuthorizeProjectedCall, so direct and nested calls deny before backend access.
func (g *Gateway) requireMemoryAdmission(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation) error {
	if err := memoryEnrollment(g.PassportRoot, auth.PrincipalID, auth.Environment); err != nil {
		return err
	}
	return g.requireLiveMemoryRun(ctx, auth, corr)
}

// requireLiveMemoryRun verifies the claimed job/run pair against the
// supervisor's durable record: the record must be a live private or owner
// assignment run bound
// to the caller's principal, context, runtime, environment and current
// generation. The correlation claim selects the record only — authority
// comes from the record's stored fields, never from headers or arguments.
func (g *Gateway) requireLiveMemoryRun(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation) error {
	deny := fmt.Errorf("%w: run context", api.ErrUnauthorized)
	if g.Runs == nil || corr.JobID == "" || corr.HermesRunID == "" {
		return deny
	}
	record, err := g.Runs.Lookup(ctx, corr.JobID)
	if err != nil {
		return deny
	}
	valid := func(record api.RunContext) bool {
		return record.JobID == corr.JobID && record.Status == "running" &&
			record.RuntimeGeneration != "" && record.RuntimeGeneration == record.CurrentGeneration &&
			record.Environment == auth.Environment && record.MayUseOwnerMemory() &&
			record.Envelope.PrincipalID == auth.PrincipalID &&
			record.Envelope.ContextID == auth.ContextID &&
			record.Envelope.RuntimeID == auth.RuntimeID
	}
	if !valid(record) {
		return deny
	}
	// A just-started assignment may call a tool before the supervisor has
	// persisted its first run event. Wait only for this same active job to gain
	// its run ID; a different ID, status, owner or generation denies at once.
	for attempts := 0; record.Assignment && record.RunID == "" && attempts < 20; attempts++ {
		select {
		case <-ctx.Done():
			return deny
		case <-time.After(100 * time.Millisecond):
		}
		record, err = g.Runs.Lookup(ctx, corr.JobID)
		if err != nil || !valid(record) {
			return deny
		}
	}
	if record.RunID == "" || record.RunID != corr.HermesRunID {
		return deny
	}
	return nil
}

// liveRunConversation binds conversation-scoped agent tools to the supervisor's
// active run record. Correlation headers select a record but cannot supply the
// conversation or keep a finished run alive.
func (g *Gateway) liveRunConversation(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation) (string, error) {
	deny := fmt.Errorf("%w: run context", api.ErrUnauthorized)
	if g.Runs == nil || corr.JobID == "" || corr.HermesRunID == "" {
		return "", deny
	}
	record, err := g.Runs.Lookup(ctx, corr.JobID)
	if err != nil || record.JobID != corr.JobID || record.RunID != corr.HermesRunID || record.Status != "running" ||
		record.RuntimeGeneration == "" || record.RuntimeGeneration != record.CurrentGeneration ||
		record.Environment != auth.Environment || record.Envelope.PrincipalID != auth.PrincipalID ||
		record.Envelope.ContextID != auth.ContextID || record.Envelope.RuntimeID != auth.RuntimeID ||
		record.Envelope.ConversationID == "" {
		return "", deny
	}
	return record.Envelope.ConversationID, nil
}
