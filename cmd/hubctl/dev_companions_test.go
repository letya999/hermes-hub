package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The dev overlay places companions/ on /usr/local/bin — the Python hub
// sidecar is not a Go build output, so hermes-runtime rebuilds must stage
// it explicitly or the rebuilt runtime loses every sidecar-backed feature.
func TestDevCompanionsStagesSidecarForRuntime(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "docker", "hub-sidecar")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "companions")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := stageDevCompanions("hermes-runtime", root, dest); err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(filepath.Join(dest, "hub-sidecar"))
	if err != nil || string(staged) != "#!/usr/bin/env python3\n" {
		t.Fatalf("sidecar not staged: %v", err)
	}
	// Other services must not ship runtime internals.
	dest2 := filepath.Join(t.TempDir(), "companions")
	if err := stageDevCompanions("communication-hub", root, dest2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest2, "hub-sidecar")); !os.IsNotExist(err) {
		t.Fatal("sidecar leaked into a non-runtime overlay")
	}
}
