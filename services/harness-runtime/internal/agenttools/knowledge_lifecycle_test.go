package agenttools

import (
	"context"
	"testing"
)

func TestKnowledgeBindingRefreshRevokeAndIsolation(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "project")
	v := fixture(t)
	stage := func(scope, state string, files map[string][]byte) {
		t.Helper()
		if _, err := StageKnowledgeSource(stateDir, "alice", scope, "docs", state, files); err != nil {
			t.Fatal(err)
		}
	}
	read := func(want string) {
		t.Helper()
		out, err := v.knowledgeRead(context.Background(), Input{Name: "docs/guide.md"})
		if err != nil || out["content"] != want || out["sha256"] == "" || out["freshness"] != "snapshot" {
			t.Errorf("scoped snapshot read: want=%s got=%v error=%v", want, out, err)
		}
	}
	stage("project", "present", map[string][]byte{"guide.md": []byte("version-one"), "sub/extra.md": []byte("extra")})
	read("version-one")
	t.Setenv("HUB_CONTEXT_ID", "other")
	if _, err := v.knowledgeRead(context.Background(), Input{Name: "docs/guide.md"}); err == nil {
		t.Error("unbound context read project source")
	}
	stage("other", "present", map[string][]byte{"guide.md": []byte("other-context")})
	read("other-context")
	t.Setenv("HUB_CONTEXT_ID", "project")
	stage("project", "present", map[string][]byte{"guide.md": []byte("version-two")})
	read("version-two")
	if _, err := v.knowledgeRead(context.Background(), Input{Name: "docs/sub/extra.md"}); err == nil {
		t.Error("removed snapshot document remained readable")
	}
	stage("project", "stale", nil)
	out, err := v.knowledgeRead(context.Background(), Input{Name: "docs/guide.md"})
	if err != nil || out["stale"] != true {
		t.Errorf("stale snapshot not identified: %v %v", out, err)
	}
	stage("project", "unavailable", nil)
	if _, err := v.knowledgeRead(context.Background(), Input{Name: "docs/guide.md"}); err == nil {
		t.Error("unavailable source served as current")
	}
	out, err = v.knowledgeSearch(context.Background(), "version-two")
	unavailable, _ := out["unavailable_sources"].([]string)
	if err != nil || len(unavailable) != 1 || unavailable[0] != "docs" {
		t.Errorf("source failure presented as empty corpus: %v %v", out, err)
	}
	stage("project", "present", map[string][]byte{"guide.md": []byte("version-three")})
	read("version-three")
	stage("project", "absent", nil)
	if _, err := v.knowledgeRead(context.Background(), Input{Name: "docs/guide.md"}); err == nil {
		t.Error("revoked snapshot remained readable")
	}
	t.Setenv("HUB_CONTEXT_ID", "other")
	read("other-context")
}
