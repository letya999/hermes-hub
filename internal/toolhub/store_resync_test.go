package toolhub

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A foreign commit (operator CLI, provisioning) must not wedge a live writer:
// the first save after it CAS-refuses, and the next mutator call must resync
// and write against current state without a process restart.
func TestMutatorResyncsAfterForeignCommit(t *testing.T) {
	for _, name := range []string{"store.json", "store.db"} {
		t.Run(name, func(t *testing.T) {
			testMutatorResyncsAfterForeignCommit(t, filepath.Join(t.TempDir(), name))
		})
	}
}

func testMutatorResyncsAfterForeignCommit(t *testing.T, path string) {
	seed := NewStore()
	if err := seed.Save(path); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	t.Cleanup(func() { _ = seed.CloseDB() })
	live, err := Load(path)
	if err != nil {
		t.Fatalf("live load: %v", err)
	}
	t.Cleanup(func() { _ = live.CloseDB() })
	writer, err := Load(path)
	if err != nil {
		t.Fatalf("foreign load: %v", err)
	}
	t.Cleanup(func() { _ = writer.CloseDB() })
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := writer.PutTeam(team); err != nil {
		t.Fatalf("foreign commit: %v", err)
	}
	if err := live.Save(path); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("stale writer save: %v", err)
	}
	// The resyncing call stays fenced (ErrConflict) — a stale retry must be a
	// deliberate operator decision, not silent convergence.
	if err := live.Save(path); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("resyncing save must stay fenced: %v", err)
	}
	// State is now current without an explicit Reload or a restart.
	if err := live.Save(path); err != nil {
		t.Fatalf("post-resync save: %v", err)
	}
	got, ok := live.TeamFor("platform")
	if !ok || len(got.Members) != 1 {
		t.Fatalf("resynced state: %+v ok=%v", got, ok)
	}
}
