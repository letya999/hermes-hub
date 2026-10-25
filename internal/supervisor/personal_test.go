package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A burst of related incidents for one scope must collapse into a single
// supervisor execution; a scope with nothing actionable never wakes it.
func TestSupervisorWakeCoalesces(t *testing.T) {
	var calls atomic.Int64
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		calls.Add(1)
		return PersonalDecision{Action: "inspect", Reason: "ok"}, nil
	}
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{}}
	m.items["alice\x00alice\x00gateway"] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.recordIncidentLocked(entry, "wedged")
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("decisions=%d, want one coalesced call", calls.Load())
	}
	// A scope with no incidents and no retained demand never calls the model.
	m.drainPersonal(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("idle scope woke supervisor: calls=%d", calls.Load())
	}
}

// A wake arriving while Decide is in flight must not start a second
// concurrent run for the same scope; it re-queues and runs after.
func TestSupervisorSingleRun(t *testing.T) {
	release := make(chan struct{})
	var calls, running, maxRunning atomic.Int64
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		calls.Add(1)
		if n := running.Add(1); n > maxRunning.Load() {
			maxRunning.Store(n)
		}
		defer running.Add(-1)
		<-release
		return PersonalDecision{Action: "inspect"}, nil
	}
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{}}
	m.items["alice\x00alice\x00gateway"] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.drainPersonal(context.Background()); close(done) }()
	// Wait for Decide to be in flight, then queue another wake for the scope.
	for running.Load() == 0 {
	}
	m.mu.Lock()
	m.queuePersonalWakeLocked("alice\x00alice\x00gateway", "wedged")
	m.mu.Unlock()
	go func() { m.drainPersonal(context.Background()) }()
	close(release)
	<-done
	for calls.Load() < 2 {
	}
	if maxRunning.Load() != 1 {
		t.Fatalf("concurrent runs for one scope: max=%d", maxRunning.Load())
	}
}

// A decision naming another scope's lease is denied server-side; the foreign
// lease is never touched.
func TestSupervisorCannotCrossScope(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	var calls atomic.Int64
	target := m.cfg.Now().Add(25 * time.Minute)
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		if calls.Add(1) == 1 {
			return PersonalDecision{Action: "renew", LeaseID: "bob-lease", RenewUntil: target}, nil
		}
		return PersonalDecision{Action: "renew", LeaseID: "alice-lease", RenewUntil: target}, nil
	}
	m.mu.Lock()
	alice := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{
		"alice-lease": {ID: "alice-lease", Owner: "job:1", ExpiresAt: m.cfg.Now().Add(time.Minute)},
	}}
	bob := &runtimeEntry{Runtime: Runtime{PrincipalID: "bob", ContextID: "bob", RuntimeID: "bob", RuntimeMode: "gateway", Generation: "gen-2", State: Busy}, leases: map[string]Lease{
		"bob-lease": {ID: "bob-lease", Owner: "job:2", ExpiresAt: m.cfg.Now().Add(time.Minute)},
	}}
	m.items["alice\x00alice\x00gateway"] = alice
	m.items["bob\x00bob\x00gateway"] = bob
	m.recordIncidentLocked(alice, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	if got := m.items["bob\x00bob\x00gateway"].leases["bob-lease"].ExpiresAt; !got.Before(m.cfg.Now().Add(2 * time.Minute)) {
		m.mu.Unlock()
		t.Fatalf("foreign lease was mutated: %v", got)
	}
	m.recordIncidentLocked(alice, "wedged")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	got := m.items["alice\x00alice\x00gateway"].leases["alice-lease"].ExpiresAt
	m.mu.Unlock()
	if !got.Equal(target) {
		t.Fatalf("own-scope renew not applied: %v, want %v", got, target)
	}
}

// A renewal past the bounded horizon is denied; the lease keeps its expiry.
func TestSupervisorRenewBounded(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		return PersonalDecision{Action: "renew", LeaseID: "own", RenewUntil: m.cfg.Now().Add(2 * time.Hour)}, nil
	}
	original := m.cfg.Now().Add(time.Minute)
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{
		"own": {ID: "own", Owner: "job:1", ExpiresAt: original},
	}}
	m.items["alice\x00alice\x00gateway"] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	got := m.items["alice\x00alice\x00gateway"].leases["own"].ExpiresAt
	m.mu.Unlock()
	if got.After(m.cfg.Now().Add(maxPersonalRenew)) {
		t.Fatalf("renew exceeded bound: %v", got)
	}
}

// A decision may request a bounded follow-up review, but never an unbounded
// self-wake loop: after the cap the chain stops.
func TestSupervisorNoSelfWakeLoop(t *testing.T) {
	var calls atomic.Int64
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		calls.Add(1)
		return PersonalDecision{Action: "inspect", Review: true}, nil
	}
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{}}
	m.items["alice\x00alice\x00gateway"] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	if calls.Load() != int64(1+maxPersonalReviews) {
		t.Fatalf("self-wake bound: %d decide calls, want exactly %d", calls.Load(), 1+maxPersonalReviews)
	}
}

