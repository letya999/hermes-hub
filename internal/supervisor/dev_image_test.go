package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDevRuntimeImageUsesOnlyExactOperatorSelection(t *testing.T) {
	root := t.TempDir()
	fallback := "hermes-hub:0.3.0-dev"
	if got, err := devRuntimeImage(root, "alice", "dev", fallback); err != nil || got != fallback {
		t.Fatalf("missing marker: %q %v", got, err)
	}
	path := filepath.Join(root, "generated", "dev-runtime-image")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hermes-hub-dev-alice-hermes-runtime:local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := devRuntimeImage(root, "alice", "dev", fallback); err != nil || got != "hermes-hub-dev-alice-hermes-runtime:local" {
		t.Fatalf("valid marker: %q %v", got, err)
	}
	if got, err := devRuntimeImage(root, "alice", "prod", "hermes-hub:0.3.0-prod"); err != nil || got != "hermes-hub:0.3.0-prod" {
		t.Fatalf("prod used dev image: %q %v", got, err)
	}
	if err := os.WriteFile(path, []byte("foreign/image:latest\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := devRuntimeImage(root, "alice", "dev", fallback); err == nil {
		t.Fatal("accepted arbitrary runtime image")
	}
}

func TestManagedRunArgsSelectsDevRuntimeImage(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	settings := writeManagedFixtures(t, root)
	m.cfg.Environment, settings.Environment = "dev", "dev"
	for _, name := range []string{"managed-runtime", "hermes"} {
		body, err := os.ReadFile(filepath.Join(root, name+".prod.env"))
		if name == "hermes" {
			body, err = os.ReadFile(filepath.Join(root, "hermes.prod.yaml"))
		}
		if err != nil {
			t.Fatal(err)
		}
		ext := ".env"
		if name == "hermes" {
			ext = ".yaml"
		}
		if err := os.WriteFile(filepath.Join(root, name+".dev"+ext), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generated", "dev-runtime-image"), []byte("hermes-hub-dev-alice-hermes-runtime:local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := m.managedRunArgs(binding(root), settings, "fixture", "gen")
	if err != nil || args[len(args)-2] != "hermes-hub-dev-alice-hermes-runtime:local" {
		t.Fatalf("managed dev image: %v %v", args, err)
	}
}

func TestControlRelaySelectsDevRuntimeImage(t *testing.T) {
	var run []string
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			run = append([]string(nil), args...)
		}
		return nil, nil
	}, nil)
	m.cfg.Environment = "dev"
	if err := os.MkdirAll(filepath.Join(root, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	image := "hermes-hub-dev-alice-hermes-runtime:local"
	if err := os.WriteFile(filepath.Join(root, "generated", "dev-runtime-image"), []byte(image+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.spawnControlRelay(context.Background(), binding(root), "runtime", 8091, "generation", "agent-net"); err != nil {
		t.Fatal(err)
	}
	if len(run) < 2 || run[len(run)-2] != image {
		t.Fatalf("relay image: %v", run)
	}
}
