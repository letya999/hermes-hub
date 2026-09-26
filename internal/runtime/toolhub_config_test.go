package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
)

func TestMaterializeOptionsFromEnvIsOptInAndUsesEnvReference(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_ENDPOINT", "")
	t.Setenv("HUB_TOOLHUB_AUTOSTART", "")
	if got := toolHubEndpoint(); got != "" {
		t.Fatalf("unset ToolHub endpoint is not opt-in: %q", got)
	}
	t.Setenv("HUB_TOOLHUB_AUTOSTART", "true")
	if got := toolHubEndpoint(); got != defaultToolHubEndpoint {
		t.Fatalf("autostart endpoint=%q", got)
	}
	t.Setenv("HUB_TOOLHUB_AUTOSTART", "")
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	original := []byte("model: {}\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "config.yaml")
	if err := stack.MaterializeHermesConfig(source, dest, materializeOptionsFromEnv()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dest)
	if err != nil || string(body) != string(original) {
		t.Fatalf("opt-in config changed: %q %v", body, err)
	}

	t.Setenv("HUB_TOOLHUB_ENDPOINT", "http://127.0.0.1:8090/mcp")
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_TOOLHUB_RECONNECT", "")
	if err := stack.MaterializeHermesConfig(source, dest, materializeOptionsFromEnv()); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "http://127.0.0.1:8090/mcp") || !strings.Contains(text, "${HUB_RUNTIME_AUTH}") || !strings.Contains(text, "timeout: 1800") || !strings.Contains(text, "mcp_reload_confirm: false") || strings.Contains(text, strings.Repeat("a", 32)) {
		t.Fatalf("ToolHub config leaked or omitted token reference: %s", text)
	}
	t.Setenv("HUB_TOOLHUB_RECONNECT", "false")
	if err := stack.MaterializeHermesConfig(source, dest, materializeOptionsFromEnv()); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(dest)
	if err != nil || strings.Contains(string(body), "mcp_reload_confirm") {
		t.Fatalf("disabled reconnect still injected the approval: %s %v", body, err)
	}
}

func TestToolHubAutostartBinaryPrefersShippedName(t *testing.T) {
	old := lookPath
	t.Cleanup(func() { lookPath = old })
	lookPath = func(name string) (string, error) {
		if name == "hub-toolhub" {
			return "/usr/local/bin/hub-toolhub", nil
		}
		return "", os.ErrNotExist
	}
	if got := toolHubAutostartBinary(); got != "hub-toolhub" {
		t.Fatalf("preferred binary=%q", got)
	}
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if got := toolHubAutostartBinary(); got != "toolhub" {
		t.Fatalf("fallback binary=%q", got)
	}
}
