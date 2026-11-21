package supervisor

import (
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
