package stack

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestInitNeverOverwrites(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "artem"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "secrets.prod.env")
	before, _ := os.ReadFile(p)
	if err := Init(d, "artem"); err == nil {
		t.Fatal("overwrote deployment")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("secrets changed")
	}
	secrets, err := ReadSecrets(p)
	if err != nil || secrets["OPENAI_API_KEY"] != "" {
		t.Fatal(err)
	}
	s, err := Read(filepath.Join(d, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(Doctor(s, secrets)) != 3 {
		t.Fatal("missing model credentials not reported")
	}
}
func TestValidation(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	for _, modify := range []func(*Settings){func(s *Settings) { s.User = "../other" }, func(s *Settings) { s.Features = []string{"telegram_write"} }, func(s *Settings) { s.Features = []string{"google_write"} }, func(s *Settings) { s.Features = []string{"missing"} }, func(s *Settings) { s.ModelURL = "http://" + "user:secret" + "@host/v1" }, func(s *Settings) { s.Timezone = "invalid/zone" }, func(s *Settings) { s.BrowserPort = 8000 }} {
		copy := s
		modify(&copy)
		if copy.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
	s.Features = []string{"telegram"}
	secrets := map[string]string{"OPENAI_API_KEY": "x", "TELEGRAM_BOT_TOKEN": "x", "TELEGRAM_ALLOWED_USERS": "*"}
	if !strings.Contains(strings.Join(Doctor(s, secrets), " "), "numeric owner") {
		t.Fatal("wildcard owner accepted")
	}
}
func TestRenderAllFeatures(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "config"), 0700)
	_ = os.WriteFile(filepath.Join(root, "config/SOUL.md"), []byte("original"), 0600)
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	s, _ := Read(filepath.Join(d, "settings.yaml"))
	s.Model = "test"
	s.ModelURL = "http://host.docker.internal:8317/v1"
	s.GoogleEmail = "me@example.org"
	s.DesktopURL = "http://host.docker.internal:8765/mcp"
	s.DraftsURL = "http://host.docker.internal:8766/mcp"
	for _, f := range Features {
		s.Features = append(s.Features, f.Name)
	}
	s.Features = nil
	for _, f := range Features {
		s.Features = append(s.Features, f.Name)
	}
	b, _ := yaml.Marshal(s)
	_ = os.WriteFile(filepath.Join(d, "settings.yaml"), b, 0600)
	if err := Render(d, root); err != nil {
		t.Fatal(err)
	}
	generatedConfig, err := os.ReadFile(filepath.Join(d, "generated", "hermes.prod.yaml"))
	if err != nil || !strings.Contains(string(generatedConfig), "/opt/hub/skills") {
		t.Fatalf("default global skills mount missing: %v %s", err, generatedConfig)
	}
	generatedCompose, err := os.ReadFile(filepath.Join(d, "generated", "compose.prod.yaml"))
	if err != nil || !strings.Contains(string(generatedCompose), filepath.ToSlash(filepath.Join(root, "config", "skills"))) {
		t.Fatalf("default global skills source missing: %v %s", err, generatedCompose)
	}
	cfg := Config(s)
	display, ok := cfg["display"].(M)
	if !ok || display["busy_input_mode"] != "queue" || display["long_running_notifications"] != true {
		t.Fatalf("long-running Telegram defaults missing: %#v", cfg["display"])
	}
	timeouts := cfg["timeouts"].(M)["tools"].(M)
	if timeouts["sequential_call"] != 1800 || timeouts["concurrent_batch"] != 1800 {
		t.Fatalf("long-running tool timeouts missing: %#v", timeouts)
	}
	servers := cfg["mcp_servers"].(M)
	// Every catalog feature enabled still yields no direct upstream MCP server:
	// connectors are served through ToolHub, never embedded in Hermes config.
	for _, name := range []string{"google", "browser", "github", "slack", "atlassian", "telegram_user", "desktop", "drafts"} {
		if _, ok := servers[name]; ok {
			t.Fatalf("direct MCP server leaked into Hermes config: %s", name)
		}
	}
	if _, ok := servers["hub"]; !ok {
		t.Fatal("platform hub tool server missing")
	}
	s.Features = []string{"telegram_user", "google", "google_write"}
	if servers := Config(s)["mcp_servers"].(M); len(servers) != 0 {
		t.Fatalf("connector features produced direct MCP servers: %v", servers)
	}
	s.Features = []string{"gitlab"}
	s.GitLabHost = "gitlab.example.com"
	s.Environment = "prod"
	composeEnv := Compose(s, "/source", "/space")["services"].(M)["hermes-runtime"].(M)["environment"].(M)
	if composeEnv["HERMES_AGENT_NOTIFY_INTERVAL"] != "60" {
		t.Fatalf("heartbeat interval missing: %#v", composeEnv["HERMES_AGENT_NOTIFY_INTERVAL"])
	}
	if composeEnv["GITLAB_HOST"] != "gitlab.example.com" {
		t.Fatal("GitLab host missing")
	}
	if composeEnv["HUB_RUNTIME_GENERATION"] != "static-me-prod" {
		t.Fatalf("static runtime generation missing: %#v", composeEnv["HUB_RUNTIME_GENERATION"])
	}
	s.Features = []string{"atlassian"}
	if _, ok := Config(s)["mcp_servers"].(M)["atlassian"]; ok {
		t.Fatal("direct Atlassian MCP config leaked")
	}
	soul := filepath.Join(d, "SOUL.md")
	_ = os.WriteFile(soul, []byte("owner changes"), 0600)
	if err := Render(d, root); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(soul)
	if string(got) != "owner changes" {
		t.Fatal("overwrote memory")
	}
	compose, _ := os.ReadFile(filepath.Join(d, "compose.prod.yaml"))
	if !strings.Contains(string(compose), "127.0.0.1:6080:6080") {
		t.Fatal("network boundary")
	}
	// docker.sock is restricted to toolhub/workload-controller; the per-service
	// boundary is asserted in TestRenderedServiceBoundaries.
	if !strings.Contains(string(compose), "target: /state/hermes/SOUL.md") || !strings.Contains(string(compose), "read_only: true") {
		t.Fatal("managed SOUL is not mounted read-only")
	}
}

