//go:build !windows

package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutorConfigViewReadableByRuntimeGroup(t *testing.T) {
	dir := t.TempDir()
	view := filepath.Join(dir, "generated", "config-view", "dev")
	if err := os.MkdirAll(view, 0755); err != nil {
		t.Fatal(err)
	}
	s := Settings{User: "alice", Environment: "dev"}
	if err := writeToolManifest(dir, s); err != nil {
		t.Fatal(err)
	}
	if err := writeToolPolicySnapshot(dir, s); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tool-manifest.json", "tool-policy.json"} {
		info, err := os.Stat(filepath.Join(view, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0640 {
			t.Fatalf("runtime view %s mode=%o, want 640", name, info.Mode().Perm())
		}
	}
}
