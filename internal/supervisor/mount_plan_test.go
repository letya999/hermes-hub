package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
)

// publishWorkspace writes a published revision whose workspace passport
// declares the boundary the spawn paths must mount (SPEC-0092 R5.2).
func publishWorkspace(t *testing.T, ctxRoot, env string) {
	t.Helper()
	rev := "r-0123456789abcdef"
	revDir := filepath.Join(ctxRoot, "generated", "passports", rev)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	header := "apiVersion: six-passport/v2\nmetadata: {space: alice, environment: " + env + ", revision: " + rev + "}\n"
	docs := map[string]string{
		"workspace": "kind: workspace\n" +
			"writable: [/workspace/work, /workspace/outbox, /vault/library, /vault/attachments]\n" +
			"read_only: [/config, /skills, /workspace/inbox, /vault/memory, /shared/docs]\n" +
			"mounts:\n    /shared/docs: {source: docs-src, mode: ro}\n",
	}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		body := header + "kind: " + name + "\n"
		if d, ok := docs[name]; ok {
			body = "apiVersion: six-passport/v2\nmetadata: {space: alice, environment: " + env + ", revision: " + rev + "}\n" + d
		}
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "generated", "passports", "current"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ctxRoot, "generated", "config-view", env), 0700); err != nil {
		t.Fatal(err)
	}
	// A published revision retires settings.yaml: the slim agent.yaml keeps
	// the operator extensions the passport does not govern.
	if err := os.WriteFile(filepath.Join(ctxRoot, "agent.yaml"), []byte(
		"schema: 3\nuser: alice\ntimezone: UTC\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\nmodel: synthetic\nmodel_url: http://model-relay:8318/v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// The supervised spawn consumes the same MountPlan as compose: the
// workspace passport, not a hardcoded list, decides what the runtime
// container sees.
func TestSupervisorMountsFollowWorkspacePassport(t *testing.T) {
	m, root := testManager(t, func(ctx context.Context, args ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	publishWorkspace(t, root, "prod")
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(bound, "hub-test-container", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	managedDir := filepath.Join(root, "managed", "prod")
	for _, want := range []string{
		"type=bind,src=" + filepath.Join(root, "generated", "config-view", "prod") + ",dst=/config,readonly",
		"type=bind,src=" + filepath.Join(managedDir, "workspace") + ",dst=/workspace",
		"type=bind,src=" + filepath.Join(managedDir, "workspace", "inbox") + ",dst=/workspace/inbox,readonly",
		"type=bind,src=" + filepath.Join(managedDir, "vault", "memory") + ",dst=/vault/memory,readonly",
		"type=bind,src=" + filepath.Join(managedDir, "vault", "library") + ",dst=/vault/library",
		"type=bind,src=" + filepath.Join(managedDir, "vault", "attachments") + ",dst=/vault/attachments",
		"type=bind,src=" + filepath.Join(root, "docs-src") + ",dst=/shared/docs,readonly",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("supervised run args missing %s", want)
		}
	}
}

// With a published workspace passport, /config is a single read-only
// directory bind: per-file mounts into it fail at container start
// ("create mountpoint ... read-only file system"). The tool policy and
// manifest must live inside the config-view instead (R5.2/S1.4).
func TestSupervisorPassportConfigSkipsNestedConfigMounts(t *testing.T) {
	m, root := testManager(t, func(ctx context.Context, args ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	publishWorkspace(t, root, "prod")
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(bound, "hub-test-container", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range args {
		if strings.Contains(arg, "dst=/config/tool-policy.json") || strings.Contains(arg, "dst=/config/tool-manifest.json") {
			t.Errorf("nested /config mount breaks the read-only passport bind: %s", arg)
		}
	}
	// The spawn-time policy snapshot still rides /config: materialized
	// inside the view (the manifest is a render-time artifact — publishing
	// preserves it).
	if _, err := os.Stat(filepath.Join(root, "generated", "config-view", "prod", "tool-policy.json")); err != nil {
		t.Errorf("config-view missing tool-policy.json: %v", err)
	}
}

// An unmanaged space — no published revision — keeps the legacy bind set:
// the workspace passport does not apply until the space migrates (R8.1).
func TestSupervisorUnmanagedMountsKeepLegacyLayout(t *testing.T) {
	m, root := testManager(t, func(ctx context.Context, args ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte(
		"schema: 1\nuser: alice\ntimezone: UTC\nmodel: synthetic\nmodel_url: http://model-relay:8318/v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(bound, "hub-test-container", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"type=bind,src=" + filepath.Join(root, "workspace") + ",dst=/scope,readonly",
		"type=bind,src=" + filepath.Join(root, "runtime") + ",dst=/state",
		"type=bind,src=" + filepath.Join(root, "hermes") + ",dst=/state/hermes",
		"type=bind,src=" + filepath.Join(root, "archive") + ",dst=/archive,readonly",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("unmanaged run args missing %s", want)
		}
	}
	joined := strings.Join(args, " ")
	for _, denied := range []string{"dst=/config,readonly", "managed\\prod", "managed/prod", "dst=/shared/"} {
		if strings.Contains(joined, denied) {
			t.Errorf("unmanaged spawn mounted passport-only path %q: %s", denied, joined)
		}
	}
}

// An unmanaged space with memory_vault owns the same /vault contract the
// compose service renders (SPEC-0085 R1.6): HUB_VAULT=/vault reaches the
// env through the service contract, so the spawn must bind the real vault
// or the agent's memory tools point at an empty rootfs path.
func TestSupervisorUnmanagedMemoryVaultBindsRealVault(t *testing.T) {
	m, root := testManager(t, func(ctx context.Context, args ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte(
		"schema: 1\nuser: alice\ntimezone: UTC\nmodel: synthetic\nmodel_url: http://model-relay:8318/v1\ntools:\n    memory_vault: {via: native}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(bound, "hub-test-container", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "type=bind,src=" + filepath.Join(root, "vault") + ",dst=/vault"
	if !slices.Contains(args, want) {
		t.Fatalf("memory_vault spawn missing %s", want)
	}
}

// The managed spawn's legacy branch (no published workspace passport yet)
// owes the same bind: managedRunArgs mirrors RuntimeService, whose env
// already carries HUB_VAULT under the same wish.
func TestSupervisorManagedLegacyMemoryVaultBindsRealVault(t *testing.T) {
	m, root := testManager(t, func(ctx context.Context, args ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	settings := writeManagedFixtures(t, root)
	settings.Tools = map[string]stack.ToolEntry{"memory_vault": {Via: stack.ToolViaNative}}
	body, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), body, 0600); err != nil {
		t.Fatal(err)
	}
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(bound, "hub-test-container", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "type=bind,src=" + filepath.Join(root, "vault") + ",dst=/vault"
	if !slices.Contains(args, want) {
		t.Fatalf("managed legacy spawn missing %s", want)
	}
}
