package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func recoveryJob(t *testing.T, m *Manager, spacesRoot, principal string) (hubruntime.ExecuteRequest, string) {
	t.Helper()
	ctxRoot := filepath.Join(spacesRoot, principal)
	for _, dir := range []string{ctxRoot, filepath.Join(ctxRoot, "runtime")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	publishSpaceRevision(t, ctxRoot, principal, nil)
	writeHermesSources(t, ctxRoot)
	request := hubruntime.ExecuteRequest{
		Envelope:       identity.TelegramEnvelope(principal, 1, principal, "policy-1"),
		JobID:          "job-" + principal,
		IdempotencyKey: "job-" + principal,
		OrganizationID: "personal",
		UserID:         principal,
		ActorID:        principal,
		ScopeID:        "user:" + principal,
		Channel:        "telegram_bot",
		Trigger:        "message",
	}
	binding, err := m.bindingFor(request)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[runtimeKey(binding)] = &runtimeEntry{Runtime: Runtime{PrincipalID: request.PrincipalID, ContextID: request.ContextID, RuntimeMode: binding.RuntimeMode, Generation: "gen-1", RuntimeHealth: "missing"}, binding: binding, leases: map[string]Lease{}}
	m.jobs[request.JobID] = jobRecord{Request: request, Status: "running", Generation: "gen-1", UpdatedAt: time.Now()}
	return request, runtimeKey(binding)
}

// A recovery blocked on one owner's runtime holds a pool slot but never the
// whole pool: another owner's missing runtime is recovered in parallel, and
// the function still waits for every worker it spawned.
func TestStuckRecoveryDoesNotStarveOtherOwners(t *testing.T) {
	m, aliceRoot := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	spacesRoot := filepath.Dir(aliceRoot)
	_, stuckKey := recoveryJob(t, m, spacesRoot, "stuck")
	_, freeKey := recoveryJob(t, m, spacesRoot, "free")
	// Pin the stuck key's recovery gate: its worker blocks inside the bounded
	// pool, exactly like a hung runtime acquire.
	stuckGate := m.lockFor("recovery:" + stuckKey)
	stuckGate.Lock()
	done := make(chan struct{})
	go func() {
		m.RecoverMissingWork(context.Background())
		close(done)
	}()
	deadline := time.After(10 * time.Second)
	for {
		m.mu.Lock()
		restored := m.items[freeKey].restored
		m.mu.Unlock()
		if restored {
			break
		}
		select {
		case <-deadline:
			t.Fatal("independent owner waited for a stuck recovery")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-done:
		t.Fatal("missing-work sweep abandoned a live worker")
	case <-time.After(50 * time.Millisecond):
	}
	stuckGate.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep never joined released workers")
	}
}

// The worker pool is finite: a full pool skips detached observation without
// losing the durable record or pinning its per-job lock.
func TestRecoveryWorkerPoolBound(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	for i := 0; i < recoveryWorkers; i++ {
		if !m.tryRecoveryWorker() {
			t.Fatalf("worker %d refused before the bound", i)
		}
	}
	if m.tryRecoveryWorker() {
		t.Fatal("recovery workers unbounded")
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "job-unconfirmed", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message"}
	m.mu.Lock()
	m.jobs[request.JobID] = jobRecord{Request: request, Status: "uncertain", Generation: "gen-1", Unconfirmed: true, UnconfirmedSince: time.Now().Add(-time.Minute), UpdatedAt: time.Now()}
	m.mu.Unlock()
	m.reconcileUnconfirmed(context.Background(), time.Now())
	lock := m.lockFor("observation:" + request.JobID)
	if !lock.TryLock() {
		t.Fatal("starved record pinned its observation lock")
	}
	lock.Unlock()
	if got := len(m.recoverySem); got != recoveryWorkers {
		t.Fatalf("recovery slots leaked: %d", got)
	}
	for i := 0; i < recoveryWorkers; i++ {
		<-m.recoverySem
	}
	if record := m.jobs[request.JobID]; !record.Unconfirmed {
		t.Fatal("skipped observation mutated the durable record")
	}
}