// The wake context is the deterministic state of the scope: open incident
// classes and live lease consumers — never transcripts, env or keys.
func TestSupervisorRequestContext(t *testing.T) {
	var got PersonalRequest
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		got = req
		return PersonalDecision{Action: "inspect"}, nil
	}
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{
		"l1": {ID: "l1", Owner: "job:1", Reason: "exec", ExpiresAt: m.cfg.Now().Add(time.Minute)},
	}}
	m.items["alice\x00alice\x00gateway"] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	if got.PrincipalID != "alice" || got.Generation != "gen-1" {
		t.Fatalf("request identity wrong: %+v", got)
	}
	if len(got.Incidents) != 1 || got.Incidents[0].Class != "uncertain" {
		t.Fatalf("incidents missing from request: %+v", got.Incidents)
	}
	if len(got.Consumers) != 1 || got.Consumers[0].ID != "l1" {
		t.Fatalf("consumers missing from request: %+v", got.Consumers)
	}
}

// A decision taken against generation N must not apply after the runtime
// moved to generation N+1 — the reviewed object no longer exists.
func TestSupervisorStaleGenerationDenied(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		m.mu.Lock()
		m.items["alice\x00alice\x00gateway"].Generation = "gen-2"
		m.mu.Unlock()
		return PersonalDecision{Action: "renew", LeaseID: "own", RenewUntil: m.cfg.Now().Add(10 * time.Minute)}, nil
	}
	original := m.cfg.Now().Add(time.Minute)
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{
		"own": {ID: "own", Owner: "job:1", ExpiresAt: original},
	}}
	m.items["alice\x00alice\x00gateway"] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	got := m.items["alice\x00alice\x00gateway"].leases["own"].ExpiresAt
	m.mu.Unlock()
	if !got.Equal(original) {
		t.Fatalf("stale-generation decision applied: %v", got)
	}
}

// The narrow ops set: pause hands the runtime to the deterministic reap path,
// restart marks it for the next verified remediation, escalate writes a
// durable incident. None touches anything outside the woken scope.
func TestSupervisorNarrowOps(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	var calls atomic.Int64
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		switch calls.Add(1) {
		case 1:
			return PersonalDecision{Action: "pause"}, nil
		case 2:
			return PersonalDecision{Action: "escalate", Reason: "repeated timeouts"}, nil
		default:
			return PersonalDecision{Action: "restart"}, nil
		}
	}
	key := "alice\x00alice\x00gateway"
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{}}
	m.items[key] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	if m.items[key].IdleDeadline.IsZero() {
		m.mu.Unlock()
		t.Fatal("pause did not set idle deadline")
	}
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	if _, ok := m.items[key].incidents["escalated"]; !ok {
		m.mu.Unlock()
		t.Fatal("escalate did not record an incident")
	}
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	if !m.items[key].personalRestart {
		m.mu.Unlock()
		t.Fatal("restart did not mark the runtime for remediation")
	}
	m.mu.Unlock()
}

// A restart decision reaches the verified remediation path on the next sweep;
// without the flag the same wedged runtime inside cooldown is untouched.
func TestSupervisorRestartHonoredBySweep(t *testing.T) {
	var restarts atomic.Int64
	var started atomic.Bool
	var ready atomic.Bool
	ready.Store(true)
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			started.Store(true)
		}
		if args[0] == "inspect" && !started.Load() {
			return nil, errors.New("not found")
		}
		if args[0] == "restart" {
			restarts.Add(1)
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error {
		if !ready.Load() {
			return errors.New("readiness unavailable")
		}
		return nil
	})
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		return PersonalDecision{Action: "restart"}, nil
	}
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ready.Store(false)
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	if restarts.Load() != 0 {
		t.Fatalf("wedged runtime restarted without a decision: %d", restarts.Load())
	}
	m.mu.Lock()
	key := "alice\x00alice\x00gateway"
	m.recordIncidentLocked(m.items[key], "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	if restarts.Load() != 1 {
		t.Fatalf("restart decision not honored by sweep: %d", restarts.Load())
	}
}

// An escalate decision records an incident, which queues another wake —
// the drain must still terminate instead of consuming its own output.
func TestSupervisorEscalateDoesNotSelfLoop(t *testing.T) {
	var calls atomic.Int64
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		calls.Add(1)
		return PersonalDecision{Action: "escalate"}, nil
	}
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{}}
	m.items["alice\x00alice\x00gateway"] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.drainPersonal(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain consumed its own wake queue forever")
	}
	if calls.Load() > 64 {
		t.Fatalf("unbounded self-feeding drain: %d calls", calls.Load())
	}
}

