package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceBootstrapManifestReachesRuntime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := InitEnvironment(dir, "alice", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("schema: 3\nworkspace:\n  goal: prepare-toolchain\n  bootstrap_revision: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	vars := RuntimeService(s, "", dir)["environment"].(M)
	if vars["HUB_BOOTSTRAP_GOAL"] != "prepare-toolchain" || vars["HUB_BOOTSTRAP_REVISION"] != "2" {
		t.Fatalf("manifest goal/retry revision not rendered: goal=%v revision=%v", vars["HUB_BOOTSTRAP_GOAL"], vars["HUB_BOOTSTRAP_REVISION"])
	}
}
