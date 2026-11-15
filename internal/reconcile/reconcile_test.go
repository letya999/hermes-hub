package reconcile

import (
	"errors"
	"slices"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/passportview"
)

func ch(path, consumer string, e passport.Effect) passport.Change {
	return passport.Change{Path: path, Before: "a", After: "b", Effect: e, Consumer: consumer}
}

// allServices stands in for a new revision declaring the owners these
// tests recreate.
var allServices = map[string]passportview.ServiceSpec{
	"hermes-runtime":    {Mode: "supervised"},
	"browser":           {Mode: "per-space"},
	"cliproxy":          {Mode: "per-space"},
	"communication-hub": {Mode: "shared"},
}

func TestPlanOrdersAndDeduplicates(t *testing.T) {
	actions := Plan([]passport.Change{
		ch("tools.core.hub:files.tools", "pending:tools", passport.EffectLive),
		ch("stack.services.cliproxy.mode", "pending:stack", passport.EffectRebuild),
		ch("config.ports.oauth-callback", "pending:config", passport.EffectRecreate),
		ch("workspace.mounts.docs.mode", "pending:workspace", passport.EffectRespawn),
		ch("stack.services.cliproxy.endpoint", "pending:stack", passport.EffectRebuild),
		ch("grants.allow.files.read", "pending:grants", passport.EffectLive),
	}, allServices)
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
	}, allServices)
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
	actions = Plan([]passport.Change{ch("stack.identity.user", "pending:stack", passport.EffectRebuild)}, allServices)
	if len(actions) != 1+len(allServices) || actions[0].Target != "*" {
		t.Fatalf("identity rebuild target: %+v", actions)
	}
}

// config.ports and config.channels recreate the service that consumes them —
// the port owner per the compile's PortOwners map, the gateway for channels —
// not the whole stack (SPEC-0092 effect table, A4).
func TestPlanTargetsConfigOwners(t *testing.T) {
	actions := Plan([]passport.Change{
		ch("config.ports.oauth-callback", "c", passport.EffectRecreate),
		ch("config.ports.browser-vnc", "c", passport.EffectRecreate),
		ch("config.ports.slack-events", "c", passport.EffectRecreate),
		ch("config.channels.telegram.bot.ref", "c", passport.EffectRecreate),
	}, allServices)
	want := map[string]bool{
		"hermes-runtime":    true, // oauth-callback owner
		"browser":           true, // browser-vnc owner
		"communication-hub": true, // slack-events + channels
	}
	got := map[string]bool{}
	for _, a := range actions {
		if a.Kind != Recreate {
			t.Fatalf("config ports/channels are recreate-class: %+v", a)
		}
		got[a.Target] = true
	}
	if len(got) != len(want) {
		t.Fatalf("recreate targets %v, want %v", got, want)
	}
	for target := range want {
		if !got[target] {
			t.Fatalf("missing recreate target %s in %v", target, got)
		}
	}
}

func TestPlanWebProviderRoutingRecreatesEgressRelay(t *testing.T) {
	changes := []passport.Change{
		ch("config.web.search_backend", "hermes-config", passport.EffectRespawn),
		ch("config.web.search_providers", "hermes-config", passport.EffectRespawn),
	}
	services := map[string]passportview.ServiceSpec{"hermes-runtime": {}, "egress-relay": {}}
	actions := Plan(changes, services)
	if len(actions) != 2 || actions[0].Kind != Recreate || actions[0].Target != "egress-relay" || actions[1].Kind != Respawn || actions[1].Target != "hermes-runtime" {
		t.Fatalf("web routing must recreate egress before runtime respawn: %+v", actions)
	}
	delete(services, "egress-relay")
	actions = Plan(changes, services)
	if len(actions) != 1 || actions[0].Kind != Respawn {
		t.Fatalf("undeclared egress relay must not be recreated: %+v", actions)
	}
}

func TestPlanPrivateMediaRecreatesEgressBeforeRuntimeRespawn(t *testing.T) {
	changes := []passport.Change{ch("stack.services.hub-media.mode", "hub-media", passport.EffectRebuild)}
	services := map[string]passportview.ServiceSpec{"hub-media": {Mode: "per-space"}, "egress-relay": {}, "hermes-runtime": {}}
	actions := Plan(changes, services)
	if len(actions) != 4 || actions[0].Kind != Rebuild || actions[0].Target != "hub-media" || actions[1].Kind != Recreate || actions[1].Target != "egress-relay" || actions[2].Kind != Recreate || actions[2].Target != "hub-media" || actions[3].Kind != Respawn || actions[3].Target != "hermes-runtime" {
		t.Fatalf("private media must refresh egress before runtime: %+v", actions)
	}
}

