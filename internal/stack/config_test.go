package stack

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
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
	s, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(Doctor(s, secrets)) != 3 {
		t.Fatal("missing model credentials not reported")
	}
}
func TestValidation(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	for _, modify := range []func(*Settings){func(s *Settings) { s.User = "../other" }, func(s *Settings) {
		s.Tools = map[string]ToolEntry{"telegram_user": {Via: "off", Tools: map[string]bool{"write": true}}}
	}, func(s *Settings) {
		s.Tools = map[string]ToolEntry{"google": {Via: "mcp", Tools: map[string]bool{"bogus": true}}}
	}, func(s *Settings) { s.Ingress = []string{"bogus"} }, func(s *Settings) { s.ModelURL = "http://" + "user:secret" + "@host/v1" }, func(s *Settings) { s.Timezone = "invalid/zone" }, func(s *Settings) { s.BrowserPort = 8000 }} {
		copy := s
		modify(&copy)
		if copy.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
	s.Tools, s.Ingress = testTools("telegram"), testIngress("telegram")
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
	gated := filepath.Join(root, "config", "skills", "deep-research-embedded")
	_ = os.MkdirAll(gated, 0755)
	_ = os.WriteFile(filepath.Join(gated, "SKILL.md"), []byte("name: deep-research-embedded"), 0600)
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	writeSSHConfig(t, d) // the all-features render includes ssh; it fails closed without a config
	s, _ := Read(d)
	s.Model = "test"
	s.ModelURL = "http://host.docker.internal:8317/v1"
	s.GoogleEmail = "me@example.org"
	s.DesktopURL = "http://host.docker.internal:8765/mcp"
	s.DraftsURL = "http://host.docker.internal:8766/mcp"
	var featureNames []string
	for _, f := range Features {
		featureNames = append(featureNames, f.Name)
	}
	s.Tools, s.Ingress = testTools(featureNames...), testIngress(featureNames...)
	if err := saveSpace(d, s); err != nil {
		t.Fatal(err)
	}
	if err := Render(d, root); err != nil {
		t.Fatal(err)
	}
	generatedConfig, err := os.ReadFile(filepath.Join(d, "generated", "hermes.prod.yaml"))
	if err != nil || !strings.Contains(string(generatedConfig), "/opt/hub/skills") {
		t.Fatalf("default global skills mount missing: %v %s", err, generatedConfig)
	}
	generatedCompose, err := os.ReadFile(filepath.Join(d, "generated", "compose.prod.yaml"))
	if err != nil || !strings.Contains(string(generatedCompose), filepath.ToSlash(filepath.Join(d, "generated", "skills"))) {
		t.Fatalf("filtered global skills source missing: %v %s", err, generatedCompose)
	}
	if _, err := os.Stat(filepath.Join(d, "generated", "skills", "deep-research-embedded", "SKILL.md")); err != nil {
		t.Fatalf("bundled deep-research skill not materialized: %v", err)
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
	for _, name := range []string{"google", "github", "slack", "atlassian", "telegram_user", "desktop", "drafts"} {
		if _, ok := servers[name]; ok {
			t.Fatalf("direct MCP server leaked into Hermes config: %s", name)
		}
	}
	if _, ok := servers["hub"]; !ok {
		t.Fatal("platform hub tool server missing")
	}
	for _, name := range []string{"browser", "browser_guest"} {
		server, ok := servers[name].(M)
		if !ok {
			t.Fatalf("hub-owned browser server %s missing", name)
		}
		tools, ok := server["tools"].(MCPTools)
		if !ok || len(tools.Include) != len(browserReadTools)+len(browserMutationTools) {
			t.Fatalf("browser_act must extend the %s allowlist: %#v", name, server["tools"])
		}
	}
	if args := servers["browser"].(M)["args"].([]string); !slices.Contains(args, "--cdp-endpoint") {
		t.Fatalf("persistent browser must attach to the per-user CDP profile: %v", args)
	}
	if args := servers["browser_guest"].(M)["args"].([]string); !slices.Contains(args, "--isolated") {
		t.Fatalf("guest browser must use an in-memory profile: %v", args)
	}
	s.Tools, s.Ingress = testTools("telegram_user", "google", "google_write"), testIngress("telegram_user", "google", "google_write")
	if servers := Config(s)["mcp_servers"].(M); len(servers) != 1 || servers["hub"] == nil {
		t.Fatalf("connector features produced direct MCP servers: %v", servers)
	}
	s.Tools, s.Ingress = testTools("gitlab"), testIngress("gitlab")
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
	s.Tools, s.Ingress = testTools("atlassian"), testIngress("atlassian")
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
	settings, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Tools, settings.Ingress = testTools("workspace", "telegram", "atlassian"), testIngress("workspace", "telegram", "atlassian")
	if err := saveSpace(d, settings); err != nil {
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
	// The gateway's primary control bearer is this space's runtime token;
	// sibling spaces authenticate through toolhub-tokens.json instead.
	if !strings.HasPrefix(string(gatewayEnv), "HUB_COMMUNICATION_AUTH=") || !strings.Contains(string(gatewayEnv), "TELEGRAM_ALLOWED_USERS=11\nTELEGRAM_BOT_TOKEN=bot\n") {
		t.Fatalf("gateway env boundary broken: %q", gatewayEnv)
	}
	if strings.Contains(string(gatewayEnv), "OPENAI_API_KEY") || strings.Contains(string(gatewayEnv), "JIRA_API_TOKEN") {
		t.Fatalf("provider secret leaked into gateway env: %q", gatewayEnv)
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
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
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
	if _, ok := gateway["environment"].(M)["HUB_COMMUNICATION_CONFIG"]; ok {
		t.Fatal("missing users file was mounted")
	}
}

func TestComposeMountsCommunicationUsersWhenPresent(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://host.docker.internal:8765")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "communication.users.yaml"), []byte("organization_id: personal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	gateway := Compose(s, dir, dir)["services"].(M)["communication-hub"].(M)
	if gateway["environment"].(M)["HUB_COMMUNICATION_CONFIG"] != "/config/communication.users.yaml" {
		t.Fatalf("users config missing: %#v", gateway["environment"])
	}
	mounted := false
	for _, raw := range gateway["volumes"].([]any) {
		volume := raw.(M)
		if volume["target"] == "/config/communication.users.yaml" && volume["read_only"] == true && volume["type"] == "bind" {
			mounted = true
		}
	}
	if !mounted {
		t.Fatalf("users file not mounted: %#v", gateway["volumes"])
	}
}

// saveSpace keeps the historical test helper name; the canonical pair
// writer lives next to readSpace.
func saveSpace(dir string, settings Settings) error { return WriteSpace(dir, settings) }

func TestTelegramGatewayAndPersonalMCPAreIndependent(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	s.Tools, s.Ingress = testTools("telegram"), testIngress("telegram")
	config := Config(s)
	if _, ok := config["mcp_servers"].(M)["telegram_user"]; ok {
		t.Fatal("bot gateway enabled personal Telegram MCP")
	}
	services := Compose(s, "/source", "/space")["services"].(M)
	if _, ok := services["communication-hub"]; !ok {
		t.Fatal("bot gateway service missing")
	}
	s.Tools, s.Ingress = testTools("telegram_user"), testIngress("telegram_user")
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
	s := Settings{Tools: testTools("telegram", "gitlab", "atlassian"), Ingress: testIngress("telegram", "gitlab", "atlassian"), MCP: map[string]MCPServer{"custom": {URL: "https://example.invalid/mcp", Headers: map[string]string{"Authorization": "Bearer ${CUSTOM_TOKEN}"}}}}
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
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("workspace", "slack_app"), Ingress: testIngress("workspace", "slack_app"), Memory: true}
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

func TestComposeKeepsDockerInControlImage(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	services := Compose(s, "/source", "/space")["services"].(M)
	if !s.DiagnosticsEnabled() || services["toolhub"].(M)["environment"].(M)["HUB_DIAGNOSTICS_DIR"] != "/diagnostics" || services["communication-hub"].(M)["environment"].(M)["HUB_DIAGNOSTICS_ENABLED"] != "true" {
		t.Fatal("default diagnostics not wired")
	}
	core := services["cliproxy"].(M)
	control := services["toolhub"].(M)
	for name, service := range services {
		logging := service.(M)["logging"].(M)
		if logging["driver"] != "local" || logging["options"].(M)["max-size"] != "10m" || logging["options"].(M)["max-file"] != "3" {
			t.Fatalf("unbounded Docker logs for %s: %#v", name, logging)
		}
	}
	if core["image"] == control["image"] || core["build"].(M)["target"] != "prod" || control["build"].(M)["target"] != "prod-control" {
		t.Fatalf("core/control images not separated: core=%v control=%v", core["image"], control["image"])
	}
	if services["workload-controller"].(M)["image"] != control["image"] || services["hermes-runtime"].(M)["image"] != core["image"] {
		t.Fatal("Docker consumers and Hermes runtime use the wrong images")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	dockerfile, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "docker", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	coreStage, controlStage, found := strings.Cut(string(dockerfile), "FROM runtime AS control")
	if !found || strings.Contains(coreStage, "COPY --from=dockercli") || strings.Contains(coreStage, "git init /slack") || !strings.Contains(controlStage, "COPY --from=dockercli") || !strings.Contains(coreStage, "--mount=type=cache,id=hermes-go-build") || !strings.Contains(coreStage, "FROM golang:1.27.1-bookworm AS cliproxy_build") {
		t.Fatal("Docker CLI or Slack MCP included in default runtime")
	}
}

func TestDiagnosticsCanBeDisabled(t *testing.T) {
	off := false
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram"), Diagnostics: &off}
	services := Compose(s, "/source", "/space")["services"].(M)
	if s.DiagnosticsEnabled() || services["toolhub"].(M)["environment"].(M)["HUB_DIAGNOSTICS_DIR"] != nil || services["communication-hub"].(M)["environment"].(M)["HUB_DIAGNOSTICS_ENABLED"] != "false" {
		t.Fatal("diagnostics: false ignored")
	}
}

func TestSlackEventsPortCustomDevAndCollision(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, SlackEventsPort: 9091, Tools: testTools("slack_app"), Ingress: testIngress("slack_app")}
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
	if err := saveSpace(dir, Settings{Schema: 3, User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("workspace", "slack_app"), Ingress: testIngress("workspace", "slack_app"), Memory: true}); err != nil {
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
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram", "transcription"), Ingress: testIngress("telegram", "transcription")}
	services := Compose(s, "/source", "/space")["services"].(M)
	gw := services["communication-hub"].(M)
	env := gw["environment"].(M)
	if env["HUB_STT_URL"] != "http://hub-stt:8090" || env["HUB_TTS_URL"] != "http://hub-tts:8090" {
		t.Fatalf("gateway media urls: %v %v", env["HUB_STT_URL"], env["HUB_TTS_URL"])
	}
	if env["HUB_STT_COMMAND"] != "/usr/local/bin/hub-stt" {
		t.Fatalf("HUB_STT_COMMAND=%v", env["HUB_STT_COMMAND"])
	}
	if env["HUB_TTS_COMMAND"] != "/usr/local/bin/hub-tts" {
		t.Fatalf("HUB_TTS_COMMAND=%v", env["HUB_TTS_COMMAND"])
	}
	// Two dedicated services and images — not one binary with a role flag.
	for role, dockerfile := range map[string]string{"hub-stt": "docker/Dockerfile.stt", "hub-tts": "docker/Dockerfile.tts"} {
		svc, ok := services[role].(M)
		if !ok {
			t.Fatalf("missing service %s", role)
		}
		if svc["build"].(M)["dockerfile"] != dockerfile {
			t.Fatalf("%s dockerfile=%v", role, svc["build"])
		}
		se := svc["environment"].(M)
		if _, present := se["HUB_MEDIA_ROLE"]; present {
			t.Fatalf("%s still carries a role flag", role)
		}
		if se["HUB_MEDIA_COMMAND"] != "/usr/local/bin/"+role[4:]+"-worker" {
			t.Fatalf("%s worker command=%v", role, se["HUB_MEDIA_COMMAND"])
		}
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
	settings, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	if err := saveSpace(d, settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	runtimeEnv, err := os.ReadFile(filepath.Join(d, "runtime.prod.env"))
	if err != nil || !strings.Contains(string(runtimeEnv), "HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp") {
		t.Fatalf("default ToolHub endpoint missing: %q %v", runtimeEnv, err)
	}
	if soul, _ := os.ReadFile(filepath.Join(d, "SOUL.md")); string(soul) != "template soul" {
		t.Fatalf("SOUL stub not healed to template: %q", soul)
	}
	secrets := "OPENAI_API_KEY=model\nHUB_TOOLHUB_ENDPOINT=http://toolhub:9000/mcp\n"
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte(secrets), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	runtimeEnv, _ = os.ReadFile(filepath.Join(d, "runtime.prod.env"))
	if !strings.Contains(string(runtimeEnv), "HUB_TOOLHUB_ENDPOINT=http://toolhub:9000/mcp") {
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

func TestSecondarySpaceRendersNoSharedInfra(t *testing.T) {
	off := false
	s := Settings{Schema: 1, Environment: "prod", User: "bob", Model: "m", ModelURL: "http://cliproxy:8317/v1", Timezone: "UTC", Tools: testTools("telegram"), Ingress: testIngress("telegram"), BrowserPort: 6080, OAuthPort: 8000, Infra: &off}
	rendered := Compose(s, "/source", "/space")
	services := rendered["services"].(M)
	if _, ok := services["hermes-runtime"]; ok {
		t.Fatal("secondary space kept a resident runtime: spawned runtimes must be the only per-user containers")
	}
	for _, name := range []string{"toolhub", "credential-broker", "workload-controller", "cliproxy", "communication-hub"} {
		if _, ok := services[name]; ok {
			t.Fatalf("secondary space rendered shared service %q: port collisions return", name)
		}
	}
	shared := rendered["networks"].(M)["hermes-hub-runtime"].(M)
	if shared["external"] != true {
		t.Fatalf("secondary space must not own the shared network: %v", shared)
	}
	volumes := rendered["volumes"].(M)
	if _, ok := volumes["broker-secrets-runtime"]; !ok {
		t.Fatal("secondary space lost its runtime broker key volume")
	}
	if _, ok := volumes["broker-state"]; ok {
		t.Fatal("secondary space claimed the shared broker state volume")
	}
}

func TestInfraRenderOwnsSharedNetwork(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Model: "m", ModelURL: "http://cliproxy:8317/v1", Timezone: "UTC", Tools: testTools("telegram"), Ingress: testIngress("telegram"), BrowserPort: 6080, OAuthPort: 8000}
	rendered := Compose(s, "/source", "/space")
	services := rendered["services"].(M)
	for _, name := range []string{"toolhub", "credential-broker", "cliproxy", "communication-hub"} {
		service, ok := services[name].(M)
		if !ok {
			t.Fatalf("infra service %q missing", name)
		}
		nets, ok := service["networks"].([]string)
		if !ok || !slices.Contains(nets, "hermes-hub-runtime") {
			t.Fatalf("infra service %q not reachable on the shared network: %v", name, service["networks"])
		}
	}
	shared := rendered["networks"].(M)["hermes-hub-runtime"].(M)
	if shared["name"] != "hermes-hub-runtime" || shared["external"] != true {
		t.Fatalf("shared network must be external operator-owned with stable name: %v", shared)
	}
}

// The broker store is a singleton per environment: a dev render must never
// silently attach the prod credential volume. Only the explicit
// HUB_BROKER_STATE_VOLUME override may move it (the pre-split migration path).
func TestBrokerStateVolumeIsPerEnvironment(t *testing.T) {
	for _, env := range []string{"dev", "prod"} {
		s := Settings{Schema: 1, Environment: env, User: "alice", Model: "m", ModelURL: "http://cliproxy:8317/v1", Timezone: "UTC", Tools: testTools("telegram"), Ingress: testIngress("telegram"), BrowserPort: 6080, OAuthPort: 8000}
		brokerState := Compose(s, "/source", "/space")["volumes"].(M)["broker-state"].(M)
		if brokerState["external"] != true || brokerState["name"] != "hermes-credential-broker-"+env {
			t.Fatalf("%s render attached %v instead of its own environment volume", env, brokerState)
		}
	}
}

func TestBrokerStateVolumeOverride(t *testing.T) {
	t.Setenv("HUB_BROKER_STATE_VOLUME", "hermes-credential-broker-real-prod-20260918")
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Model: "m", ModelURL: "http://cliproxy:8317/v1", Timezone: "UTC", Tools: testTools("telegram"), Ingress: testIngress("telegram"), BrowserPort: 6080, OAuthPort: 8000}
	brokerState := Compose(s, "/source", "/space")["volumes"].(M)["broker-state"].(M)
	if brokerState["name"] != "hermes-credential-broker-real-prod-20260918" {
		t.Fatalf("legacy migration override lost: %v", brokerState)
	}
}

func TestRenderMountsImmutableEffectiveConfig(t *testing.T) {
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
	settings, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	if err := saveSpace(d, settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	effective, err := os.ReadFile(filepath.Join(d, "generated", "hermes-effective.prod.yaml"))
	if err != nil {
		t.Fatal("effective config not materialized:", err)
	}
	if !strings.Contains(string(effective), "http://toolhub:8090/mcp") {
		t.Fatalf("materialized config missing toolhub server: %q", effective)
	}
	compose, err := os.ReadFile(filepath.Join(d, "generated", "compose.prod.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "hermes-effective.prod.yaml") || !strings.Contains(string(compose), "/state/hermes/config.yaml") {
		t.Fatalf("immutable config mount missing from compose: %q", compose)
	}
}

func TestBrowserCapabilityReadOnlyByDefault(t *testing.T) {
	for _, features := range [][]string{{"browser"}, {"browser", "browser_act"}, {"browser_act"}, {"meet"}} {
		s := Settings{Tools: testTools(features...), Ingress: testIngress(features...)}
		servers := Config(s)["mcp_servers"].(M)
		persistent, hasPersistent := servers["browser"].(M)
		guest, hasGuest := servers["browser_guest"].(M)
		switch {
		case s.Has("browser"):
			if !hasPersistent || !hasGuest {
				t.Fatalf("browser feature must render both profiles: %v", servers)
			}
			for name, server := range map[string]M{"browser": persistent, "browser_guest": guest} {
				include := server["tools"].(MCPTools).Include
				for _, mutation := range browserMutationTools {
					if slices.Contains(include, mutation) && !s.Has("browser_act") {
						t.Fatalf("%s leaks mutation %s without browser_act", name, mutation)
					}
					if !slices.Contains(include, mutation) && s.Has("browser_act") {
						t.Fatalf("browser_act did not enable %s on %s", mutation, name)
					}
				}
				for _, read := range browserReadTools {
					if !slices.Contains(include, read) {
						t.Fatalf("%s missing read tool %s", name, read)
					}
				}
			}
		default:
			if hasPersistent || hasGuest {
				t.Fatalf("features %v must not render browser servers", features)
			}
		}
	}
}

func TestBrowserGuestReservedAndMountsPerUser(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Join(d, "workspace.yaml")
	body, _ := os.ReadFile(workspacePath)
	body = append(body, []byte("mcp:\n    browser_guest:\n        url: https://evil.invalid/mcp\n")...)
	if err := os.WriteFile(workspacePath, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(d); err == nil {
		t.Fatal("user MCP claimed the reserved browser_guest name")
	}
}
