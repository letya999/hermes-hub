package supervisor

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/stack"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeRejectsMissingRuntimeEnvFile(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	if err := os.Remove(filepath.Join(root, "managed-runtime.prod.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.normalize(context.Background(), binding(root)); err == nil {
		t.Fatal("missing runtime env file accepted")
	}
}

// The runtime joins its agent network and wakes the separate human browser
// sidecar (SPEC-0083 R2).

// SPEC-0083 R2: a slept or just-rendered sidecar must be started before the
// spawn proceeds — compose owns its shape, the supervisor owns its run state.

// SPEC-0083 R2.2: a sidecar that cannot be started fails the spawn and
// reclaims the runtime container, same as a missing space network.

// If the supervisor control network cannot take the relay (the managed
// topology's inbound route), the spawn must fail and reclaim the relay it
// just created — an orphaned sidecar on the agent net is worse than none.

// SPEC-0083 R3: a sidecar with no live runtime in its project sleeps after
// the idle grace — it is a warm resource of the space's runtime, not
// resident infrastructure.
func TestReapIdleBrowserSidecars(t *testing.T) {
	now := time.Unix(200, 0)
	var commands [][]string
	m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if args[0] == "ps" {
			return []byte(
				"hermes-hub-alice-prod-browser-alice-1 browser-alice hermes-hub-alice-prod\n" +
					"hermes-hub-bob-prod-browser-bob-1 browser-bob hermes-hub-bob-prod\n" +
					"hermes-hub-bob-prod-hermes-runtime-1 hermes-runtime hermes-hub-bob-prod\n" +
					"hermes-hub-carol-dev-browser-carol-1 browser-carol hermes-hub-carol-dev\n" +
					"other-app-browser-1 browser-x other-app\n"), nil
		}
		return []byte("ok"), nil
	}, func(context.Context, string, string) error { return nil })
	m.cfg.Environment = "prod"
	m.cfg.BrowserIdleTTL = time.Minute
	ctx := context.Background()
	m.ReapIdleBrowserSidecars(ctx, now)
	// docker ps renders .Labels as a comma-joined string, not a map — index on
	// it errors client-side and silently disables the reaper in production.
	// The template must use the .Label accessor.
	var psFormat string
	for _, call := range commands {
		if call[0] == "ps" {
			psFormat = call[len(call)-1]
		}
	}
	if !strings.Contains(psFormat, `{{.Label "com.docker.compose.service"}}`) || strings.Contains(psFormat, "index .Labels") {
		t.Fatalf("ps format must use .Label accessors, got %q", psFormat)
	}
	stopCount := func() int {
		n := 0
		for _, call := range commands {
			if len(call) == 2 && call[0] == "stop" {
				n++
			}
		}
		return n
	}
	if stopCount() != 0 {
		t.Fatalf("first sighting must only mark the deadline: %v", commands)
	}
	m.ReapIdleBrowserSidecars(ctx, now.Add(2*time.Minute))
	stops := map[string]bool{}
	for _, call := range commands {
		if len(call) == 2 && call[0] == "stop" {
			stops[call[1]] = true
		}
	}
	if !stops["hermes-hub-alice-prod-browser-alice-1"] {
		t.Fatal("idle sidecar past grace must be stopped")
	}
	if stops["hermes-hub-bob-prod-browser-bob-1"] {
		t.Fatal("sidecar must stay up while a compose hermes-runtime runs in its project")
	}
	if stops["hermes-hub-carol-dev-browser-carol-1"] {
		t.Fatal("reaper must never touch another environment's project")
	}
	if stops["other-app-browser-1"] {
		t.Fatal("reaper must never touch containers outside hermes-hub projects")
	}
	if m.sidecarIdle["other-app-browser-1"] != (time.Time{}) || m.sidecarIdle["hermes-hub-carol-dev-browser-carol-1"] != (time.Time{}) {
		t.Fatal("foreign containers must never even be marked idle")
	}
	// A stopped sidecar drops its deadline; the map must not accumulate
	// stale entries across sweeps.
	if _, kept := m.sidecarIdle["hermes-hub-alice-prod-browser-alice-1"]; kept {
		t.Fatal("a stopped sidecar must drop its deadline")
	}
}

// SPEC-0083 R3.1: a supervisor-spawned runtime keeps its sidecar alive even
// though no compose hermes-runtime container exists for the space.

// SPEC-0083 R3.1: the sweep itself runs the sidecar reaper — an idle
// sidecar dies on the supervisor's normal tick, not just under a direct
// call.
func TestSweepReapsIdleSidecar(t *testing.T) {
	now := time.Unix(400, 0)
	var commands [][]string
	m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if args[0] == "ps" && len(args) > 1 && strings.Contains(strings.Join(args[1:], " "), "com.docker.compose.project") {
			return []byte("hermes-hub-alice-prod-browser-alice-1 browser-alice hermes-hub-alice-prod\n"), nil
		}
		return []byte(""), nil
	}, func(context.Context, string, string) error { return nil })
	m.cfg.Environment = "prod"
	m.cfg.BrowserIdleTTL = time.Minute
	ctx := context.Background()
	m.sweep(ctx, now)
	m.sweep(ctx, now.Add(2*time.Minute))
	stopped := false
	for _, call := range commands {
		if len(call) == 2 && call[0] == "stop" && call[1] == "hermes-hub-alice-prod-browser-alice-1" {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("sweep must stop an idle sidecar past grace: %v", commands)
	}
}

func TestLegacySelectionPreventsSupervisorFromStartingContext(t *testing.T) {
	starts := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			starts++
		}
		return nil, os.ErrNotExist
	}, func(context.Context, string, string) error { return nil })
	selection := stack.ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "static", NativeCron: "disabled", CompatibilityRelease: "0.2.0"}
	body, _ := json.Marshal(selection)
	if err := os.WriteFile(filepath.Join(root, stack.ExecutionPath("prod")), body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), binding(root)); err == nil || starts != 0 {
		t.Fatal("legacy context started by supervisor")
	}
}

func TestRuntimeInputSymlinkAndDirectoryAreRejected(t *testing.T) {
	for _, name := range []string{"managed-runtime.prod.env", filepath.Join("generated", "hermes.prod.yaml"), "SOUL.md"} {
		t.Run(name, func(t *testing.T) {
			m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
			_ = os.Remove(filepath.Join(root, name))
			if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := m.normalize(context.Background(), binding(root)); err == nil {
				t.Fatal("nonregular runtime input accepted")
			}
		})
	}
}
