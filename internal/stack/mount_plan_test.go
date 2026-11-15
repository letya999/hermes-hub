package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The published workspace passport owns the runtime container's filesystem
// shape (SPEC-0092 R5.2): one /config view, the /workspace base with
// read-only sub-binds, the /vault skeleton split by mode, and every
// declared /shared/* mount — not a hardcoded list.
func TestComposeRuntimeMountsFollowWorkspacePassport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	rev := "r-0123456789abcdef"
	writeSixPassportsInEnvironment(t, dir, "alice", "prod", rev, map[string]string{
		"workspace.yaml": "apiVersion: six-passport/v2\nkind: workspace\nmetadata: {space: alice, environment: prod, revision: " + rev + "}\n" +
			"writable: [/workspace/work, /workspace/outbox, /vault/library, /vault/attachments]\n" +
			"read_only: [/config, /skills, /workspace/inbox, /vault/memory, /shared/docs]\n" +
			"mounts:\n" +
			"    /shared/docs: {source: docs-src, mode: ro}\n",
	})
	if err := os.MkdirAll(filepath.Join(dir, "generated", "config-view", "prod"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	runtime := RuntimeService(s, root, dir)
	vols, _ := runtime["volumes"].([]any)
	binds := map[string]M{}
	for _, v := range vols {
		m, ok := v.(M)
		if !ok {
			continue
		}
		target, _ := m["target"].(string)
		binds[target] = m
	}
	has := func(target, wantSource string, wantRO bool) {
		t.Helper()
		m, ok := binds[target]
		if !ok {
			t.Errorf("runtime mounts missing %s", target)
			return
		}
		src, _ := m["source"].(string)
		ro, _ := m["read_only"].(bool)
		if wantSource != "" && src != filepath.ToSlash(wantSource) {
			t.Errorf("%s source = %q, want %q", target, src, wantSource)
		}
		if ro != wantRO {
			t.Errorf("%s read_only = %v, want %v", target, ro, wantRO)
		}
	}
	has("/config", filepath.Join(dir, "generated", "config-view", "prod"), true)
	managed := filepath.Join(dir, "managed", "prod")
	has("/workspace", filepath.Join(managed, "workspace"), false)
	has("/workspace/inbox", filepath.Join(managed, "workspace", "inbox"), true)
	has("/shared/docs", filepath.Join(dir, "docs-src"), true)
	for target := range binds {
		if target == "/vault" || strings.HasPrefix(target, "/vault/") {
			t.Fatalf("runtime received a direct vault mount: %s", target)
		}
		if strings.HasPrefix(target, "/shared/") && target != "/shared/docs" {
			t.Fatalf("undeclared shared mount leaked: %s", target)
		}
		if strings.HasPrefix(target, "/config/") {
			t.Fatalf("nested mount under the read-only /config bind: %s", target)
		}
	}
}

// The managed runtime mounts the same passport /config view: the executor
// contracts must not be bound under it (R5.2).
func TestComposeManagedRuntimeSkipsNestedConfigMounts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	rev := "r-0123456789abcdef"
	writeSixPassportsInEnvironment(t, dir, "alice", "prod", rev, map[string]string{
		"workspace.yaml": "apiVersion: six-passport/v2\nkind: workspace\nmetadata: {space: alice, environment: prod, revision: " + rev + "}\n" +
			"writable: [/workspace]\nread_only: [/config, /skills]\n",
		"stack.yaml": "apiVersion: six-passport/v2\nkind: stack\nmetadata: {space: alice, environment: prod, revision: " + rev + "}\n" +
			"identity: {user: alice}\nservices:\n    hermes-runtime: {mode: supervised}\n",
	})
	if err := os.MkdirAll(filepath.Join(dir, "generated", "config-view", "prod"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(
		"schema: 3\nuser: alice\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\nmodel: synthetic\nmodel_url: http://model-relay:8318/v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	runtime := RuntimeService(s, root, dir)
	vols, _ := runtime["volumes"].([]any)
	for _, v := range vols {
		m, ok := v.(M)
		if !ok {
			continue
		}
		if target, _ := m["target"].(string); strings.HasPrefix(target, "/config/") {
			t.Fatalf("nested mount under the read-only /config bind: %s", target)
		}
	}
}

// The runtime cannot directly mount ToolHub-owned vault data.
func TestMountPlanMemoryVaultStaysOutsideRuntime(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	rev := "r-0123456789abcdef"
	writeSixPassportsInEnvironment(t, dir, "alice", "prod", rev, map[string]string{
		"workspace.yaml": "apiVersion: six-passport/v2\nkind: workspace\nmetadata: {space: alice, environment: prod, revision: " + rev + "}\n" +
			"writable: [/workspace, /vault/library, /vault/attachments]\n" +
			"read_only: [/config, /skills, /vault/memory]\n",
	})
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\nuser: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaNative}}
	if s.PassportWorkspace == nil {
		t.Fatal("fixture lost the workspace passport")
	}
	for _, pm := range MountPlan(dir, "prod", filepath.Join(dir, "managed", "prod"), s, *s.PassportWorkspace) {
		if pm.Target == "/vault" || strings.HasPrefix(pm.Target, "/vault/") {
			t.Errorf("runtime received direct vault mount %s", pm.Target)
		}
	}
}

// Render materializes every workspace directory the MountPlan binds without
// touching an existing projects tree (SPEC-0092 R5.2).
func TestRenderMaterializesLayoutWithoutTouchingProjects(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	rev := "r-0123456789abcdef"
	writeSixPassportsInEnvironment(t, dir, "alice", "prod", rev, map[string]string{
		"stack.yaml": "apiVersion: six-passport/v2\nkind: stack\nmetadata: {space: alice, environment: prod, revision: " + rev + "}\n" +
			"identity: {user: alice}\nservices:\n    hermes-runtime: {mode: supervised}\n",
		"workspace.yaml": "apiVersion: six-passport/v2\nkind: workspace\nmetadata: {space: alice, environment: prod, revision: " + rev + "}\n" +
			"writable: [/workspace/work, /workspace/outbox, /workspace/projects, /workspace/custom, /vault/library, /vault/attachments]\n" +
			"read_only: [/config, /skills, /workspace/inbox, /vault/memory]\n",
	})
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "workspace", "projects", "repo", ".git")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "HEAD"), []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(dir, root, "prod"); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{dir, filepath.Join(dir, "managed", "prod")} {
		for _, name := range []string{"workspace/inbox", "workspace/custom"} {
			info, err := os.Stat(filepath.Join(base, filepath.FromSlash(name)))
			if err != nil || !info.IsDir() {
				t.Errorf("render did not materialize %s under %s: %v", name, base, err)
			}
		}
	}
	if head, err := os.ReadFile(filepath.Join(repo, "HEAD")); err != nil || string(head) != "ref: refs/heads/main\n" {
		t.Errorf("render touched the projects tree: %v", err)
	}
}
