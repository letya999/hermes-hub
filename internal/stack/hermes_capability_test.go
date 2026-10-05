package stack

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestManagedHermesSourceRejectsEveryAlternativeIngress(t *testing.T) {
	inventory, err := ManagedCapabilityInventory()
	if err != nil || inventory.SourcePin == "" || inventory.InventorySHA256 == "" || len(inventory.Platforms) != 27 {
		t.Fatalf("pinned inventory: %+v %v", inventory, err)
	}
	s := Settings{Schema: 1, Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, User: "alice", Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	zero := Config(s)
	if len(zero["mcp_servers"].(M)) != 0 {
		t.Fatal("managed source included direct MCP")
	}
	for _, platform := range inventory.Platforms {
		if tools, ok := zero["platform_toolsets"].(M)[platform].([]string); !ok || len(tools) != 0 {
			t.Fatalf("enabled native platform %s: %v", platform, zero["platform_toolsets"].(M)[platform])
		}
	}
	if !reflect.DeepEqual(zero["agent"].(M)["disabled_toolsets"], inventory.DisabledToolsets) {
		t.Fatal("denial inventory mismatch")
	}
	dir := t.TempDir()
	source, dest := filepath.Join(dir, "source.yaml"), filepath.Join(dir, "effective.yaml")
	write := func(value M) {
		t.Helper()
		body, err := yaml.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	base, err := yaml.Marshal(zero)
	if err != nil {
		t.Fatal(err)
	}
	write(zero)
	services := filepath.Join(dir, "self-services.json")
	if err := os.WriteFile(services, []byte(`{"features":["github"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	opts := MaterializeOptions{Managed: true, ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true, SelfServicesPath: services}
	if err := MaterializeHermesConfig(source, dest, opts); err != nil {
		t.Fatal(err)
	}
	effective, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedEffectiveConfig(dest, s, opts); err != nil {
		t.Fatalf("rendered managed config rejected: %v", err)
	}
	for name, content := range map[string][]byte{
		"malformed": []byte("invalid: ["),
		"fallback":  []byte("{}\n"),
		"native":    bytes.Replace(effective, []byte("api_server: []"), []byte("api_server: [terminal]"), 1),
		"mcp":       append(bytes.Clone(effective), []byte("\nunreviewed: true\n")...),
	} {
		t.Run("effective-"+name, func(t *testing.T) {
			if bytes.Equal(content, effective) {
				t.Fatal("test did not change effective config")
			}
			if err := os.WriteFile(dest, content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := ValidateManagedEffectiveConfig(dest, s, opts); err == nil {
				t.Fatal("changed effective config accepted")
			}
			if err := os.WriteFile(dest, effective, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	changed := s
	changed.Model = "other"
	if err := ValidateManagedEffectiveConfig(dest, changed, opts); err == nil {
		t.Fatal("wrong reviewed model accepted")
	}
	missing := opts
	missing.RuntimeAuthPresent = false
	if err := ValidateManagedEffectiveConfig(dest, s, missing); err == nil {
		t.Fatal("missing managed token accepted")
	}
	var decoded M
	if err := yaml.Unmarshal(effective, &decoded); err != nil {
		t.Fatal(err)
	}
	servers := decoded["mcp_servers"].(M)
	if len(servers) != 1 || servers["toolhub"] == nil || !bytes.Contains(effective, []byte("HUB_RUNTIME_AUTH")) || bytes.Contains(effective, []byte("github")) {
		t.Fatalf("wrong managed endpoint: %s", effective)
	}
	for name, mutate := range map[string]func(M){
		"api-fallback":    func(m M) { delete(m["platform_toolsets"].(M), "api_server") },
		"native-terminal": func(m M) { m["platform_toolsets"].(M)["api_server"] = []string{"terminal"} },
		"missing-deny":    func(m M) { m["agent"].(M)["disabled_toolsets"] = []string{"file"} },
		"direct-mcp":      func(m M) { m["mcp_servers"].(M)["escape"] = M{"url": "https://example.invalid/mcp"} },
		"plugin":          func(m M) { m["plugins"].(M)["enabled"] = []string{"escape"} },
		"skill":           func(m M) { m["skills"].(M)["external_dirs"] = []string{"/tmp/escape"} },
		"hook":            func(m M) { m["hooks"].(M)["before_tool"] = []string{"/tmp/escape"} },
		"memory":          func(m M) { m["memory"].(M)["memory_enabled"] = true },
		"stt":             func(m M) { m["stt"].(M)["enabled"] = true },
		"lazy-install":    func(m M) { m["security"].(M)["allow_lazy_installs"] = true },
		"unknown":         func(m M) { m["unexpected_extension"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			var changed M
			if err := yaml.Unmarshal(base, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(changed)
			write(changed)
			if err := MaterializeHermesConfig(source, dest, opts); err == nil {
				t.Fatal("unreviewed source accepted")
			}
			after, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(after, effective) {
				t.Fatalf("rejected edit changed effective config: %v", err)
			}
		})
	}
	write(zero)
	for _, omitted := range []MaterializeOptions{{Managed: true, ToolHubEndpoint: opts.ToolHubEndpoint}, {Managed: true, RuntimeAuthPresent: true}} {
		if err := MaterializeHermesConfig(source, dest, omitted); err == nil {
			t.Fatal("managed endpoint/token absence accepted")
		}
	}
	bad := s
	bad.Tools = testTools("meet")
	if err := bad.Validate(); err == nil {
		t.Fatal("plugin-backed toolset widened managed profile")
	}
	bad = s
	bad.MCP = map[string]MCPServer{"escape": {URL: "https://example.invalid/mcp"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("direct MCP admitted into managed settings")
	}
	for _, route := range []string{"http://host.docker.internal:8317/v1", "https://example.invalid/v1", "http://cliproxy:8317/v1", "http://model-relay:8318/admin"} {
		bad = s
		bad.ModelURL = route
		if err := bad.Validate(); err == nil {
			t.Fatalf("unreviewed managed model route accepted: %s", route)
		}
	}
	for _, model := range []string{"", " synthetic", "synthetic\nother"} {
		bad = s
		bad.Model = model
		if err := bad.Validate(); err == nil {
			t.Fatalf("unreviewed managed model identifier accepted: %q", model)
		}
	}
}

func TestManagedRenderFailsUntilRuntimeIsolationExists(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	settings, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings.CapabilityMode, settings.CapabilityProfileID, settings.CapabilityGeneration = "managed", "alice-default", 1
	settings.Model = "synthetic"
	settings.ModelURL = "http://model-relay:8318/v1"
	settings.Memory, settings.Tools, settings.Ingress = false, nil, nil
	if err := WriteSpace(dir, settings); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(dir, root, "dev"); err == nil || !strings.Contains(err.Error(), "managed runtime requires supervisor-executed runtimes") {
		t.Fatalf("managed runtime did not fail at isolation boundary: %v", err)
	}
}

func TestManagedSupervisedRenderOmitsRuntimeService(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	settings, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings.CapabilityMode, settings.CapabilityProfileID, settings.CapabilityGeneration = "managed", "alice-default", 1
	settings.Model = "synthetic"
	settings.ModelURL = "http://model-relay:8318/v1"
	settings.Memory, settings.Tools, settings.Ingress = false, nil, nil
	if err := WriteSpace(dir, settings); err != nil {
		t.Fatal(err)
	}
	selection := ExecutionSelection{Schema: 1, User: "alice", Environment: "dev", Mode: "supervisor", SupervisorURL: "http://localhost:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
	selectionBody, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ExecutionPath("dev")), selectionBody, 0600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "SOUL.md"), []byte("# test soul\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(dir, root, "dev"); err != nil {
		t.Fatalf("managed supervised render: %v", err)
	}
	composeBody, err := os.ReadFile(filepath.Join(dir, "generated", "compose.dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var composed M
	if err := yaml.Unmarshal(composeBody, &composed); err != nil {
		t.Fatal(err)
	}
	services := composed["services"].(M)
	if _, ok := services["hermes-runtime"]; ok {
		t.Fatal("managed compose still ships a static runtime service")
	}
	for _, relay := range []string{"model-relay", "toolhub-relay"} {
		if _, ok := services[relay]; !ok {
			t.Fatalf("managed compose lost relay %s", relay)
		}
	}
	agentNet := composed["networks"].(M)["hermes-hub-agent-alice-dev"].(M)
	if agentNet["internal"] != true {
		t.Fatal("managed agent network is not internal")
	}
	if _, err := os.Stat(filepath.Join(dir, "managed-runtime.dev.env")); err != nil {
		t.Fatal("managed runtime env file missing after render")
	}
}

func TestManagedComposeSeparatesHermesDataFromControl(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080}
	topology := Compose(s, t.TempDir(), t.TempDir())
	services := topology["services"].(M)
	if _, ok := services["hermes-runtime"]; ok {
		t.Fatal("deployable compose still ships the managed runtime service")
	}
	runtime := RuntimeService(s, t.TempDir(), t.TempDir())
	runtimeEnv := runtime["environment"].(M)
	if runtimeEnv["HERMES_BUNDLES_DIR"] != "/state/hermes/skill-bundles" || runtimeEnv["HERMES_ENABLE_PROJECT_PLUGINS"] != "0" {
		t.Fatal("managed extension discovery can be redirected")
	}
	control := services["toolhub"].(M)
	volumes := runtime["volumes"].([]any)
	for _, volume := range volumes {
		mount := volume.(M)
		target := mount["target"].(string)
		if target == "/archive" || target == "/state/google" || target == "/state/telegram" || target == "/state/browser" {
			t.Fatalf("managed runtime retains broad data mount %s", target)
		}
		if target == "/state" && strings.Contains(mount["source"].(string), "/runtime") && !strings.Contains(mount["source"].(string), "/managed/") {
			t.Fatalf("managed runtime shares control state: %v", mount)
		}
		if target == "/state/hermes/config.yaml" && mount["read_only"] != true {
			t.Fatalf("executable source writable: %v", mount)
		}
		if target == "/state/hermes/hooks" || target == "/state/hermes/plugins" || target == "/state/hermes/skills" {
			t.Fatalf("managed extension root still depends on mutable host source: %v", mount)
		}
	}
	if envFile := runtime["env_file"].([]any)[0].(M)["path"].(string); !strings.HasSuffix(envFile, "/managed-runtime.dev.env") {
		t.Fatalf("managed runtime still receives broad secret file: %s", envFile)
	}
	workspaceOK := false
	for _, volume := range volumes {
		mount := volume.(M)
		if mount["target"] == "/workspace" && strings.Contains(mount["source"].(string), "/managed/") && mount["read_only"] != true {
			workspaceOK = true
		}
	}
	if !workspaceOK {
		t.Fatal("managed workspace is not a durable bind under managed/<env>/workspace")
	}
	if strings.Contains(strings.Join(runtime["tmpfs"].([]string), ","), "/workspace:") {
		t.Fatal("managed workspace must persist across respawns, not tmpfs")
	}
	for _, name := range []string{"skills", "hooks", "plugins", "skill-bundles", "scripts", "bin", "node", "lsp"} {
		if !slices.Contains(runtime["tmpfs"].([]string), "/state/hermes/"+name+":ro,mode=0555") {
			t.Fatalf("managed %s root is not empty read-only tmpfs", name)
		}
	}
	if runtime["environment"].(M)["HUB_TOOLHUB_STORE"] != nil || runtime["read_only"] != true {
		t.Fatal("managed runtime retains store path or writable image")
	}
	if len(runtime["extra_hosts"].([]string)) != 0 {
		t.Fatal("managed runtime retains host-gateway alias")
	}
	if len(runtime["ports"].([]string)) != 0 {
		t.Fatal("managed runtime exposes legacy host callback ports")
	}
	if networks := runtime["networks"].([]string); !reflect.DeepEqual(networks, []string{managedAgentNetwork(s)}) {
		t.Fatalf("managed runtime reached shared network: %v", networks)
	}
	if network := topology["networks"].(M)[managedAgentNetwork(s)].(M); network["internal"] != true {
		t.Fatalf("agent network is not internal: %v", network)
	}
	if control["networks"].(M)[managedAgentNetwork(s)] != nil {
		t.Fatal("ToolHub control endpoints are directly reachable from agent network")
	}
	if control["networks"].(M)[sharedNetworkName].(M)["aliases"].([]string)[0] != "toolhub-control" {
		t.Fatal("ToolHub control alias is missing from shared network")
	}
	if !slices.Contains(services["model-relay"].(M)["networks"].([]string), managedAgentNetwork(s)) {
		t.Fatal("model relay is absent from private agent network")
	}
	toolhubRelay := services["toolhub-relay"].(M)
	if toolhubRelay["networks"].(M)[managedAgentNetwork(s)].(M)["aliases"].([]string)[0] != "toolhub" {
		t.Fatal("private agent network lacks the narrow ToolHub MCP alias")
	}
	if toolhubRelay["volumes"] != nil || toolhubRelay["env_file"] != nil || toolhubRelay["ports"] != nil || len(toolhubRelay["extra_hosts"].([]string)) != 0 {
		t.Fatalf("ToolHub relay has unnecessary authority: %v", toolhubRelay)
	}
	if slices.Contains(services["cliproxy"].(M)["networks"].([]string), managedAgentNetwork(s)) {
		t.Fatal("agent can bypass model relay and reach CLIProxy directly")
	}
	relay := services["model-relay"].(M)
	if relay["volumes"] != nil || relay["env_file"] != nil || relay["ports"] != nil || len(relay["extra_hosts"].([]string)) != 0 {
		t.Fatalf("model relay has unnecessary authority: %v", relay)
	}
	if slices.Contains(services["credential-broker"].(M)["networks"].([]string), managedAgentNetwork(s)) {
		t.Fatal("broker reachable from private agent network")
	}
	secondary := false
	s.Infra = &secondary
	if networks := RuntimeService(s, t.TempDir(), t.TempDir())["networks"].([]string); !reflect.DeepEqual(networks, []string{managedAgentNetwork(s)}) {
		t.Fatalf("secondary managed runtime reached shared network: %v", networks)
	}
	secondaryServices := Compose(s, t.TempDir(), t.TempDir())["services"].(M)
	if _, ok := secondaryServices["model-relay"]; !ok {
		t.Fatal("secondary agent lacks its private model route")
	}
	if relay, ok := secondaryServices["toolhub-relay"].(M); !ok || relay["networks"].(M)[managedAgentNetwork(s)].(M)["aliases"].([]string)[0] != "toolhub" {
		t.Fatal("secondary agent lacks its private ToolHub MCP route")
	}
	if control["environment"].(M)["HUB_TOOLHUB_STORE"] == nil {
		t.Fatal("control service lost its authoritative store")
	}
}

func TestNativeToolsetValidation(t *testing.T) {
	if err := ValidateNativeToolsets([]string{"terminal", "memory", "todo"}); err != nil {
		t.Fatalf("reviewed carve-out rejected: %v", err)
	}
	if err := ValidateNativeToolsets(nil); err != nil {
		t.Fatal("empty carve-out rejected")
	}
	for _, names := range [][]string{
		{"delegation"},           // sub-agent literal toolsets bypass the denylist
		{"bot_room"},             // forged room-policy bypass
		{"hermes-gateway"},       // platform adapter listener inside the agent
		{"not a name"},           // invalid format
		{"terminal", "terminal"}, // duplicate
	} {
		if err := ValidateNativeToolsets(names); err == nil {
			t.Fatalf("carve-out accepted: %v", names)
		}
	}
	s := Settings{Schema: 1, Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, User: "alice", Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	s.Tools = testNativeTools("terminal", "memory")
	if err := s.Validate(); err != nil {
		t.Fatalf("managed settings with carve-outs rejected: %v", err)
	}
	s.Tools = testNativeTools("a2a")
	if err := s.Validate(); err == nil {
		t.Fatal("unreviewed native toolset accepted into managed settings")
	}
	s.Tools = testNativeTools("terminal")
	s.CapabilityMode, s.CapabilityProfileID, s.CapabilityGeneration = "", "", 0
	// Outside managed mode a native entry is an ordinary allowlist grant on the
	// unmanaged platform_toolsets surface, so it stays valid.
	if err := s.Validate(); err != nil {
		t.Fatalf("native entry rejected outside managed mode: %v", err)
	}
}

func TestNativeCarveoutSubtractsRenderedDenylist(t *testing.T) {
	inventory, err := ManagedCapabilityInventory()
	if err != nil {
		t.Fatal(err)
	}
	s := Settings{Schema: 1, Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, User: "alice", Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Tools: testNativeTools("terminal", "memory", "todo")}
	zero := Config(s)
	disabled := zero["agent"].(M)["disabled_toolsets"].([]string)
	for _, carved := range []string{"terminal", "memory", "todo"} {
		if slices.Contains(disabled, carved) {
			t.Fatalf("carved-out toolset %s still denied", carved)
		}
	}
	if len(disabled) != len(inventory.DisabledToolsets)-3 {
		t.Fatalf("denylist shrank by %d, expected 3", len(inventory.DisabledToolsets)-len(disabled))
	}
	// The carved-out memory toolset needs its subsystem flag; the rest stay
	// at the reviewed zero posture.
	if zero["memory"].(M)["memory_enabled"] != true {
		t.Fatal("carved memory toolset rendered without memory_enabled")
	}
	if zero["compression"].(M)["enabled"] != false || zero["curator"].(M)["enabled"] != false {
		t.Fatal("carve-out re-enabled bypassing sub-agent paths")
	}
}

func TestNativeCarveoutMaterializeAndAttest(t *testing.T) {
	s := Settings{Schema: 1, Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, User: "alice", Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Tools: testNativeTools("terminal")}
	zero := Config(s)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	body, err := yaml.Marshal(zero)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	opts := MaterializeOptions{Managed: true, ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true, NativeToolsets: []string{"terminal"}}
	dest := filepath.Join(dir, "effective.yaml")
	if err := MaterializeHermesConfig(source, dest, opts); err != nil {
		t.Fatalf("carved materialize: %v", err)
	}
	if err := ValidateManagedEffectiveConfig(dest, s, opts); err != nil {
		t.Fatalf("carved effective config rejected: %v", err)
	}
	var effective M
	raw, _ := os.ReadFile(dest)
	if err := yaml.Unmarshal(raw, &effective); err != nil {
		t.Fatal(err)
	}
	for _, name := range effective["agent"].(map[string]any)["disabled_toolsets"].([]any) {
		if name == "terminal" {
			t.Fatal("effective config still denies the carved terminal toolset")
		}
	}
	// A source rendered without the same carve-out is an unreviewed edit:
	// the attestation must not accept a mismatched denial set.
	stale := Config(Settings{Model: s.Model, ModelURL: s.ModelURL, Timezone: s.Timezone})
	body, _ = yaml.Marshal(stale)
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeHermesConfig(source, dest, opts); err == nil {
		t.Fatal("full-deny source accepted under a carved grant")
	}
	bad := opts
	bad.NativeToolsets = []string{"delegation"}
	if err := MaterializeHermesConfig(source, dest, bad); err == nil {
		t.Fatal("unapproved native toolset reached materialize")
	}
	if err := ValidateManagedEffectiveConfig(dest, s, bad); err == nil {
		t.Fatal("unapproved native toolset reached runtime attestation")
	}
}

func TestManagedOrganizationRuntimeDoesNotMountOrgDocuments(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Organization: "acme", OrganizationDocsDir: t.TempDir(), Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080}
	for _, infra := range []bool{true, false} {
		s.Infra = &infra
		service := RuntimeService(s, t.TempDir(), t.TempDir())
		for _, volume := range service["volumes"].([]any) {
			if volume.(M)["target"] == "/org" {
				t.Fatalf("managed organization documents mounted for infra=%t", infra)
			}
		}
	}
}

func TestManagedEgressHosts(t *testing.T) {
	dir := t.TempDir()
	unmanaged := Settings{Schema: 1, User: "alice", Environment: "dev"}
	if hosts := managedEgressHosts(unmanaged, dir); hosts != nil {
		t.Fatalf("unmanaged egress: %v", hosts)
	}
	s := Settings{Schema: 1, User: "alice", Environment: "dev", CapabilityMode: "managed",
		CapabilityProfileID: "alice-default", CapabilityGeneration: 1,
		Web: WebSettings{SearchBackend: "keenable", SearchProviders: []string{"ddgs"}},
		Tools: map[string]ToolEntry{
			"web":      {Via: ToolViaNative},
			"files":    {Via: ToolViaToolHub},
			"routines": {Via: ToolViaToolHub},
			"hh":       {Via: ToolViaToolHub},
		}}
	hosts := managedEgressHosts(s, dir)
	for _, want := range []string{"api.keenable.ai", "duckduckgo.com", "*.duckduckgo.com", "communication-hub", "api.hh.ru"} {
		if !slices.Contains(hosts, want) {
			t.Fatalf("egress host %s missing: %v", want, hosts)
		}
	}
	s.Tools["files"] = ToolEntry{Via: ToolViaNative}
	if hosts := managedEgressHosts(s, dir); !slices.Contains(hosts, "communication-hub") {
		t.Fatalf("routines sibling host lost: %v", hosts)
	}
}

func TestManagedEgressRelayRenders(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	settings, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings.CapabilityMode, settings.CapabilityProfileID, settings.CapabilityGeneration = "managed", "alice-default", 1
	settings.Model = "synthetic"
	settings.ModelURL = "http://model-relay:8318/v1"
	settings.Memory, settings.Ingress = false, nil
	settings.Tools = map[string]ToolEntry{
		"web":       {Via: ToolViaNative},
		"search":    {Via: ToolViaNative},
		"memory":    {Via: ToolViaNative},
		"todo":      {Via: ToolViaNative},
		"files":     {Via: ToolViaToolHub},
		"documents": {Via: ToolViaToolHub},
		"images":    {Via: ToolViaToolHub},
		"artifacts": {Via: ToolViaToolHub},
		"routines":  {Via: ToolViaToolHub},
		"services":  {Via: ToolViaToolHub},
		"image_gen": {Via: ToolViaToolHub},
	}
	settings.Web = WebSettings{SearchBackend: "keenable", ExtractBackend: "keenable", SearchProviders: []string{"keenable", "ddgs"}}
	if err := WriteSpace(dir, settings); err != nil {
		t.Fatal(err)
	}
	// A provider key must reach the managed runtime env — the spawned
	// container has no other path to it.
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=k-test\nKEENABLE_API_KEY=kk-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selection := ExecutionSelection{Schema: 1, User: "alice", Environment: "dev", Mode: "supervisor", SupervisorURL: "http://localhost:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
	selectionBody, _ := json.Marshal(selection)
	if err := os.WriteFile(filepath.Join(dir, ExecutionPath("dev")), selectionBody, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("# soul\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(dir, root, "dev"); err != nil {
		t.Fatalf("managed render with egress: %v", err)
	}
	composeBody, err := os.ReadFile(filepath.Join(dir, "generated", "compose.dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var composed M
	if err := yaml.Unmarshal(composeBody, &composed); err != nil {
		t.Fatal(err)
	}
	relay, ok := composed["services"].(M)["egress-relay"].(M)
	if !ok {
		t.Fatal("egress relay not rendered")
	}
	hosts := relay["environment"].(M)["HUB_EGRESS_RELAY_HOSTS"].(string)
	for _, want := range []string{"api.keenable.ai", "duckduckgo.com", "hub-media", "communication-hub"} {
		if !strings.Contains(hosts, want) {
			t.Fatalf("egress allowlist misses %s: %s", want, hosts)
		}
	}
	if networks := relay["networks"].(M); networks["hermes-hub-agent-alice-dev"] == nil || networks[sharedNetworkName] == nil {
		t.Fatalf("egress relay must bridge agent and shared networks: %v", networks)
	}
	env, err := ReadSecrets(filepath.Join(dir, "managed-runtime.dev.env"))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"HTTPS_PROXY":                   "http://egress-relay:8319",
		"HTTP_PROXY":                    "http://egress-relay:8319",
		"HUB_EGRESS_PROXY":              "http://egress-relay:8319",
		"KEENABLE_API_KEY":              "kk-test",
		"HUB_MEDIA_URL":                 "http://hub-media:8090",
		"HUB_COMMUNICATION_CONTROL_URL": "http://communication-hub:8081",
	} {
		if env[key] != want {
			t.Fatalf("managed env %s = %q, want %q", key, env[key], want)
		}
	}
	if env["HUB_MEDIA_AUTH"] == "" || env["HUB_COMMUNICATION_AUTH"] == "" {
		t.Fatal("managed env lost media or communication auth")
	}
	// The effective Hermes config carries the operator's provider selection.
	effective, err := os.ReadFile(filepath.Join(dir, "generated", "hermes-effective.dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(effective), "keenable") {
		t.Fatal("managed effective config dropped web provider selection")
	}
	// The spawned container reconstructs the same expectation from env: the
	// web provider selection must ride HUB_MANAGED_WEB into the runtime.
	runtimeService := RuntimeService(settings, "", dir)
	envMap, ok := runtimeService["environment"].(M)
	if !ok {
		t.Fatal("runtime service has no environment")
	}
	webJSON, ok := envMap["HUB_MANAGED_WEB"].(string)
	if !ok || webJSON == "" {
		t.Fatal("managed runtime env lost HUB_MANAGED_WEB")
	}
	var carried WebSettings
	if err := json.Unmarshal([]byte(webJSON), &carried); err != nil {
		t.Fatalf("HUB_MANAGED_WEB is not JSON: %v", err)
	}
	if !reflect.DeepEqual(carried, settings.Web) {
		t.Fatalf("HUB_MANAGED_WEB round-trip changed the selection: %+v", carried)
	}
	// Attestation must accept the effective config when — and only when —
	// the web selection arrives with the materialize options.
	attestOpts := MaterializeOptions{Managed: true, ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true, ToolHubReconnect: true,
		NativeToolsets: settings.nativeCarveouts(), Web: carried}
	validate := Settings{Model: settings.Model, ModelURL: settings.ModelURL, Timezone: settings.Timezone}
	effectivePath := filepath.Join(dir, "generated", "hermes-effective.dev.yaml")
	if err := ValidateManagedEffectiveConfig(effectivePath, validate, attestOpts); err != nil {
		t.Fatalf("effective config rejected with carried web selection: %v", err)
	}
	attestOpts.Web = WebSettings{}
	if err := ValidateManagedEffectiveConfig(effectivePath, validate, attestOpts); err == nil {
		t.Fatal("web block accepted without the provider selection in transit")
	}
}
