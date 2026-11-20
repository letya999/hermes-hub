package spawn_test

import (
	"errors"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

func runtimeState(t *testing.T, s *store.Store, id string) string {
	t.Helper()
	state, err := func() (string, error) {
		var st string
		err := s.Tx(t.Context(), func(tx *store.Tx) error {
			return tx.Scoped(tenant).QueryRow("SELECT state FROM runtimes WHERE {tenant} AND id = ?", id).Scan(&st)
		})
		return st, err
	}()
	if err != nil {
		t.Fatalf("state %s: %v", id, err)
	}
	return state
}

func leaseCount(t *testing.T, s *store.Store, runtimeID string) int {
	t.Helper()
	var n int
	err := s.Tx(t.Context(), func(tx *store.Tx) error {
		var err error
		n, err = spawn.LeaseCount(tx.Scoped(tenant), runtimeID)
		return err
	})
	if err != nil {
		t.Fatalf("lease count %s: %v", runtimeID, err)
	}
	return n
}

// Every runtime hold is a lease row: the count is the row count, never a
// stored counter. Acquire writes the row and the warm -> busy move in one
// transaction, so a failure between the steps cannot leave a phantom hold
// pinning the runtime busy; the busy deadline is the backstop that sweeps a
// wedged runtime back to warm.
func TestLeaseCountDerivedFromLeaseRows(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, migrations.FS)
		create(t, s, spawn.RuntimeMachine, "rt1")
		if err := transition(t, s, spawn.RuntimeMachine, "rt1", "ready"); err != nil {
			t.Fatal(err)
		}
		acquire := func(runtimeID, leaseID string) error {
			return s.Tx(t.Context(), func(tx *store.Tx) error {
				return spawn.AcquireLease(tx.Scoped(tenant), runtimeID, leaseID, t0)
			})
		}
		release := func(runtimeID, leaseID string) error {
			return s.Tx(t.Context(), func(tx *store.Tx) error {
				return spawn.ReleaseLease(tx.Scoped(tenant), runtimeID, leaseID, t0)
			})
		}
		if err := acquire("rt1", "l1"); err != nil {
			t.Fatalf("acquire l1: %v", err)
		}
		if err := acquire("rt1", "l2"); err != nil {
			t.Fatalf("acquire l2: %v", err)
		}
		if n := leaseCount(t, s, "rt1"); n != 2 {
			t.Fatalf("count %d, want 2", n)
		}
		if st := runtimeState(t, s, "rt1"); st != "busy" {
			t.Fatalf("state %q, want busy", st)
		}
		// The v0 defect: a crash after the counter write but before the row
		// insert pinned the runtime busy. Here the row and the move are one
		// transaction — a failure rolls back both.
		crash := errors.New("crash after acquire")
		err := s.Tx(t.Context(), func(tx *store.Tx) error {
			if err := spawn.AcquireLease(tx.Scoped(tenant), "rt1", "l3", t0); err != nil {
				return err
			}
			return crash
		})
		if !errors.Is(err, crash) {
			t.Fatalf("crash propagation: %v", err)
		}
		if n := leaseCount(t, s, "rt1"); n != 2 {
			t.Fatalf("count after crash %d, want 2 (l3 rolled back)", n)
		}
		if st := runtimeState(t, s, "rt1"); st != "busy" {
			t.Fatalf("state after crash %q, want busy from the live holds", st)
		}
		if err := release("rt1", "l1"); err != nil {
			t.Fatalf("release l1: %v", err)
		}
		if st := runtimeState(t, s, "rt1"); st != "busy" {
			t.Fatalf("one hold left, state %q, want busy", st)
		}
		if err := release("rt1", "l2"); err != nil {
			t.Fatalf("release l2: %v", err)
		}
		if n := leaseCount(t, s, "rt1"); n != 0 {
			t.Fatalf("count %d, want 0", n)
		}
		if st := runtimeState(t, s, "rt1"); st != "warm" {
			t.Fatalf("last hold released, state %q, want warm", st)
		}
		// A release of a missing row is not_found, not a silent no-op.
		if err := release("rt1", "l2"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("double release: %v, want not_found", err)
		}
		// Backstop: a runtime whose hold outlives the busy bound is drained
		// by the sweeper — the runtime can never pin busy forever, and a
		// hold's dirty runtime is reclaimed, not returned to the warm pool.
		create(t, s, spawn.RuntimeMachine, "rt2")
		if err := transition(t, s, spawn.RuntimeMachine, "rt2", "ready"); err != nil {
			t.Fatal(err)
		}
		if err := transition(t, s, spawn.RuntimeMachine, "rt2", "lease"); err != nil {
			t.Fatal(err)
		}
		past := t0.Add(spawn.RuntimeMachine.Deadlines["busy"].After + time.Minute)
		moved, err := fsm.Sweep(t.Context(), s, spawn.RuntimeMachine, past, 10)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if moved != 2 {
			t.Fatalf("sweep moved %d, want 2 (wedged busy + idle warm)", moved)
		}
		if st := runtimeState(t, s, "rt2"); st != "draining" {
			t.Fatalf("wedged busy after sweep: %q, want draining", st)
		}
		if st := runtimeState(t, s, "rt1"); st != "draining" {
			t.Fatalf("idle warm after sweep: %q, want draining", st)
		}
	})
}
