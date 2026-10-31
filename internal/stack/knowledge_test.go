package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// R8.1/R8.5: knowledge_ui is a registered hub-side feature that requires the
// memory vault — it cannot render a UI over nothing.
func TestKnowledgeUIFeature(t *testing.T) {
	found := false
	for _, f := range Features {
		if f.Name == "knowledge_ui" {
			found = true
		}
	}
	if !found {
		t.Fatal("knowledge_ui missing from the Features registry")
	}
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC",
		BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools: testTools("knowledge_ui"), Ingress: testIngress()}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "memory_vault") {
		t.Fatalf("knowledge_ui without memory_vault must be refused: %v", err)
	}
	s.Tools = testTools("knowledge_ui", "memory_vault")
	if err := s.Validate(); err != nil {
		t.Fatalf("knowledge_ui + memory_vault must validate: %v", err)
	}
}

// knowledgeSpace renders a knowledge_ui-enabled space's compose services.
func knowledgeSpace(t *testing.T, features ...string) (M, Settings) {
	t.Helper()
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools(append(features, "memory_vault")...),
		Ingress: testIngress()}
	dir := t.TempDir()
	return Compose(s, t.TempDir(), dir)["services"].(M), s
}

// R8.1/R8.5: the space renders exactly one open-knowledge replica on a pinned
// BuildKit-built image — scale-out is not a knob.
func TestOKRenderSingleReplica(t *testing.T) {
	services, s := knowledgeSpace(t, "knowledge_ui")
	svc, ok := services["open-knowledge"].(M)
	if !ok {
		t.Fatalf("open-knowledge service missing: %#v", services)
	}
	if svc["image"] != "hermes-hub:0.5.0-"+s.Environment+"-knowledge" {
		t.Fatalf("knowledge image must be the role tag: %v", svc["image"])
	}
	build, ok := svc["build"].(M)
	if !ok || build["target"] != s.Environment+"-knowledge" || build["dockerfile"] != "docker/Dockerfile" {
		t.Fatalf("knowledge image must build via the hub Dockerfile: %v", svc["build"])
	}
	deploy, ok := svc["deploy"].(M)
	if !ok || deploy["replicas"] != 1 {
		t.Fatalf("replicas must be pinned to 1: %v", svc["deploy"])
	}
	cmd := fmt.Sprint(svc["command"])
	if !strings.Contains(cmd, "ok init --no-mcp --no-skills") || !strings.Contains(cmd, "ok start") {
		t.Fatalf("first-boot init + start contract missing: %v", svc["command"])
	}
}

// R8.2: the service mounts the space vault as the OpenKnowledge project root
// (/data) read-write — the editor's whole purpose is editing vault memory.
func TestOKMountVaultRoot(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools("knowledge_ui", "memory_vault"),
		Ingress: testIngress()}
	dir := t.TempDir()
	svc := Compose(s, t.TempDir(), dir)["services"].(M)["open-knowledge"].(M)
	want := filepath.ToSlash(filepath.Join(dir, "vault"))
	volumes, ok := svc["volumes"].([]any)
	if !ok {
		t.Fatalf("open-knowledge mounts nothing: %v", svc)
	}
	found := false
	for _, raw := range volumes {
		v, _ := raw.(M)
		if v["type"] == "bind" && v["source"] == want && v["target"] == "/data" {
			found = true
			if v["read_only"] == true {
				t.Fatal("vault mount must be writable — the editor edits it")
			}
		}
	}
	if !found {
		t.Fatalf("vault bind %s → /data missing: %v", want, svc["volumes"])
	}
}

