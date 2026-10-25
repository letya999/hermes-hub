package toolhub

import (
	"errors"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// An MCP connector that keeps dying gets a bounded recovery budget: each
// admission that respawns a known-but-not-running workload counts against
// the connection's recovery window, and exhausting it degrades the
// connection instead of restart-looping forever. Ordinary admits against a
// live workload do not consume the budget.
func TestWatchdogRecoveryBudget(t *testing.T) {
	store, auth, binding := seededStore(t)
	owner := &api.OwnerRef{Type: api.ContextOwner, ID: "alice"}
	workload, err := api.NewWorkloadInstance(binding, owner, "", 1, time.Now(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status = api.StoppedStatus
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	admit := func(api.EffectiveBinding) error { return nil }
	for i := 0; i < 2; i++ {
		if err := store.Authorize(auth, binding.ToolBindingID, admit); err != nil {
			t.Fatalf("respawn %d denied: %v", i, err)
		}
	}
	if err := store.Authorize(auth, binding.ToolBindingID, admit); !errors.Is(err, api.ErrDegraded) {
		t.Fatalf("recovery budget exceeded without degrade: %v", err)
	}
	var connection api.Connection
	for _, candidate := range store.Connections() {
		if candidate.ConnectionID == "google-work" {
			connection = candidate
		}
	}
	if connection.Status != api.DegradedStatus || connection.RecoveryCount < 3 {
		t.Fatalf("connection not degraded after budget exhaustion: %+v", connection)
	}
	// The degraded connection rejects resolve outright — no more restarts.
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, api.ErrDegraded) {
		t.Fatalf("degraded connection still admits: %v", err)
	}
}
