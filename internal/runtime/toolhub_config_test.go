package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyToolHubConfigIsOptInAndUsesEnvReference(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("model: {}\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyToolHubConfig(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != string(original) {
		t.Fatalf("opt-in config changed: %q %v", body, err)
	}

	t.Setenv("HUB_TOOLHUB_ENDPOINT", "http://127.0.0.1:8090/mcp")
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	if err := applyToolHubConfig(path); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "http://127.0.0.1:8090/mcp") || !strings.Contains(text, "${HUB_RUNTIME_AUTH}") || strings.Contains(text, strings.Repeat("a", 32)) {
		t.Fatalf("ToolHub config leaked or omitted token reference: %s", text)
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

func TestApplyToolHubConfigRejectsUnsafeOrConflictingConfiguration(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mcp_servers:\n  toolhub:\n    url: http://127.0.0.1:9000/mcp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://public.example/mcp", "http://127.0.0.1:9001/mcp"} {
		t.Setenv("HUB_TOOLHUB_ENDPOINT", endpoint)
		if err := applyToolHubConfig(path); err == nil {
			t.Fatalf("unsafe/conflicting endpoint accepted: %s", endpoint)
		}
	}
}
