package stack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestInitNeverOverwrites(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "artem"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "artem", "r-0000000000000001")
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
	s, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	// Under the published-revision model the only missing piece is the
	// provider key: model/route come from the models doc.
	if issues := Doctor(s, secrets); len(issues) != 1 || !strings.Contains(issues[0], "OPENAI_API_KEY") {
		t.Fatalf("missing model credentials not reported: %v", issues)
	}
}
func TestValidation(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	for _, modify := range []func(*Settings){func(s *Settings) { s.User = "../other" }, func(s *Settings) {
		s.Tools = map[string]ToolEntry{"telegram_user": {Via: "off", Tools: map[string]bool{"write": true}}}
	}, func(s *Settings) {
		s.Tools = map[string]ToolEntry{"google": {Via: "mcp", Tools: map[string]bool{"bogus": true}}}
	}, func(s *Settings) { s.Ingress = []string{"bogus"} }, func(s *Settings) { s.ModelURL = "http://" + "user:secret" + "@host/v1" }, func(s *Settings) { s.Timezone = "invalid/zone" }, func(s *Settings) {
		s.Tools = testTools("browser")
		s.BrowserPort = 8000
	}} {
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
func TestExposureFeatureRegistered(t *testing.T) {
	found := false
	for _, f := range Features {
		if f.Name == "exposure" {
			found = true
		}
	}
	if !found {
		t.Fatal("exposure feature flag missing")
	}
}

func TestRenderAllFeatures(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "config"), 0700)
	_ = os.WriteFile(filepath.Join(root, "config/SOUL.md"), []byte("original"), 0600)
	gated := filepath.Join(root, "config", "skills", "deep-research-embedded")
	_ = os.MkdirAll(gated, 0755)
	_ = os.WriteFile(filepath.Join(gated, "SKILL.md"), []byte("---\nname: deep-research-embedded\ndescription: gated fixture\n---\n"), 0600)
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "me", "r-0000000000000001")
	writeSSHConfig(t, d)      // the all-features render includes ssh; it fails closed without a config
	writeExposureConfig(t, d) // same contract for the exposure family
	s, _ := ReadEffective(context.Background(), testReader(d), d, "prod")
	s.Model = "test"
	s.ModelURL = "http://host.docker.internal:8317/v1"
	s.GoogleEmail = "me@example.org"
	s.MemoryProvider = "hindsight"
	s.MemoryProviderURL = "http://hindsight.invalid:8888"
	// Under the passport model the vault flag is config.memory.enabled —
	// memory_vault/knowledge_ui/memory_provider wishes gate on it.
	s.Memory = true
	var featureNames []string
	for _, f := range Features {
		if f.Name == "meet" {
			// meet needs plugin support sealed under managed mode — it is
			// denied by construction, not merely unselected.
			continue
		}
		featureNames = append(featureNames, f.Name)
	}
	s.Tools, s.Ingress = testTools(featureNames...), testIngress(featureNames...)
	s.Hosting = HostingSettings{Slots: []string{"site"}}
	exposurePath := filepath.Join(d, "connections", "exposure", "config.yaml")
	exposureBody, exposureErr := os.ReadFile(exposurePath)
	if exposureErr != nil {
		t.Fatal(exposureErr)
	}
	if err := os.WriteFile(exposurePath, []byte(strings.ReplaceAll(string(exposureBody), "zone: alice", "zone: me")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveSpace(t, d, s); err != nil {
		t.Fatal(err)
	}
	// All-features render: grant every section the fixture selects —
	// native Hermes toolsets are default-deny (SPEC-0083 R3.1).
	seedGovernance(t, d, func(doc *api.Governance) {
		granted := map[string]bool{}
		for name, entry := range s.Tools {
			if section := s.sectionForEntry(name, entry); section != "" && !granted[section] {
				granted[section] = true
				activeToolGrant(t, doc, "me", section, time.Hour)
			}
		}
		// Implicit hub-owned emissions carry their own sections.
		for _, section := range []string{api.SectionHubTools, api.SectionHubBrowser, "hub:browser_guest"} {
			if !granted[section] {
				activeToolGrant(t, doc, "me", section, time.Hour)
			}
		}
	})
	if err := Render(context.Background(), testReader(d), d, root); err != nil {
		t.Fatal(err)
	}
	generatedConfig, err := os.ReadFile(filepath.Join(d, "generated", "hermes.prod.yaml"))
	if err != nil || strings.Contains(string(generatedConfig), "/opt/hub/skills") {
		t.Fatalf("legacy skills loader path still rendered: %v %s", err, generatedConfig)
	}
	// Under managed mode the runtime is supervisor-spawned: its mounts live
	// in the RuntimeService spawn contract, not the compose file.
	spawned, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range RuntimeService(spawned, root, d)["volumes"].([]any) {
		mount, _ := v.(M)
		if mount["source"] == filepath.ToSlash(filepath.Join(d, "generated", "skills-effective")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("effective skills view mount missing from RuntimeService: %v", RuntimeService(spawned, root, d)["volumes"])
	}
	if _, err := os.Stat(filepath.Join(d, "generated", "skills-effective", "deep-research-embedded", "SKILL.md")); err != nil {
		t.Fatalf("bundled deep-research skill not materialized into the view: %v", err)
	}
	cfg := Config(s)
	display, ok := cfg["display"].(M)
	if !ok || display["busy_input_mode"] != "queue" || display["long_running_notifications"] != true {
		t.Fatalf("long-running Telegram defaults missing: %#v", cfg["display"])
	}
	servers := cfg["mcp_servers"].(M)
	// Under managed mode connector features never embed direct upstream MCP
	// servers — ToolHub projects them through its own governed endpoint.
	for _, name := range []string{"google", "github", "slack", "atlassian", "telegram_user", "desktop", "drafts"} {
		if _, ok := servers[name]; ok {
			t.Fatalf("direct MCP server leaked into Hermes config: %s", name)
		}
	}
	// Browser and timeout policy must survive on the published managed path.
	timeouts := cfg["timeouts"].(M)["tools"].(M)
	if timeouts["sequential_call"] != 1800 || timeouts["concurrent_batch"] != 1800 {
		t.Fatalf("long-running tool timeouts missing: %#v", timeouts)
	}
	for _, name := range []string{"browser", "browser_guest"} {
		if _, exposed := servers[name]; exposed {
			t.Fatalf("managed browser sidecar exposed directly to the model: %s", name)
		}
	}
	static := s
	// The runtime env contract is shared between compose and the supervised
	// spawn spec; RuntimeService carries it verbatim.
	spawned.Tools, spawned.Ingress = testTools("gitlab"), testIngress("gitlab")
	spawned.GitLabHost = "gitlab.example.com"
	composeEnv := RuntimeService(spawned, root, d)["environment"].(M)
	if composeEnv["HERMES_AGENT_NOTIFY_INTERVAL"] != "60" {
		t.Fatalf("heartbeat interval missing: %#v", composeEnv["HERMES_AGENT_NOTIFY_INTERVAL"])
	}
	if composeEnv["GITLAB_HOST"] != "gitlab.example.com" {
		t.Fatal("GitLab host missing")
	}
	// The static runtime generation is the non-supervised contract: a
	// supervised runtime gets its dynamic generation from the supervisor.
	static.ExecutionMode = ""
	staticEnv := RuntimeService(static, root, d)["environment"].(M)
	if staticEnv["HUB_RUNTIME_GENERATION"] != "static-me-prod" {
		t.Fatalf("static runtime generation missing: %#v", staticEnv["HUB_RUNTIME_GENERATION"])
	}
	static.Tools, static.Ingress = testTools("atlassian"), testIngress("atlassian")
	if _, ok := Config(static)["mcp_servers"].(M)["atlassian"]; ok {
		t.Fatal("direct Atlassian MCP config leaked")
	}
	soul := filepath.Join(d, "SOUL.md")
	_ = os.WriteFile(soul, []byte("owner changes"), 0600)
	if err := Render(context.Background(), testReader(d), d, root); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(soul)
	if string(got) != "owner changes" {
		t.Fatal("overwrote memory")
	}
	compose, _ := os.ReadFile(filepath.Join(d, "generated", "compose.prod.yaml"))
	if !strings.Contains(string(compose), "127.0.0.1:6081:6080") {
		t.Fatal("network boundary")
	}
	// docker.sock is restricted to toolhub/workload-controller; the per-service
	// boundary is asserted in TestRenderedServiceBoundaries. The effective
	// SOUL bind is a runtime mount — under managed supervision it lives in
	// the spawn contract.
	soulRO := false
	for _, v := range RuntimeService(spawned, root, d)["volumes"].([]any) {
		mount, _ := v.(M)
		if mount["target"] == "/state/hermes/SOUL.md" && mount["read_only"] == true {
			soulRO = true
		}
	}
	if !soulRO {
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
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	settings, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Tools, settings.Ingress = testTools("workspace", "telegram", "atlassian"), testIngress("workspace", "telegram", "atlassian")
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	secrets := "OPENAI_API_KEY=model\nTELEGRAM_BOT_TOKEN=bot\nTELEGRAM_ALLOWED_USERS=11\nJIRA_API_TOKEN=provider\n"
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte(secrets), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	automationRoot := filepath.Join(d, "automation", "prod")
	if info, err := os.Stat(automationRoot); err != nil || !info.IsDir() {
		t.Fatalf("prod automation root missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d, "automation", "dev")); !os.IsNotExist(err) {
		t.Fatalf("prod render created dev automation state: %v", err)
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
	// A managed per-space compose carries only this space's per-space
	// services: the supervised runtime is supervisor-spawned and the shared
	// control plane renders in the infra space's compose, never here.
	services := Compose(settings, root, d)["services"].(M)
	for _, name := range []string{"model-relay", "toolhub-relay"} {
		if _, ok := services[name]; !ok {
			t.Fatalf("per-space service %s missing: %#v", name, services)
		}
	}
	for _, name := range []string{"hermes-runtime", "communication-hub", "cliproxy", "toolhub", "workload-controller", "credential-broker"} {
		if _, ok := services[name]; ok {
			t.Fatalf("shared/supervised service %s leaked into the per-space compose", name)
		}
	}
}

// An unmanaged owner has no rendered model-relay, so the night rollup's
// model seam (SPEC-0085 R5.1) opts in through secrets: the relay URL and
// caller bearer reach the gateway env file, and — unlike gateway-owned
// secrets — the same keys still reach the runtime env unchanged.
func TestRenderRoutesRelayEnvPairToGateway(t *testing.T) {
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
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	settings, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Tools, settings.Ingress = testTools("workspace", "telegram"), testIngress("workspace", "telegram")
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	secrets := "TELEGRAM_BOT_TOKEN=bot\nTELEGRAM_ALLOWED_USERS=11\nHUB_MODEL_RELAY_URL=http://relay.invalid:8318\nHUB_MODEL_RELAY_AUTH=caller-bearer\nHUB_MODEL_RELAY_ADMIN=admin-bearer\n"
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte(secrets), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	gatewayEnv, err := os.ReadFile(filepath.Join(d, "communication.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gatewayEnv), "HUB_MODEL_RELAY_URL=http://relay.invalid:8318") || !strings.Contains(string(gatewayEnv), "HUB_MODEL_RELAY_AUTH=caller-bearer") || !strings.Contains(string(gatewayEnv), "HUB_MODEL_RELAY_ADMIN=admin-bearer") {
		t.Fatalf("relay env pair not routed to the gateway: %q", gatewayEnv)
	}
	runtimeEnv, err := os.ReadFile(filepath.Join(d, "runtime.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runtimeEnv), "HUB_MODEL_RELAY_URL=http://relay.invalid:8318") {
		t.Fatalf("relay url stripped from runtime env: %q", runtimeEnv)
	}
}

func TestRenderRoutesCliproxyManagementToModelRelay(t *testing.T) {
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
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	settings, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model-relay:8318/v1"
	settings.CapabilityProfileID = "p"
	settings.CapabilityGeneration = 1
	settings.Tools, settings.Ingress = testTools("workspace", "telegram"), testIngress("workspace", "telegram")
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("alice")
	docs["config"] = "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\nchannels:\n  telegram: {}\n"
	docs["stack"] = "kind: stack\nidentity: {user: alice}\nservices:\n" +
		"  hermes-runtime: {mode: supervised}\n" +
		"  model-relay: {mode: per-space}\n" +
		"  toolhub-relay: {mode: per-space}\n" +
		"  toolhub: {mode: shared}\n" +
		"  communication-hub: {mode: shared}\n" +
		"  credential-broker: {mode: shared}\n"
	publishManagedDocs(t, d, "alice", "r-0123456789abcdef", docs)
	selection := `{"schema":1,"user":"alice","environment":"prod","mode":"supervisor","supervisor_url":"http://localhost:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`
	if err := os.WriteFile(filepath.Join(d, ExecutionPath("prod")), []byte(selection), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := "OPENAI_API_KEY=model\nTELEGRAM_BOT_TOKEN=bot\nTELEGRAM_ALLOWED_USERS=11\nHUB_CLIPROXY_MGMT_URL=http://cliproxy:8317\nHUB_CLIPROXY_MGMT_KEY=mgmt\nHUB_CLIPROXY_AUTH_INDEX=idx\n"
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte(secrets), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	gatewayEnv, err := os.ReadFile(filepath.Join(d, "communication.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(gatewayEnv), "HUB_CLIPROXY") {
		t.Fatalf("cliproxy management key still reaches the gateway: %q", gatewayEnv)
	}
	relayEnv, err := os.ReadFile(filepath.Join(d, "model-relay.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HUB_CLIPROXY_MGMT_URL=http://cliproxy:8317", "HUB_CLIPROXY_MGMT_KEY=mgmt", "HUB_CLIPROXY_AUTH_INDEX=idx"} {
		if !strings.Contains(string(relayEnv), want) {
			t.Fatalf("model-relay env missing %q: %q", want, relayEnv)
		}
	}
	runtimeEnv, err := os.ReadFile(filepath.Join(d, "runtime.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(runtimeEnv), "HUB_CLIPROXY") {
		t.Fatalf("cliproxy management key leaked into agent runtime env: %q", runtimeEnv)
	}
	relay, ok := Compose(settings, root, d)["services"].(M)["model-relay"].(M)
	if !ok {
		t.Fatal("model-relay service missing")
	}
	found := false
	for _, file := range relay["env_file"].([]any) {
		if strings.Contains(file.(M)["path"].(string), "model-relay.prod.env") {
			found = true
		}
	}
	if !found {
		t.Fatalf("model-relay env file not mounted: %v", relay["env_file"])
	}
}

func TestDisabledComponentsStayCold(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("workspace", "telegram"), Ingress: testIngress("workspace", "telegram")}
	runtime := RuntimeService(s, "/source", "/space")
	for _, port := range runtime["ports"].([]string) {
		if strings.Contains(port, ":6080") {
			t.Fatalf("browser port exposed without the feature: %s", port)
		}
	}
	env := runtime["environment"].(M)
	if env["HUB_BROWSER"] != "false" || env["HUB_MEET"] != "false" {
		t.Fatalf("browser env claims a disabled feature: %#v", env)
	}
	servers := Config(s)["mcp_servers"].(M)
	for _, name := range []string{"browser", "browser_guest"} {
		if _, ok := servers[name]; ok {
			t.Fatalf("browser MCP rendered without the feature: %s", name)
		}
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
	// The supervised gateway must not receive the per-runtime auth file —
	// toolhub.auth is the controller-admission token, not a runtime auth.
	for _, raw := range gateway["env_file"].([]any) {
		if strings.HasSuffix(raw.(M)["path"].(string), "runtime.auth") {
			t.Fatalf("supervisor gateway received per-runtime auth file: %#v", raw)
		}
	}
	if gateway["environment"].(M)["HUB_SUPERVISOR_AUTH"] != "${HUB_SUPERVISOR_AUTH:?set HUB_SUPERVISOR_AUTH or provision supervisor.auth for this stack}" {
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

// Each space's generated effective view mounts under /skills-effective-by/<space>
// so the shared gateway resolves /name and $name per calling principal; spaces
// without a rendered view are skipped rather than bind-created on the host.
func TestComposeMountsPerPrincipalSkillViews(t *testing.T) {
	spacesRoot := filepath.Join(t.TempDir(), "spaces")
	dir := filepath.Join(spacesRoot, "alice")
	for _, space := range []string{"alice", "bob"} {
		view := filepath.Join(spacesRoot, space, "generated", "skills-effective")
		if err := os.MkdirAll(view, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(spacesRoot, "carol"), 0755); err != nil {
		t.Fatal(err) // no generated view — must not be mounted
	}
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	gateway := Compose(s, dir, dir)["services"].(M)["communication-hub"].(M)
	if gateway["environment"].(M)["HUB_SKILLS_BY_PRINCIPAL"] != "/skills-effective-by" {
		t.Fatalf("by-principal env missing: %#v", gateway["environment"])
	}
	mounted := map[string]bool{}
	for _, raw := range gateway["volumes"].([]any) {
		volume := raw.(M)
		if target, ok := volume["target"].(string); ok && strings.HasPrefix(target, "/skills-effective-by/") {
			mounted[strings.TrimPrefix(target, "/skills-effective-by/")] = true
			if volume["read_only"] != true {
				t.Fatalf("skill views must mount read-only: %#v", volume)
			}
		}
	}
	for _, want := range []string{"alice", "bob"} {
		if !mounted[want] {
			t.Fatalf("view for %q not mounted: %#v", want, gateway["volumes"])
		}
	}
	if mounted["carol"] {
		t.Fatalf("space without a generated view must not be mounted: %#v", gateway["volumes"])
	}
	// Outside a spaces/ root no sibling discovery happens — the dir layout is
	// the contract, not "whatever sits next to the space dir".
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	stray := filepath.Join(elsewhere, "alice")
	if err := os.MkdirAll(filepath.Join(elsewhere, "bob", "generated", "skills-effective"), 0755); err != nil {
		t.Fatal(err)
	}
	strayGateway := Compose(s, stray, stray)["services"].(M)["communication-hub"].(M)
	if _, ok := strayGateway["environment"].(M)["HUB_SKILLS_BY_PRINCIPAL"]; ok {
		t.Fatal("by-principal env must not be set outside a spaces/ root")
	}
}

// platform_usage runs inside the shared toolhub: it resolves each caller's
// relay meter + models.yaml under /model-state-by/<space> and
// /model-config-by/<space>, mounted read-only per sibling so a caller only
// ever reads its own space's counters and limits.
func TestComposeMountsPerPrincipalModelState(t *testing.T) {
	spacesRoot := filepath.Join(t.TempDir(), "spaces")
	dir := filepath.Join(spacesRoot, "alice")
	for _, space := range []string{"alice", "bob"} {
		if err := os.MkdirAll(filepath.Join(spacesRoot, space, "generated", "model-registry", "dev"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(spacesRoot, space, "managed", "dev", "model-relay"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(spacesRoot, "carol"), 0755); err != nil {
		t.Fatal(err) // no model dirs — must not be mounted
	}
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	toolhub := Compose(s, dir, dir)["services"].(M)["toolhub"].(M)
	env := toolhub["environment"].(M)
	if env["HUB_MODEL_USAGE_BY"] != "/model-state-by" || env["HUB_MODEL_REGISTRY_BY"] != "/model-config-by" {
		t.Fatalf("model by-principal env missing: %#v", env)
	}
	state, config := map[string]bool{}, map[string]bool{}
	for _, raw := range toolhub["volumes"].([]any) {
		volume := raw.(M)
		target, _ := volume["target"].(string)
		if name, ok := strings.CutPrefix(target, "/model-state-by/"); ok {
			state[name] = true
		}
		if name, ok := strings.CutPrefix(target, "/model-config-by/"); ok {
			config[name] = true
		}
		if strings.HasPrefix(target, "/model-state-by/") || strings.HasPrefix(target, "/model-config-by/") {
			if volume["read_only"] != true {
				t.Fatalf("model state/config must mount read-only: %#v", volume)
			}
		}
	}
	for _, want := range []string{"alice", "bob"} {
		if !state[want] || !config[want] {
			t.Fatalf("model state/config for %q not mounted: %#v", want, toolhub["volumes"])
		}
	}
	if state["carol"] || config["carol"] {
		t.Fatalf("space without model dirs must not be mounted: %#v", toolhub["volumes"])
	}
}

// /v1/skill-git writes drafts + .sources mirrors into the CALLING principal's
// store, so the shared gateway mounts each space's skills/ writable under
// /skills-store-by/<space>; spaces without a store are skipped.
func TestComposeMountsPerPrincipalSkillStores(t *testing.T) {
	spacesRoot := filepath.Join(t.TempDir(), "spaces")
	dir := filepath.Join(spacesRoot, "alice")
	for _, space := range []string{"alice", "bob"} {
		if err := os.MkdirAll(filepath.Join(spacesRoot, space, "skills"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(spacesRoot, "carol"), 0755); err != nil {
		t.Fatal(err) // no skills store — must not be mounted
	}
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	gateway := Compose(s, dir, dir)["services"].(M)["communication-hub"].(M)
	if gateway["environment"].(M)["HUB_SKILLS_STORE_BY"] != "/skills-store-by" {
		t.Fatalf("store-by env missing: %#v", gateway["environment"])
	}
	mounted := map[string]bool{}
	for _, raw := range gateway["volumes"].([]any) {
		volume := raw.(M)
		if target, ok := volume["target"].(string); ok && strings.HasPrefix(target, "/skills-store-by/") {
			mounted[strings.TrimPrefix(target, "/skills-store-by/")] = true
			if volume["read_only"] == true {
				t.Fatalf("skill stores must mount writable — drafts and mirrors are created here: %#v", volume)
			}
		}
	}
	for _, want := range []string{"alice", "bob"} {
		if !mounted[want] {
			t.Fatalf("store for %q not mounted: %#v", want, gateway["volumes"])
		}
	}
	if mounted["carol"] {
		t.Fatalf("space without a skills store must not be mounted: %#v", gateway["volumes"])
	}
}

// saveSpace keeps the historical test helper name: mutating a space's
// configuration now means publishing a new revision, the same way space
// apply does — the helper renders the Settings into the six docs.
func saveSpace(t *testing.T, dir string, settings Settings) error {
	t.Helper()
	publishManagedDocs(t, dir, settings.User, fmt.Sprintf("r-%013x", time.Now().UnixNano()), settingsToDocs(settings))
	return nil
}

func TestComposeMountsModelRelayAdminsWhenPresent(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://host.docker.internal:8765")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model-relay-admins.json"), []byte(`{"alice":{"url":"http://model-relay-alice:8318","admin":"x"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := Settings{Schema: 1, Environment: "dev", User: "infra", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	gateway := Compose(s, dir, dir)["services"].(M)["communication-hub"].(M)
	if gateway["environment"].(M)["HUB_MODEL_RELAY_ADMINS_FILE"] != "/config/model-relay-admins.json" {
		t.Fatalf("admins file env missing: %#v", gateway["environment"])
	}
	mounted := false
	for _, raw := range gateway["volumes"].([]any) {
		volume := raw.(M)
		if volume["target"] == "/config/model-relay-admins.json" && volume["read_only"] == true && volume["type"] == "bind" {
			mounted = true
		}
	}
	if !mounted {
		t.Fatalf("admins file not mounted: %#v", gateway["volumes"])
	}
	toolhub := Compose(s, dir, dir)["services"].(M)["toolhub"].(M)
	if toolhub["environment"].(M)["HUB_MODEL_RELAY_ADMINS_FILE"] != "/config/model-relay-admins.json" || toolhub["environment"].(M)["HUB_MODEL_RELAY_URL"] != "http://model-relay-infra:8318" {
		t.Fatal("ToolHub model relay routes missing")
	}
	adminSource := false
	for _, raw := range toolhub["env_file"].([]any) {
		if strings.HasSuffix(raw.(M)["path"].(string), "/model-relay-admin.auth") {
			adminSource = true
		}
	}
	if !adminSource {
		t.Fatal("ToolHub owner model relay admin credential missing")
	}
}

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
	if err != nil || !ok || server != "" || config != nil {
		t.Fatal("hh must not write a direct hub tool server", server, ok, err)
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
	s := Settings{Model: "synthetic", ModelURL: "http://model-relay:8318/v1", CapabilityProfileID: "me-default", CapabilityGeneration: 1, Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("workspace", "slack_app"), Ingress: testIngress("workspace", "slack_app"), Memory: true}
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
	if err := s.Validate(); err == nil {
		t.Fatal("honcho native extension accepted")
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

func TestTelegramAuthDefaultsOnWithTelegramIngress(t *testing.T) {
	dir := t.TempDir()
	authEnv := filepath.Join(dir, "telegram-auth.dev.env")
	if err := os.WriteFile(authEnv, []byte("HUB_TELEGRAM_AUTH_API_ID=123\nHUB_TELEGRAM_AUTH_API_HASH="+strings.Repeat("a", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := Settings{Model: "synthetic", ModelURL: "http://model-relay:8318/v1", CapabilityProfileID: "me-default", CapabilityGeneration: 1, Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("workspace", "telegram"), Ingress: testIngress("workspace", "telegram")}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if !s.Has("telegram_auth") {
		t.Fatal("telegram_auth must auto-enable with telegram ingress")
	}
	services := Compose(s, "/source", dir)["services"].(M)
	gw := services["communication-hub"].(M)
	// QR login lives in the one communication-hub build — the daemon runs the
	// lean core role image like every other non-agent service (SPEC-0082 R3).
	if gw["image"] != "hermes-hub:0.5.0-dev-core" {
		t.Fatalf("gateway must run the core image: %#v", gw["image"])
	}
	if gw["environment"].(M)["HUB_TELEGRAM_AUTH_ENABLED"] != "true" {
		t.Fatalf("auth not enabled by default: %#v", gw["environment"])
	}
	if services["credential-broker"].(M)["image"] != "hermes-hub:0.5.0-dev-core" {
		t.Fatal("broker must run the core image")
	}
	files := gw["env_file"].([]any)
	if len(files) < 2 || files[1].(M)["path"] != filepath.ToSlash(authEnv) {
		t.Fatal("private Telegram app credentials were not mounted")
	}

	off := false
	s.TelegramAuth = &off
	services = Compose(s, "/source", dir)["services"].(M)
	if s.Has("telegram_auth") {
		t.Fatal("telegram_auth must be off when explicitly false")
	}
	if services["communication-hub"].(M)["environment"].(M)["HUB_TELEGRAM_AUTH_ENABLED"] != "false" {
		t.Fatal("explicit opt-out not propagated")
	}

	on := true
	s.TelegramAuth = &on
	s.Tools = testTools("workspace")
	s.Ingress = testIngress("workspace")
	if s.Validate() == nil {
		t.Fatal("auth without Telegram channel accepted")
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
	// SPEC-0082 R3: daemons run the lean core image, Docker consumers the
	// control image; only hermes-runtime keeps the agent image.
	if core["image"] != "hermes-hub:0.5.0-prod-core" || core["build"].(M)["target"] != "prod-core" {
		t.Fatalf("cliproxy not on the core role image: %#v", core["image"])
	}
	if control["image"] != "hermes-hub:0.5.0-prod-control" || control["build"].(M)["target"] != "prod-control" {
		t.Fatalf("toolhub not on the control role image: %#v", control["image"])
	}
	if services["workload-controller"].(M)["image"] != control["image"] || RuntimeService(s, "/source", "/space")["image"] != "hermes-hub:0.5.0-prod" {
		t.Fatal("Docker consumers and Hermes runtime use the wrong images")
	}
	// R1.5/R4: shm_size exists for the browser sidecar's Chromium workload
	// only; daemons and the runtime do not get shared memory they never touch.
	for name, service := range services {
		_, hasShm := service.(M)["shm_size"]
		if !strings.HasPrefix(name, "browser-") && hasShm {
			t.Fatalf("shm_size leaks onto %s — scope it to the browser sidecar", name)
		}
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	dockerfile, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "docker", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	coreStage, controlStage, found := strings.Cut(string(dockerfile), "FROM core AS control")
	if i := strings.Index(controlStage, "\nFROM "); i >= 0 {
		controlStage = controlStage[:i]
	}
	if !found || strings.Contains(controlStage, "COPY --from=python_build") || strings.Contains(coreStage, "git init /slack") || !strings.Contains(controlStage, "COPY --from=dockercli") || !strings.Contains(coreStage, "--mount=type=cache,id=hermes-go-build") || !strings.Contains(coreStage, "FROM golang:1.27.4-bookworm AS cliproxy_build") {
		t.Fatal("Docker CLI or Python payload leaked across role boundary")
	}
}

func TestNoServiceUsesRuntimeExceptRuntime(t *testing.T) {
	// SPEC-0082 R3.2: the fat agent image is hermes-runtime's alone; every
	// other service resolves to a lean role image or its own dedicated tag.
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	services := Compose(s, "/source", "/space")["services"].(M)
	for name, raw := range services {
		image, _ := raw.(M)["image"].(string)
		switch {
		case name == "hermes-runtime":
			if image != "hermes-hub:0.5.0-dev" {
				t.Fatalf("hermes-runtime lost the agent image: %q", image)
			}
			// The service builds its own tag: supervisor/managed renders drop
			// hermes-runtime, so nothing else can carry this build section.
			if raw.(M)["build"].(M)["target"] != "dev" {
				t.Fatalf("hermes-runtime must build the %q target: %#v", "dev", raw.(M)["build"])
			}
		case name == "hub-stt" || name == "hub-tts":
			if image != "hermes-hub-"+strings.TrimPrefix(name, "hub-")+":0.5.0-dev" {
				t.Fatalf("%s lost its dedicated voice image: %q", name, image)
			}
		case name == "toolhub" || name == "workload-controller" || name == "supervisor":
			if image != "hermes-hub:0.5.0-dev-control" {
				t.Fatalf("%s not on the control image: %q", name, image)
			}
		default:
			if image != "hermes-hub:0.5.0-dev-core" {
				t.Fatalf("%s does not run a lean role image: %q", name, image)
			}
		}
	}
	// Managed mode spawns its own relays; image_gen adds hub-media; a
	// dedicated cliproxy belongs to a secondary space. Same role rules.
	managed := s
	managed.CapabilityProfileID = "profile"
	managed.CapabilityGeneration = 1
	managed.Tools, managed.Ingress = testTools("telegram", "image_gen", "hosting"), testIngress("telegram", "image_gen", "hosting")
	managedServices := Compose(managed, "/source", "/space")["services"].(M)
	for name, raw := range managedServices {
		image, _ := raw.(M)["image"].(string)
		switch {
		case name == "hub-stt" || name == "hub-tts":
			continue
		case name == "toolhub" || name == "workload-controller" || name == "supervisor" || strings.HasPrefix(name, "hosting-"):
			if !strings.HasSuffix(image, "-control") {
				t.Fatalf("%s not on the control image: %q", name, image)
			}
		default:
			if image != "hermes-hub:0.5.0-dev-core" {
				t.Fatalf("%s does not run a lean role image: %q", name, image)
			}
		}
	}
	secondary := Settings{Schema: 1, Environment: "dev", User: "bob", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram"), Cliproxy: "dedicated"}
	for name, raw := range Compose(secondary, "/source", "/space2")["services"].(M) {
		image, _ := raw.(M)["image"].(string)
		if name == "cliproxy-bob" && image != "hermes-hub:0.5.0-dev-core" {
			t.Fatalf("dedicated cliproxy not on the core image: %q", image)
		}
	}
	// SPEC-0082 R3.3: healthchecks run a Go binary the role image carries —
	// the lean images ship no curl, wget or shell-bound interpreters.
	coreBinaries := map[string]bool{"hubctl": true, "hub-runtime": true, "toolhub": true, "communication-hub": true, "credential-broker": true, "hub-media": true, "cellinit": true, "cli-proxy-api": true}
	for name, raw := range services {
		check, ok := raw.(M)["healthcheck"].(M)
		if !ok {
			continue
		}
		test, _ := check["test"].([]string)
		if len(test) < 2 || test[0] != "CMD" || !coreBinaries[test[1]] {
			t.Fatalf("%s healthcheck does not invoke a role-image Go binary: %#v", name, check["test"])
		}
	}
}

func TestSupervisedSpaceWiresAgentExec(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram"), ExecutionMode: "supervisor", SupervisorURL: "http://host.docker.internal:8876"}
	services := Compose(s, "/source", "/space")["services"].(M)
	hub := services["toolhub"].(M)
	env := hub["environment"].(M)
	if env["HUB_AGENT_EXEC_MODE"] != "supervisor" {
		t.Fatal("supervised space lost the agent-tools executor channel")
	}
	aliases := hub["networks"].(M)[sharedNetworkName].(M)["aliases"].([]string)
	if len(aliases) != 2 || aliases[0] != "toolhub-control" || aliases[1] != "toolhub-alice" || services["communication-hub"].(M)["environment"].(M)["HUB_TOOLHUB_URL"] != "http://toolhub-control:8090" {
		t.Fatal("gateway supervisor control URL lacks its matching Docker DNS alias")
	}
	if got := services["communication-hub"].(M)["environment"].(M)["HUB_ENVIRONMENT"]; got != "prod" {
		t.Fatalf("gateway rights requests lost their environment: %v", got)
	}
	s2 := s
	s2.ExecutionMode = ""
	s2.SupervisorURL = ""
	env2 := Compose(s2, "/source", "/space")["services"].(M)["toolhub"].(M)["environment"].(M)
	if env2["HUB_AGENT_EXEC_MODE"] != nil {
		t.Fatal("non-supervised space gained the supervisor executor channel")
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

// The toolhub store lives inside the /state bind, so the `.initialized`
// marker cannot detect whole-volume loss. The installation witness needs an
// independent writable mount — the diagnostics host bind is the only one —
// so the env is rendered exactly when that mount exists.
func TestToolhubInstallationWitnessUsesIndependentMount(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	env := Compose(s, "/source", "/space")["services"].(M)["toolhub"].(M)["environment"].(M)
	if env["HUB_TOOLHUB_IDENTITY_WITNESS"] != "/diagnostics/.toolhub-installation" {
		t.Fatalf("installation witness not bound to an independent mount: %v", env["HUB_TOOLHUB_IDENTITY_WITNESS"])
	}
	off := false
	s.Diagnostics = &off
	env = Compose(s, "/source", "/space")["services"].(M)["toolhub"].(M)["environment"].(M)
	if env["HUB_TOOLHUB_IDENTITY_WITNESS"] != nil {
		t.Fatal("witness configured without an independent writable mount")
	}
}

func TestSlackEventsPortCustomDevAndCollision(t *testing.T) {
	s := Settings{Model: "synthetic", ModelURL: "http://model-relay:8318/v1", CapabilityProfileID: "me-default", CapabilityGeneration: 1, Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, SlackEventsPort: 9091, Tools: testTools("slack_app"), Ingress: testIngress("slack_app")}
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
	publishManagedRevision(t, dir, "alice", "r-0000000000000001")
	if err := saveSpace(t, dir, Settings{Model: "synthetic", ModelURL: "http://model-relay:8318/v1", CapabilityProfileID: "me-default", CapabilityGeneration: 1, Schema: 3, User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("workspace", "slack_app"), Ingress: testIngress("workspace", "slack_app"), Memory: true}); err != nil {
		t.Fatal(err)
	}
	dev, err := ReadEnvironment(context.Background(), testReader(dir), dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if dev.SlackEventsPort != 8082 {
		t.Fatalf("dev slack events port=%d", dev.SlackEventsPort)
	}
}

func TestComposeRendersServiceOnlyVoice(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram", "transcription"), Ingress: testIngress("telegram", "transcription")}
	services := Compose(s, "/source", "/space")["services"].(M)
	gw := services["communication-hub"].(M)
	env := gw["environment"].(M)
	if env["HUB_STT_URL"] != "http://hub-stt:8090" || env["HUB_TTS_URL"] != "http://hub-tts:8090" {
		t.Fatalf("gateway media urls: %v %v", env["HUB_STT_URL"], env["HUB_TTS_URL"])
	}
	// SPEC-0082 R2: no in-image worker fallback survives — the gateway talks to
	// the dedicated services or reports voice unavailable.
	for _, key := range []string{"HUB_STT_COMMAND", "HUB_TTS_COMMAND", "HF_HOME", "XDG_CACHE_HOME"} {
		if _, present := env[key]; present {
			t.Fatalf("gateway still carries %s", key)
		}
	}
	for _, key := range []string{"HUB_TTS_VOICE", "HUB_TTS_VOICE_EN", "HUB_TTS_VOICE_RU", "HUB_TTS_VOICE_KK", "HUB_TTS_VOICE_ES"} {
		if env[key] != "${"+key+":-}" {
			t.Fatalf("gateway does not pass %s through: %v", key, env[key])
		}
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
	if err != nil || strings.Contains(string(df), "docker/hub-stt") || strings.Contains(string(df), "docker/hub-tts") || strings.Contains(string(df), "faster-whisper") {
		t.Fatalf("main image still ships the local voice fallback: %v", err)
	}
	// The command workers themselves must be gone: a lingering script is a
	// dead exec path into a missing binary (SPEC-0082 R2).
	for _, worker := range []string{"hub-stt", "hub-tts"} {
		if _, err := os.Stat(filepath.Join(root, "docker", worker)); err == nil {
			t.Fatalf("dead command worker docker/%s still present", worker)
		}
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
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	settings, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
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
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
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
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	runtimeEnv, _ = os.ReadFile(filepath.Join(d, "runtime.prod.env"))
	if !strings.Contains(string(runtimeEnv), "HUB_TOOLHUB_ENDPOINT=\n") || strings.Contains(string(runtimeEnv), "HUB_TOOLHUB_ENDPOINT=http") {
		t.Fatalf("empty opt-out lost: %q", runtimeEnv)
	}
	if err := os.WriteFile(filepath.Join(d, "SOUL.md"), []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
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
		reachable := false
		switch nets := service["networks"].(type) {
		case []string:
			reachable = slices.Contains(nets, "hermes-hub-runtime")
		case M:
			_, reachable = nets["hermes-hub-runtime"]
		}
		if !reachable {
			t.Fatalf("infra service %q not reachable on the shared network: %v", name, service["networks"])
		}
	}
	shared := rendered["networks"].(M)["hermes-hub-runtime"].(M)
	if shared["name"] != "hermes-hub-runtime" || shared["external"] != true {
		t.Fatalf("shared network must be external operator-owned with stable name: %v", shared)
	}
}

func TestToolHubMountsRuntimeBrokerSigningKeyReadOnly(t *testing.T) {
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Model: "m", ModelURL: "http://cliproxy:8317/v1", Timezone: "UTC", Tools: testTools("telegram"), Ingress: testIngress("telegram"), BrowserPort: 6080, OAuthPort: 8000}
	toolhub := Compose(s, "/source", "/space")["services"].(M)["toolhub"].(M)
	if toolhub["environment"].(M)["HUB_CREDENTIAL_BROKER_RUNTIME_KEY_FILE"] != "/run/broker-runtime-secrets/runtime.private" {
		t.Fatal("ToolHub runtime broker identity points outside its mounted key")
	}
	found := false
	for _, raw := range toolhub["volumes"].([]any) {
		mount := raw.(M)
		if mount["target"] == "/run/broker-runtime-secrets" {
			found = mount["source"] == "broker-secrets-runtime" && mount["read_only"] == true
		}
	}
	if !found {
		t.Fatal("ToolHub lost its read-only runtime broker key volume")
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
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	settings, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	effective, err := os.ReadFile(filepath.Join(d, "generated", "hermes-effective.prod.yaml"))
	if err != nil {
		t.Fatal("effective config not materialized:", err)
	}
	if !strings.Contains(string(effective), managedToolHubProxyEndpoint) {
		t.Fatal("materialized config missing managed ToolHub proxy")
	}
	// The immutable config mount is a runtime mount — under managed
	// supervision it lives in the spawn contract, not compose.
	found := false
	for _, v := range RuntimeService(settings, root, d)["volumes"].([]any) {
		mount, _ := v.(M)
		if mount["read_only"] == true && strings.HasSuffix(fmt.Sprint(mount["source"]), "hermes-effective.prod.yaml") && mount["target"] == "/state/hermes/config.yaml" {
			found = true
		}
	}
	if !found {
		t.Fatalf("immutable config mount missing from RuntimeService: %v", RuntimeService(settings, root, d)["volumes"])
	}
}

func TestBrowserGuestReservedAndMountsPerUser(t *testing.T) {
	d := t.TempDir()
	docs := managedDocs("alice")
	docs["tools"] += "connectors:\n  browser_guest: {url: \"https://evil.invalid/mcp\"}\n"
	publishManagedDocs(t, d, "alice", "r-0123456789abcdef", docs)
	if _, err := ReadEffective(context.Background(), testReader(d), d, "prod"); err == nil {
		t.Fatal("user MCP claimed the reserved browser_guest name")
	}
}

func TestManagedRenderProvisionsSkillsStoreDir(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0000000000000001")
	settings, err := ReadEffective(context.Background(), testReader(dir), dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model-relay:8318/v1"
	settings.CapabilityProfileID = "p"
	settings.CapabilityGeneration = 1
	settings.Tools, settings.Ingress = testTools("workspace"), testIngress("workspace")
	if err := saveSpace(t, dir, settings); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	selection := `{"schema":1,"user":"alice","environment":"prod","mode":"supervisor","supervisor_url":"http://localhost:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`
	if err := os.WriteFile(filepath.Join(dir, ExecutionPath("prod")), []byte(selection), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(dir), dir, root, "prod"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime", "hermes", "home", "cache", "workspace", "skills"} {
		if _, err := os.Stat(filepath.Join(dir, "managed", "prod", name)); err != nil {
			t.Fatalf("managed dir %q missing: %v", name, err)
		}
	}
}

func TestSupervisedRenderNoProxyOmitsHumanBrowserSidecar(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0000000000000001")
	settings, err := ReadEffective(context.Background(), testReader(dir), dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model-relay:8318/v1"
	settings.CapabilityProfileID = "p"
	settings.CapabilityGeneration = 1
	settings.Tools, settings.Ingress = testTools("browser", "media=toolhub"), testIngress("browser")
	if err := saveSpace(t, dir, settings); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("alice")
	docs["stack"] = "kind: stack\nidentity: {user: alice}\nservices:\n" +
		"  hermes-runtime: {mode: supervised}\n" +
		"  model-relay: {mode: per-space}\n" +
		"  toolhub-relay: {mode: per-space}\n" +
		"  browser: {mode: per-space}\n" +
		"  toolhub: {mode: shared}\n" +
		"  credential-broker: {mode: shared}\n"
	docs["tools"] = "kind: tools\ncore:\n  discovery: {via: toolhub}\nextended:\n  browser: {via: \"mcp:playwright\"}\n  routines: {via: toolhub}\n"
	publishManagedDocs(t, dir, "alice", "r-0123456789abcdef", docs)
	selection := `{"schema":1,"user":"alice","environment":"prod","mode":"supervisor","supervisor_url":"http://localhost:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`
	if err := os.WriteFile(filepath.Join(dir, ExecutionPath("prod")), []byte(selection), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(dir), dir, root, "prod"); err != nil {
		t.Fatal(err)
	}
	env, err := ReadSecrets(filepath.Join(dir, "managed-runtime.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	hosts := strings.Split(env["NO_PROXY"], ",")
	if !slices.Contains(hosts, "toolhub") || slices.Contains(hosts, "browser-alice") {
		t.Fatal("supervised NO_PROXY must include ToolHub and exclude the human browser")
	}
}

// R8.3 multi-principal: the skill-credential lease reads the CALLER's
// index+store — toolhub mounts every space's effective view and skill
// store read-only under per-principal roots, like the gateway does.
func TestComposeMountsPerPrincipalSkillStoresForToolhub(t *testing.T) {
	spacesRoot := filepath.Join(t.TempDir(), "spaces")
	dir := filepath.Join(spacesRoot, "alice")
	for _, space := range []string{"alice", "bob"} {
		if err := os.MkdirAll(filepath.Join(spacesRoot, space, "generated", "skills-effective"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(spacesRoot, space, "skills"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	services := Compose(s, dir, dir)["services"].(M)
	toolhub, ok := services["toolhub"].(M)
	if !ok {
		t.Fatalf("toolhub service missing: %#v", services)
	}
	env := toolhub["environment"].(M)
	if env["HUB_SKILLS_BY_PRINCIPAL"] != "/skills-effective-by" || env["HUB_SKILLS_STORE_BY"] != "/skills-store-by" {
		t.Fatalf("per-principal envs missing: %#v", env)
	}
	effMount, storeMount := map[string]bool{}, map[string]bool{}
	for _, raw := range toolhub["volumes"].([]any) {
		volume := raw.(M)
		target, _ := volume["target"].(string)
		if name, ok := strings.CutPrefix(target, "/skills-effective-by/"); ok {
			effMount[name] = true
		}
		if name, ok := strings.CutPrefix(target, "/skills-store-by/"); ok {
			storeMount[name] = true
			if volume["read_only"] != true {
				t.Fatalf("skill stores must mount read-only: %#v", volume)
			}
		}
	}
	for _, want := range []string{"alice", "bob"} {
		if !effMount[want] || !storeMount[want] {
			t.Fatalf("principal %q missing mounts: eff=%v store=%v", want, effMount, storeMount)
		}
	}
}

func TestComposeMountsPublishedPassportsPerPrincipalForToolhub(t *testing.T) {
	spacesRoot := filepath.Join(t.TempDir(), "spaces")
	dir := filepath.Join(spacesRoot, "alice")
	for _, user := range []string{"alice", "bob"} {
		member := filepath.Join(spacesRoot, user)
		docs := managedDocs(user)
		docs["config"] += "memory: {enabled: true}\n"
		publishManagedDocs(t, member, user, "r-0123456789abcdef", docs)
		if err := os.MkdirAll(filepath.Join(spacesRoot, user, "vault"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	carol := filepath.Join(spacesRoot, "carol")
	disabled := managedDocs("carol")
	disabled["config"] += "memory: {enabled: false}\n"
	publishManagedDocs(t, carol, "carol", "r-0123456789abcdef", disabled)
	if err := os.MkdirAll(filepath.Join(carol, "vault"), 0700); err != nil {
		t.Fatal(err)
	}
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	s.siblingMemory = map[string]bool{"alice": true, "bob": true}
	services := Compose(s, dir, dir)["services"].(M)
	toolhub := services["toolhub"].(M)
	if toolhub["environment"].(M)["HUB_VAULTS_BY_PRINCIPAL"] != "/vaults-by" {
		t.Fatal("ToolHub lacks the per-principal vault root")
	}
	vaultMounted := map[string]string{}
	for _, raw := range toolhub["volumes"].([]any) {
		volume := raw.(M)
		if target, ok := volume["target"].(string); ok && strings.HasPrefix(target, "/vaults-by/") {
			if volume["read_only"] == true {
				t.Fatalf("ToolHub vault writer received a read-only mount: %v", volume)
			}
			vaultMounted[target] = volume["source"].(string)
		}
	}
	for _, user := range []string{"alice", "bob"} {
		if got := vaultMounted["/vaults-by/"+user]; got != filepath.ToSlash(filepath.Join(spacesRoot, user, "vault")) {
			t.Fatalf("vault mount for %s = %q", user, got)
		}
	}
	if vaultMounted["/vaults-by/carol"] != "" {
		t.Fatal("disabled memory gained a ToolHub vault mount")
	}
	// M32/#515: vault access is ToolHub-owned — the runtime container itself
	// never mounts any vault path, as source or target.
	runtime := RuntimeService(s, dir, dir)
	for _, raw := range runtime["volumes"].([]any) {
		volume := raw.(M)
		target, _ := volume["target"].(string)
		source, _ := volume["source"].(string)
		if strings.Contains(target, "vault") || strings.Contains(source, "vault") {
			t.Fatalf("runtime must not mount the vault: %#v", volume)
		}
	}
}

func TestComposeMountsAutomationOwnersAndLegacyPreflight(t *testing.T) {
	spacesRoot := filepath.Join(t.TempDir(), "spaces")
	dir := filepath.Join(spacesRoot, "alice")
	for _, user := range []string{"alice", "bob"} {
		if err := os.MkdirAll(filepath.Join(spacesRoot, user, "automation", "dev"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(spacesRoot, "carol", "automation", "prod"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "automation-routing"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	toolhub := Compose(s, dir, dir)["services"].(M)["toolhub"].(M)
	env := toolhub["environment"].(M)
	if env["HUB_AUTOMATION_OWNERS"] != "/automation-owners" || env["HUB_AUTOMATION_LEGACY_SPOOL"] != "/legacy-communication" || env["HUB_AUTOMATION_ROUTING"] != "/automation-routing/dev.json" || env["HUB_AUTOMATION_ENVIRONMENT"] != "dev" {
		t.Fatal("automation owner paths or environment missing")
	}
	if env["HUB_AUTOMATION_JOB_URL"] != "http://hermes-runtime:8080" || env["HUB_AUTOMATION_JOB_PATH"] != "/v1/execute" {
		t.Fatal("static automation route does not match the runtime HTTP contract")
	}
	mounted := map[string]M{}
	for _, raw := range toolhub["volumes"].([]any) {
		mount := raw.(M)
		if target, ok := mount["target"].(string); ok {
			mounted[target] = mount
		}
	}
	for _, user := range []string{"alice", "bob"} {
		mount := mounted["/automation-owners/"+user]
		if mount == nil || mount["source"] != filepath.ToSlash(filepath.Join(spacesRoot, user, "automation", "dev")) || mount["read_only"] == true {
			t.Fatalf("missing writable dev automation owner %s: %v", user, mount)
		}
	}
	if mounted["/automation-owners/carol"] != nil {
		t.Fatal("prod automation state leaked into dev ToolHub")
	}
	if mount := mounted["/legacy-communication"]; mount == nil || mount["source"] != "communication-hub-data" || mount["read_only"] != true {
		t.Fatalf("legacy preflight mount: %v", mount)
	}
	if mount := mounted["/automation-routing"]; mount == nil || mount["read_only"] != true {
		t.Fatal("routing view is not mounted read-only")
	}
}

func TestRenderAutomationRoutingContainsOnlyChannelIdentities(t *testing.T) {
	dir := t.TempDir()
	s := Settings{Environment: "dev", User: "alice"}
	if err := renderAutomationRouting(dir, s, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "SLACK_ALLOWED_USERS": "TEAM/USER", "TELEGRAM_BOT_TOKEN": "secret-marker"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "automation-routing", "dev.json"))
	if err != nil || !strings.Contains(string(body), `"telegram_ids":[11]`) || !strings.Contains(string(body), `"team_id":"TEAM"`) || strings.Contains(string(body), "secret-marker") {
		t.Fatal("fallback routing projection is invalid or contains a credential")
	}
	if err := os.WriteFile(filepath.Join(dir, "communication.users.yaml"), []byte("organization_id: personal\nusers:\n  - id: bob\n    enabled: true\n    runtime_id: bob\n    telegram_ids: [22]\n    state_dir: /private-state\n    workspace_dir: /private-workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renderAutomationRouting(dir, s, nil); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{filepath.Join(dir, "automation-routing"): 0755, filepath.Join(dir, "automation-routing", "dev.json"): 0644} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want {
				t.Fatalf("routing bind mode: %s got=%o want=%o", filepath.Base(path), info.Mode().Perm(), want)
			}
		}
	}
	body, err = os.ReadFile(filepath.Join(dir, "automation-routing", "dev.json"))
	if err != nil || !strings.Contains(string(body), `"id":"bob"`) || strings.Contains(string(body), "private-") || strings.Contains(string(body), `"id":"alice"`) {
		t.Fatal("multi-user routing projection retained unrelated fields")
	}
}

func TestDiagnosticRotationMountsOnlyDedicatedDirectory(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools("telegram"), Ingress: testIngress("telegram"), ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876"}
	services := Compose(s, "/source", "/space")["services"].(M)
	for _, service := range []string{"toolhub", "supervisor"} {
		found := false
		for _, volume := range services[service].(M)["volumes"].([]any) {
			v, ok := volume.(M)
			if !ok {
				continue
			}
			source, _ := v["source"].(string)
			if strings.HasSuffix(source, "/.local/supervisor.log") {
				t.Error("single-file bind pins an obsolete inode after rotation")
			}
			if strings.HasSuffix(source, "/.local/diagnostics") {
				found = true
				if service == "toolhub" && v["read_only"] != true {
					t.Fatal("collector host logs must be read-only")
				}
			}
			if strings.HasSuffix(source, "/.local") {
				t.Fatal("diagnostics exposed the whole host control directory")
			}
		}
		if !found {
			t.Errorf("%s lacks dedicated rotation directory", service)
		}
	}
}

// The runtime learns its read-only workspace boundaries from the rendered
// environment (R4.2): inbox originals are never agent-writable, and mounts
// declared ro join the boundary list.
func TestComposeRendersWorkspaceReadOnlyBoundaries(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testTools(), Ingress: testIngress(),
		Workspace: Workspace{Mounts: []Mount{{From: "shared", To: "/workspace/shared", Mode: "ro"}}}}
	env := RuntimeService(s, "/source", "/space")["environment"].(M)
	ro, _ := env["HUB_WORKSPACE_RO"].(string)
	for _, want := range []string{"inbox", "shared"} {
		if !strings.Contains(","+ro+",", ","+want+",") {
			t.Fatalf("HUB_WORKSPACE_RO = %q, want %q listed", ro, want)
		}
	}
}