func TestRenderSplitsGatewaySecretsFromRuntime(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	settings, err := Read(filepath.Join(d, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Features = []string{"workspace", "telegram", "atlassian"}
	if err := saveSettings(filepath.Join(d, "settings.yaml"), settings); err != nil {
		t.Fatal(err)
	}
	secrets := "OPENAI_API_KEY=model\nTELEGRAM_BOT_TOKEN=bot\nTELEGRAM_ALLOWED_USERS=11\nJIRA_API_TOKEN=provider\n"
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte(secrets), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	runtimeEnv, err := os.ReadFile(filepath.Join(d, "runtime.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	gatewayEnv, err := os.ReadFile(filepath.Join(d, "communication.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runtimeEnv), "OPENAI_API_KEY=model") || strings.Contains(string(runtimeEnv), "TELEGRAM_BOT_TOKEN") || strings.Contains(string(runtimeEnv), "TELEGRAM_ALLOWED_USERS") {
		t.Fatalf("runtime env boundary broken: %q", runtimeEnv)
	}
	if string(gatewayEnv) != "TELEGRAM_ALLOWED_USERS=11\nTELEGRAM_BOT_TOKEN=bot\n" {
		t.Fatalf("gateway env boundary broken: %q", gatewayEnv)
	}
	services := Compose(settings, root, d)["services"].(M)
	for _, name := range []string{"hermes-runtime", "communication-hub", "cliproxy", "toolhub", "workload-controller", "credential-broker"} {
		if _, ok := services[name]; !ok {
			t.Fatalf("service %s missing: %#v", name, services)
		}
	}
	if services["communication-hub"].(M)["entrypoint"].([]string)[0] != "communication-hub" {
		t.Fatalf("split services missing: %#v", services)
	}
	for _, raw := range services["communication-hub"].(M)["volumes"].([]any) {
		if raw.(M)["target"] == "/state" || raw.(M)["target"] == "/workspace" {
			t.Fatal("gateway received a user mount")
		}
	}
	if _, ok := services["hermes-runtime"].(M)["ports"].([]string); !ok {
		t.Fatal("runtime service missing browser/OAuth port mapping")
	}
}

func TestComposeSupervisorModeOmitsResidentRuntime(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://host.docker.internal:8765")
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Features: []string{"telegram"}}
	services := Compose(s, "/source", "/space")["services"].(M)
	if _, ok := services["hermes-runtime"]; ok {
		t.Fatal("supervisor mode still starts resident Hermes runtime")
	}
	gateway, ok := services["communication-hub"].(M)
	if !ok {
		t.Fatal("communication hub missing")
	}
	if _, ok := gateway["depends_on"]; ok {
		t.Fatal("supervisor mode still depends on static runtime")
	}
	if files := gateway["env_file"].([]any); len(files) != 1 {
		t.Fatalf("supervisor gateway received per-runtime auth file: %#v", files)
	}
	if gateway["environment"].(M)["HUB_SUPERVISOR_AUTH"] != "${HUB_SUPERVISOR_AUTH}" {
		t.Fatal("supervisor auth placeholder missing")
	}
}

func saveSettings(path string, settings Settings) error {
	b, err := yaml.Marshal(settings)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

func TestTelegramGatewayAndPersonalMCPAreIndependent(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	s.Features = []string{"telegram"}
	config := Config(s)
	if _, ok := config["mcp_servers"].(M)["telegram_user"]; ok {
		t.Fatal("bot gateway enabled personal Telegram MCP")
	}
	services := Compose(s, "/source", "/space")["services"].(M)
	if _, ok := services["communication-hub"]; !ok {
		t.Fatal("bot gateway service missing")
	}
	s.Features = []string{"telegram_user"}
	config = Config(s)
	if _, ok := config["mcp_servers"].(M)["telegram_user"]; ok {
		t.Fatal("personal Telegram MCP is served through ToolHub, not embedded")
	}
	services = Compose(s, "/source", "/space")["services"].(M)
	if _, ok := services["communication-hub"]; ok {
		t.Fatal("personal Telegram MCP selected bot gateway service")
	}
}

func TestSelfEnvKeysIncludeCatalogConnectors(t *testing.T) {
	s := Settings{Features: []string{"telegram", "gitlab", "atlassian"}, MCP: map[string]MCPServer{"custom": {URL: "https://example.invalid/mcp", Headers: map[string]string{"Authorization": "Bearer ${CUSTOM_TOKEN}"}}}}
	keys := strings.Join(selfEnvKeys(s), ",")
	if !strings.Contains(keys, "OPENAI_API_KEY") || !strings.Contains(keys, "FIRECRAWL_API_KEY") || strings.Contains(keys, "TELEGRAM_BOT_TOKEN") || !strings.Contains(keys, "GITLAB_TOKEN") || !strings.Contains(keys, "JIRA_URL") || !strings.Contains(keys, "JIRA_USERNAME") || !strings.Contains(keys, "JIRA_API_TOKEN") || !strings.Contains(keys, "CUSTOM_TOKEN") || !strings.Contains(keys, "TELEGRAM_API_ID") {
		t.Fatalf("unexpected self-env keys: %s", keys)
	}
}

func TestServiceCatalogConfig(t *testing.T) {
	gitlab, ok := ServiceInfoByName("gitlab")
	if !ok || !gitlab.SelfService || len(gitlab.Requires) != 1 || gitlab.Requires[0] != "GITLAB_TOKEN" {
		t.Fatal(gitlab, ok)
	}
	// Upstream connectors are served through ToolHub and must not write a
	// direct MCP definition into the Hermes config.
	server, config, ok, err := ServiceMCPConfig("atlassian")
	if err != nil || !ok || server != "" || config != nil {
		t.Fatal(server, config, ok, err)
	}
	if _, _, _, err := ServiceMCPConfig("browser"); err == nil {
		t.Fatal("host-managed service accepted")
	}
	for _, name := range []string{"google", "google_write", "github", "slack", "telegram_user", "telegram_write", "gitlab"} {
		server, config, ok, err := ServiceMCPConfig(name)
		if err != nil || !ok || server != "" || config != nil {
			t.Fatal(name, server, config, ok, err)
		}
	}
	server, config, ok, err = ServiceMCPConfig("hh")
	if err != nil || !ok || server != "hub" || config == nil {
		t.Fatal("hh must still enable the platform hub tool server", server, ok, err)
	}
	if _, _, _, err := ServiceMCPConfig("unknown"); err == nil {
		t.Fatal("unknown service accepted")
	}
	if _, ok := ServiceInfoByName("unknown"); ok {
		t.Fatal("unknown service found")
	}
}

func TestSecretParsing(t *testing.T) {
	for _, content := range []string{"TOKEN='quoted'\n", "TOKEN=a\nTOKEN=b\n", "BAD NAME=a\n"} {
		p := filepath.Join(t.TempDir(), "env")
		_ = os.WriteFile(p, []byte(content), 0600)
		if _, err := ReadSecrets(p); err == nil {
			t.Fatalf("accepted %q", content)
		}
	}
	p := filepath.Join(t.TempDir(), "env")
	_ = os.WriteFile(p, []byte("TOKEN=$literal#stillliteral\n"), 0600)
	s, err := ReadSecrets(p)
	if err != nil || s["TOKEN"] != "$literal#stillliteral" {
		t.Fatal(s, err)
	}
}

func TestSlackAppIsNotSlackDataToolsAndHonchoIsOptIn(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Features: []string{"workspace", "slack_app"}, Memory: true}
	cfg := Config(s)
	if _, ok := cfg["mcp_servers"].(M)["slack"]; ok {
		t.Fatal("slack_app created Slack read/write tools")
	}
	services := Compose(s, "/source", "/space")["services"].(M)
	if _, ok := services["communication-hub"]; !ok {
		t.Fatal("slack_app did not start communication-hub")
	}
	gw := services["communication-hub"].(M)
	ports, _ := gw["ports"].([]string)
	if len(ports) != 1 || ports[0] != "127.0.0.1:8081:8081" {
		t.Fatalf("slack events port not published: %#v", gw["ports"])
	}
	if gw["environment"].(M)["HUB_COMMUNICATION_LISTEN"] != "0.0.0.0:8081" {
		t.Fatal("slack events listen address missing")
	}
	s.Honcho = true
	if s.Validate() == nil {
		t.Fatal("honcho without official URL accepted")
	}
	s.HonchoURL = "http://127.0.0.1:8000"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeHonchoConfig(d, s); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(d, "hermes", "honcho.json"))
	if err != nil || !strings.Contains(string(body), "http://127.0.0.1:8000") {
		t.Fatalf("honcho.json=%s err=%v", body, err)
	}
	cfg = Config(s)
	memory := cfg["memory"].(M)
	if memory["provider"] == "honcho" {
		t.Fatal("honcho provider forced into native Hermes config")
	}
	if GatewayOwnedSecret("SLACK_SIGNING_SECRET") == false || GatewayOwnedSecret("OPENAI_API_KEY") {
		t.Fatal("gateway secret classification")
	}
}

