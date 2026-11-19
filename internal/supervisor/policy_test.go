package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// config.supervision.idle_stop is the imperative idle policy: the rendered
// policy file wins over the deployment default, and a rewritten file applies
// on the next decision without respawning the runtime (SPEC-0092 R4.2, A4).
func TestSupervisorPolicyReloadLive(t *testing.T) {
	m := &Manager{cfg: Config{WarmTTL: 30 * time.Minute, Environment: "dev"}}
	dir := t.TempDir()
	policy := filepath.Join(dir, "runtime.dev.policy.json")
	binding := Binding{ContextRoot: dir}
	if err := os.WriteFile(policy, []byte(`{"idle_stop":"5m"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := m.idleTTL(binding); got != 5*time.Minute {
		t.Fatalf("idle_stop must win over the default, got %v", got)
	}
	if err := os.WriteFile(policy, []byte(`{"idle_stop":"9m"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := m.idleTTL(binding); got != 9*time.Minute {
		t.Fatalf("policy rewrite must apply live, got %v", got)
	}
	if err := os.Remove(policy); err != nil {
		t.Fatal(err)
	}
	if got := m.idleTTL(binding); got != 30*time.Minute {
		t.Fatalf("missing policy must fall back to the default, got %v", got)
	}
}

// The same policy governs the lease-release deadline: with idle_stop the
// released runtime retires on the passport's bound, not the deployment
// default.
func TestSupervisorPolicyGovernsReleaseDeadline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.dev.policy.json"), []byte(`{"idle_stop":"5m"}`), 0600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	m := &Manager{cfg: Config{WarmTTL: 30 * time.Minute, Environment: "dev", StateDir: stateDir, Now: time.Now}, locks: map[string]*sync.Mutex{}, statePath: filepath.Join(stateDir, "state.json")}
	binding := Binding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", ContextRoot: dir}
	key := "alice\x00alice\x00gateway"
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", State: Busy, Generation: "gen-1"}, binding: binding, leases: map[string]Lease{
		"lease-1": {ID: "lease-1", Kind: LeaseJob, Owner: "supervisor", Reason: ensureHoldReason, Generation: "gen-1"},
	}}
	m.items = map[string]*runtimeEntry{key: entry}
	m.jobs = map[string]jobRecord{}
	if err := m.releaseKey(key); err != nil {
		t.Fatal(err)
	}
	want := m.cfg.Now().Add(5 * time.Minute)
	if d := entry.IdleDeadline; d.Before(want.Add(-time.Second)) || d.After(want.Add(time.Second)) {
		t.Fatalf("release must retire on the policy bound, got %v (want ≈%v)", d, want)
	}
	// The per-lease release path retires on the same bound.
	key2 := "bob\x00bob\x00gateway"
	entry2 := &runtimeEntry{Runtime: Runtime{PrincipalID: "bob", ContextID: "bob", RuntimeID: "bob", RuntimeMode: "gateway", State: Busy, Generation: "gen-1"}, binding: Binding{PrincipalID: "bob", ContextID: "bob", RuntimeID: "bob", RuntimeMode: "gateway", ContextRoot: dir}, leases: map[string]Lease{
		"lease-2": {ID: "lease-2", Kind: LeaseStream, Owner: "caller", Generation: "gen-1"},
	}}
	m.items[key2] = entry2
	if err := m.ReleaseLease("lease-2"); err != nil {
		t.Fatal(err)
	}
	if d := entry2.IdleDeadline; d.Before(want.Add(-time.Second)) || d.After(want.Add(time.Second)) {
		t.Fatalf("ReleaseLease must retire on the policy bound, got %v", d)
	}
	// A stale lease swept off a busy runtime retires it on the same bound.
	key3 := "carol\x00carol\x00gateway"
	entry3 := &runtimeEntry{Runtime: Runtime{PrincipalID: "carol", ContextID: "carol", RuntimeID: "carol", RuntimeMode: "gateway", State: Busy, Generation: "gen-2"}, binding: Binding{PrincipalID: "carol", ContextID: "carol", RuntimeID: "carol", RuntimeMode: "gateway", ContextRoot: dir}, leases: map[string]Lease{
		"lease-stale": {ID: "lease-stale", Kind: LeaseStream, Owner: "caller", Generation: "gen-1"},
	}}
	m.items[key3] = entry3
	m.mu.Lock()
	m.sweepStaleLeasesLocked(m.cfg.Now())
	m.mu.Unlock()
	if d := entry3.IdleDeadline; d.Before(want.Add(-time.Second)) || d.After(want.Add(time.Second)) {
		t.Fatalf("swept release must retire on the policy bound, got %v", d)
	}
}

// restart: never is imperative too — the remediation sweep may still observe
// and report a wedged runtime, but no restart leaves the manager when the
// space's supervision policy forbids it.
func TestSupervisionRestartNeverBlocksRemediation(t *testing.T) {
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
	m.cfg.Environment = "dev"
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.dev.policy.json"), []byte(`{"restart":"never"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ready.Store(false)
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	m.mu.Lock()
	key := "alice\x00alice\x00gateway"
	m.recordIncidentLocked(m.items[key], "uncertain")
	m.mu.Unlock()
	m.drainPersonal(context.Background())
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	if restarts.Load() != 0 {
		t.Fatalf("restart: never still restarted the runtime: %d", restarts.Load())
	}
}

// A protected stack-health watcher still observes — readiness and ownership
// update — but the remediation action never leaves the manager.
func TestSupervisionProtectedWatcherOnlyObserves(t *testing.T) {
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
	m.cfg.Environment = "dev"
	if _, err := m.Ensure(context.Background(), binding(root)); err != nil {
		t.Fatal(err)
	}
	policy := `{"restart":"always","watchers":{"stack-health":{"protected":true}}}`
	if err := os.WriteFile(filepath.Join(root, "runtime.dev.policy.json"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	ready.Store(false)
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	m.mu.Lock()
	key := "alice\x00alice\x00gateway"
	entry := m.items[key]
	entry.consecutiveUncertain = 3
	entry.readinessBadSince = m.cfg.Now().Add(-wedgedReadinessWindow - time.Minute)
	entry.lastRemediatedAt = m.cfg.Now().Add(-wedgedRemediationCooldown - time.Minute)
	entry.leases = map[string]Lease{}
	m.pins[key] = hubruntime.ExecuteRequest{}
	m.mu.Unlock()
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	if restarts.Load() != 0 {
		t.Fatalf("protected watcher restarted the runtime: %d", restarts.Load())
	}
	m.mu.Lock()
	if m.items[key].HealthCheckedAt.IsZero() {
		m.mu.Unlock()
		t.Fatal("protected watcher skipped observation entirely")
	}
	m.mu.Unlock()
}

// stack-health.every is the probe cadence: inside the interval the entry is
// not re-probed at all — the previously observed health stands.
func TestSupervisionWatcherEveryThrottlesProbe(t *testing.T) {
	var probes atomic.Int64
	var started atomic.Bool
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			started.Store(true)
		}
		if args[0] == "inspect" && !started.Load() {
			return nil, errors.New("not found")
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error {
		probes.Add(1)
		return nil
	})
	m.cfg.Environment = "dev"
	if _, err := m.Ensure(context.Background(), binding(root)); err != nil {
		t.Fatal(err)
	}
	policy := `{"watchers":{"stack-health":{"every":"1h"}}}`
	if err := os.WriteFile(filepath.Join(root, "runtime.dev.policy.json"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	first := probes.Load()
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	if probes.Load() != first {
		t.Fatalf("probe ran inside the watcher interval: %d → %d", first, probes.Load())
	}
}