func TestPlanMemorySwitchStopsExistingWorkers(t *testing.T) {
	services := map[string]passportview.ServiceSpec{
		"hermes-runtime": {Mode: "supervised"}, "communication-hub": {Mode: "shared"}, "toolhub": {Mode: "shared"},
	}
	actions := Plan([]passport.Change{ch("config.memory.enabled", "toolhub", passport.EffectRecreate)}, services)
	want := map[string]Kind{"communication-hub": Recreate, "toolhub": Recreate, "hermes-runtime": Respawn}
	if len(actions) != len(want) {
		t.Fatalf("memory switch left a worker on old config: %+v", actions)
	}
	for _, action := range actions {
		if want[action.Target] != action.Kind || !slices.Contains(action.Paths, "config.memory.enabled") {
			t.Fatalf("unexpected memory switch action: %+v", action)
		}
	}
}

func TestPlanHostingChangesReconcileHostingConsumers(t *testing.T) {
	services := map[string]passportview.ServiceSpec{"hosting": {Mode: "per-space"}, "hermes-runtime": {Mode: "supervised"}}
	actions := Plan([]passport.Change{
		ch("config.hosting", "compose", passport.EffectRebuild),
		ch("config.hosting.slots", "compose", passport.EffectRecreate),
		ch("config.hosting.image", "compose", passport.EffectRecreate),
		ch("config.hosting.runtime", "compose", passport.EffectRecreate),
	}, services)
	got := map[string]Kind{}
	for _, action := range actions {
		got[action.Target] = action.Kind
	}
	if len(got) != 2 || got["hosting"] != Recreate || got["hosting-controller"] != Recreate {
		t.Fatalf("hosting apply targeted wrong consumers: %+v", actions)
	}
	removed := ch("config.hosting.image", "compose", passport.EffectRecreate)
	removed.After = ""
	actions = Plan([]passport.Change{removed}, services)
	got = map[string]Kind{}
	for _, action := range actions {
		got[action.Target] = action.Kind
	}
	if got["hosting"] != Recreate || got["*"] != Remove || got["hosting-controller"] != "" {
		t.Fatalf("hosting image reset did not remove controller: %+v", actions)
	}
}

func TestPlanStaticHostingAdditionSkipsEmptyImage(t *testing.T) {
	services := map[string]passportview.ServiceSpec{"hosting": {Mode: "per-space"}, "hermes-runtime": {Mode: "supervised"}}
	actions := Plan([]passport.Change{
		{Path: "config.hosting", Before: "<none>", After: "", Effect: passport.EffectRebuild},
		{Path: "config.hosting.image", Before: "", After: "", Effect: passport.EffectRecreate},
		{Path: "config.hosting.runtime", Before: "", After: "", Effect: passport.EffectRecreate},
		{Path: "config.hosting.slots", Before: "", After: "[m32test]", Effect: passport.EffectRecreate},
	}, services)
	if len(actions) != 1 || actions[0].Kind != Recreate || actions[0].Target != "hosting" {
		t.Fatalf("static hosting addition planned unrelated effects: %+v", actions)
	}
}

// A port/channel leaf whose owner the new revision does not declare is
// dead config — nothing renders the container, so the plan must not
// schedule a recreate for it (the compose call would fail on a phantom
// service and roll the whole apply back).
func TestPlanSkipsRecreateForUndeclaredOwners(t *testing.T) {
	actions := Plan([]passport.Change{
		ch("config.ports.browser-vnc", "compose", passport.EffectRecreate),
		ch("config.ports.oauth-callback", "compose", passport.EffectRecreate),
		ch("config.channels.telegram.voice.in", "communication", passport.EffectRecreate),
	}, map[string]passportview.ServiceSpec{
		"hermes-runtime":    {Mode: "supervised"},
		"communication-hub": {Mode: "shared"},
	})
	if len(actions) != 2 {
		t.Fatalf("want 2 recreate actions (runtime + shared gateway), got %+v", actions)
	}
	var targets []string
	for _, a := range actions {
		targets = append(targets, a.Target)
	}
	if !slices.Contains(targets, "hermes-runtime") || !slices.Contains(targets, "communication-hub") {
		t.Fatalf("missing declared owners in %v", targets)
	}
	if slices.Contains(targets, "browser") {
		t.Fatalf("undeclared owner must not be recreated: %v", targets)
	}
}

