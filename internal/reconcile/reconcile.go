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
}

// Plan groups a classified diff into ordered unique actions. The action
// target is the service instance a stack change names, "hermes-runtime" for
// config/model changes (the runtime consumes them) and the consumer name for
// live leaves.
func Plan(changes []passport.Change) []Action {
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
			}
		case passport.EffectRecreate:
			a = get(Recreate, runtimeTarget(c.Path))
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
// config and models changes land on hermes-runtime; a stack.services leaf
// recreates its own service.
func runtimeTarget(path string) string {
	if strings.HasPrefix(path, "stack.services.") {
		return stackTarget(path)
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

// Run executes the plan in order. The first failure rolls the current
// pointer back to previous and re-runs the already-applied live hooks so
// their state matches the restored revision; the report always names the
// failed action.
func Run(actions []Action, h Handlers, publishRollback func() error, revision, previous string) Report {
	rep := Report{Revision: revision, Previous: previous}
	var appliedLive []Action
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
			rep.RolledBack = true
			if rbErr := publishRollback(); rbErr != nil {
				rep.Steps = append(rep.Steps, StepResult{Kind: a.Kind, Target: a.Target, Status: "failed", Detail: "rollback pointer: " + rbErr.Error()})
			}
			for _, la := range appliedLive {
				st := StepResult{Kind: Live, Target: la.Target, Status: "rolled-back"}
				if hook := h.Live[la.Target]; hook != nil {
					if err := hook(); err != nil {
						st.Status, st.Detail = "failed", "live rollback: "+err.Error()
					}
				}
				rep.Steps = append(rep.Steps, st)
			}
			return rep
		}
		if a.Kind == Live && step.Status == "applied" {
			appliedLive = append(appliedLive, a)
		}
		rep.Steps = append(rep.Steps, step)
	}
	return rep
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
