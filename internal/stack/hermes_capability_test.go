package stack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
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

// extra_hosts must always render as a mapping, never the ["host:ip"] sequence:
// `docker compose -f base -f override` validation rejects sequence values on
// merge, which breaks every dev-rebuild/dev-image override on these stacks.
func TestComposeExtraHostsAlwaysMapping(t *testing.T) {
	for _, s := range []Settings{
		{Schema: 1, User: "alice", Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080},
		{Schema: 1, User: "alice", Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080, Web: WebSettings{SearchBackend: "keenable"}, Tools: map[string]ToolEntry{"web": {Via: ToolViaNative}}},
		{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080, Tools: testTools("telegram"), Ingress: testIngress("telegram")},
	} {
		for name, raw := range Compose(s, t.TempDir(), t.TempDir())["services"].(M) {
			service := raw.(M)
			if hosts, present := service["extra_hosts"]; present {
				if _, ok := hosts.(M); !ok {
					t.Fatalf("%s renders extra_hosts as %T — compose merge requires a mapping", name, hosts)
				}
			}
		}
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
	if hosts, ok := runtime["extra_hosts"].(M); !ok || len(hosts) != 0 {
		t.Fatalf("managed runtime extra_hosts is not an empty mapping: %T", runtime["extra_hosts"])
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
	if _, ok := services["model-relay"].(M)["networks"].(M)[managedAgentNetwork(s)]; !ok {
		t.Fatal("model relay is absent from private agent network")
	}
	toolhubRelay := services["toolhub-relay"].(M)
	if toolhubRelay["networks"].(M)[managedAgentNetwork(s)].(M)["aliases"].([]string)[0] != "toolhub" {
		t.Fatal("private agent network lacks the narrow ToolHub MCP alias")
	}
	hosts, hostsOK := toolhubRelay["extra_hosts"].(M)
	if toolhubRelay["volumes"] != nil || toolhubRelay["env_file"] != nil || toolhubRelay["ports"] != nil || !hostsOK || len(hosts) != 0 {
		t.Fatalf("ToolHub relay has unnecessary authority: %v", toolhubRelay)
	}
	if slices.Contains(services["cliproxy"].(M)["networks"].([]string), managedAgentNetwork(s)) {
		t.Fatal("agent can bypass model relay and reach CLIProxy directly")
	}
	relay := services["model-relay"].(M)
	if relayHosts, relayOK := relay["extra_hosts"].(M); relay["ports"] != nil || !relayOK || len(relayHosts) != 0 {
		t.Fatalf("model relay has unnecessary authority: %v", relay)
	}
	// Bounded authority only: the merged registry mounts read-only, the
	// selection state dir is the single writable mount, and env files carry
	// provider key names plus the owner-switch bearer — no literals.
	var relayMounts []string
	for _, m := range relay["volumes"].([]any) {
		relayMounts = append(relayMounts, fmt.Sprint(m.(M)["target"]))
	}
	slices.Sort(relayMounts)
	if fmt.Sprint(relayMounts) != "[/run/broker-secrets /run/model-config /run/model-state]" {
		t.Fatalf("model relay mounts widened: %v", relayMounts)
	}
	var relayEnvFiles int
	for _, f := range relay["env_file"].([]any) {
		p := fmt.Sprint(f.(M)["path"])
		if strings.HasSuffix(p, "runtime.dev.env") || strings.HasSuffix(p, "model-relay.auth") {
			relayEnvFiles++
		}
	}
	if relayEnvFiles != 2 {
		t.Fatalf("model relay env files widened: %v", relay["env_file"])
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
	if control["environment"].(M)["HUB_AUDIT_LEDGER"] != "/state/toolhub/audit.jsonl" {
		t.Fatal("managed dispatch requires the durable audit ledger path")
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
		{"messaging"},            // messaging is not a reviewed managed carve-out
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
	// In-loop compression is reviewed-on with an explicit token cap; only the
	// detached curator sub-agent (which would bypass the denylist) stays off.
	compression := zero["compression"].(M)
	if compression["enabled"] != true || compression["in_place"] != true ||
		compression["micro_compact"] != false || compression["threshold_tokens"] != 200000 {
		t.Fatalf("managed compression posture changed: %v", compression)
	}
	if zero["curator"].(M)["enabled"] != false {
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
	// Native toolsets are default-deny (SPEC-0083 R3.1): grant the fixture's
	// native selections before render.
	seedGovernance(t, dir, func(doc *api.Governance) {
		for _, section := range []string{"native:web", "native:search", "native:memory", "native:todo"} {
			activeToolGrant(t, doc, "alice", section, time.Hour)
		}
	})
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
	// The image_gen grant rides the same contract: the spawned runtime
	// reconstructs the block from HUB_MANAGED_IMAGE_GEN.
	imageGenJSON, ok := envMap["HUB_MANAGED_IMAGE_GEN"].(string)
	if !ok || imageGenJSON == "" {
		t.Fatal("managed runtime env lost HUB_MANAGED_IMAGE_GEN")
	}
	var carriedGen media.ImageGen
	if err := json.Unmarshal([]byte(imageGenJSON), &carriedGen); err != nil {
		t.Fatalf("HUB_MANAGED_IMAGE_GEN is not JSON: %v", err)
	}
	if carriedGen != settings.managedImageGenGrant() {
		t.Fatalf("HUB_MANAGED_IMAGE_GEN round-trip changed the grant: %+v", carriedGen)
	}
	// Attestation must accept the effective config when — and only when —
	// the web and image_gen selections arrive with the materialize options.
	attestOpts := MaterializeOptions{Managed: true, ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true, ToolHubReconnect: true,
		NativeToolsets: settings.nativeCarveouts(), Web: carried, ImageGen: carriedGen}
	validate := Settings{Model: settings.Model, ModelURL: settings.ModelURL, Timezone: settings.Timezone}
	effectivePath := filepath.Join(dir, "generated", "hermes-effective.dev.yaml")
	if err := ValidateManagedEffectiveConfig(effectivePath, validate, attestOpts); err != nil {
		t.Fatalf("effective config rejected with carried web selection: %v", err)
	}
	attestOpts.Web = WebSettings{}
	if err := ValidateManagedEffectiveConfig(effectivePath, validate, attestOpts); err == nil {
		t.Fatal("web block accepted without the provider selection in transit")
	}
	attestOpts.Web = carried
	attestOpts.ImageGen = media.ImageGen{}
	if err := ValidateManagedEffectiveConfig(effectivePath, validate, attestOpts); err == nil {
		t.Fatal("image_gen block accepted without the grant in transit")
	}
}

// TestAssignmentToolhubFamily: the standing-assignment surface is a
// deliverable ToolHub capability family — a space may wish assignments:
// toolhub, the wish maps to the assignments capability, and the control
// plane host opens exactly like routines (SPEC-0080 R2.3).
func TestAssignmentToolhubFamily(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", CapabilityMode: "managed",
		CapabilityProfileID: "alice-default", CapabilityGeneration: 1,
		Tools: map[string]ToolEntry{"assignments": {Via: ToolViaToolHub}}}
	if err := s.validateTools(); err != nil {
		t.Fatalf("assignments: toolhub must validate: %v", err)
	}
	if got := ToolHubCapabilityID("assignments"); got != "assignments" {
		t.Fatalf("capability id = %q", got)
	}
	hosts := managedEgressHosts(s, t.TempDir())
	if !slices.Contains(hosts, "communication-hub") {
		t.Fatalf("assignments must open the control plane egress: %v", hosts)
	}
}

// SPEC-0084 R3.3: the native skills toolset is always off — the hub-built
// read-only view at /skills replaces upstream loading entirely, so `skills`
// is not a carve-out and a wish plus active grant still cannot lift it onto
// a platform list or off the denylist.
func TestNativeSkillsToolsetAlwaysDenied(t *testing.T) {
	if slices.Contains(NativeCarveoutToolsets(), "skills") {
		t.Fatal("skills is a reviewed carve-out — must never be re-enableable")
	}
	if err := ValidateNativeToolsets([]string{"skills"}); err == nil {
		t.Fatal("skills accepted as a native carve-out")
	}
	d := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	s, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	s.Model, s.ModelURL = "test", "http://model.invalid/v1"
	// A `skills: {via: native}` wish can be persisted but never renders —
	// validation refuses it before any grant question.
	s.Tools = map[string]ToolEntry{"skills": {Via: ToolViaNative}}
	if err := saveSpace(d, s); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err == nil || !strings.Contains(err.Error(), "not a reviewed native toolset") {
		t.Fatalf("skills wish rendered: %v", err)
	}
	s.Tools = map[string]ToolEntry{}
	if err := saveSpace(d, s); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(d, "generated", "hermes.prod.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg M
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	disabled := cfg["agent"].(M)["disabled_toolsets"].([]any)
	if !slices.Contains(disabled, "skills") {
		t.Fatalf("skills toolset left the denylist under a grant: %v", disabled)
	}
	for platform, list := range cfg["platform_toolsets"].(M) {
		if slices.Contains(list.([]any), "skills") {
			t.Fatalf("skills toolset emitted for platform %s", platform)
		}
	}
	if cfg["curator"].(M)["enabled"] != false {
		t.Fatal("unmanaged render must pin the curator off")
	}
	if _, err := os.Stat(filepath.Join(d, "hermes", ".no-bundled-skills")); err != nil {
		t.Fatalf("bundled-skills marker missing after render: %v", err)
	}
}

// The hub-owned browser sidecar is infrastructure like the model relay: it
// renders directly into managed mcp_servers under the same governance gate
// the unmanaged path uses (hub:browser / hub:browser_guest), never through a
// model-selectable connector. Read tools ship by default; mutation tools
// stay behind the explicit browser_act capability.
func TestManagedRenderBrowserServer(t *testing.T) {
	base := Settings{Schema: 1, Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, User: "alice", Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}

	s := base
	s.Tools = map[string]ToolEntry{"browser": {Via: ToolViaMCP}}
	servers := Config(s)["mcp_servers"].(M)
	persistent, ok := servers["browser"].(M)
	if !ok || persistent["url"] != "http://browser-alice:8931/mcp" {
		t.Fatalf("managed browser server missing or wrong: %v", servers)
	}
	guest, ok := servers["browser_guest"].(M)
	if !ok || guest["url"] != "http://browser-alice:8932/mcp" {
		t.Fatalf("managed guest browser server missing or wrong: %v", servers)
	}
	for _, tool := range []string{"browser_navigate", "browser_take_screenshot", "browser_snapshot"} {
		if !slices.Contains(persistent["tools"].(MCPTools).Include, tool) {
			t.Fatalf("managed browser lacks read tool %s", tool)
		}
	}
	for _, tool := range browserMutationTools {
		if slices.Contains(persistent["tools"].(MCPTools).Include, tool) {
			t.Fatalf("mutation tool %s projected without browser_act", tool)
		}
	}

	// browser_act widens the include list on both endpoints.
	act := base
	act.Tools = map[string]ToolEntry{"browser": {Via: ToolViaMCP, Tools: map[string]bool{"act": true}}}
	actServers := Config(act)["mcp_servers"].(M)
	if !slices.Contains(actServers["browser"].(M)["tools"].(MCPTools).Include, "browser_click") {
		t.Fatal("browser_act did not admit mutation tools")
	}
	if !slices.Contains(actServers["browser_guest"].(M)["tools"].(MCPTools).Include, "browser_click") {
		t.Fatal("browser_act did not admit guest mutation tools")
	}

	// Governance denies are honored the same way as the unmanaged render:
	// a denied section emits no server at all.
	denied := base
	denied.Tools = map[string]ToolEntry{"browser": {Via: ToolViaMCP}}
	denied.gov = &toolGovernance{decisions: map[string]api.PolicyDecision{
		api.SectionHubBrowser: {Effect: api.RuleDeny},
		"hub:browser_guest":   {Effect: api.RuleDeny},
	}}
	if servers := Config(denied)["mcp_servers"].(M); len(servers) != 0 {
		t.Fatalf("denied browser section still rendered: %v", servers)
	}
	partial := denied
	partial.gov = &toolGovernance{decisions: map[string]api.PolicyDecision{
		api.SectionHubBrowser: {Effect: api.RuleDeny},
		"hub:browser_guest":   {Effect: api.RuleAllow},
	}}
	partialServers := Config(partial)["mcp_servers"].(M)
	if partialServers["browser"] != nil || partialServers["browser_guest"] == nil {
		t.Fatalf("per-section governance not honored: %v", partialServers)
	}

	// No browser wish: managed mcp_servers stays empty (toolhub is injected
	// at materialization).
	empty := Config(base)["mcp_servers"].(M)
	if len(empty) != 0 {
		t.Fatalf("zero-wish render gained servers: %v", empty)
	}
}

// The materialization attestation must accept the rendered browser server:
// the grant rides MaterializeOptions.Browser so the in-container
// reconstruction emits the identical server set.
func TestManagedBrowserMaterializeAndAttest(t *testing.T) {
	s := Settings{Schema: 1, Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, User: "alice", Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Tools: map[string]ToolEntry{"browser": {Via: ToolViaMCP, Tools: map[string]bool{"act": true}}}}
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
	opts := MaterializeOptions{Managed: true, ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true,
		Browser: BrowserGrant{Persistent: true, Guest: true, Act: true}, User: "alice"}
	dest := filepath.Join(dir, "effective.yaml")
	if err := MaterializeHermesConfig(source, dest, opts); err != nil {
		t.Fatalf("browser materialize: %v", err)
	}
	if err := ValidateManagedEffectiveConfig(dest, s, opts); err != nil {
		t.Fatalf("browser effective config rejected: %v", err)
	}
	var effective M
	raw, _ := os.ReadFile(dest)
	if err := yaml.Unmarshal(raw, &effective); err != nil {
		t.Fatal(err)
	}
	servers, _ := effective["mcp_servers"].(map[string]any)
	if servers["browser"] == nil || servers["browser_guest"] == nil || servers["toolhub"] == nil {
		t.Fatalf("effective config lost managed servers: %v", servers)
	}

	// A grant narrower than the render is unreviewed drift: the attestation
	// rebuilds the same zero-template and must reject the widening.
	shrunk := opts
	shrunk.Browser = BrowserGrant{}
	if err := ValidateManagedEffectiveConfig(dest, s, shrunk); err == nil {
		t.Fatal("effective browser surface accepted under a revoked grant")
	}
}

// The spawned managed runtime reconstructs the browser grant inside the
// container; it rides RuntimeService env as JSON exactly like
// HUB_MANAGED_IMAGE_GEN, so the in-container preflight emits the identical
// server set without consulting agent-editable state.
func TestManagedRuntimeEnvCarriesBrowserGrant(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080,
		Tools: map[string]ToolEntry{"browser": {Via: ToolViaMCP, Tools: map[string]bool{"act": true}}}}
	env := RuntimeService(s, t.TempDir(), t.TempDir())["environment"].(M)
	var grant BrowserGrant
	raw, ok := env["HUB_MANAGED_BROWSER"].(string)
	if !ok {
		t.Fatalf("HUB_MANAGED_BROWSER absent from managed runtime env: %v", env)
	}
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		t.Fatalf("HUB_MANAGED_BROWSER is not the grant JSON: %v", err)
	}
	if grant != (BrowserGrant{Persistent: true, Guest: true, Act: true}) {
		t.Fatalf("runtime browser grant mismatch: %+v", grant)
	}

	// A denied guest section reaches the container as grant data, not as a
	// rendered server the agent could widen.
	gated := s
	gated.gov = &toolGovernance{decisions: map[string]api.PolicyDecision{
		"hub:browser_guest": {Effect: api.RuleDeny},
	}}
	env = RuntimeService(gated, t.TempDir(), t.TempDir())["environment"].(M)
	grant = BrowserGrant{}
	if err := json.Unmarshal([]byte(env["HUB_MANAGED_BROWSER"].(string)), &grant); err != nil {
		t.Fatal(err)
	}
	if grant != (BrowserGrant{Persistent: true, Act: true}) {
		t.Fatalf("denied guest leaked into runtime grant: %+v", grant)
	}

	// No browser wish: no grant rides the env.
	plain := s
	plain.Tools = nil
	env = RuntimeService(plain, t.TempDir(), t.TempDir())["environment"].(M)
	if env["HUB_MANAGED_BROWSER"] != nil {
		t.Fatalf("browser grant rendered without a wish: %v", env["HUB_MANAGED_BROWSER"])
	}
}

// The RenderEnvironment materialization is the same attestation boundary the
// supervisor walks: a browser-wish space must render an effective config that
// carries the sidecar servers end-to-end, or the runtime attestation rejects
// its own reviewed render.
func TestManagedRenderEnvironmentCarriesBrowserGrant(t *testing.T) {
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
	settings.Tools = map[string]ToolEntry{"browser": {Via: ToolViaMCP, Tools: map[string]bool{"act": true}}}
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
		t.Fatalf("managed browser render: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "generated", "hermes-effective.dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"browser-alice:8931/mcp", "browser-alice:8932/mcp", "toolhub"} {
		if !strings.Contains(text, want) {
			t.Fatalf("effective render missing %q:\n%s", want, text)
		}
	}
	runtimeEnv, err := os.ReadFile(filepath.Join(dir, "managed-runtime.dev.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runtimeEnv), "HUB_TOOLHUB_ENDPOINT=") {
		t.Fatal("managed runtime env lost the ToolHub endpoint")
	}
}
