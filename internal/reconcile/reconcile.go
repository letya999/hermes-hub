// Package reconcile turns a passport diff into the minimum ordered action
// set and executes it: rebuild the images that changed, recreate the
// containers that changed, respawn the runtime when its boundary moved and
// run live hooks for everything else. Any failure rolls the current pointer
// back to the previous revision and re-runs the live hooks so no half-active
// revision is left behind (SPEC-0092 R1.2).
package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/passportview"
)

// Kind is the ordered action class — execution always runs rebuild, then
// recreate, then respawn, then live.
type Kind string

const (
	Rebuild  Kind = "rebuild"
	Recreate Kind = "recreate"
	Remove   Kind = "remove"
	Respawn  Kind = "respawn"
	Live     Kind = "live"
)

var order = map[Kind]int{Rebuild: 0, Recreate: 1, Remove: 2, Respawn: 3, Live: 4}

// Action is one deduplicated unit of work.
type Action struct {
	Kind      Kind     `json:"kind"`
	Target    string   `json:"target"`    // service instance or "runtime"
	Consumers []string `json:"consumers"` // registry consumers behind the action
	Paths     []string `json:"paths"`     // changed leaves that forced it
}

// StepResult records what happened to one action.
type StepResult struct {
	Kind   Kind   `json:"kind"`
	Target string `json:"target"`
	Status string `json:"status"` // applied|deferred|failed|rolled-back
	Detail string `json:"detail,omitempty"`
}

// Report is the JSON-safe receipt space apply returns.
type Report struct {
	Revision   string       `json:"revision"`
	Previous   string       `json:"previous_revision,omitempty"`
	Steps      []StepResult `json:"steps"`
	RolledBack bool         `json:"rolled_back"`
	Failed     *Action      `json:"failed_action,omitempty"`
	Pruned     []string     `json:"pruned_revisions,omitempty"`
}

