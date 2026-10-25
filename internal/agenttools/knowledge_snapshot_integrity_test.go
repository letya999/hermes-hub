package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgeSnapshotIntegrityAndFreshness(t *testing.T) {
	dir := t.TempDir()
	large := make([]byte, 8<<20)
	for _, source := range []string{"one", "two"} {
		if _, err := StageKnowledgeSource(dir, "alice", "alice", source, "present", map[string][]byte{"a": large, "b": large}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := StageKnowledgeSource(dir, "alice", "alice", "three", "present", map[string][]byte{"a": []byte("x")}); err == nil {
		t.Error("context aggregate exceeded 32 MiB")
	}
	other := t.TempDir()
	t.Setenv("HUB_STATE", other)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	root, err := StageKnowledgeSource(other, "alice", "alice", "docs", "present", map[string][]byte{"guide.md": []byte("needle")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StageKnowledgeSource(other, "alice", "alice", "docs", "stale", nil); err != nil {
		t.Fatal(err)
	}
	v := fixture(t)
	for _, tool := range []string{"list", "search"} {
		var out map[string]any
		var err error
		if tool == "list" {
			out, err = v.knowledgeList(context.Background())
		} else {
			out, err = v.knowledgeSearch(context.Background(), "needle")
		}
		stale, _ := out["stale_sources"].([]string)
		if err != nil || len(stale) != 1 || stale[0] != "docs" {
			t.Errorf("%s hid stale state: %v %v", tool, out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".bindings.json"), []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if v := recover(); v != nil {
			t.Errorf("corrupt metadata panicked: %v", v)
		}
	}()
	if _, err := StageKnowledgeSource(other, "alice", "alice", "docs", "present", map[string][]byte{"guide.md": []byte("next")}); err == nil {
		t.Error("corrupt metadata silently replaced")
	}
}
