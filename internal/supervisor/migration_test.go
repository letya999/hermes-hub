package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/stack"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupervisedRuntimeUsesDevEnvFile(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Environment = "dev"
	b, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(b, "fixture", 19000, "fixture-generation")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "src="+root+",dst=/scope") {
		t.Fatal("whole context root mounted into runtime")
	}
	if !strings.Contains(joined, "--env-file "+filepath.Join(root, "runtime.dev.env")) {
		t.Fatal("dev runtime env file missing from docker run")
	}
	if strings.Contains(joined, "runtime.prod.env") {
		t.Fatal("prod runtime env selected for dev supervisor")
	}
	if !strings.Contains(joined, "hermes.dev.yaml") {
		t.Fatal("dev hermes config not mounted")
	}
	if strings.Contains(joined, "test-key") {
		t.Fatal("secret exposed in arguments")
	}
}

func TestManagedSupervisorRefusesBeforeSpawnPreparation(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	configDir := filepath.Join(filepath.Dir(root), "config")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "SOUL.md"), []byte("template"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SOUL.md"), []byte("# User instructions\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Even an incomplete managed profile must fail before the legacy path
	// mutates the space or constructs a shared-network Docker command.
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte("schema: 1\nuser: alice\ncapability_mode: managed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if args, err := m.runArgsWithGeneration(binding(root), "fixture", 19000, "generation"); err == nil || args != nil {
		t.Fatalf("managed runtime entered legacy spawn: args=%v err=%v", args, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "SOUL.md"))
	if err != nil || string(body) != "# User instructions\n\n" {
		t.Fatalf("managed refusal mutated SOUL: %q %v", body, err)
	}
}

