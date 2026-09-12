package supervisor

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/stack"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupervisedRuntimePreservesComposeEnvironmentAndConnectionPaths(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte("schema: 1\nuser: alice\ntimezone: UTC\nfeatures: [workspace, browser]\nbrowser_port: 6080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.prod.env"), []byte("OPENAI_API_KEY=synthetic-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args := m.runArgsWithGeneration(b, "fixture", 19000, "fixture-generation")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--env-file " + filepath.Join(root, "runtime.prod.env"), "--env-file " + filepath.Join(root, "runtime.auth"), "dst=/scope,readonly", "dst=/state/hermes", "dst=/state/google", "dst=/state/telegram", "dst=/state/browser", "dst=/state/home", "dst=/workspace", "dst=/archive,readonly", "HUB_BROWSER=true", "HUB_FEATURES=workspace,browser", "HUB_PERSISTENT_HERMES=true", "HUB_STATE=/state", "--tmpfs /tmp:", "--add-host host.docker.internal:host-gateway"} {
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
	for _, target := range []string{"/state/hermes", "/state/google", "/state/browser", "/workspace"} {
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

func TestLegacySelectionPreventsSupervisorFromStartingContext(t *testing.T) {
	starts := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			starts++
		}
		return nil, os.ErrNotExist
	}, func(context.Context, string, string) error { return nil })
	selection := stack.ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "legacy", NativeCron: "disabled", CompatibilityRelease: "0.2.0"}
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
			if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := m.normalize(binding(root)); err == nil {
				t.Fatal("nonregular runtime input accepted")
			}
		})
	}
}
