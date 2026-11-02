package reconcile

import (
	"errors"
	"slices"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
)

func ch(path, consumer string, e passport.Effect) passport.Change {
	return passport.Change{Path: path, Before: "a", After: "b", Effect: e, Consumer: consumer}
}

func TestPlanOrdersAndDeduplicates(t *testing.T) {
	actions := Plan([]passport.Change{
		ch("tools.core.hub:files.tools", "pending:tools", passport.EffectLive),
		ch("stack.services.cliproxy.mode", "pending:stack", passport.EffectRebuild),
		ch("config.ports.oauth-callback", "pending:config", passport.EffectRecreate),
		ch("workspace.mounts.docs.mode", "pending:workspace", passport.EffectRespawn),
		ch("stack.services.cliproxy.endpoint", "pending:stack", passport.EffectRebuild),
		ch("grants.allow.files.read", "pending:grants", passport.EffectLive),
	})
	kinds := []Kind{}
	for _, a := range actions {
		kinds = append(kinds, a.Kind)
	}
	if !slices.IsSortedFunc(kinds, func(a, b Kind) int { return order[a] - order[b] }) {
		t.Fatalf("actions not ordered rebuild<recreate<respawn<live: %v", kinds)
	}
	// Same service, two changed leaves -> one rebuild action with both paths.
	var rebuilds []Action
	for _, a := range actions {
		if a.Kind == Rebuild {
			rebuilds = append(rebuilds, a)
		}
	}
	if len(rebuilds) != 1 || rebuilds[0].Target != "cliproxy" || len(rebuilds[0].Paths) != 2 {
		t.Fatalf("rebuild dedup wrong: %+v", rebuilds)
	}
	// Live leaves stay keyed by consumer, not by service.
	var lives []Action
	for _, a := range actions {
		if a.Kind == Live {
			lives = append(lives, a)
		}
	}
	if len(lives) != 2 {
		t.Fatalf("live actions per consumer: %+v", lives)
	}
}

func TestPlanTargetsOwnServiceAndRuntime(t *testing.T) {
	actions := Plan([]passport.Change{
		ch("stack.services.browser.mode", "pending:stack", passport.EffectRecreate),
		ch("config.ports.x", "pending:config", passport.EffectRecreate),
	})
	var svc, rt bool
	for _, a := range actions {
		if a.Kind == Recreate && a.Target == "browser" {
			svc = true
		}
		if a.Kind == Recreate && a.Target == "hermes-runtime" {
			rt = true
		}
	}
	if !svc || !rt {
		t.Fatalf("recreate targets: %+v", actions)
	}
	// stack.identity affects every image.
	actions = Plan([]passport.Change{ch("stack.identity.user", "pending:stack", passport.EffectRebuild)})
	if len(actions) != 1 || actions[0].Target != "*" {
		t.Fatalf("identity rebuild target: %+v", actions)
	}
}

func TestRunRollbackOnFailure(t *testing.T) {
	var calls []string
	h := Handlers{
		Rebuild:  func(s string) error { calls = append(calls, "rebuild:"+s); return nil },
		Recreate: func(s string) error { calls = append(calls, "recreate:"+s); return errors.New("compose exploded") },
		Respawn:  func() error { calls = append(calls, "respawn"); return nil },
		Live: map[string]func() error{
			"toolhub-projection": func() error { calls = append(calls, "live:toolhub"); return nil },
			"zz-failing-hook":    func() error { return errors.New("hook exploded") },
		},
	}
	actions := Plan([]passport.Change{
		ch("stack.services.cliproxy.mode", "pending:stack", passport.EffectRebuild),
		ch("config.ports.p", "pending:config", passport.EffectRecreate),
		ch("tools.core.x.via", "toolhub-projection", passport.EffectLive),
		ch("tools.core.y.via", "zz-failing-hook", passport.EffectLive),
	})
	rep := Run(actions, h, func() error { calls = append(calls, "pointer:prev"); return nil }, "r-new", "r-prev")
	if !rep.RolledBack || rep.Failed == nil || rep.Failed.Kind != Recreate {
		t.Fatalf("expected recreate failure + rollback: %+v", rep)
	}
	if !slices.Contains(calls, "pointer:prev") {
		t.Fatalf("current pointer never repointed: %v", calls)
	}
	// Live runs last: an earlier live failure re-runs the hooks that
	// already applied after the pointer is restored.
	calls = nil
	actions = Plan([]passport.Change{
		ch("tools.core.x.via", "toolhub-projection", passport.EffectLive),
		ch("tools.core.y.via", "zz-failing-hook", passport.EffectLive),
	})
	rep = Run(actions, h, func() error { calls = append(calls, "pointer:prev"); return nil }, "r-new", "r-prev")
	if !rep.RolledBack || rep.Failed == nil || rep.Failed.Target != "zz-failing-hook" {
		t.Fatalf("expected live failure + rollback: %+v", rep)
	}
	if len(calls) != 3 || calls[0] != "live:toolhub" || calls[1] != "pointer:prev" || calls[2] != "live:toolhub" {
		t.Fatalf("applied live hook not re-run after rollback: %v", calls)
	}
}

func TestRunPendingConsumersDeferNotFail(t *testing.T) {
	h := Handlers{
		Rebuild:  func(string) error { return nil },
		Recreate: func(string) error { return nil },
		Respawn:  func() error { return nil },
		Live:     map[string]func() error{},
	}
	actions := Plan([]passport.Change{ch("grants.allow.x", "pending:CHG-0259", passport.EffectLive)})
	rep := Run(actions, h, func() error { return nil }, "r-1", "r-0")
	if rep.RolledBack || rep.Failed != nil {
		t.Fatalf("deferred live consumer must not fail the apply: %+v", rep)
	}
	if len(rep.Steps) != 1 || rep.Steps[0].Status != "deferred" {
		t.Fatalf("pending consumer should defer: %+v", rep.Steps)
	}
}

func TestRunFailureRollbackErrorReported(t *testing.T) {
	h := Handlers{
		Rebuild: func(string) error { return errors.New("image failed") },
		Live:    map[string]func() error{},
	}
	actions := Plan([]passport.Change{ch("stack.identity.user", "pending:stack", passport.EffectRebuild)})
	rep := Run(actions, h, func() error { return errors.New("disk full") }, "r-1", "r-0")
	if !rep.RolledBack || rep.Failed == nil {
		t.Fatalf("expected failure: %+v", rep)
	}
	last := rep.Steps[len(rep.Steps)-1]
	if last.Detail == "" || rep.Steps[len(rep.Steps)-2].Status != "failed" {
		t.Fatalf("rollback error should be reported: %+v", rep.Steps)
	}
}
