package stack

import (
	"path/filepath"
	"testing"
)

// The sandbox bootstrap goal/retry revision is owner-authored configuration:
// it lives in the published workspace passport and reaches the runtime env
// verbatim.
func TestWorkspaceBootstrapManifestReachesRuntime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := InitEnvironment(dir, "alice", "prod"); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("alice")
	docs["workspace"] = "kind: workspace\nwritable: [/workspace]\nread_only: [/vault/memory]\ngoal: prepare-toolchain\nbootstrap_revision: 2\n"
	publishManagedDocs(t, dir, "alice", "r-0000000000000001", docs)
	s, err := ReadEnvironment(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	vars := RuntimeService(s, "", dir)["environment"].(M)
	if vars["HUB_BOOTSTRAP_GOAL"] != "prepare-toolchain" || vars["HUB_BOOTSTRAP_REVISION"] != "2" {
		t.Fatalf("manifest goal/retry revision not rendered: goal=%v revision=%v", vars["HUB_BOOTSTRAP_GOAL"], vars["HUB_BOOTSTRAP_REVISION"])
	}
}
