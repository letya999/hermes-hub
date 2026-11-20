// Package backendtest is the launch-backend conformance suite: one case set
// every backend runs against a Leg that builds it and observes its work.
// Provider-specific observation travels through optional hooks: the docker
// leg inspects containers through the CLI, the process leg queries its
// helper child's /self endpoint.
package backendtest

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
)

// SelfInfo is what a process-leg helper child reports about itself.
type SelfInfo struct {
	Args []string
	Env  []string
}

// BindMount is one container bind as the daemon reports it.
type BindMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// ContainerInfo is the daemon-side view of a started workload.
type ContainerInfo struct {
	Binds    []BindMount
	Networks []string
	Args     []string
	Cmd      []string
	Env      []string
}

// Leg adapts the shared cases to one provider. Container and NetworkName
// are set by the docker leg, Self by the process leg; a nil hook selects the
// generic leg's own assertions instead.
type Leg struct {
	Factory      func(t *testing.T) backend.Backend
	Spec         func(t *testing.T, key string) backend.Spec
	Container    func(t *testing.T, key string) ContainerInfo
	Self         func(t *testing.T, rt backend.Runtime) SelfInfo
	SpawnForeign func(t *testing.T)
	NetworkName  func(space, edge string) string
	RefusesEdges bool
}

// Suite runs every conformance case as a subtest against the leg.
func Suite(t *testing.T, leg Leg) {
	t.Helper()
	t.Run("start_stop_inspect_list", func(t *testing.T) { StartStopInspectList(t, leg) })
	t.Run("inspect_unknown_not_found", func(t *testing.T) { InspectUnknownNotFound(t, leg) })
	t.Run("only_own_workspace_mounted", func(t *testing.T) { OnlyOwnWorkspaceMounted(t, leg) })
	t.Run("no_secret_in_env_or_argv", func(t *testing.T) { NoSecretInEnvOrArgv(t, leg) })
	t.Run("network_edges_applied", func(t *testing.T) { NetworkEdgesApplied(t, leg) })
	t.Run("list_returns_only_own_workloads", func(t *testing.T) { ListReturnsOnlyOwnWorkloads(t, leg) })
	t.Run("mount_option_injection_refused", func(t *testing.T) { MountOptionInjectionRefused(t, leg) })
}

func key() string { return "rt-" + rand.Text()[:16] }

func stopOnCleanup(t *testing.T, be backend.Backend, id string) {
	t.Helper()
	// t.Context() is already cancelled when cleanups run.
	t.Cleanup(func() { _ = be.Stop(context.Background(), id) })
}

// StartStopInspectList covers the lifecycle: a started runtime inspects as
// running, lists, stops, and afterwards inspects as typed not-found.
func StartStopInspectList(t *testing.T, leg Leg) {
	t.Helper()
	be := leg.Factory(t)
	spec := leg.Spec(t, key())
	rt, err := be.Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	stopOnCleanup(t, be, spec.Key)
	if rt.ID != spec.Key {
		t.Fatalf("runtime id %q, want %q", rt.ID, spec.Key)
	}
	got, err := be.Inspect(t.Context(), spec.Key)
	if err != nil {
		t.Fatalf("inspect started: %v", err)
	}
	if got.State != "running" {
		t.Fatalf("state %q, want running", got.State)
	}
	if !hasRuntime(t, be, spec.Key) {
		t.Fatal("list misses the started runtime")
	}
	if err := be.Stop(t.Context(), spec.Key); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := be.Inspect(t.Context(), spec.Key); !errors.Is(err, backend.ErrNotFound) {
		t.Fatalf("inspect stopped: %v, want backend.ErrNotFound", err)
	}
	if hasRuntime(t, be, spec.Key) {
		t.Fatal("stopped runtime still listed")
	}
}

// InspectUnknownNotFound: an id the backend never started answers the typed
// not-found, never a bare error S4 cannot classify.
func InspectUnknownNotFound(t *testing.T, leg Leg) {
	t.Helper()
	if _, err := leg.Factory(t).Inspect(t.Context(), "rt-never-started"); !errors.Is(err, backend.ErrNotFound) {
		t.Fatalf("inspect unknown: %v, want backend.ErrNotFound", err)
	}
}

// OnlyOwnWorkspaceMounted: docker binds are exactly the Spec's; the process
// child sees only its own directories — spec mounts never reach it.
func OnlyOwnWorkspaceMounted(t *testing.T, leg Leg) {
	t.Helper()
	be := leg.Factory(t)
	workspace := t.TempDir()
	config := t.TempDir()
	foreign := t.TempDir()
	spec := leg.Spec(t, key())
	spec.Mounts = []backend.Mount{
		{Source: workspace, Target: "/workspace"},
		{Source: config, Target: "/config", ReadOnly: true},
		{Source: foreign, Target: "/foreign"},
	}
	rt, err := be.Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	stopOnCleanup(t, be, spec.Key)
	if leg.Container != nil {
		want := []BindMount{
			{Source: filepath.Clean(workspace), Target: "/workspace"},
			{Source: filepath.Clean(config), Target: "/config", ReadOnly: true},
			{Source: filepath.Clean(foreign), Target: "/foreign"},
		}
		got := leg.Container(t, spec.Key).Binds
		if len(got) != len(want) {
			t.Fatalf("binds %v, want exactly %v", got, want)
		}
		for _, w := range want {
			found := false
			for _, g := range got {
				if g.Target == w.Target && g.ReadOnly == w.ReadOnly && strings.EqualFold(filepath.Clean(g.Source), w.Source) {
					found = true
				}
			}
			if !found {
				t.Fatalf("binds %v miss %v", got, w)
			}
		}
		return
	}
	self := leg.Self(t, rt)
	leaks := []string{workspace, config, foreign, "/workspace", "/config", "/foreign"}
	for _, line := range slices.Concat(self.Args, self.Env) {
		for _, leak := range leaks {
			if strings.Contains(line, leak) {
				t.Fatalf("spec mount %q reached the child through %q", leak, line)
			}
		}
	}
}

