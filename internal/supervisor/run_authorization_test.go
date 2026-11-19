package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/identityhub"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestOwnedRegistryRefusesUnsignedRuntimeAdmission(t *testing.T) {
	calls := 0
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("unexpected runtime call")
	}, nil)
	registry := m.cfg.Identity.(*identityhub.Registry)
	if err := registry.EnsureSigningKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.cfg.Identity, m.ownedIdentity = nil, registry
	const body = `{"identity_schema":1,"principal_id":"alice","external_identity_id":"telegram-1","conversation_id":"telegram-1","delivery_target_id":"telegram-1","context_id":"alice","runtime_id":"alice","policy_version":"policy-1","organization_id":"personal","user_id":"alice","actor_id":"alice","scope_id":"user:alice","channel":"telegram_bot","trigger":"message","idempotency_key":"one","job_id":"job-1","text":"synthetic message"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+m.cfg.RuntimeAuth)
	out := httptest.NewRecorder()
	m.Handler().ServeHTTP(out, request)
	if out.Code != http.StatusServiceUnavailable || out.Header().Get("X-Hub-Admission") != "not-dispatched" || calls != 0 {
		t.Fatalf("unsigned run crossed admission: status=%d runtime_calls=%d", out.Code, calls)
	}
	if _, err := m.Run(t.Context(), "job-1"); !errors.Is(err, task.ErrNotFound) {
		t.Fatal("unsigned refusal consumed the durable job ID")
	}
}

func TestSignedRunCannotReachEffectAfterAuthorityChanges(t *testing.T) {
	ctx := t.Context()
	registry, _ := fixtureIdentity(t)
	if err := registry.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	const job, run, revision = "job-1", "run-1", "passport-1"
	const generation, environment, audience = "gen-1", "dev", "tool-hub"
	auth := identity.TelegramEnvelope("alice", 7, "alice", "policy-1")
	binding := Binding{PrincipalID: "alice", SubjectID: "alice", AgentID: "agent:alice:default",
		ContextID: "alice", RuntimeMode: "gateway", ScopeID: "user:alice", OrganizationID: "personal"}
	key := runtimeKey(binding)
	stateDir := t.TempDir()
	m := &Manager{cfg: Config{Identity: registry, Environment: environment, RuntimeAuth: "synthetic-reader", Now: time.Now, StateDir: stateDir},
		statePath: filepath.Join(stateDir, "supervisor.json"),
		items:     map[string]*runtimeEntry{key: {Runtime: Runtime{Generation: generation}}}}
	_, templateRevision, err := m.identityTemplate(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	bundle := contract.PolicyBundle{Space: binding.ContextID, Environment: environment, PassportRevision: revision, Passports: map[string]json.RawMessage{}}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		bundle.Passports[name] = json.RawMessage(fmt.Sprintf(`{"kind":%q,"metadata":{"space":"alice","environment":"dev","revision":%q}}`, name, revision))
	}
	staged, err := registry.StagePassports(ctx, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePassports(ctx, binding.ContextID, environment, "", staged.Digest); err != nil {
		t.Fatal(err)
	}
	signed, _, err := registry.BindPassports(ctx, contract.TemplateRequest{Subject: binding.SubjectID, Actor: binding.AgentID, Space: binding.ContextID}, environment)
	if err != nil {
		t.Fatal(err)
	}
	record := jobRecord{Status: "running", Generation: generation, RunDeadline: time.Now().Add(time.Minute),
		ActingIdentity: task.ActingIdentity{Subject: binding.SubjectID, Actor: binding.AgentID, Organization: binding.OrganizationID, TemplateRevision: templateRevision},
		PolicyRevision: signed.Digest, PassportDigest: staged.Digest, Initiator: "system:tool-hub",
		Request: hubruntime.ExecuteRequest{Envelope: auth, ScopeID: binding.ScopeID, JobID: job, TaskID: "task-1", Channel: "telegram_bot",
			Assignment: true, AssignmentID: "assignment-1", AssignmentPlanRevision: 3},
		Response: hubruntime.ExecuteResponse{RunID: run}}
	m.jobs = map[string]jobRecord{job: record}
	server := httptest.NewServer(m.Handler())
	defer server.Close()
	identities := contract.NewHTTPClient(server.URL, m.cfg.RuntimeAuth, server.Client())
	runs := task.NewHTTPClient(server.URL, m.cfg.RuntimeAuth, server.Client())
	derivedKey := task.VerificationCredential(m.cfg.RuntimeAuth)
	if _, err := contract.NewHTTPClient(server.URL, derivedKey, server.Client()).SigningKeys(ctx); err != nil {
		t.Fatalf("scoped reader could not load public keys: %v", err)
	}
	reqHTTP := httptest.NewRequest(http.MethodPost, "/v1/execute", nil)
	reqHTTP.Header.Set("Authorization", "Bearer "+derivedKey)
	refusal := httptest.NewRecorder()
	m.Handler().ServeHTTP(refusal, reqHTTP)
	if refusal.Code != http.StatusForbidden {
		t.Fatal("read-only verifier credential reached execution")
	}
	current, err := runs.Run(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	grant := make(chan hubruntime.RunAuthorization, 1)
	runtimeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var received hubruntime.RunAuthorization
		bound, err := runs.Run(r.Context(), job)
		if err != nil || bound.RunID != run || r.URL.Path != hubruntime.RunAuthorizationPath ||
			r.Header.Get("Authorization") != "Bearer synthetic-runtime" ||
			r.Header.Get(hubruntime.RunDeliveryHeader) != m.runDeliveryProof(record) || json.NewDecoder(r.Body).Decode(&received) != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		grant <- received
		w.WriteHeader(http.StatusNoContent)
	}))
	defer runtimeServer.Close()
	m.cfg.HTTP = runtimeServer.Client()
	if current.RunID != run {
		t.Fatal("fixture cannot replay authorized run")
	}
	// The producer starts without a run ID and publishes it through the
	// same admission event as the production runtime stream.
	pending := record
	pending.Response.RunID = ""
	m.mu.Lock()
	m.jobs[job] = pending
	m.mu.Unlock()
	var stream strings.Builder
	encoder := json.NewEncoder(&stream)
	for _, event := range []hubruntime.ExecuteResponse{
		{JobID: job, RunID: run, SessionID: "session-1", RuntimeGeneration: generation, Status: "running", LastEvent: "run.admitted"},
		{JobID: job, RunID: run, SessionID: "session-1", RuntimeGeneration: generation, Status: "completed", LastEvent: "run.completed"},
	} {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	result := m.consumeJobStream(ctx, record.Request, Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeMode: "gateway",
		Generation: generation, Address: runtimeServer.URL}, "synthetic-runtime", strings.NewReader(stream.String()), nil)
	if result.Status != "completed" {
		t.Fatalf("signed dispatch did not complete: %s", result.Status)
	}
	var token string
	select {
	case delivered := <-grant:
		if delivered.RunID != run || delivered.Generation != generation || delivered.JobID != job || len(delivered.Tokens) != 5 {
			t.Fatal("private delivery did not preserve exact run scope")
		}
		token = delivered.Tokens[audience]
	default:
		t.Fatal("admitted run did not receive signed access")
	}
	m.mu.Lock()
	m.jobs[job] = record
	m.mu.Unlock()
	effects := 0
	call := func(token, recipient, requestedRun string) error {
		_, _, err := task.AuthorizeRun(ctx, identities, runs, token, recipient, environment, requestedRun, time.Now())
		if err == nil {
			effects++
		}
		return err
	}
	if err := call(token, audience, run); err != nil || effects != 1 {
		t.Fatalf("valid execution refused, effects=%d err=%v", effects, err)
	}
	effects = 0
	header, rest, _ := strings.Cut(token, ".")
	payload, signature, _ := strings.Cut(rest, ".")
	changed := byte('A')
	if signature[0] == changed {
		changed = 'B'
	}
	for _, tc := range []struct {
		name, token, audience, run string
		change                     func(*jobRecord)
	}{
		{name: "missing", audience: audience, run: run},
		{name: "forged", token: header + "." + payload + "." + string(changed) + signature[1:], audience: audience, run: run},
		{name: "run alias", token: token, audience: audience, run: "run-other"},
		{name: "wrong audience", token: token, audience: "model-gateway", run: run},
		{name: "policy revision changed", token: token, audience: audience, run: run, change: func(r *jobRecord) {
			r.PolicyRevision = contract.PolicyDigest([]byte("other"))
		}},
		{name: "space changed", token: token, audience: audience, run: run, change: func(r *jobRecord) {
			r.Request.ContextID = "other"
			r.Request.ScopeID = "user:other"
		}},
		{name: "assignment replaced", token: token, audience: audience, run: run, change: func(r *jobRecord) {
			r.Request.AssignmentPlanRevision++
		}},
		{name: "cancel requested", token: token, audience: audience, run: run, change: func(r *jobRecord) {
			r.CancelRequested = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := record
			if tc.change != nil {
				tc.change(&candidate)
			}
			m.mu.Lock()
			m.jobs[job] = candidate
			m.mu.Unlock()
			if err := call(tc.token, tc.audience, tc.run); !errors.Is(err, task.ErrForbidden) || effects != 0 {
				t.Fatalf("invalid run reached effect, effects=%d err=%v", effects, err)
			}
		})
	}
	m.mu.Lock()
	m.jobs[job] = record
	m.items[key].Generation = "gen-2"
	m.mu.Unlock()
	if err := call(token, audience, run); !errors.Is(err, task.ErrForbidden) || effects != 0 {
		t.Fatalf("stale generation reached effect, effects=%d err=%v", effects, err)
	}
	m.mu.Lock()
	m.items[key].Generation = generation
	m.mu.Unlock()
	if err := registry.ActivatePassports(ctx, binding.ContextID, environment, staged.Digest, ""); err != nil {
		t.Fatal(err)
	}
	if err := call(token, audience, run); !errors.Is(err, task.ErrForbidden) || effects != 0 {
		t.Fatalf("removed publication reached effect, effects=%d err=%v", effects, err)
	}
}
