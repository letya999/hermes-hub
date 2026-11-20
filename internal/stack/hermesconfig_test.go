package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestApplyHermesConfigEmptyFile(t *testing.T) {
	dir := t.TempDir()
	effective := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(effective, nil, 0600); err != nil {
		t.Fatal(err)
	}
	err := ApplyHermesConfig(effective, MaterializeOptions{
		ToolHubEndpoint:    "http://toolhub:8090/mcp",
		ToolHubTokenEnv:    "HUB_RUNTIME_AUTH",
		RuntimeAuthPresent: true,
		Context:            &ContextConfig{CompactEngine: "auto"},
	})
	if err == nil {
		t.Fatal("empty config accepted without a reviewed template")
	}
	body, readErr := os.ReadFile(effective)
	if readErr != nil || len(body) != 0 {
		t.Fatal("refused config was modified")
	}
}

func TestMaterializeHermesConfigAddsToolHubServer(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	if err := os.WriteFile(source, reviewedHermesSource(t), 0600); err != nil {
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
	for _, want := range []string{managedToolHubProxyEndpoint, "${HUB_RUNTIME_AUTH}", "timeout: 1800", "mcp_reload_confirm: false"} {
		if !strings.Contains(text, want) {
			t.Fatalf("materialized config missing %s: %s", want, text)
		}
	}
}

func TestMaterializationReplacesFileAndPreservesItOnFailure(t *testing.T) {
	dir := t.TempDir()
	source, dest := filepath.Join(dir, "source.yaml"), filepath.Join(dir, "effective.yaml")
	if err := os.WriteFile(source, reviewedHermesSource(t), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("previous\n"), 0644); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(dir, "original.yaml")
	if err := os.Link(dest, original); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := MaterializeHermesConfig(source, dest, MaterializeOptions{ToolHubEndpoint: "http://toolhub:8090/mcp", RuntimeAuthPresent: true}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("modified existing host-owned file instead of replacing it")
	}
	good, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("invalid: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeHermesConfig(source, dest, MaterializeOptions{ToolHubEndpoint: "http://toolhub:8090/mcp", RuntimeAuthPresent: true}); err == nil {
		t.Fatal("invalid YAML accepted")
	}
	unchanged, err := os.ReadFile(dest)
	if err != nil || string(unchanged) != string(good) {
		t.Fatalf("failed edit changed config: %q %v", unchanged, err)
	}
}

func TestSupervisedComposeRequiresControlAuth(t *testing.T) {
	s := Settings{User: "alice", Environment: "dev", ExecutionMode: "supervisor", SupervisorURL: "http://host.docker.internal:8876", Tools: map[string]ToolEntry{"workspace": {Via: ToolViaNative}}, Ingress: []string{"telegram"}}
	services := Compose(s, t.TempDir(), t.TempDir())["services"].(M)
	for service, key := range map[string]string{"communication-hub": "HUB_SUPERVISOR_AUTH", "toolhub": "HUB_COMMUNICATION_AUTH"} {
		e := services[service].(M)["environment"].(M)
		value, _ := e[key].(string)
		if !strings.HasPrefix(value, "${HUB_SUPERVISOR_AUTH:?") {
			t.Errorf("%s can silently start without control auth: %q", service, value)
		}
	}
}

func TestMaterializeHermesConfigRequiresToolHub(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	original := []byte("model: {}\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "config.yaml")
	if err := MaterializeHermesConfig(source, dest, MaterializeOptions{}); err == nil {
		t.Fatal("missing ToolHub authority accepted")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("refused materialization created a destination")
	}
	body, err := os.ReadFile(source)
	if err != nil || string(body) != string(original) {
		t.Fatalf("opt-out config changed: %q %v", body, err)
	}
}

func TestToolHubConfigLeavesExistingApproval(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	if err := os.WriteFile(source, []byte("model: {}\napprovals:\n  mcp_reload_confirm: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := MaterializeOptions{ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true, ToolHubReconnect: true}
	if err := applyToolHubConfigWith(source, opts); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(source)
	if err != nil || !strings.Contains(string(body), "mcp_reload_confirm: true") {
		t.Fatalf("existing approval value not preserved: %s %v", body, err)
	}
	opts.ToolHubReconnect = false
	if err := applyToolHubConfigWith(source, opts); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(source)
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

func TestManagedToolHubConfigUsesRunBindingProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("model: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyToolHubConfigWith(path, MaterializeOptions{ToolHubEndpoint: "http://toolhub:8090/mcp", RuntimeAuthPresent: true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), managedToolHubProxyEndpoint) || strings.Contains(string(body), "http://toolhub:8090/mcp") {
		t.Fatalf("managed ToolHub bypasses runtime run binding: %s", body)
	}
}

func reviewedHermesSource(t *testing.T) []byte {
	t.Helper()
	body, err := yaml.Marshal(Config(Settings{Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC"}))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
