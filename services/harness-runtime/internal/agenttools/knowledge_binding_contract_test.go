package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgeBindingContract(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUB_STATE", dir)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "project")
	for _, tc := range []struct {
		name, owner, scope, source, state string
		files                             map[string][]byte
	}{
		{"owner", "../escape", "project", "docs", "present", nil},
		{"scope", "alice", "../escape", "docs", "present", nil},
		{"source", "alice", "project", "../docs", "present", nil},
		{"state", "alice", "project", "docs", "imaginary", nil},
		{"document", "alice", "project", "docs", "present", map[string][]byte{"../bad": []byte("x")}},
		{"large", "alice", "project", "docs", "present", map[string][]byte{"big": make([]byte, knowledgeLimit+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := StageKnowledgeSource(dir, tc.owner, tc.scope, tc.source, tc.state, tc.files); err == nil {
				t.Error("invalid snapshot accepted")
			}
		})
	}
	if _, err := StageKnowledgeSource(dir, "alice", "project", "manual", "present", map[string][]byte{"sub/guide.md": []byte("needle")}); err != nil {
		t.Fatal(err)
	}
	v := fixture(t)
	out, err := v.knowledgeList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range out["items"].([]any) {
		if x, ok := item.(knowledgeItem); ok && x.Name == "manual/sub/guide.md" {
			found = true
		}
	}
	if !found {
		t.Error("bound documents absent from list")
	}
	out, err = v.knowledgeSearch(context.Background(), "needle")
	if err != nil {
		t.Fatal(err)
	}
	fragments, _ := out["fragments"].([]knowledgeFragment)
	if len(fragments) != 1 || fragments[0].Source != "manual/sub/guide.md" || fragments[0].SHA256 == "" {
		t.Errorf("bound search/provenance missing: %v", out)
	}
	// A context alias must not make another context writable by the operator API.
	target := filepath.Join(dir, "knowledge", "alice", "private")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(target))
	if err := os.Symlink("private", "alias"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := StageKnowledgeSource(dir, "alice", "alias", "docs", "present", map[string][]byte{"x": []byte("foreign")}); err == nil {
		t.Error("staging followed foreign-context symlink")
	}
	if _, err := os.Stat(filepath.Join(target, ".bindings.json")); !os.IsNotExist(err) {
		t.Error("foreign context was modified")
	}
}