// R8.1/R8.5: no host port may ever publish — the UI edge is a later stage
// and must opt in explicitly; the service is reachable only inside the
// space-private network (OK binds its container interface via
// OK_ALLOW_EXTERNAL, which exposes nothing past that network).
func TestOKNoPublishedPorts(t *testing.T) {
	services, _ := knowledgeSpace(t, "knowledge_ui")
	svc := services["open-knowledge"].(M)
	if ports, ok := svc["ports"]; ok && len(ports.([]string)) > 0 {
		t.Fatalf("open-knowledge must publish no host ports: %v", ports)
	}
	env, _ := svc["environment"].(M)
	if fmt.Sprint(env["OK_ALLOW_EXTERNAL"]) != "1" || fmt.Sprint(env["OK_BIND"]) != "0.0.0.0" {
		t.Fatalf("ok must bind its container interface for the private net: %v", svc["environment"])
	}
	if fmt.Sprint(env["PORT"]) != "8080" {
		t.Fatalf("the MCP URL pins 8080 — OK picks a dynamic port without PORT: %v", svc["environment"])
	}
	if fmt.Sprint(env["HOME"]) != "/tmp" {
		t.Fatalf("the image HOME is on the read-only rootfs — HOME must land on writable tmpfs: %v", svc["environment"])
	}
}

// R8.1/R8.5: the service joins only the space-private network — the project
// default net in static mode, the internal agent net in managed mode — never
// the shared hub net or a publishable net. It keeps the DNS alias
// `open-knowledge` so the M20.9 MCP URL resolves on both shapes.
func TestOKPrivateNetOnly(t *testing.T) {
	nets := func(mode string) M {
		s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
			Model: "test", ModelURL: "http://model.invalid/v1", CapabilityMode: mode,
			Tools: testTools("knowledge_ui", "memory_vault"), Ingress: testIngress()}
		if mode == "managed" {
			s.CapabilityProfileID = "managed-dev"
			s.CapabilityGeneration = 1
		}
		svc := Compose(s, t.TempDir(), t.TempDir())["services"].(M)["open-knowledge"].(M)
		raw, _ := svc["networks"].(M)
		return raw
	}
	static := nets("")
	if len(static) != 1 {
		t.Fatalf("static mode must join exactly one network: %v", static)
	}
	if _, ok := static["default"]; !ok {
		t.Fatalf("static mode must join the project default network: %v", static)
	}
	managed := nets("managed")
	if len(managed) != 1 {
		t.Fatalf("managed mode must join exactly one network: %v", managed)
	}
	for name := range managed {
		if !strings.HasPrefix(name, "hermes-hub-agent-") {
			t.Fatalf("managed mode must join only the internal agent network: %v", managed)
		}
	}
}

// R8.3: the external-write coexistence contract is operator-visible in
// docs/operations.md — the spike-verified OK behavior (stale save rejected,
// conflicts surfaced, nothing silently overwritten, owner wins rollup) is
// the policy the vault writer relies on.
func TestExternalWritePolicyDocumented(t *testing.T) {
	data, err := os.ReadFile("../../docs/operations.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stale save", ".ok/local/conflicts.json", "silent overwrite"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("operations.md lost the external-write policy clause %q", want)
		}
	}
}

// R9.1: the projected read surface is the tool list verified against the
// pinned @inkeep/open-knowledge@0.88.2 bundle — eight read-only verbs. `lint`
// is absent because its fix argument mutates and tools.include filters by
// name only; `checkpoint` and `share_link` are absent because upstream labels
// them readOnlyHint while persisting git history / minting a share URL.
func TestOKToolListVerified(t *testing.T) {
	want := []string{"audit", "config", "exec", "history", "links", "palette", "search", "skills"}
	if !slices.Equal(okReadTools, want) {
		t.Fatalf("verified read surface drifted: got %v want %v", okReadTools, want)
	}
	for _, tool := range okReadTools {
		if slices.Contains(okMutationTools, tool) {
			t.Fatalf("%s sits in both the read surface and the mutation set", tool)
		}
	}
}

