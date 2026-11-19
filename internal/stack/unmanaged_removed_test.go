package stack

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passportview"
)

func TestHermesMaterializationRejectsLegacyConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts MaterializeOptions
	}{
		{"missing_authority", MaterializeOptions{}},
		{"injected_mcp", MaterializeOptions{ToolHubEndpoint: "http://toolhub:8090/mcp", RuntimeAuthPresent: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			original := bytes.Replace(reviewedHermesSource(t), []byte("mcp_servers: {}"), []byte("mcp_servers:\n  injected:\n    command: arbitrary-command"), 1)
			if !bytes.Contains(original, []byte("arbitrary-command")) {
				t.Fatal("fixture did not inject an MCP server")
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := ApplyHermesConfig(path, tc.opts); err == nil {
				t.Fatal("legacy config accepted without passport validation")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("refused materialization modified the config")
			}
		})
	}
}

// The unmanaged capability mode is gone: no Settings field or YAML key can
// select it, every passport refuses a stray capability_mode key, and a
// Settings value without any mode marker still renders the managed wiring.
// Removing a passport field therefore cannot re-enable a legacy render path.
func TestNoUnmanagedRenderPath(t *testing.T) {
	settingsType := reflect.TypeOf(Settings{})
	for i := 0; i < settingsType.NumField(); i++ {
		field := settingsType.Field(i)
		if field.Name == "CapabilityMode" || strings.HasPrefix(field.Tag.Get("yaml"), "capability_mode") {
			t.Fatalf("Settings still carries a capability mode switch: %s", field.Name)
		}
	}
	for _, kind := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		body := "apiVersion: " + passportview.APIVersionV2 + "\nkind: " + kind + "\nmetadata: {space: alice, environment: prod, revision: r-1}\ncapability_mode: unmanaged\n"
		if _, err := passportview.Decode(kind, []byte(body)); err == nil || !strings.Contains(err.Error(), "capability_mode") {
			t.Fatalf("%s passport accepted a capability_mode key: %v", kind, err)
		}
	}
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	if !reflect.DeepEqual(Config(s), managedHermesConfig(s)) {
		t.Fatal("Hermes config without a mode marker left the managed render")
	}
	if _, ok := Config(s)["mcp_servers"].(M)["hub"]; ok {
		t.Fatal("legacy hub stdio server rendered")
	}
	runtime := RuntimeService(s, t.TempDir(), "/space")
	env := runtime["environment"].(M)
	for _, key := range []string{"HUB_CAPABILITY_MODE", "HUB_TOOLHUB_STORE", "HUB_CREDENTIAL_BROKER_RUNTIME_URL"} {
		if _, ok := env[key]; ok {
			t.Fatalf("runtime env carries legacy %s", key)
		}
	}
	if env["HERMES_IGNORE_RULES"] != "1" || env["HERMES_ENABLE_PROJECT_PLUGINS"] != "0" {
		t.Fatalf("runtime env lost the managed fences: %v", env)
	}
	if path := runtime["env_file"].([]any)[0].(M)["path"]; path != "/space/managed-runtime.prod.env" {
		t.Fatalf("runtime reads the legacy env file: %v", path)
	}
	if ports := runtime["ports"].([]string); len(ports) != 0 {
		t.Fatalf("runtime publishes host ports: %v", ports)
	}
	if nets := runtime["networks"].([]string); !slices.Equal(nets, []string{managedAgentNetwork(s)}) {
		t.Fatalf("runtime left the internal agent network: %v", nets)
	}
	for _, raw := range runtime["volumes"].([]any) {
		if volume, ok := raw.(M); ok && volume["target"] == "/state" && volume["source"] != "/space/managed/prod/runtime" {
			t.Fatalf("runtime state uses the legacy layout: %v", volume["source"])
		}
	}
	compose := Compose(s, t.TempDir(), "/space")
	if _, ok := compose["services"].(M)["hermes-runtime"]; ok {
		t.Fatal("compose kept a resident runtime outside supervisor spawn")
	}
	if _, ok := compose["networks"].(M)[managedAgentNetwork(s)]; !ok {
		t.Fatal("compose lacks the internal agent network")
	}
}

// A minimal space whose every tool is a reviewed native carve-out still
// renders: the runtime advertises the native toolsets, the api_server
// platform selects them, and no ToolHub MCP entry stands in for them.
func TestMinimalNativeStackStillRenders(t *testing.T) {
	native := []string{"file", "memory", "terminal", "todo"}
	s := Settings{Schema: 3, Environment: "prod", User: "alice", Timezone: "UTC", Model: "synthetic", ModelURL: "http://model-relay:8318/v1",
		CapabilityProfileID: "alice-default", CapabilityGeneration: 1, Tools: testNativeTools(native...)}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg := Config(s)
	if selected := cfg["platform_toolsets"].(M)["api_server"].([]string); !slices.Equal(selected, native) {
		t.Fatalf("api_server native toolsets: %v, want %v", selected, native)
	}
	if servers := cfg["mcp_servers"].(M); len(servers) != 0 {
		t.Fatalf("native-only space rendered MCP servers: %v", servers)
	}
	env := RuntimeService(s, t.TempDir(), t.TempDir())["environment"].(M)
	if env["HUB_NATIVE_TOOLSETS"] != strings.Join(native, ",") {
		t.Fatalf("runtime native toolsets: %v", env["HUB_NATIVE_TOOLSETS"])
	}
	services := Compose(s, t.TempDir(), t.TempDir())["services"].(M)
	for _, name := range []string{"model-relay", "toolhub-relay"} {
		if _, ok := services[name]; !ok {
			t.Fatalf("native-only space lost the %s relay", name)
		}
	}
}
