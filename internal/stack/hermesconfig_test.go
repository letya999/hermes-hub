package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeSelfServicesState(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "self-services.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

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
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(effective)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "http://toolhub:8090/mcp") {
		t.Fatalf("empty config did not gain toolhub entry: %s", body)
	}
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

func TestLegacyMaterializationReplacesFileAndPreservesItOnFailure(t *testing.T) {
	dir := t.TempDir()
	source, dest := filepath.Join(dir, "source.yaml"), filepath.Join(dir, "effective.yaml")
	if err := os.WriteFile(source, []byte("model: {}\n"), 0600); err != nil {
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
	if err := MaterializeHermesConfig(source, dest, MaterializeOptions{}); err != nil {
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
	if err := MaterializeHermesConfig(source, dest, MaterializeOptions{}); err == nil {
		t.Fatal("invalid YAML accepted")
	}
	unchanged, err := os.ReadFile(dest)
	if err != nil || string(unchanged) != string(good) {
		t.Fatalf("failed edit changed config: %q %v", unchanged, err)
	}
}

func TestLegacyHubReceivesRoutineControlEnvironment(t *testing.T) {
	dir := t.TempDir()
	source, dest := filepath.Join(dir, "source.yaml"), filepath.Join(dir, "effective.yaml")
	if err := os.WriteFile(source, []byte("mcp_servers:\n  hub:\n    command: hubctl\n    env:\n      CUSTOM: keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeHermesConfig(source, dest, MaterializeOptions{}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	e := cfg["mcp_servers"].(map[string]any)["hub"].(map[string]any)["env"].(map[string]any)
	for key, want := range map[string]string{"HUB_COMMUNICATION_CONTROL_URL": "${HUB_COMMUNICATION_CONTROL_URL}", "HUB_COMMUNICATION_AUTH": "${HUB_RUNTIME_AUTH}", "HUB_PRINCIPAL_ID": "${HUB_PRINCIPAL_ID}", "HUB_USER_ID": "${HUB_USER_ID}", "HUB_ORG_ACTIONS": "${HUB_ORG_ACTIONS}", "CUSTOM": "keep"} {
		if e[key] != want {
			t.Errorf("hub child env %s: got %v, want %s", key, e[key], want)
		}
	}
}

func TestRenderedHubReceivesRoutineControlEnvironment(t *testing.T) {
	cfg := Config(Settings{User: "alice", Model: "synthetic", ModelURL: "http://model:8000/v1", Tools: map[string]ToolEntry{"workspace": {Via: ToolViaNative}}})
	servers := cfg["mcp_servers"].(M)
	hub, ok := servers["hub"].(M)
	if !ok {
		t.Fatal("workspace hub server missing")
	}
	e := hub["env"].(M)
	for key, want := range map[string]string{"HUB_COMMUNICATION_CONTROL_URL": "${HUB_COMMUNICATION_CONTROL_URL}", "HUB_COMMUNICATION_AUTH": "${HUB_RUNTIME_AUTH}", "HUB_PRINCIPAL_ID": "${HUB_PRINCIPAL_ID}", "HUB_USER_ID": "${HUB_USER_ID}"} {
		if e[key] != want {
			t.Errorf("rendered child env %s: got %v, want %s", key, e[key], want)
		}
	}
}

func TestHubImageEnvironmentUsesEffectiveConfigWithoutParentLeak(t *testing.T) {
	t.Setenv("UNRELATED_PROVIDER_KEY", "synthetic-unrelated")
	cfg := Config(Settings{User: "alice", Model: "synthetic", ModelURL: "http://model:8000/v1", Tools: map[string]ToolEntry{"workspace": {Via: ToolViaNative}}})
	e := cfg["mcp_servers"].(M)["hub"].(M)["env"].(M)
	if e["OPENAI_API_KEY"] != "${OPENAI_API_KEY}" || e["HUB_HERMES_CONFIG"] != "/state/hermes/config.yaml" {
		t.Fatalf("image child missing explicit model/config refs: %v", e)
	}
	if _, leaked := e["UNRELATED_PROVIDER_KEY"]; leaked {
		t.Fatal("unrelated key forwarded")
	}
	dir := t.TempDir()
	source, effective := filepath.Join(dir, "source.yaml"), filepath.Join(dir, "effective.yaml")
	if err := os.WriteFile(source, []byte("mcp_servers:\n  hub:\n    command: hubctl\n    env: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeHermesConfig(source, effective, MaterializeOptions{}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(effective)
	if err != nil {
		t.Fatal(err)
	}
	var loaded map[string]any
	if err := yaml.Unmarshal(body, &loaded); err != nil {
		t.Fatal(err)
	}
	child := loaded["mcp_servers"].(map[string]any)["hub"].(map[string]any)["env"].(map[string]any)
	// The hub child runs inside the runtime container, so the env must carry
	// the container mount path — never the host-side materialization path.
	if child["OPENAI_API_KEY"] != "${OPENAI_API_KEY}" || child["HUB_HERMES_CONFIG"] != "/state/hermes/config.yaml" {
		t.Fatalf("hub child env must use the container config path, got: %v", child)
	}
}

func TestHubChildCarriesPolicyAndFullRuntimeIdentity(t *testing.T) {
	cfg := Config(Settings{User: "alice", Model: "synthetic", ModelURL: "http://model:8000/v1", Tools: map[string]ToolEntry{"workspace": {Via: ToolViaNative}}})
	e := cfg["mcp_servers"].(M)["hub"].(M)["env"].(M)
	for _, key := range []string{"HUB_CONTEXT_ID", "HUB_RUNTIME_ID", "HUB_POLICY_VERSION", "HUB_TOOL_POLICY", "HUB_TOOLS_RO", "HUB_ORG_SCOPED", "HUB_ORG_ACTIONS", "HUB_FEATURES", "HUB_DEFER_RUNTIME_RESTART"} {
		if e[key] != "${"+key+"}" {
			t.Errorf("hub child lost trusted policy/identity %s: %v", key, e[key])
		}
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
