package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMaterializedTerminalPolicyAndOperatorVocabulary(t *testing.T) {
	root := t.TempDir()
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Tools: map[string]ToolEntry{"terminal": {Via: ToolViaToolHub}}}
	if _, err := MaterializeToolPolicySnapshot(root, s); err == nil {
		t.Fatal("policy path without generated directory accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := MaterializeToolPolicySnapshot(root, s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err := json.Unmarshal(b, &policy); err != nil {
		t.Fatal(err)
	}
	if policy["principal"] != "alice" || policy["schema"] != float64(1) || p != filepath.Join(root, "generated", "tool-policy.dev.json") {
		t.Fatalf("wrong caller policy: %s", b)
	}
	names := NativeCarveoutToolsets()
	if !slices.IsSorted(names) || !slices.Contains(names, "terminal") || !slices.Contains(names, "code_execution") {
		t.Fatal("operator cannot distinguish native execution choices")
	}
}

func TestSelfSettingsCannotRestoreNativeTerminal(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	overlay := filepath.Join(dir, "self-settings.json")
	baseline := []byte("platform_toolsets:\n  telegram: [memory]\ncompression:\n  enabled: false\n")
	if err := os.WriteFile(config, baseline, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte(`{"compression.enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applySelfSettings(config, MaterializeOptions{SelfSettingsPath: overlay}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(config)
	if strings.Contains(string(b), "terminal") || !strings.Contains(string(b), "enabled: true") {
		t.Fatalf("safe overlay changed authority: %s", b)
	}
	for _, bad := range []string{`{"platform_toolsets.telegram":"terminal"}`, `{"cli_registries":"pypi"}`, "{"} {
		_ = os.WriteFile(overlay, []byte(bad), 0600)
		if err := applySelfSettings(config, MaterializeOptions{SelfSettingsPath: overlay}); err == nil {
			t.Fatal("authority overlay accepted")
		}
		after, _ := os.ReadFile(config)
		if string(after) != string(b) {
			t.Fatal("rejected overlay changed config")
		}
	}
	_ = os.WriteFile(overlay, []byte(`{"compression.enabled":true}`), 0600)
	if err := applySelfSettings(filepath.Join(dir, "missing"), MaterializeOptions{SelfSettingsPath: overlay}); err == nil {
		t.Fatal("missing config accepted")
	}
	_ = os.WriteFile(config, []byte("["), 0600)
	if err := applySelfSettings(config, MaterializeOptions{SelfSettingsPath: overlay}); err == nil {
		t.Fatal("corrupt config overwritten")
	}
}

func TestToolHubTerminalRemovesNativeEvenWhenGovernanceAllows(t *testing.T) {
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Tools: map[string]ToolEntry{"terminal": {Via: ToolViaToolHub}}}
	// nil governance allows emission, so this catches the backend-routing bug.
	cfg := Config(s)
	for _, channel := range []string{"cli", "telegram", "api_server", "cron"} {
		sets := cfg["platform_toolsets"].(M)[channel].([]string)
		if slices.Contains(sets, "terminal") || slices.Contains(sets, "code_execution") {
			t.Fatalf("%s exposes native execution: %v", channel, sets)
		}
		if !slices.Contains(sets, "file") {
			t.Fatalf("unrelated file surface removed: %v", sets)
		}
	}
	s.Tools["code_execution"] = ToolEntry{Via: ToolViaNative}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("native code execution bypass accepted: %v", err)
	}
	s.Tools["terminal"] = ToolEntry{Via: ToolViaNative}
	if !slices.Contains(Config(s)["platform_toolsets"].(M)["cli"].([]string), "terminal") {
		t.Fatal("explicit unrestricted native mode lost")
	}
}

// TestUnnamedPlatformsFallClosed pins the config-leak that gave the model a
// real in-runtime shell: upstream falls back to a platform's full-access
// composite whenever platform_toolsets does not name it, and the gateway
// serves api_server to every caller. Every platform in the pinned inventory
// must be emitted explicitly, and the global disabled_toolsets net must strip
// exec surfaces the space never granted.
func TestUnnamedPlatformsFallClosed(t *testing.T) {
	inventory, err := ManagedCapabilityInventory()
	if err != nil {
		t.Fatal(err)
	}
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		ExecutionMode: "supervisor", Tools: map[string]ToolEntry{"terminal": {Via: ToolViaToolHub}}}
	platforms := Config(s)["platform_toolsets"].(M)
	for _, platform := range inventory.Platforms {
		sets, ok := platforms[platform].([]string)
		if !ok {
			t.Fatalf("platform %s unnamed: upstream composite fallback is live", platform)
		}
		served := slices.Contains([]string{"cli", "local", "telegram", "api_server", "cron"}, platform)
		if served && len(sets) == 0 {
			t.Fatalf("served platform %s lost the reviewed surface", platform)
		}
		if !served && len(sets) != 0 {
			t.Fatalf("unserved platform %s got tools: %v", platform, sets)
		}
	}
	denied := Config(s)["agent"].(M)["disabled_toolsets"].([]string)
	for _, name := range []string{"terminal", "code_execution", "delegation", "cronjob", "homeassistant", "computer_use"} {
		if !slices.Contains(denied, name) {
			t.Fatalf("exec/actuation toolset %s not denied for unnamed platforms", name)
		}
	}
	for _, name := range []string{"debugging", "safe", "yuanbao", "search"} {
		if slices.Contains(denied, name) {
			t.Fatalf("composite %s in denylist would strip enabled core tools", name)
		}
	}
	for _, name := range []string{"file", "memory", "skills", "todo", "session_search"} {
		if slices.Contains(denied, name) {
			t.Fatalf("enabled toolset %s landed on the denylist", name)
		}
	}
	// An explicitly granted native toolset lifts only that toolset off the net.
	s.Tools["terminal"] = ToolEntry{Via: ToolViaNative}
	denied = Config(s)["agent"].(M)["disabled_toolsets"].([]string)
	if slices.Contains(denied, "terminal") || !slices.Contains(denied, "code_execution") {
		t.Fatalf("native terminal grant did not lift exactly its own entry: %v", denied)
	}
}

func TestCLIRegistryEgressIsOptInAndDefinitionScoped(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	base := cliControllerSection(s, "")
	if slices.Contains(base.AllowedEgress, "pypi.org") {
		t.Fatal("registry egress enabled without operator selection")
	}
	s.CLIRegistries = []string{"pypi", "npm", "cargo", "go"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg := cliControllerSection(s, "")
	for _, host := range []string{"pypi.org", "files.pythonhosted.org", "registry.npmjs.org", "index.crates.io", "static.crates.io", "proxy.golang.org", "sum.golang.org"} {
		if !slices.Contains(cfg.AllowedEgress, host) || !slices.Contains(cfg.Ceiling.Egress, host) {
			t.Fatalf("missing registry host %s", host)
		}
	}
	for _, approval := range cfg.Approved {
		if slices.Contains(approval.Execution.Egress, "pypi.org") {
			t.Fatal("controller ceiling widened shipped definition egress")
		}
	}
	for _, bad := range [][]string{{"*"}, {"pypi", "pypi"}, {"evil.example"}} {
		s.CLIRegistries = bad
		if err := s.Validate(); err == nil {
			t.Fatalf("invalid registry selection accepted: %v", bad)
		}
	}
}

func TestCLIRuntimeUIDAndToolboxKnobs(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	ref := "hermes-cli-artifact/shfmt@sha256:" + strings.Repeat("a", 64)
	s.CLIRuntime = "runsc"
	s.CLIPerPrincipalUID = true
	s.CLIToolboxes = map[string][]string{"user-cli-bundle": {ref}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg := cliControllerSection(s, "")
	if cfg.Runtime != "runsc" || !cfg.PerPrincipalUID || cfg.Toolboxes["user-cli-bundle"][0] != ref {
		t.Fatalf("cli knobs not wired: %+v", cfg)
	}
	for _, bad := range []string{"gvisor", "docker", "runsc ", ""} {
		if bad == "" {
			continue
		}
		s.CLIRuntime = bad
		if err := s.Validate(); err == nil {
			t.Fatalf("runtime %q accepted", bad)
		}
	}
	s.CLIRuntime = "runsc"
	for _, bad := range [][]string{
		{"img:latest"}, {"img@sha256:xyz"}, {"img"}, {"img@sha256:" + strings.Repeat("a", 63)},
		{"../escape@sha256:" + strings.Repeat("a", 64)}, {""}, {},
	} {
		s.CLIToolboxes = map[string][]string{"b": bad}
		if err := s.Validate(); err == nil {
			t.Fatalf("toolbox member shape accepted: %v", bad)
		}
	}
}
