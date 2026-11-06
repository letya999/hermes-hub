package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// runAuthorityFunc fakes the lifecycle authority seam — the supervisor job
// record the exact-run check consumes. Everything else on the path is real:
// store authorization, audit fences, the passport gate and the backend fence.
type runAuthorityFunc func(ctx context.Context, jobID string) (api.RunContext, error)

func (f runAuthorityFunc) Lookup(ctx context.Context, jobID string) (api.RunContext, error) {
	return f(ctx, jobID)
}

// memoryToolFixture registers a synthetic memory.search capability admitted
// for auth — the same admission shape the passport test builds, shared so
// each gate test only varies the gate under focus.
func memoryToolFixture(t *testing.T) (*Store, identity.Envelope) {
	t.Helper()
	store, auth, policy, profile := managedStore(t)
	definition := agentDefinition()
	definition.Tools = []api.ToolSpec{{
		Name: "memory_search", CapabilityID: "memory.search", Requires: []string{"memory.search"},
		Effect: api.ReadEffect, Audience: api.AudienceModel, Executor: "handler:memory",
		Description: "Search a memory fixture.", Uses: []api.CapabilityUse{{Action: "read", Resource: "memory"}},
	}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	rule := api.CapabilityRule{CapabilityID: "memory.search", ImplementationDigest: digest,
		Action: "read", Resource: "memory", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy.Revision++
	policy.Ceiling = append(policy.Ceiling, rule)
	policy.Defaults = append(policy.Defaults, rule)
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	profile.PassportRevision = "r-0123456789abcdee"
	profile.Selections = append(profile.Selections, api.CapabilitySelection{CapabilityID: "memory.search", DefinitionID: definition.DefinitionID,
		DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "memory_search", Name: "memory_search"})
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	grantDefinitionRights(t, store, auth, definition)
	return store, auth
}

// privateRunRecord is the supervisor record for a live interactive owner
// run — the shape /v1/jobs/<id> returns after the M2 extension.
func privateRunRecord(auth identity.Envelope, jobID, runID string) api.RunContext {
	return api.RunContext{
		JobID: jobID, RunID: runID, SessionID: "session-1",
		RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
		Environment: auth.Environment, Status: "running",
		ScopeID: "user:" + auth.PrincipalID, Trigger: "message", Channel: "telegram_bot",
		Envelope: identity.Envelope{Schema: auth.Schema, PrincipalID: auth.PrincipalID,
			ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion},
	}
}

// TestMemoryRunCannotBorrowOwnerAuthority proves the exact-run admission
// gate (SPEC-0098 M2): a memory.* call must claim a job/run pair whose
// supervisor record is a live private run bound to the caller's verified
// envelope. A concurrent private run never authorizes a sibling group run;
// forged, foreign, stale or replayed claims all fail before the backend.
func TestMemoryRunCannotBorrowOwnerAuthority(t *testing.T) {
	store, auth := memoryToolFixture(t)
	passports := t.TempDir()
	writeMemoryPassport(t, passports, "alice", true)

	ownerEnv := identity.Envelope{PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID}
	records := map[string]api.RunContext{
		"job-private": privateRunRecord(auth, "job-private", "run-private-1"),
		// Same owner, concurrent group run — never reaches private memory.
		"job-group": {JobID: "job-group", RunID: "run-group-1", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: auth.Environment, Status: "running", ScopeID: "group:alice", Trigger: "message", Channel: "telegram_bot", Envelope: ownerEnv},
		// Same owner, private scope, autonomous trigger — background type.
		"job-goal": {JobID: "job-goal", RunID: "run-goal-1", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: auth.Environment, Status: "running", ScopeID: "user:alice", Trigger: "goal", Channel: "telegram_bot", Envelope: ownerEnv},
		// Protected system form on the owner's runtime — system type.
		"job-sys": {JobID: "job-sys", RunID: "run-sys-1", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: auth.Environment, Status: "running", ScopeID: "user:alice", Trigger: "credential-form", Channel: "communication", Envelope: ownerEnv},
		// Another principal's live private run.
		"job-other": {JobID: "job-other", RunID: "run-other-1", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: auth.Environment, Status: "running", ScopeID: "user:bob", Trigger: "message", Channel: "telegram_bot",
			Envelope: identity.Envelope{PrincipalID: "bob", ContextID: "bob", RuntimeID: "runtime"}},
		// A run bound to a superseded runtime generation.
		"job-stale-gen": {JobID: "job-stale-gen", RunID: "run-stalegen-1", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-9",
			Environment: auth.Environment, Status: "running", ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot", Envelope: ownerEnv},
		// A run recorded under a different environment.
		"job-env": {JobID: "job-env", RunID: "run-env-1", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: "prod", Status: "running", ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot", Envelope: ownerEnv},
		// A private run of another context/runtime than the caller's.
		"job-ctx": {JobID: "job-ctx", RunID: "run-ctx-1", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: auth.Environment, Status: "running", ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot",
			Envelope: identity.Envelope{PrincipalID: auth.PrincipalID, ContextID: "other-context", RuntimeID: auth.RuntimeID}},
		"job-rt": {JobID: "job-rt", RunID: "run-rt-1", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: auth.Environment, Status: "running", ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot",
			Envelope: identity.Envelope{PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: "other-runtime"}},
		// The authority answers a record that names a different job.
		"job-alias": {JobID: "job-private", RunID: "run-alias-1", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: auth.Environment, Status: "running", ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot", Envelope: ownerEnv},
		// An admitted job that never produced a run id.
		"job-norun": {JobID: "job-norun", RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: auth.Environment, Status: "running", ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot", Envelope: ownerEnv},
	}
	for _, finished := range []struct{ jobID, runID, status string }{
		{"job-done", "run-done-1", "completed"},
		{"job-uncertain", "run-uncertain-1", "uncertain"},
		{"job-approval", "run-approval-1", "waiting_for_approval"},
	} {
		record := privateRunRecord(auth, finished.jobID, finished.runID)
		record.Status = finished.status
		records[finished.jobID] = record
	}

	calls := 0
	g := &Gateway{Store: store, PassportRoot: passports,
		AuditWrite: func(string, map[string]string) error { return nil },
		Runs: runAuthorityFunc(func(_ context.Context, jobID string) (api.RunContext, error) {
			record, ok := records[jobID]
			if !ok {
				return api.RunContext{}, errors.New("unknown job")
			}
			return record, nil
		}),
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			calls++
			return api.BackendResult{Text: "found"}, nil
		})}

	call := func(corr api.CallCorrelation, args map[string]any) error {
		ctx := api.WithCallCorrelation(t.Context(), corr)
		_, err := g.CallAuthorized(ctx, auth, "memory_search", args)
		return err
	}

	// The valid live private run admits the call and reaches the backend.
	if err := call(api.HeaderCorrelation("job-private", "run-private-1"), nil); err != nil || calls != 1 {
		t.Fatalf("live private run denied: calls=%d err=%v", calls, err)
	}

	// Every denial must land before the backend.
	for _, denied := range []struct {
		name string
		corr api.CallCorrelation
		args map[string]any
	}{
		{"group run of same owner", api.HeaderCorrelation("job-group", "run-group-1"), nil},
		{"background goal run", api.HeaderCorrelation("job-goal", "run-goal-1"), nil},
		{"system credential-form run", api.HeaderCorrelation("job-sys", "run-sys-1"), nil},
		{"another user's private run", api.HeaderCorrelation("job-other", "run-other-1"), nil},
		{"superseded generation", api.HeaderCorrelation("job-stale-gen", "run-stalegen-1"), nil},
		{"wrong environment", api.HeaderCorrelation("job-env", "run-env-1"), nil},
		{"run of another context", api.HeaderCorrelation("job-ctx", "run-ctx-1"), nil},
		{"run of another runtime", api.HeaderCorrelation("job-rt", "run-rt-1"), nil},
		{"authority record names another job", api.HeaderCorrelation("job-alias", "run-alias-1"), nil},
		{"completed run", api.HeaderCorrelation("job-done", "run-done-1"), nil},
		{"uncertain/stale run", api.HeaderCorrelation("job-uncertain", "run-uncertain-1"), nil},
		{"approval-blocked run", api.HeaderCorrelation("job-approval", "run-approval-1"), nil},
		{"forged run id on a live job", api.HeaderCorrelation("job-private", "run-other-1"), nil},
		{"unknown job", api.HeaderCorrelation("job-ghost", "run-x"), nil},
		{"job claim without run id", api.HeaderCorrelation("job-private", ""), nil},
		{"run claim without job id", api.HeaderCorrelation("", "run-private-1"), nil},
		{"no run claim at all", api.CallCorrelation{}, nil},
		{"run id smuggled in arguments", api.CallCorrelation{}, map[string]any{"run_id": "run-private-1", "job_id": "job-private"}},
		{"run claim on a runless job", api.HeaderCorrelation("job-norun", "run-x"), nil},
	} {
		before := calls
		if err := call(denied.corr, denied.args); err == nil || calls != before {
			t.Fatalf("%s reached backend: calls=%d err=%v", denied.name, calls, err)
		}
	}

	// With no authority configured the gate denies even a well-formed claim —
	// an absent lifecycle record is never an admit.
	g.Runs = nil
	if err := call(api.HeaderCorrelation("job-private", "run-private-1"), nil); err == nil || calls != 1 {
		t.Fatalf("unconfigured authority admitted: calls=%d err=%v", calls, err)
	}
}

// The production startup path consumes the rendered supervisor control
// channel: with HUB_RUNTIME_SUPERVISOR_URL and HUB_COMMUNICATION_AUTH set,
// NewEndpointHandler must expose a working run authority that reaches
// /v1/jobs/<id>; without them the gateway carries none and the gate denies.
func TestEndpointHandlerWiresRunAuthority(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	var gotPath, gotAuthHeader string
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuthHeader = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"job_id": "job-1", "status": "completed"})
	}))
	defer supervisor.Close()
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", supervisor.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "control-token")
	config := EndpointConfig{Token: strings.Repeat("t", 32), Auth: auth,
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		})}
	handler, err := NewEndpointHandler(config, store)
	if err != nil {
		t.Fatal(err)
	}
	runs := handler.(*projectionEndpoint).gateway.Runs
	if runs == nil {
		t.Fatal("production wiring dropped the run authority")
	}
	rc, err := runs.Lookup(context.Background(), "job-1")
	if err != nil || rc.JobID != "job-1" || rc.Status != "completed" {
		t.Fatalf("wired authority lookup failed: %+v err=%v", rc, err)
	}
	if gotPath != "/v1/jobs/job-1" || gotAuthHeader != "Bearer control-token" {
		t.Fatalf("authority called %q auth=%q", gotPath, gotAuthHeader)
	}
}