// A failed Decide applies nothing: a half-decision (pause returned alongside
// an error) must never act, and the scope can still wake cleanly later.
func TestSupervisorDecideErrorAppliesNothing(t *testing.T) {
	var calls atomic.Int64
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		if calls.Add(1) == 1 {
			return PersonalDecision{Action: "pause"}, errors.New("model unavailable")
		}
		return PersonalDecision{Action: "pause"}, nil
	}
	key := "alice\x00alice\x00gateway"
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{}}
	m.items[key] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	if !m.items[key].IdleDeadline.IsZero() {
		m.mu.Unlock()
		t.Fatal("failed decision was partially applied")
	}
	m.recordIncidentLocked(entry, "wedged")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	if m.items[key].IdleDeadline.IsZero() {
		m.mu.Unlock()
		t.Fatal("clean retry after error was not applied")
	}
	m.mu.Unlock()
}

// The serve-side loop drains a queued wake without any test calling drain:
// an incident enqueues, the loop runs Decide once, context cancel stops it.
func TestSupervisorWakeLoopDrains(t *testing.T) {
	decided := make(chan struct{}, 4)
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		decided <- struct{}{}
		return PersonalDecision{Action: "inspect"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.personalLoop(ctx)
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{}}
	m.items["alice\x00alice\x00gateway"] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	select {
	case <-decided:
	case <-time.After(5 * time.Second):
		t.Fatal("wake never reached the supervisor loop")
	}
}

// With Decide configured, Serve runs the wake loop itself — no test calls
// drainPersonal; the incident reaches the model on its own.
func TestSupervisorServeRunsWakeLoop(t *testing.T) {
	decided := make(chan struct{}, 4)
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		decided <- struct{}{}
		return PersonalDecision{Action: "inspect"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, m, "127.0.0.1:0") }()
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{}}
	m.items["alice\x00alice\x00gateway"] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	select {
	case <-decided:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve never drained the queued wake")
	}
}

// Every decision lands one allowlisted line in the signal journal — applied
// or denied — so supervision cost and actions stay auditable.
func TestSupervisorDecisionsAudited(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	var calls atomic.Int64
	m.cfg.Decide = func(_ context.Context, req PersonalRequest) (PersonalDecision, error) {
		if calls.Add(1) == 1 {
			return PersonalDecision{Action: "renew", LeaseID: "missing-lease", RenewUntil: m.cfg.Now().Add(time.Minute)}, nil
		}
		return PersonalDecision{Action: "inspect", Reason: "observed"}, nil
	}
	key := "alice\x00alice\x00gateway"
	m.mu.Lock()
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", State: Busy}, leases: map[string]Lease{}}
	m.items[key] = entry
	m.recordIncidentLocked(entry, "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.mu.Lock()
	m.recordIncidentLocked(entry, "wedged")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	body, err := os.ReadFile(m.signalPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "personal-denied") || !strings.Contains(string(body), "renew") {
		t.Fatalf("denied decision not audited: %s", body)
	}
	if !strings.Contains(string(body), "personal-applied") {
		t.Fatalf("applied decision not audited: %s", body)
	}
}

// The production decider posts the allowlisted request to the managed relay
// and parses a strict JSON decision; a malformed reply fails closed.
func TestSupervisorRelayDecide(t *testing.T) {
	var sawBody []byte
	var sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sawBody = body
		sawAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": `{"action":"renew","lease_id":"l1","renew_until":"2030-01-01T00:00:00Z"}`}}}})
	}))
	defer server.Close()
	t.Setenv("HUB_MANAGED_MODEL_URL", server.URL)
	t.Setenv("HUB_PERSONAL_MODEL", "sup-small")
	t.Setenv("OPENAI_API_KEY", "test-key")
	decision, err := relayDecide(context.Background(), PersonalRequest{PrincipalID: "alice", Generation: "gen-1"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != "renew" || decision.LeaseID != "l1" {
		t.Fatalf("decision=%+v", decision)
	}
	if !strings.Contains(string(sawBody), "sup-small") {
		t.Fatal("model not sent to relay")
	}
	if sawAuth != "Bearer test-key" {
		t.Fatalf("relay auth missing: %q", sawAuth)
	}
	server.Close()
	_, err = relayDecide(context.Background(), PersonalRequest{PrincipalID: "alice"})
	if err == nil {
		t.Fatal("unreachable relay must fail closed")
	}
}

// The personal supervisor is opt-in: without HUB_PERSONAL_MODEL no decider
// exists and no model call can happen; with it the relay decider is wired.
func TestSupervisorRelayDecideWiring(t *testing.T) {
	t.Setenv("HUB_PERSONAL_MODEL", "")
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	if m.cfg.Decide != nil {
		t.Fatal("decider configured without opt-in")
	}
	t.Setenv("HUB_PERSONAL_MODEL", "sup-small")
	t.Setenv("HUB_MANAGED_MODEL_URL", "http://127.0.0.1:1")
	m2, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	if m2.cfg.Decide == nil {
		t.Fatal("configured model did not wire the decider")
	}
}
