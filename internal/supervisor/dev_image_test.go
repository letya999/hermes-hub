package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDevRuntimeImageUsesOnlyExactOperatorSelection(t *testing.T) {
	root := t.TempDir()
	fallback := "hermes-hub:0.5.0-dev"
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
	if got, err := devRuntimeImage(root, "alice", "prod", "hermes-hub:0.5.0-prod"); err != nil || got != "hermes-hub:0.5.0-prod" {
		t.Fatalf("prod used dev image: %q %v", got, err)
	}
	if err := os.WriteFile(path, []byte("foreign/image:latest\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := devRuntimeImage(root, "alice", "dev", fallback); err == nil {
		t.Fatal("accepted arbitrary runtime image")
	}
}

func TestDevScriptImageUsesOnlyExactOperatorSelection(t *testing.T) {
	root := t.TempDir()
	fallback := "hermes-hub:0.5.0-dev"
	if got, err := devScriptImage(root, "alice", "dev", fallback); err != nil || got != fallback {
		t.Fatalf("missing marker: %q %v", got, err)
	}
	path := filepath.Join(root, "generated", "dev-script-image")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hermes-hub-dev-alice-communication-hub:local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := devScriptImage(root, "alice", "dev", fallback); err != nil || got != "hermes-hub-dev-alice-communication-hub:local" {
		t.Fatalf("valid marker: %q %v", got, err)
	}
	if got, err := devScriptImage(root, "alice", "prod", "hermes-hub:0.5.0-prod"); err != nil || got != "hermes-hub:0.5.0-prod" {
		t.Fatalf("prod used dev image: %q %v", got, err)
	}
	// A runtime pin never substitutes for the script image.
	if err := os.WriteFile(path, []byte("hermes-hub-dev-alice-hermes-runtime:local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := devScriptImage(root, "alice", "dev", fallback); err == nil {
		t.Fatal("runtime image accepted for script containers")
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
	if err := m.spawnControlRelay(context.Background(), binding(root), "runtime", 8091, "generation", "agent-net", "http://runtime:8080", "", true); err != nil {
		t.Fatal(err)
	}
	if len(run) < 2 || run[len(run)-2] != image {
		t.Fatalf("relay image: %v", run)
	}
}
