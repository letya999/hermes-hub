package stack

import (
	"bytes"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// nativeToolsets are the upstream surfaces a user mcp_servers entry must not
// shadow with a second MCP wrapper (SPEC-0040).
var nativeToolsets = []string{
	"terminal", "file", "web", "skills", "todo", "cronjob",
	"messaging", "memory", "session_search", "google_meet", "vision", "image_gen",
}

func profileSettings(features ...string) Settings {
	return Settings{Schema: 1, Environment: "prod", User: "me", Model: "m",
		ModelURL: "https://example.invalid/v1", Timezone: "UTC",
		OAuthPort: 8000, BrowserPort: 6080, GoogleEmail: "me@example.org",
		DesktopURL: "https://example.invalid/d", DraftsURL: "https://example.invalid/r",
		Tools: testTools(features...), Ingress: testIngress(features...)}
}

func TestCapabilityProfileIsDeterministic(t *testing.T) {
	all := make([]string, 0, len(Features))
	for _, f := range Features {
		all = append(all, f.Name)
	}
	s := profileSettings(all...)
	first, err := yaml.Marshal(Config(s))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		next, err := yaml.Marshal(Config(s))
		if err != nil || !bytes.Equal(first, next) {
			t.Fatalf("catalog render is not deterministic: %v", err)
		}
	}
}

func TestCapabilityProfileSingleSurface(t *testing.T) {
	// No rendered mcp_servers entry may carry a native toolset name, even
	// with every feature enabled.
	servers := Config(profileSettings(func() []string {
		var all []string
		for _, f := range Features {
			all = append(all, f.Name)
		}
		return all
	}()...))["mcp_servers"].(M)
	for _, name := range nativeToolsets {
		if _, ok := servers[name]; ok {
			t.Fatalf("native toolset %q duplicated as an MCP server", name)
		}
	}
	// Owner mcp_servers cannot reserve a native or hub-owned surface name.
	for _, name := range append(slices.Clone(nativeToolsets),
		"hub", "browser", "browser_guest", "toolhub",
		"google", "github", "slack", "atlassian", "telegram_user", "desktop", "drafts") {
		s := profileSettings("workspace")
		s.MCP = map[string]MCPServer{name: {URL: "https://example.invalid/mcp"}}
		if err := s.Validate(); err == nil {
			t.Fatalf("owner MCP took reserved name %q", name)
		}
	}
	// A plain owner name still works.
	s := profileSettings("workspace")
	s.MCP = map[string]MCPServer{"notes": {URL: "https://example.invalid/mcp"}}
	if err := s.Validate(); err != nil {
		t.Fatalf("owner MCP rejected: %v", err)
	}
}

func TestCapabilityProfileOptInAbsent(t *testing.T) {
	// Default feature set renders only its own surface; nothing opt-in leaks.
	def := profileSettings("workspace", "browser", "hh")
	cfg := Config(def)
	servers := cfg["mcp_servers"].(M)
	for _, name := range []string{"browser", "browser_guest", "hub"} {
		if _, ok := servers[name]; !ok {
			t.Fatalf("default capability %q missing", name)
		}
	}
	if _, ok := cfg["image_gen"]; ok {
		t.Fatal("image_gen section present without the feature")
	}
	if toolsets := cfg["platform_toolsets"].(M)["cli"].([]string); slices.Contains(toolsets, "google_meet") {
		t.Fatal("opt-in toolset google_meet present without meet")
	}
	// Opt-in credentials enter self-env only while the feature is enabled.
	if slices.Contains(selfEnvKeys(def), "FAL_KEY") {
		t.Fatal("FAL_KEY reachable without image_gen")
	}
	on := profileSettings("workspace", "image_gen")
	on.ImageGen.Provider = "fal"
	if !slices.Contains(selfEnvKeys(on), "FAL_KEY") {
		t.Fatal("FAL_KEY missing for image_gen provider fal")
	}
	// Dropping the feature revokes the credential surface.
	if slices.Contains(selfEnvKeys(profileSettings("workspace")), "FAL_KEY") {
		t.Fatal("FAL_KEY survived image_gen removal")
	}
}

func TestCapabilityProfileDependencyClosure(t *testing.T) {
	// A toggle cannot live on a denied entry — `off` takes no extra fields —
	// and each sub-capability names its parent family in the same entry.
	parentVia := map[string]string{"browser": "mcp", "google": "mcp", "telegram_user": "mcp", "ssh": "toolhub"}
	for child, pair := range map[string][2]string{
		"browser_act": {"browser", "act"}, "google_write": {"google", "write"},
		"telegram_write": {"telegram_user", "write"}, "ssh_write": {"ssh", "write"},
		"ssh_shell": {"ssh", "shell"}, "ssh_tunnel": {"ssh", "tunnel"},
	} {
		parent, token := pair[0], pair[1]
		s := profileSettings("workspace")
		s.Tools = map[string]ToolEntry{parent: {Via: "off", Tools: map[string]bool{token: true}}}
		if err := s.Validate(); err == nil {
			t.Fatalf("%s accepted on a denied %s", child, parent)
		}
		s.Tools = map[string]ToolEntry{parent: {Via: parentVia[parent], Tools: map[string]bool{token: true}}}
		if err := s.Validate(); err != nil {
			t.Fatalf("%s toggle on %s rejected: %v", child, parent, err)
		}
	}
}

func TestCapabilityProfileCatalogIntegrity(t *testing.T) {
	// The feature list is the catalog spine: unique names, self-service is a
	// strict subset, and ToolHub-served connectors never emit a direct entry.
	seen := map[string]bool{}
	for _, f := range Features {
		if seen[f.Name] {
			t.Fatalf("duplicate feature %q", f.Name)
		}
		seen[f.Name] = true
	}
	for name := range selfServiceNames {
		if !seen[name] {
			t.Fatalf("self-service %q is not a feature", name)
		}
	}
	for _, name := range []string{"google", "github", "slack", "atlassian", "telegram_user", "gitlab"} {
		serverName, _, ok, err := ServiceMCPConfig(name)
		if err != nil || serverName != "" || !ok {
			t.Fatalf("ToolHub connector %q emitted a direct MCP entry", name)
		}
	}
}