// NoSecretInEnvOrArgv: a Spec env value travels only through the child's
// environment, never through argv on either backend.
func NoSecretInEnvOrArgv(t *testing.T, leg Leg) {
	t.Helper()
	be := leg.Factory(t)
	marker := "m40-secret-" + rand.Text()[:16]
	spec := leg.Spec(t, key())
	spec.Env = append(spec.Env, "HUB_CONFORMANCE_MARKER="+marker)
	rt, err := be.Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	stopOnCleanup(t, be, spec.Key)
	var argv, env []string
	if leg.Container != nil {
		info := leg.Container(t, spec.Key)
		argv = slices.Concat(info.Args, info.Cmd)
		env = info.Env
	} else {
		self := leg.Self(t, rt)
		argv, env = self.Args, self.Env
	}
	if strings.Contains(strings.Join(argv, "\x00"), marker) {
		t.Fatalf("env value leaked into argv: %q", argv)
	}
	if !slices.Contains(env, "HUB_CONFORMANCE_MARKER="+marker) {
		t.Fatalf("env value missing from the workload environment: %q", env)
	}
}

// NetworkEdgesApplied: docker attaches exactly the per-space edge networks
// (none when the spec has none); a backend that cannot isolate refuses.
func NetworkEdgesApplied(t *testing.T, leg Leg) {
	t.Helper()
	be := leg.Factory(t)
	spec := leg.Spec(t, key())
	spec.Space = "edge-space"
	spec.Edges = []string{"alpha", "beta"}
	rt, err := be.Start(t.Context(), spec)
	if leg.RefusesEdges {
		if err == nil {
			_ = be.Stop(t.Context(), spec.Key)
			t.Fatal("backend accepted network edges it cannot isolate")
		}
		return
	}
	if err != nil {
		t.Fatalf("start with edges: %v", err)
	}
	stopOnCleanup(t, be, spec.Key)
	_ = rt
	want := []string{leg.NetworkName(spec.Space, "alpha"), leg.NetworkName(spec.Space, "beta")}
	slices.Sort(want)
	got := slices.Clone(leg.Container(t, spec.Key).Networks)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("networks %v, want exactly %v", got, want)
	}
	// No edges means no network at all.
	spec2 := leg.Spec(t, key())
	rt2, err := be.Start(t.Context(), spec2)
	if err != nil {
		t.Fatalf("start without edges: %v", err)
	}
	_ = rt2
	stopOnCleanup(t, be, spec2.Key)
	if nets := leg.Container(t, spec2.Key).Networks; len(nets) != 0 {
		t.Fatalf("edge-less spec attached networks %v, want none", nets)
	}
}

// ListReturnsOnlyOwnWorkloads: workloads the backend did not start — even
// ones sharing the test label — never appear in its List.
func ListReturnsOnlyOwnWorkloads(t *testing.T, leg Leg) {
	t.Helper()
	be := leg.Factory(t)
	if leg.SpawnForeign != nil {
		leg.SpawnForeign(t)
	}
	spec := leg.Spec(t, key())
	if _, err := be.Start(t.Context(), spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	stopOnCleanup(t, be, spec.Key)
	list, err := be.List(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != spec.Key {
		t.Fatalf("list %v, want only %q", list, spec.Key)
	}
}

// MountOptionInjectionRefused: a mount source or target that carries the
// characters the docker --mount CSV grammar splits on would inject extra
// mount options — a comma reads as a new option, '=' a value, '"' or a
// newline breaks the flag. Such a Spec is refused before any daemon call
// or disk touch, and the backend shows no workload for the key.
func MountOptionInjectionRefused(t *testing.T, leg Leg) {
	t.Helper()
	be := leg.Factory(t)
	base := t.TempDir()
	ok := filepath.Join(base, "ok")
	if err := os.Mkdir(ok, 0o700); err != nil {
		t.Fatal(err)
	}
	// Sources that exist on disk AND carry option-breaking characters:
	// docker would parse src=<path>,dst=<tgt> and treat the tail as more
	// options, so "does not exist" is not the refusal being exercised.
	evilComma := filepath.Join(base, "src,readonly")
	evilEquals := filepath.Join(base, "src=x")
	for _, dir := range []string{evilComma, evilEquals} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []backend.Mount{
		{Source: evilComma, Target: "/mnt"},
		{Source: evilEquals, Target: "/mnt"},
		{Source: "evil\"quoted", Target: "/mnt"},
		{Source: "evil\nnewline", Target: "/mnt"},
		{Source: "evil\rreturn", Target: "/mnt"},
		{Source: ok, Target: "/m,nt"},
		{Source: ok, Target: "/m=nt"},
		{Source: ok, Target: "/mnt\nx"},
	} {
		spec := leg.Spec(t, key())
		spec.Mounts = []backend.Mount{m}
		if _, err := be.Start(t.Context(), spec); err == nil {
			_ = be.Stop(t.Context(), spec.Key)
			t.Fatalf("mount %+v: start succeeded, want option-injection refusal", m)
		}
		if _, err := be.Inspect(t.Context(), spec.Key); !errors.Is(err, backend.ErrNotFound) {
			t.Fatalf("mount %+v: refused start left a workload behind: %v", m, err)
		}
	}
}

func hasRuntime(t *testing.T, be backend.Backend, id string) bool {
	t.Helper()
	list, err := be.List(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rt := range list {
		if rt.ID == id {
			return true
		}
	}
	return false
}
