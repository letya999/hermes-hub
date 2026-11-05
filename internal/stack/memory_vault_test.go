package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// SPEC-0085 R1.1: memory_vault is an opt-in user-space feature. It registers
// in the classic vocabulary, honors an explicit off, and can never be granted
// at organization scope (the vault is a per-user store).
func TestMemoryVaultFeatureRegistered(t *testing.T) {
	if !slices.ContainsFunc(Features, func(f Feature) bool { return f.Name == "memory_vault" }) {
		t.Fatal("memory_vault missing from the feature registry")
	}
	s := validSettings()
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaNative}}
	if err := s.Validate(); err != nil {
		t.Fatalf("memory_vault feature rejected: %v", err)
	}
	if !s.Has("memory_vault") {
		t.Fatal("enabled memory_vault did not report via Has()")
	}
	// Deny-wins: an explicit off entry means the feature renders nothing.
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaOff}}
	if s.Has("memory_vault") {
		t.Fatal("tools: {memory_vault: off} still reports the feature")
	}
	// Organization scope is rejected: the vault belongs to one user space.
	org := OrganizationSettings{Schema: 1, Organization: "acme", Features: []string{"memory_vault"}}
	if err := org.Validate(); err == nil || !strings.Contains(err.Error(), "memory_vault") {
		t.Fatalf("organization-scoped memory_vault must be rejected, got %v", err)
	}
}

func TestPublishedMemorySwitchOverridesSelectedTool(t *testing.T) {
	disabled := false
	enabled := true
	s := validSettings()
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaNative}}
	s.PassportConfig = &passportview.Config{Memory: passportview.Memory{Enabled: &disabled}}
	if s.Has("memory_vault") || slices.Contains(s.FeatureList(), "memory_vault") {
		t.Fatal("selected memory tool enabled a passport-disabled vault")
	}
	s.Tools = nil
	s.PassportConfig.Memory.Enabled = &enabled
	if !s.Has("memory_vault") || !slices.Contains(s.FeatureList(), "memory_vault") {
		t.Fatal("passport-enabled vault still depends on tool selection")
	}
}

// SPEC-0085 R1.1/R1.6: memory_vault is a hub-side flag, not an agent toolset —
// enabled it still emits nothing onto the projected tool surface, and denied
// (absent or off) it leaves zero traces in the render.
func TestMemoryVaultDeniedRendersNothing(t *testing.T) {
	s := validSettings()
	cfg := Config(s)
	for _, platform := range []string{"cli", "api_server"} {
		for _, name := range cfg["platform_toolsets"].(M)[platform].([]string) {
			if name == "memory_vault" {
				t.Fatalf("hub flag leaked into platform_toolsets[%s]", platform)
			}
		}
	}
	// Enabled but still hub-side only: the flag must never become a toolset.
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaNative}}
	cfg = Config(s)
	for _, platform := range []string{"cli", "api_server"} {
		for _, name := range cfg["platform_toolsets"].(M)[platform].([]string) {
			if name == "memory_vault" {
				t.Fatalf("hub flag leaked into platform_toolsets[%s]", platform)
			}
		}
	}
}

// Vault state belongs to ToolHub and never mounts into the runtime.
func TestRuntimeNeverMountsVault(t *testing.T) {
	dir := t.TempDir()
	vaultMount := func(s Settings) M {
		for _, v := range RuntimeService(s, t.TempDir(), dir)["volumes"].([]any) {
			m := v.(M)
			if m["target"] == "/vault" {
				return m
			}
		}
		return nil
	}
	s := validSettings()
	if vaultMount(s) != nil {
		t.Fatal("vault mount rendered without the feature")
	}
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaNative}}
	if vaultMount(s) != nil {
		t.Fatal("runtime mounted ToolHub-owned vault")
	}
	if env := RuntimeService(s, t.TempDir(), dir)["environment"].(M); env["HUB_VAULT"] != nil {
		t.Fatalf("runtime received HUB_VAULT: %v", env["HUB_VAULT"])
	}
	s.CapabilityMode = "managed"
	s.PassportWorkspace = &passportview.Workspace{Writable: []string{"/workspace"}}
	for _, v := range RuntimeService(s, t.TempDir(), dir)["volumes"].([]any) {
		m := v.(M)
		if target, _ := m["target"].(string); target == "/vault" || strings.HasPrefix(target, "/vault/") {
			t.Fatalf("runtime mounted ToolHub-owned vault at %s", target)
		}
	}
}

// SPEC-0085 R1.6: the gateway reads CORE for private-run injection, so it
// mounts the vault read-only — writes stay in the runtime's governed writer.
func TestGatewayVaultMountReadWrite(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.Ingress = []string{"telegram"}
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaNative}}
	gateway, ok := Compose(s, "", dir)["services"].(M)["communication-hub"].(M)
	if !ok {
		t.Fatal("communication-hub service missing")
	}
	env, _ := gateway["environment"].(M)
	if env["HUB_VAULT"] != "/vault" {
		t.Fatalf("gateway missing HUB_VAULT: %v", env["HUB_VAULT"])
	}
	found := false
	for _, volume := range gateway["volumes"].([]any) {
		m, ok := volume.(M)
		if ok && m["target"] == "/vault" {
			found = true
			if m["read_only"] == true {
				t.Fatal("gateway vault mount must be read-write — the comm plane writes daily leaves")
			}
			if m["source"] != filepath.ToSlash(filepath.Join(dir, "vault")) {
				t.Fatalf("vault mount must bind spaces/<user>/vault, got %v", m["source"])
			}
		}
	}
	if !found {
		t.Fatal("gateway does not mount the vault")
	}
	// Without the feature the gateway sees nothing.
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaOff}}
	gateway = Compose(s, "", dir)["services"].(M)["communication-hub"].(M)
	if env := gateway["environment"].(M); env["HUB_VAULT"] != nil {
		t.Fatalf("denied feature still renders HUB_VAULT: %v", env["HUB_VAULT"])
	}
	for _, volume := range gateway["volumes"].([]any) {
		if m, ok := volume.(M); ok && m["target"] == "/vault" {
			t.Fatal("denied feature still mounts the vault")
		}
	}
}

