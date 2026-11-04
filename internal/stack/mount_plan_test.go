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
	writeSixPassports(t, dir, "alice", rev, map[string]string{
		"workspace.yaml": "apiVersion: six-passport/v2\nkind: workspace\nmetadata: {space: alice, environment: dev, revision: " + rev + "}\n" +
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
	s, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	runtime := Compose(s, root, dir)["services"].(M)["hermes-runtime"].(M)
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
	has("/workspace", filepath.Join(dir, "workspace"), false)
	has("/workspace/inbox", filepath.Join(dir, "workspace", "inbox"), true)
	has("/vault/memory", filepath.Join(dir, "vault", "memory"), true)
	has("/vault/library", filepath.Join(dir, "vault", "library"), false)
	has("/vault/attachments", filepath.Join(dir, "vault", "attachments"), false)
	has("/shared/docs", filepath.Join(dir, "docs-src"), true)
	for target := range binds {
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
	writeSixPassports(t, dir, "alice", rev, map[string]string{
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
	s, err := Read(dir)
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

// Under memory_vault + a published workspace passport the whole space vault
// bind-mounts read-write at /vault (SPEC-0085 R1.1) — the managed MountPlan
// is the only mount source on the supervised spawn path, so dropping the
// skeleton without the real bind leaves HUB_VAULT dangling. Exactly one
// /vault bind, no shadowing sub-mounts (SPEC-0092 R5.2).
func TestMountPlanMemoryVaultBindsRealVault(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	rev := "r-0123456789abcdef"
	writeSixPassports(t, dir, "alice", rev, map[string]string{
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
	s, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaNative}}
	if s.PassportWorkspace == nil {
		t.Fatal("fixture lost the workspace passport")
	}
	var vaultMounts int
	for _, pm := range MountPlan(dir, "prod", filepath.Join(dir, "managed", "prod"), s, *s.PassportWorkspace) {
		if pm.Target == "/vault" {
			vaultMounts++
			if pm.Source != filepath.Join(dir, "vault") {
				t.Errorf("/vault source = %q, want the space vault %q", pm.Source, filepath.Join(dir, "vault"))
			}
			if pm.ReadOnly {
				t.Error("/vault must mount read-write — the governed writer commits into it")
			}
		}
		if strings.HasPrefix(pm.Target, "/vault/") {
			t.Errorf("skeleton sub-mount %s shadows the whole-vault bind", pm.Target)
		}
	}
	if vaultMounts != 1 {
		t.Fatalf("/vault bind count = %d, want 1", vaultMounts)
	}
}

// Render materializes every directory the MountPlan binds from the space —
// including the /vault skeleton — without writing memory content or
// touching an existing projects tree (SPEC-0092 R5.2).
func TestRenderMaterializesLayoutWithoutTouchingProjects(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	rev := "r-0123456789abcdef"
	writeSixPassports(t, dir, "alice", rev, map[string]string{
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
	agent, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(agent), "\n") {
		if strings.HasPrefix(line, "model:") || strings.HasPrefix(line, "model_url:") {
			continue
		}
		kept = append(kept, line)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(strings.Join(kept, "\n")+
		"model: synthetic\nmodel_url: http://model-relay:8318/v1\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\n"), 0600); err != nil {
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
		for _, name := range []string{"vault/memory", "vault/library", "vault/attachments", "workspace/inbox", "workspace/custom"} {
			info, err := os.Stat(filepath.Join(base, filepath.FromSlash(name)))
			if err != nil || !info.IsDir() {
				t.Errorf("render did not materialize %s under %s: %v", name, base, err)
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "vault", "memory")); err != nil || len(entries) != 0 {
		t.Errorf("render wrote content into vault/memory: %v entries=%v", err, entries)
	}
	if head, err := os.ReadFile(filepath.Join(repo, "HEAD")); err != nil || string(head) != "ref: refs/heads/main\n" {
		t.Errorf("render touched the projects tree: %v", err)
	}
}
