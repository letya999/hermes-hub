package toolhub

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// requireMemoryAdmission is the SPEC-0098 M2 gate on the shared call path:
// published memory enablement first, then the exact-run check against the
// lifecycle authority. It runs before waiting for the vault and again inside
// AuthorizeProjectedCall, so direct and nested calls deny before backend access.
func (g *Gateway) requireMemoryAdmission(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation) error {
	if err := memoryEnrollment(ctx, g.Spawn, auth.PrincipalID, auth.Environment); err != nil {
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
	record, policy, err := g.checkedRun(ctx, auth, corr)
	if err != nil || !api.MayUseOwnerMemory(record) {
		return fmt.Errorf("%w: run context", api.ErrUnauthorized)
	}
	if policy != nil {
		memoryRun, _, err := task.AuthorizeRun(ctx, g.RunIdentity, g.Runs, corr.MemoryToken, contract.MemoryAudience, auth.Environment, corr.HermesRunID, time.Now())
		if err != nil || memoryRun.JobID != record.JobID || memoryRun.PolicyRevision != record.PolicyRevision {
			return fmt.Errorf("%w: memory run authority", api.ErrUnauthorized)
		}
		if !memoryPolicyAllows(*policy, record.ScopeID) {
			return fmt.Errorf("%w: memory policy", api.ErrUnauthorized)
		}
	}
	return nil
}

func memoryPolicyAllows(policy contract.PolicyBundle, scope string) bool {
	scopes := policy.Identity.Restrictions.MemoryScopes
	doc, err := spawnv1.Decode("config", policy.Passports["config"])
	if err != nil || (scopes != nil && !slices.Contains(scopes, scope)) {
		return false
	}
	memory := doc.(*spawnv1.ConfigDoc).Memory
	return memory.Enabled != nil && *memory.Enabled
}

// lookupLiveRun is the shared run-verification gate for every call that
// carries job/run correlation: the supervisor's durable record must exist,
// be live, run on the current generation and match the caller's principal,
// context, runtime and environment. Correlation selects the record; the
// record supplies the authority. A just-started assignment may call before
// its first run event is persisted, so a matching active job waits briefly
// for its run ID — a different ID, status, owner or generation denies at
// once.
func (g *Gateway) lookupLiveRun(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation) (api.RunContext, error) {
	record, _, err := g.checkedRun(ctx, auth, corr)
	return record, err
}

func (g *Gateway) checkedRun(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation) (api.RunContext, *contract.PolicyBundle, error) {
	deny := fmt.Errorf("%w: run context", api.ErrUnauthorized)
	if g.Runs == nil || corr.JobID == "" || corr.HermesRunID == "" {
		return api.RunContext{}, nil, deny
	}
	matches := func(record api.RunContext) bool {
		return record.JobID == corr.JobID && record.Environment == auth.Environment &&
			record.Envelope.PrincipalID == auth.PrincipalID &&
			record.Envelope.ContextID == auth.ContextID &&
			record.Envelope.RuntimeID == auth.RuntimeID
	}
	if corr.RunToken != "" {
		record, policy, err := task.AuthorizeRun(ctx, g.RunIdentity, g.Runs, corr.RunToken, contract.ToolHubAudience, auth.Environment, corr.HermesRunID, time.Now())
		org := record.ActingIdentity.Organization
		if org == "" {
			org = "personal"
		}
		if err != nil || !matches(record) || (auth.Organization != "" && auth.Organization != org) {
			return api.RunContext{}, nil, deny
		}
		return record, &policy, nil
	}
	if (g.RunIdentity != nil && corr.Scoped) || corr.Delegation != nil || corr.MemoryToken != "" {
		return api.RunContext{}, nil, deny
	}
	valid := func(record api.RunContext) bool {
		return matches(record) && record.Status == "running" && !record.Revoked && record.PolicyRevision == "" &&
			record.RuntimeGeneration != "" && record.RuntimeGeneration == record.CurrentGeneration
	}
	record, err := g.Runs.Run(ctx, corr.JobID)
	if err != nil || !valid(record) {
		return api.RunContext{}, nil, deny
	}
	for attempts := 0; record.Assignment && record.RunID == "" && attempts < 20; attempts++ {
		select {
		case <-ctx.Done():
			return api.RunContext{}, nil, deny
		case <-time.After(100 * time.Millisecond):
		}
		record, err = g.Runs.Run(ctx, corr.JobID)
		if err != nil || !valid(record) {
			return api.RunContext{}, nil, deny
		}
	}
	if record.RunID == "" || record.RunID != corr.HermesRunID {
		return api.RunContext{}, nil, deny
	}
	return record, nil, nil
}

// liveRunConversation binds conversation-scoped agent tools to the supervisor's
// active run record. Correlation headers select a record but cannot supply the
// conversation or keep a finished run alive.
func (g *Gateway) liveRunConversation(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation) (string, error) {
	record, err := g.lookupLiveRun(ctx, auth, corr)
	if err != nil || record.Envelope.ConversationID == "" {
		return "", fmt.Errorf("%w: run context", api.ErrUnauthorized)
	}
	return record.Envelope.ConversationID, nil
}

// runAuthorityFromEnv keeps an absent control peer fail-closed for run calls.
func runAuthorityFromEnv() task.Client {
	if origin, key := runVerificationPeer(); origin != "" {
		return task.NewHTTPClient(origin, key, nil)
	}
	url := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL")), "/")
	token := strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_AUTH"))
	if url == "" || token == "" {
		return nil
	}
	return task.NewHTTPClient(url, token, nil)
}

func runVerificationPeer() (string, string) {
	origin := strings.TrimSpace(os.Getenv("HUB_RUN_VERIFY_URL"))
	key := strings.TrimSpace(os.Getenv("HUB_RUN_VERIFY_AUTH"))
	if origin != "" && key == "" {
		key = task.VerificationCredential(automationServiceToken())
	}
	return origin, key
}
