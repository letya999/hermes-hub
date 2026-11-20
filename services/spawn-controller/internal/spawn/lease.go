package spawn

import (
	"errors"
	"time"

	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/store"
)

// LeaseCount is the number of lease rows on the runtime — the count is the
// rows, never a stored counter.
func LeaseCount(q store.Scoped, runtimeID string) (int, error) {
	var n int
	err := q.QueryRow("SELECT count(*) FROM leases WHERE {tenant} AND runtime_id = ?", runtimeID).Scan(&n)
	return n, err
}

// AcquireLease writes one hold row and, on a warm runtime, moves warm ->
// busy through the machine, inside the caller's transaction: a failure
// anywhere rolls back row and move together, so a crash cannot leave the
// phantom hold that pinned v0 runtimes busy forever. Only warm and busy
// runtimes accept holds.
func AcquireLease(q store.Scoped, runtimeID, leaseID string, now time.Time) error {
	state, err := fsm.State(q, RuntimeMachine, runtimeID)
	if err != nil {
		return err
	}
	switch state {
	case "warm":
		if err := fsm.Transition(q, RuntimeMachine, runtimeID, "lease", now); err != nil {
			return err
		}
	case "busy":
		// Confirm busy under the transaction's row lock: a concurrent
		// release that already unleased it must fail this insert.
		result, err := q.Exec("UPDATE runtimes SET deadline = deadline WHERE {tenant} AND id = ? AND state = 'busy'", runtimeID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return fsm.ErrStale
		}
	default:
		return fsm.ErrForbiddenTransition
	}
	return q.Insert("leases", []string{"id", "runtime_id", "created_at"}, leaseID, runtimeID, now.Unix())
}

// ReleaseLease removes one hold row and, when it was the last, moves a
// still-busy runtime back to warm through the machine — same transaction.
// A missing row is not_found; a runtime that left busy on its own (drained,
// quarantined, absent) is not walked backwards.
func ReleaseLease(q store.Scoped, runtimeID, leaseID string, now time.Time) error {
	result, err := q.Exec("DELETE FROM leases WHERE {tenant} AND id = ?", leaseID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return store.ErrNotFound
	}
	n, err := LeaseCount(q, runtimeID)
	if err != nil || n > 0 {
		return err
	}
	state, err := fsm.State(q, RuntimeMachine, runtimeID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || state != "busy" {
		return err
	}
	return fsm.Transition(q, RuntimeMachine, runtimeID, "unlease", now)
}