func TestRunRollbackOnFailure(t *testing.T) {
	var calls []string
	recreateCalls, failingHookCalls := 0, 0
	h := Handlers{
		Rebuild: func(s string) error { calls = append(calls, "rebuild:"+s); return nil },
		Recreate: func(s string) error {
			calls = append(calls, "recreate:"+s)
			recreateCalls++
			if recreateCalls == 1 {
				return errors.New("compose exploded")
			}
			return nil
		},
		Respawn: func() error { calls = append(calls, "respawn"); return nil },
		Live: map[string]func() error{
			"toolhub-projection": func() error { calls = append(calls, "live:toolhub"); return nil },
			"zz-failing-hook": func() error {
				calls = append(calls, "live:failing")
				failingHookCalls++
				if failingHookCalls == 1 {
					return errors.New("hook exploded")
				}
				return nil
			},
		},
	}
	actions := Plan([]passport.Change{
		ch("stack.services.cliproxy.mode", "pending:stack", passport.EffectRebuild),
		ch("config.ports.p", "pending:config", passport.EffectRecreate),
		ch("tools.core.x.via", "toolhub-projection", passport.EffectLive),
		ch("tools.core.y.via", "zz-failing-hook", passport.EffectLive),
	}, allServices)
	rep := Run(actions, h, func() error { calls = append(calls, "pointer:prev"); return nil }, "r-new", "r-prev")
	if !rep.RolledBack || rep.Failed == nil || rep.Failed.Kind != Recreate {
		t.Fatalf("expected recreate failure + rollback: %+v", rep)
	}
	if !slices.Contains(calls, "pointer:prev") {
		t.Fatalf("current pointer never repointed: %v", calls)
	}
	if calls[len(calls)-1] != "recreate:cliproxy" {
		t.Fatalf("failed recreate was not replayed against prior revision: %v", calls)
	}
	// Live runs last: an earlier live failure re-runs the hooks that
	// already applied after the pointer is restored.
	calls = nil
	actions = Plan([]passport.Change{
		ch("tools.core.x.via", "toolhub-projection", passport.EffectLive),
		ch("tools.core.y.via", "zz-failing-hook", passport.EffectLive),
	}, allServices)
	rep = Run(actions, h, func() error { calls = append(calls, "pointer:prev"); return nil }, "r-new", "r-prev")
	if !rep.RolledBack || rep.Failed == nil || rep.Failed.Target != "zz-failing-hook" {
		t.Fatalf("expected live failure + rollback: %+v", rep)
	}
	if len(calls) != 5 || calls[0] != "live:toolhub" || calls[1] != "live:failing" || calls[2] != "pointer:prev" || calls[3] != "live:failing" || calls[4] != "live:toolhub" {
		t.Fatalf("applied live hook not re-run after rollback: %v", calls)
	}
}

func TestRunPartialServiceRollbackIsNotSuccess(t *testing.T) {
	var calls []string
	h := Handlers{
		Recreate: func(target string) error {
			calls = append(calls, target)
			if len(calls) == 2 {
				return errors.New("restore failed")
			}
			return nil
		},
		Live: map[string]func() error{"failure": func() error { return errors.New("apply failed") }},
	}
	rep := Run([]Action{{Kind: Recreate, Target: "hosting"}, {Kind: Live, Target: "failure"}}, h, func() error { return nil }, "new", "old")
	if rep.RolledBack || rep.Failed == nil || len(rep.Steps) < 4 || rep.Steps[len(rep.Steps)-1].Status != "failed" {
		t.Fatalf("partial hosting restore claimed success: %+v", rep)
	}
}

func TestRunPendingConsumersDeferNotFail(t *testing.T) {
	h := Handlers{
		Rebuild:  func(string) error { return nil },
		Recreate: func(string) error { return nil },
		Respawn:  func() error { return nil },
		Live:     map[string]func() error{},
	}
	actions := Plan([]passport.Change{ch("grants.allow.x", "pending:CHG-0259", passport.EffectLive)}, allServices)
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
	actions := Plan([]passport.Change{ch("stack.identity.user", "pending:stack", passport.EffectRebuild)}, allServices)
	rep := Run(actions, h, func() error { return errors.New("disk full") }, "r-1", "r-0")
	if rep.Failed == nil {
		t.Fatalf("expected failure: %+v", rep)
	}
	// A failed rollback is not a rollback: RolledBack stays false so callers
	// never report a clean restore while state diverges.
	if rep.RolledBack {
		t.Fatalf("failed rollback reported as rolled back: %+v", rep)
	}
	last := rep.Steps[len(rep.Steps)-1]
	if last.Detail == "" || rep.Steps[len(rep.Steps)-2].Status != "failed" {
		t.Fatalf("rollback error should be reported: %+v", rep.Steps)
	}
}