func TestNormalizeRejectsMissingRuntimeEnvFile(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	if err := os.Remove(filepath.Join(root, "runtime.prod.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.normalize(binding(root)); err == nil {
		t.Fatal("missing runtime env file accepted")
	}
}

func TestSupervisedRuntimePreservesComposeEnvironmentAndConnectionPaths(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte("schema: 1\nuser: alice\ntimezone: UTC\nfeatures: [workspace, browser]\nbrowser_port: 6080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.prod.env"), []byte("OPENAI_API_KEY=synthetic-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hermes.prod.yaml"), []byte("model: {}"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(b, "fixture", 19000, "fixture-generation")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--env-file " + filepath.Join(root, "runtime.prod.env"), "--env-file " + filepath.Join(root, "runtime.auth"), "dst=/scope,readonly", "dst=/state/hermes", "dst=/state/google", "dst=/state/telegram", "dst=/state/home", "dst=/workspace", "dst=/archive,readonly", "HUB_BROWSER=true", "HUB_FEATURES=browser,workspace", "BROWSER_CDP_URL=http://browser-alice:9222", "HUB_STATE=/state", "--tmpfs /tmp:", "--add-host host.docker.internal:host-gateway"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Index(joined, "runtime.prod.env") > strings.Index(joined, "runtime.auth") {
		t.Fatal("runtime auth must override provider env")
	}
	if strings.Contains(joined, "synthetic-only") || strings.Contains(joined, "dst=/src") || strings.Contains(joined, "docker.sock") {
		t.Fatal("secret/source/control exposed in arguments")
	}
	// The browser stack lives in the per-space sidecar: the spawned runtime
	// carries neither the profile mount nor the shared-memory budget for
	// in-container Chromium (SPEC-0082 R4.1).
	if strings.Contains(joined, "dst=/state/browser") || strings.Contains(joined, "--shm-size") {
		t.Fatal("browser profile mount or shared memory leaked into the lean runtime")
	}
	for _, target := range []string{"/state/hermes", "/state/google", "/workspace"} {
		count := 0
		for _, arg := range args {
			if strings.HasSuffix(arg, ",dst="+target) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("mount %s count=%d", target, count)
		}
	}
}

// Spawned unmanaged runtimes join only the shared control network; a browser
// or meet space's sidecar answers on the space's own compose default net. The
// spawn must dual-home the runtime so browser-<user> resolves (SPEC-0082 R4.2).
func TestBrowserSpaceRuntimeJoinsSpaceNetwork(t *testing.T) {
	for _, tc := range []struct {
		user     string
		features string
	}{
		{"alice", "workspace, browser"},
		{"bob", "workspace, meet"},
	} {
		t.Run(tc.user, func(t *testing.T) {
			var commands [][]string
			m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				cp := append([]string(nil), args...)
				commands = append(commands, cp)
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				return []byte("running"), nil
			}, func(context.Context, string, string) error { return nil })
			ctxRoot := filepath.Join(m.cfg.SpacesRoot, tc.user)
			for _, name := range []string{"runtime", "hermes", "workspace"} {
				if err := os.MkdirAll(filepath.Join(ctxRoot, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(ctxRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
				t.Fatal(err)
			}
			writeHermesSources(t, ctxRoot)
			if err := os.WriteFile(filepath.Join(ctxRoot, "settings.yaml"), []byte("schema: 1\nuser: "+tc.user+"\ntimezone: UTC\nfeatures: ["+tc.features+"]\nbrowser_port: 6080\n"), 0600); err != nil {
				t.Fatal(err)
			}
			b := Binding{PrincipalID: tc.user, ContextID: tc.user, RuntimeID: tc.user, RuntimeMode: "gateway", UserID: tc.user, OrganizationID: "personal", PolicyVersion: "policy-1", ContextRoot: ctxRoot}
			if _, err := m.Ensure(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			connect := -1
			run := -1
			for i, call := range commands {
				if len(call) >= 4 && call[0] == "network" && call[1] == "connect" && call[2] == "hermes-hub-"+tc.user+"-prod_default" {
					connect = i
				}
				if call[0] == "run" {
					run = i
				}
			}
			if run < 0 {
				t.Fatal("runtime was not spawned")
			}
			if connect < 0 {
				t.Fatal("browser-enabled spawn must connect the runtime to the space default network")
			}
			if connect < run {
				t.Fatal("network connect must follow the run so the container exists")
			}
		})
	}
}

// If the space default network is absent (hubctl up never ran for this
// browser space), the spawn must fail and reclaim the started container —
// a runtime that cannot reach its sidecar is worse than no runtime.
func TestBrowserSpaceSpawnFailsWhenSpaceNetworkMissing(t *testing.T) {
	var commands [][]string
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		cp := append([]string(nil), args...)
		commands = append(commands, cp)
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if len(args) >= 2 && args[0] == "network" && args[1] == "connect" {
			return []byte("no such network"), errors.New("exit status 1")
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte("schema: 1\nuser: alice\ntimezone: UTC\nfeatures: [workspace, browser]\nbrowser_port: 6080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), binding(root)); err == nil {
		t.Fatal("spawn without the space network must fail")
	}
	removed := false
	for _, call := range commands {
		if len(call) >= 2 && call[0] == "rm" {
			removed = true
		}
	}
	if !removed {
		t.Fatal("orphaned runtime container was not reclaimed after connect failure")
	}
}

func TestSupervisedRuntimeMountsToolPolicySnapshot(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Environment = "prod"
	b, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(b, "fixture", 19000, "fixture-generation")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	// The executor contract must ride into the supervised runtime exactly as
	// the rendered compose mounts it: generated snapshot, read-only, pinned
	// to /config/tool-policy.json (HUB_TOOL_POLICY).
	if !strings.Contains(joined, "tool-policy.prod.json,dst=/config/tool-policy.json,readonly") {
		t.Fatalf("tool policy snapshot missing from spawn args: %s", joined)
	}
	if _, err := os.Stat(filepath.Join(root, "generated", "tool-policy.prod.json")); err != nil {
		t.Fatalf("tool policy snapshot not materialized at spawn: %v", err)
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
	for _, name := range []string{"runtime.prod.env", "hermes.prod.yaml", "SOUL.md"} {
		t.Run(name, func(t *testing.T) {
			m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
			_ = os.Remove(filepath.Join(root, name))
			if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := m.normalize(binding(root)); err == nil {
				t.Fatal("nonregular runtime input accepted")
			}
		})
	}
}
