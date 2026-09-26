package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSelfServicesState(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "self-services.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMaterializeHermesConfigAddsToolHubServer(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	if err := os.WriteFile(source, []byte("model: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "nested", "config.yaml")
	err := MaterializeHermesConfig(source, dest, MaterializeOptions{
		ToolHubEndpoint:    "http://toolhub:8090/mcp",
		ToolHubTokenEnv:    "HUB_RUNTIME_AUTH",
		RuntimeAuthPresent: true,
		ToolHubReconnect:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"http://toolhub:8090/mcp", "${HUB_RUNTIME_AUTH}", "timeout: 1800", "mcp_reload_confirm: false"} {
		if !strings.Contains(text, want) {
			t.Fatalf("materialized config missing %s: %s", want, text)
		}
	}
}

func TestMaterializeHermesConfigToolHubOptIn(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	original := []byte("model: {}\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "config.yaml")
	if err := MaterializeHermesConfig(source, dest, MaterializeOptions{ToolHubEndpoint: ""}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dest)
	if err != nil || string(body) != string(original) {
		t.Fatalf("opt-out config changed: %q %v", body, err)
	}
}

func TestMaterializeHermesConfigLeavesExistingApproval(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	if err := os.WriteFile(source, []byte("model: {}\napprovals:\n  mcp_reload_confirm: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "config.yaml")
	opts := MaterializeOptions{ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true, ToolHubReconnect: true}
	if err := MaterializeHermesConfig(source, dest, opts); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dest)
	if err != nil || !strings.Contains(string(body), "mcp_reload_confirm: true") {
		t.Fatalf("existing approval value not preserved: %s %v", body, err)
	}
	opts.ToolHubReconnect = false
	if err := MaterializeHermesConfig(source, dest, opts); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(dest)
	if err != nil || !strings.Contains(string(body), "mcp_reload_confirm: true") {
		t.Fatalf("disabled reconnect dropped the approval: %s %v", body, err)
	}
}

func TestApplyToolHubConfigRejectsUnsafeOrConflicting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("mcp_servers:\n  toolhub:\n    url: http://127.0.0.1:9000/mcp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://public.example/mcp", "http://127.0.0.1:9001/mcp"} {
		err := applyToolHubConfigWith(path, MaterializeOptions{ToolHubEndpoint: endpoint, ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true})
		if err == nil {
			t.Fatalf("unsafe/conflicting endpoint accepted: %s", endpoint)
		}
	}
}

func TestApplyToolHubConfigRequiresAuthToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("model: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyToolHubConfigWith(path, MaterializeOptions{ToolHubEndpoint: "http://toolhub:8090/mcp", RuntimeAuthPresent: false}); err == nil {
		t.Fatal("missing runtime auth accepted")
	}
}

func TestApplySelfServicesMergesMCPConfig(t *testing.T) {
	dir := t.TempDir()
	services := writeSelfServicesState(t, dir, `{"features":["atlassian","gitlab"]}`)
	config := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(config, []byte("model: {}\nmcp_servers: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applySelfServicesFrom(config, services); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	// Connectors are ToolHub-managed: enabling them must not inject a direct
	// MCP definition into the Hermes config.
	if strings.Contains(text, "mcp-atlassian") || strings.Contains(text, "mcp.atlassian.com") || strings.Contains(text, "gitlab") {
		t.Fatal(text)
	}
}

func TestSelfServicesRejectsInvalidState(t *testing.T) {
	dir := t.TempDir()
	services := filepath.Join(dir, "self-services.json")
	for _, body := range []string{`not-json`, `{"features":["workspace"]}`, `{"features":["gitlab","gitlab"]}`} {
		if err := os.WriteFile(services, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSelfServicesFeatures(services); err == nil {
			t.Fatal("invalid self-services accepted", body)
		}
	}
	if err := os.Remove(services); err != nil {
		t.Fatal(err)
	}
	if features, err := ReadSelfServicesFeatures(services); err != nil || features != nil {
		t.Fatal(features, err)
	}
	if err := os.Mkdir(services, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSelfServicesFeatures(services); err == nil {
		t.Fatal("directory self-services accepted")
	}
}

func TestApplySelfServicesNoopAndConfigErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent", "self-services.json")
	if err := applySelfServicesFrom(filepath.Join(dir, "missing.yaml"), missing); err != nil {
		t.Fatal(err)
	}
	services := writeSelfServicesState(t, dir, `{"features":["github"]}`)
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("not: [yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applySelfServicesFrom(bad, services); err == nil {
		t.Fatal("invalid Hermes config accepted")
	}
	noServers := filepath.Join(dir, "no-servers.yaml")
	if err := os.WriteFile(noServers, []byte("model: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applySelfServicesFrom(noServers, services); err != nil {
		t.Fatal(err)
	}
}
