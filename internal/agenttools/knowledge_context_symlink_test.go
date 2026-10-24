package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgeContextCannotAliasAnotherContext(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "project")
	owner := filepath.Join(stateDir, "knowledge", "alice")
	if err := os.MkdirAll(filepath.Join(owner, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "private", "secret.md"), []byte("private-context-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(owner)
	if err := os.Symlink("private", filepath.Join(owner, "project")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	v := fixture(t)
	for _, name := range []string{"knowledge_read", "knowledge_search", "knowledge_list"} {
		if _, err := v.Call(context.Background(), name, Input{Name: "secret.md", Query: "private-context-marker"}); err == nil {
			t.Errorf("%s accepted a sibling context through symlink", name)
		}
	}
}
