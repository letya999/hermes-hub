package reconcile

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
)

// Removing a stack.services instance means the container goes away — the
// plan must not emit a rebuild for a service the new render no longer has
// (docker compose build would hit "no such service"); it emits one
// project-level remove action instead.
func TestRemovedStackInstancePlansRemoveNotRebuild(t *testing.T) {
	actions := Plan([]passport.Change{
		{Path: "stack.services.cliproxy-google-2.mode", Before: "per-space", After: "", Effect: passport.EffectRebuild, Consumer: "pending:CHG-0257"},
	}, allServices)
	if len(actions) != 1 {
		t.Fatalf("removal must plan exactly one action: %+v", actions)
	}
	a := actions[0]
	if a.Kind != Remove || a.Target != "*" {
		t.Fatalf("removal must plan remove * (project sweep), got %s %s", a.Kind, a.Target)
	}
}

// Additions stay rebuilds — the classification must not overreach.
func TestAddedStackInstanceKeepsRebuild(t *testing.T) {
	actions := Plan([]passport.Change{
		{Path: "stack.services.cliproxy-google-2.mode", Before: "", After: "per-space", Effect: passport.EffectRebuild, Consumer: "pending:CHG-0257"},
	}, allServices)
	if len(actions) != 1 || actions[0].Kind != Rebuild || actions[0].Target != "cliproxy-google-2" {
		t.Fatalf("addition must stay rebuild <instance>: %+v", actions)
	}
}

// A remove action must actually run its handler — an unhandled kind would
// be recorded applied while nothing swept the orphaned container.
func TestRunExecutesRemoveHandler(t *testing.T) {
	var removed bool
	rep := Run([]Action{{Kind: Remove, Target: "*"}}, Handlers{
		Remove: func() error { removed = true; return nil },
	}, func() error { return nil }, "r2", "r1")
	if !removed {
		t.Fatal("remove action never reached the Remove handler")
	}
	if rep.Steps[0].Status != "applied" {
		t.Fatalf("remove step must report applied: %+v", rep.Steps[0])
	}
}