// R9.1: every verb the pinned server registers is classified — the read and
// mutation lists partition the 19-tool surface, so a rename or an unnoticed
// new verb cannot slip through unadmitted or undeclared.
func TestOKSideChannelExcluded(t *testing.T) {
	verified := []string{"audit", "checkpoint", "config", "delete", "edit",
		"exec", "history", "import", "install", "links", "lint", "move",
		"palette", "preview_url", "restore_version", "search", "share_link",
		"skills", "write"}
	all := append(slices.Clone(okReadTools), okMutationTools...)
	slices.Sort(all)
	if !slices.Equal(all, verified) {
		t.Fatalf("tool classification drifted from the 0.88.2 surface: got %v want %v", all, verified)
	}
}

// R9.1/R9.3: the knowledge_ui grant projects exactly the verified read
// surface as an mcp_servers include list — the space-private service URL, a
// bounded timeout, no write verb, no stdio shape.
func TestReadGrantSurfaceExact(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools("knowledge_ui", "memory_vault"),
		Ingress: testIngress()}
	server, ok := Config(s)["mcp_servers"].(M)["open_knowledge"].(M)
	if !ok {
		t.Fatal("knowledge_ui must render mcp_servers.open_knowledge")
	}
	if server["url"] != "http://open-knowledge:8080/mcp" {
		t.Fatalf("read grant must hit the space-private OK endpoint: %v", server["url"])
	}
	if server["command"] != nil || server["args"] != nil {
		t.Fatalf("the grant must be a URL server, never stdio: %v", server)
	}
	tools, ok := server["tools"].(MCPTools)
	if !ok || !slices.Equal(tools.Include, okReadTools) || len(tools.Exclude) > 0 {
		t.Fatalf("the projected surface must be exactly the verified include list: %v", server["tools"])
	}
}

// R9.1/R9.2: managed mode projects the same hub-owned endpoint through
// managedMCPServers, and the feature's governance section denies it the same
// way — a denied native:knowledge_ui emits no server entry at all.
func TestManagedRenderKnowledgeServer(t *testing.T) {
	base := Settings{Schema: 1, Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, User: "alice", Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}

	s := base
	s.Tools = testTools("knowledge_ui", "memory_vault")
	server, ok := Config(s)["mcp_servers"].(M)["open_knowledge"].(M)
	if !ok || server["url"] != "http://open-knowledge:8080/mcp" {
		t.Fatalf("managed OK server missing or wrong: %v", Config(s)["mcp_servers"])
	}
	if tools := server["tools"].(MCPTools); !slices.Equal(tools.Include, okReadTools) {
		t.Fatalf("managed read surface drifted: %v", tools.Include)
	}

	denied := base
	denied.Tools = testTools("knowledge_ui", "memory_vault")
	denied.gov = &toolGovernance{decisions: map[string]api.PolicyDecision{
		"native:knowledge_ui": {Effect: api.RuleDeny},
	}}
	if servers := Config(denied)["mcp_servers"].(M); len(servers) != 0 {
		t.Fatalf("denied native:knowledge_ui still rendered: %v", servers)
	}

	if servers := Config(base)["mcp_servers"].(M); len(servers) != 0 {
		t.Fatalf("zero-wish render gained servers: %v", servers)
	}
}

// R9.2: a write verb reaches the runtime only through the include list — the
// projected surface registers none of them, so a direct MCP call or a model
// guess dies as an unregistered tool, which is the deny receipt. No
// write-side grant exists to leak through another render field either.
func TestWriteVerbDeniedDirect(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools("knowledge_ui", "memory_vault"),
		Ingress: testIngress()}
	server := Config(s)["mcp_servers"].(M)["open_knowledge"].(M)
	include := server["tools"].(MCPTools).Include
	for _, verb := range okMutationTools {
		if slices.Contains(include, verb) {
			t.Fatalf("write verb %s projected onto the read surface", verb)
		}
	}
	for field := range server {
		if field != "url" && field != "timeout" && field != "tools" && field != "skip_preflight" {
			t.Fatalf("unexpected field %q on the read grant — nothing else may ride it", field)
		}
	}
}

