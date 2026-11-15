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
		"stack": "kind: stack\nidentity: {user: alice, infra: false}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n",
		"config": "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n",
		"models": "kind: models\ndefault: m\nroutes:\n  m: {ladder: [cliproxy]}\nservices:\n  cliproxy: {via: cliproxy, endpoint: http://cliproxy:8317/v1, models: [{id: m}]}\n",
		"workspace": "kind: workspace\n" +
			"writable: [/workspace/work, /workspace/outbox]\n" +
			"read_only: [/config, /skills, /workspace/inbox, /shared/docs]\n" +
			"mounts:\n    /shared/docs: {source: docs-src, mode: ro}\n",
		"tools":  "kind: tools\nbinding: {profile_id: alice-default, generation: 1}\ncore:\n  discovery: {via: toolhub}\n  files: {via: toolhub}\n",
		"grants": "kind: grants\nallow: {}\n",
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
	if err := os.WriteFile(filepath.Join(ctxRoot, "generated", "config-view", env, "revision"), []byte(rev+"\n"), 0600); err != nil {
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
		"type=bind,src=" + filepath.Join(root, "docs-src") + ",dst=/shared/docs,readonly",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("supervised run args missing %s", want)
		}
	}
	for _, arg := range args {
		if strings.Contains(arg, "dst=/vault") {
			t.Errorf("supervised runtime mounts vault: %s", arg)
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

// The managed layout is the only spawn contract left: the published config
// view mounts read-only at /config, managed state dirs carry /state, and the
// legacy /scope, /archive and top-level context binds never appear.
func TestSupervisorUnmanagedMountsKeepLegacyLayout(t *testing.T) {
	m, root := testManager(t, func(ctx context.Context, args ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(bound, "hub-test-container", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"dst=/config,readonly", ",dst=/state ", ",dst=/state/hermes", ",dst=/workspace"} {
		if !strings.Contains(joined, want) {
			t.Errorf("managed run args missing %s: %s", want, joined)
		}
	}
	for _, denied := range []string{"dst=/scope", "dst=/archive", "dst=/shared/", filepath.Join(root, "runtime") + ",dst=/state,"} {
		if strings.Contains(joined, denied) {
			t.Errorf("managed spawn mounted legacy path %q: %s", denied, joined)
		}
	}
}

// A legacy memory flag cannot widen the runtime filesystem boundary.
func TestSupervisorUnmanagedMemoryVaultHasNoVaultMount(t *testing.T) {
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
	for _, arg := range args {
		if strings.Contains(arg, "dst=/vault") {
			t.Fatalf("memory_vault spawn mounted vault: %s", arg)
		}
	}
}

// The managed spawn without a published workspace passport also stays sealed.
func TestSupervisorManagedLegacyMemoryVaultHasNoVaultMount(t *testing.T) {
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
	for _, arg := range args {
		if strings.Contains(arg, "dst=/vault") {
			t.Fatalf("managed legacy spawn mounted vault: %s", arg)
		}
	}
}