// SPEC-0085 R1.6: with memory_vault on, the native memory toolset leaves the
// projected surface entirely — disabled_toolsets keeps "memory" and the
// subsystem flag renders false on both render paths.
func TestNativeMemoryDisabledWithVault(t *testing.T) {
	s := validSettings()
	s.Tools = map[string]ToolEntry{
		"memory":       {Via: ToolViaNative},
		"memory_vault": {Via: ToolViaNative},
	}
	cfg := Config(s)
	if mem := cfg["memory"].(M); mem["memory_enabled"].(bool) || mem["user_profile_enabled"].(bool) {
		t.Fatalf("unmanaged render keeps native memory armed: %v", mem)
	}
	for platform, sets := range cfg["platform_toolsets"].(M) {
		if slices.Contains(sets.([]string), "memory") {
			t.Fatalf("unmanaged platform_toolsets[%s] still serves memory", platform)
		}
	}
	if !slices.Contains(cfg["agent"].(M)["disabled_toolsets"].([]string), "memory") {
		t.Fatal("unmanaged disabled_toolsets lost memory")
	}
	mcfg := managedHermesConfig(s)
	if mcfg["memory"].(M)["memory_enabled"].(bool) {
		t.Fatal("managed render keeps memory_enabled true")
	}
	if !slices.Contains(mcfg["agent"].(M)["disabled_toolsets"].([]string), "memory") {
		t.Fatal("managed disabled_toolsets lost memory")
	}
	// Without the vault the same carve-out arms exactly as before.
	s.Tools = map[string]ToolEntry{"memory": {Via: ToolViaNative}}
	cfg = Config(s)
	if !cfg["memory"].(M)["memory_enabled"].(bool) {
		t.Fatal("native memory carve-out regressed without the vault")
	}
	if slices.Contains(cfg["agent"].(M)["disabled_toolsets"].([]string), "memory") {
		t.Fatal("carved-out memory wrongly denied without the vault")
	}
}

// SPEC-0085 R1.6: native memory files stay on disk untouched — M20.7 imports
// them later; render must neither rewrite nor remove them.
func TestNativeFilesUntouched(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0000000000000001")
	seedGovernance(t, dir, func(doc *api.Governance) {
		activeToolGrant(t, doc, "alice", "native:memory_vault", time.Hour)
	})
	settings, err := ReadEffective(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Tools = testTools("workspace", "memory_vault")
	if err := saveSpace(t, dir, settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	memDir := filepath.Join(dir, "hermes", "memories")
	if err := os.MkdirAll(memDir, 0700); err != nil {
		t.Fatal(err)
	}
	memory := filepath.Join(memDir, "MEMORY.md")
	user := filepath.Join(memDir, "USER.md")
	if err := os.WriteFile(memory, []byte("remember the ferns"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("owner likes tea"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(dir, root, "prod"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{memory: "remember the ferns", user: "owner likes tea"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("native memory file disturbed: %s err=%v", path, err)
		}
	}
}

// SPEC-0085 R1.1: render materializes the git-backed scaffold into the space
// only when memory_vault is enabled.
func TestRenderCreatesVaultTree(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0000000000000001")
	// native:memory_vault is a deny-default section like every pseudo entry:
	// the tools wish alone is not enough — an operator grant admits it.
	seedGovernance(t, dir, func(doc *api.Governance) {
		activeToolGrant(t, doc, "alice", "native:memory_vault", time.Hour)
	})
	settings, err := ReadEffective(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	// config.memory.enabled is the authoritative gate under passports: the
	// tools wish selects the surface but the enabled flag admits the vault.
	settings.Memory = true
	settings.Tools = testTools("workspace", "memory_vault")
	if err := saveSpace(t, dir, settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(dir, root, "prod"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "vault", ".git")); err != nil || !info.IsDir() {
		t.Fatalf("render did not materialize the vault repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vault", "memory", "CORE.md")); err != nil {
		t.Fatalf("render did not materialize the vault tree: %v", err)
	}
}

// SPEC-0085 R5.1: the nightly rollup clock is the space-local
// memory.rollup_time (default 04:00) — the gateway env carries it only
// while the feature is on, an explicit setting wins, denied renders nothing.
func TestMemoryRollupTimeRendered(t *testing.T) {
	dir := t.TempDir()
	envOf := func(s Settings) M {
		gateway, ok := Compose(s, "", dir)["services"].(M)["communication-hub"].(M)
		if !ok {
			t.Fatal("communication-hub service missing")
		}
		env, _ := gateway["environment"].(M)
		return env
	}
	s := validSettings()
	s.Ingress = []string{"telegram"}
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaNative}}
	if got := envOf(s)["HUB_MEMORY_ROLLUP_TIME"]; got != "04:00" {
		t.Fatalf("default rollup time = %v, want 04:00", got)
	}
	s.MemoryRollupTime = "02:30"
	if got := envOf(s)["HUB_MEMORY_ROLLUP_TIME"]; got != "02:30" {
		t.Fatalf("configured rollup time = %v, want 02:30", got)
	}
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaOff}}
	if got := envOf(s)["HUB_MEMORY_ROLLUP_TIME"]; got != nil {
		t.Fatalf("denied feature still renders rollup time: %v", got)
	}
}