func TestSlackEventsPortCustomDevAndCollision(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, SlackEventsPort: 9091, Features: []string{"slack_app"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	gw := Compose(s, "/source", "/space")["services"].(M)["communication-hub"].(M)
	ports, _ := gw["ports"].([]string)
	if len(ports) != 1 || ports[0] != "127.0.0.1:9091:8081" {
		t.Fatalf("custom slack events port: %#v", gw["ports"])
	}
	s.SlackEventsPort = 6080
	if s.Validate() == nil {
		t.Fatal("colliding slack events port accepted")
	}
	dir := t.TempDir()
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	encoded, err := yaml.Marshal(Settings{Schema: 1, User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Features: []string{"workspace", "slack_app"}, Memory: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	dev, err := ReadEnvironment(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if dev.SlackEventsPort != 8082 {
		t.Fatalf("dev slack events port=%d", dev.SlackEventsPort)
	}
}

func TestTranscriptionWiresHubSTTAndDockerfile(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Features: []string{"telegram", "transcription"}}
	gw := Compose(s, "/source", "/space")["services"].(M)["communication-hub"].(M)
	env := gw["environment"].(M)
	if env["HUB_STT_COMMAND"] != "/usr/local/bin/hub-stt" {
		t.Fatalf("HUB_STT_COMMAND=%v", env["HUB_STT_COMMAND"])
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	df, err := os.ReadFile(filepath.Join(root, "docker", "Dockerfile"))
	if err != nil || !strings.Contains(string(df), "docker/hub-stt") {
		t.Fatalf("Dockerfile missing hub-stt copy: %v", err)
	}
	stt, err := os.ReadFile(filepath.Join(root, "docker", "hub-stt"))
	if err != nil || !strings.Contains(string(stt), "faster_whisper") {
		t.Fatalf("hub-stt worker missing faster_whisper: %v", err)
	}
}

func TestRenderWiresToolHubEndpointAndHealsSoulStub(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("template soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	settings, err := Read(filepath.Join(d, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	if err := saveSettings(filepath.Join(d, "settings.yaml"), settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	runtimeEnv, err := os.ReadFile(filepath.Join(d, "runtime.prod.env"))
	if err != nil || !strings.Contains(string(runtimeEnv), "HUB_TOOLHUB_ENDPOINT=http://host.docker.internal:8090/mcp") {
		t.Fatalf("default ToolHub endpoint missing: %q %v", runtimeEnv, err)
	}
	if soul, _ := os.ReadFile(filepath.Join(d, "SOUL.md")); string(soul) != "template soul" {
		t.Fatalf("SOUL stub not healed to template: %q", soul)
	}
	secrets := "OPENAI_API_KEY=model\nHUB_TOOLHUB_ENDPOINT=http://custom:9/mcp\n"
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte(secrets), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	runtimeEnv, _ = os.ReadFile(filepath.Join(d, "runtime.prod.env"))
	if !strings.Contains(string(runtimeEnv), "HUB_TOOLHUB_ENDPOINT=http://custom:9/mcp") {
		t.Fatalf("explicit ToolHub endpoint lost: %q", runtimeEnv)
	}
	secrets = "OPENAI_API_KEY=model\nHUB_TOOLHUB_ENDPOINT=\n"
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte(secrets), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	runtimeEnv, _ = os.ReadFile(filepath.Join(d, "runtime.prod.env"))
	if !strings.Contains(string(runtimeEnv), "HUB_TOOLHUB_ENDPOINT=\n") || strings.Contains(string(runtimeEnv), "HUB_TOOLHUB_ENDPOINT=http") {
		t.Fatalf("empty opt-out lost: %q", runtimeEnv)
	}
	if err := os.WriteFile(filepath.Join(d, "SOUL.md"), []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	if soul, _ := os.ReadFile(filepath.Join(d, "SOUL.md")); string(soul) != "mine" {
		t.Fatalf("user SOUL overwritten: %q", soul)
	}
}