// R9.2: the deferred write grant has no vocabulary — every tools spelling of
// `knowledge_ui_write` is refused at validation, which is the standard deny
// path the owner meets until per-path scoping lands upstream.
func TestWriteDenyReceipt(t *testing.T) {
	for name, via := range map[string]string{"native": ToolViaNative, "toolhub": ToolViaToolHub, "mcp": ToolViaMCP} {
		s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
			Model: "test", ModelURL: "http://model.invalid/v1",
			Tools:   map[string]ToolEntry{"knowledge_ui_write": {Via: via}, "memory_vault": {Via: ToolViaNative}, "knowledge_ui": {Via: ToolViaNative}},
			Ingress: testIngress()}
		if err := s.Validate(); err == nil {
			t.Fatalf("knowledge_ui_write via %s must be refused — the grant is deferred", name)
		}
	}
}

// R9.2/R9.4: revoking the feature's governance section removes the endpoint
// on the unmanaged render exactly like the managed one — no projection, no
// direct path, fail-closed.
func TestOKGovernanceDenyRemovesServer(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools("knowledge_ui", "memory_vault"),
		Ingress: testIngress()}
	s.gov = &toolGovernance{decisions: map[string]api.PolicyDecision{
		"native:knowledge_ui": {Effect: api.RuleDeny},
	}}
	if servers := Config(s)["mcp_servers"].(M); servers["open_knowledge"] != nil {
		t.Fatalf("denied native:knowledge_ui still rendered: %v", servers)
	}
}

// R9.3: a down OK must degrade honestly — the runtime boots and
// memory_search/memory_card keep working; the failed call surfaces the real
// connection error rather than a preflight stall or a faked answer.
func TestOKDownDegradedHonest(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools("knowledge_ui", "memory_vault"),
		Ingress: testIngress()}
	server := Config(s)["mcp_servers"].(M)["open_knowledge"].(M)
	if server["skip_preflight"] != true {
		t.Fatalf("a down OK must not stall runtime boot — skip_preflight required: %v", server)
	}
}

// R9.4: OK's project root is the vault — upstream confines every path arg to
// that root, and the service mounts nothing else, so no OK call can reach a
// host path outside spaces/<id>/vault.
func TestOKPathConfined(t *testing.T) {
	services, _ := knowledgeSpace(t, "knowledge_ui")
	svc := services["open-knowledge"].(M)
	volumes, _ := svc["volumes"].([]any)
	for _, raw := range volumes {
		vol, _ := raw.(M)
		if vol["target"] != "/data" {
			t.Fatalf("OK mounts more than the vault project root: %v", vol)
		}
		if !strings.HasSuffix(filepath.ToSlash(fmt.Sprint(vol["source"])), "/vault") {
			t.Fatalf("OK project root is not the space vault: %v", vol)
		}
	}
}

// R8.5: feature off → zero render effects; and even when on, the hub-side
// flag never lands on the agent's native toolset list.
func TestOKDisabledRendersNothing(t *testing.T) {
	services, s := knowledgeSpace(t)
	if _, ok := services["open-knowledge"]; ok {
		t.Fatal("open-knowledge rendered with the feature off")
	}
	// R9.4: revocation is fail-closed — with the feature off no endpoint
	// exists for a direct MCP call to reach at all.
	if server := Config(s)["mcp_servers"].(M)["open_knowledge"]; server != nil {
		t.Fatalf("feature-off render still exposes the OK endpoint: %v", server)
	}
	// On: the pseudo toolset is a hub subsystem flag, not an agent toolset.
	s = Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools("knowledge_ui", "memory_vault"),
		Ingress: testIngress()}
	cfg := Config(s)
	for channel, raw := range cfg["platform_toolsets"].(M) {
		for _, name := range raw.([]string) {
			if name == "knowledge_ui" {
				t.Fatalf("knowledge_ui must never render as an agent toolset (channel %s)", channel)
			}
		}
	}
}