// Plan groups a classified diff into ordered unique actions. The action
// target is the service instance a stack change names, "hermes-runtime" for
// config/model changes (the runtime consumes them) and the consumer name for
// live leaves. services is the new revision's stack.services set.
func Plan(changes []passport.Change, services map[string]passportview.ServiceSpec) []Action {
	byKey := map[string]*Action{}
	get := func(k Kind, target string) *Action {
		key := string(k) + ":" + target
		if a, ok := byKey[key]; ok {
			return a
		}
		a := &Action{Kind: k, Target: target}
		byKey[key] = a
		return a
	}
	for _, c := range changes {
		if c.Path == "config.web.backend" || c.Path == "config.web.search_backend" || c.Path == "config.web.extract_backend" || c.Path == "config.web.search_providers" || c.Path == "stack.services.hub-media.mode" {
			if _, rendered := services["egress-relay"]; rendered {
				a := get(Recreate, "egress-relay")
				a.Consumers = appendUnique(a.Consumers, "egress-relay")
				a.Paths = appendUnique(a.Paths, c.Path)
			}
		}
		if c.Path == "stack.services.hub-media.mode" {
			if _, rendered := services["hermes-runtime"]; rendered {
				a := get(Respawn, "hermes-runtime")
				a.Consumers = appendUnique(a.Consumers, "hermes-runtime")
				a.Paths = appendUnique(a.Paths, c.Path)
			}
		}
		if c.Path == "config.hosting" {
			// The typed hosting leaves below own all deployment effects.
			continue
		}
		if strings.HasPrefix(c.Path, "config.hosting.") {
			if c.Before == c.After {
				continue
			}
			if _, rendered := services["hosting"]; !rendered {
				continue
			}
			a := get(Recreate, "hosting")
			a.Consumers = appendUnique(a.Consumers, c.Consumer)
			a.Paths = appendUnique(a.Paths, c.Path)
			if c.Path != "config.hosting.slots" {
				kind, target := Recreate, "hosting-controller"
				if c.Path == "config.hosting.image" && c.After == "" {
					kind, target = Remove, "*"
				} else if c.After == "" {
					continue
				}
				a = get(kind, target)
				a.Consumers = appendUnique(a.Consumers, c.Consumer)
				a.Paths = appendUnique(a.Paths, c.Path)
			}
			continue
		}
		if c.Path == "config.memory.enabled" {
			for _, target := range []string{"communication-hub", "toolhub"} {
				if _, rendered := services[target]; rendered {
					a := get(Recreate, target)
					a.Consumers = appendUnique(a.Consumers, c.Consumer)
					a.Paths = appendUnique(a.Paths, c.Path)
				}
			}
			if _, rendered := services["hermes-runtime"]; rendered {
				a := get(Respawn, "hermes-runtime")
				a.Consumers = appendUnique(a.Consumers, c.Consumer)
				a.Paths = appendUnique(a.Paths, c.Path)
			}
			continue
		}
		var a *Action
		switch c.Effect {
		case passport.EffectRebuild:
			// A services leaf that vanishes removes a container, not a
			// rebuild — the new render has no such service and the build
			// would fail with "no such service". One project-level remove
			// sweep covers every instance a revision drops.
			if strings.HasPrefix(c.Path, "stack.services.") && c.After == "" {
				a = get(Remove, "*")
			} else {
				a = get(Rebuild, stackTarget(c.Path))
				targets := []string{a.Target}
				if a.Target == "*" {
					targets = nil
					for name := range services {
						targets = append(targets, name)
					}
				}
				for _, target := range targets {
					activate := get(Recreate, target)
					activate.Consumers = appendUnique(activate.Consumers, c.Consumer)
					activate.Paths = appendUnique(activate.Paths, c.Path)
				}
			}
		case passport.EffectRecreate:
			target := runtimeTarget(c.Path)
			if _, rendered := services[target]; !rendered {
				// The owner is dead config — the new revision declares no
				// such service, so there is no container to recreate (the
				// leaf still shows in the classified diff).
				continue
			}
			a = get(Recreate, target)
		case passport.EffectRespawn:
			a = get(Respawn, "hermes-runtime")
		default:
			a = get(Live, c.Consumer)
		}
		if c.Consumer != "" {
			a.Consumers = appendUnique(a.Consumers, c.Consumer)
		}
		a.Paths = appendUnique(a.Paths, c.Path)
	}
	out := make([]Action, 0, len(byKey))
	for _, a := range byKey {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if order[out[i].Kind] != order[out[j].Kind] {
			return order[out[i].Kind] < order[out[j].Kind]
		}
		return out[i].Target < out[j].Target
	})
	for i := range out {
		sort.Strings(out[i].Consumers)
		sort.Strings(out[i].Paths)
	}
	return out
}

// stackTarget names the service instance a stack leaf belongs to; non-service
// stack changes (identity) rebuild every image, expressed as "*".
func stackTarget(path string) string {
	if strings.HasPrefix(path, "stack.services.") {
		rest := strings.TrimPrefix(path, "stack.services.")
		if i := strings.IndexByte(rest, '.'); i >= 0 {
			return rest[:i]
		}
		return rest
	}
	return "*"
}

// runtimeTarget maps a recreate-class leaf to the container that consumes it:
// config.ports.<name> recreates the declared owner, config.channels the shared
// gateway, a stack.services leaf its own service; remaining config/models
// changes land on hermes-runtime.
func runtimeTarget(path string) string {
	if strings.HasPrefix(path, "stack.services.") {
		return stackTarget(path)
	}
	if name, ok := strings.CutPrefix(path, "config.ports."); ok {
		if owner, known := passport.PortOwners[name]; known {
			return owner
		}
		return "hermes-runtime"
	}
	if strings.HasPrefix(path, "config.channels") {
		return "communication-hub"
	}
	return "hermes-runtime"
}

func appendUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// Handlers are the real effects an action performs; tests inject stubs, the
// CLI wires the compose/supervisor paths. A missing live hook means the
// consumer's runtime wiring belongs to a later stage — the step is recorded
// as deferred, never silently claimed applied.
type Handlers struct {
	Rebuild  func(service string) error
	Recreate func(service string) error
	Remove   func() error
	Respawn  func() error
	Live     map[string]func() error
}

// Run executes the plan in order. On failure it restores the previous
// revision and replays every action that may already have changed a service.
func Run(actions []Action, h Handlers, publishRollback func() error, revision, previous string) Report {
	rep := Report{Revision: revision, Previous: previous}
	var applied []Action
	for _, a := range actions {
		step := StepResult{Kind: a.Kind, Target: a.Target, Status: "applied"}
		var err error
		switch a.Kind {
		case Rebuild:
			err = h.Rebuild(a.Target)
		case Recreate:
			err = h.Recreate(a.Target)
		case Remove:
			err = h.Remove()
		case Respawn:
			err = h.Respawn()
		case Live:
			hook, ok := h.Live[a.Target]
			switch {
			case !ok || strings.HasPrefix(a.Target, "pending:"):
				step.Status, step.Detail = "deferred", "consumer wiring lands with "+a.Target
			case hook == nil:
				step.Status, step.Detail = "deferred", "consumer has no live hook yet"
			default:
				err = hook()
			}
		}
		if err != nil {
			step.Status, step.Detail = "failed", err.Error()
			rep.Steps = append(rep.Steps, step)
			failed := a
			rep.Failed = &failed
			if rbErr := publishRollback(); rbErr != nil {
				rep.Steps = append(rep.Steps, StepResult{Kind: a.Kind, Target: a.Target, Status: "failed", Detail: "rollback pointer: " + rbErr.Error()})
			} else {
				rep.RolledBack = true
				if a.Kind != Rebuild {
					applied = append(applied, a) // a failed handler may have changed its target.
				}
				for i := len(applied) - 1; i >= 0; i-- {
					prior := applied[i]
					st := StepResult{Kind: prior.Kind, Target: prior.Target, Status: "rolled-back"}
					replayErr := replayAction(prior, h)
					if replayErr != nil {
						st.Status, st.Detail = "failed", "rollback: "+replayErr.Error()
						rep.RolledBack = false
					}
					rep.Steps = append(rep.Steps, st)
				}
			}
			return rep
		}
		if a.Kind != Rebuild && step.Status == "applied" {
			applied = append(applied, a)
		}
		rep.Steps = append(rep.Steps, step)
	}
	return rep
}

func replayAction(action Action, h Handlers) error {
	switch action.Kind {
	case Rebuild:
		return nil // an unused rebuilt image does not change the old service
	case Recreate:
		if h.Recreate != nil {
			return h.Recreate(action.Target)
		}
	case Remove:
		if h.Remove != nil {
			return h.Remove()
		}
	case Respawn:
		if h.Respawn != nil {
			return h.Respawn()
		}
	case Live:
		if hook := h.Live[action.Target]; hook != nil {
			return hook()
		}
	}
	return fmt.Errorf("rollback handler unavailable for %s %s", action.Kind, action.Target)
}

// Replay conservatively converges every action that might have started before
// a process stop. Re-running a service effect must be safe on the old render.
func Replay(actions []Action, h Handlers) error {
	for i := len(actions) - 1; i >= 0; i-- {
		if err := replayAction(actions[i], h); err != nil {
			return fmt.Errorf("%s %s: %w", actions[i].Kind, actions[i].Target, err)
		}
	}
	return nil
}

// Summarize renders the plan grouped by effect class for the dry-run diff
// view — one line per action, paths folded in.
func Summarize(actions []Action) string {
	var b strings.Builder
	for _, a := range actions {
		fmt.Fprintf(&b, "%s %s", a.Kind, a.Target)
		if len(a.Paths) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(a.Paths, ", "))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
