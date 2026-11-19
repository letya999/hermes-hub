package stack

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	s := Settings{CapabilityProfileID: "me-default", CapabilityGeneration: 1, Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC",
		BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model-relay:8318/v1",
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
// (/data) read-write — the editor's whole purpose is editing vault memory,
// and owner edits land as working-tree changes the CommitExternalEdits sweep
// commits as hermes-owner.
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

// R8.1/R8.5: no host port may ever publish without ui_port — the only edge
// is the opt-in loopback. The service is reachable only inside the
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
		t.Fatalf("the loopback publish pins 8080 — OK picks a dynamic port without PORT: %v", svc["environment"])
	}
	if fmt.Sprint(env["HOME"]) != "/tmp" {
		t.Fatalf("the image HOME is on the read-only rootfs — HOME must land on writable tmpfs: %v", svc["environment"])
	}
}

// R8.1/R8.5: ok resolves its server-authority registry through the passwd
// home (/state/home), not $HOME — on the read-only rootfs that mkdir fails
// closed and the service crash-loops, so the disposable home needs its own
// tmpfs alongside /tmp.
func TestOKWritablePasswdHome(t *testing.T) {
	services, _ := knowledgeSpace(t, "knowledge_ui")
	svc := services["open-knowledge"].(M)
	tmpfs, _ := svc["tmpfs"].([]string)
	found := false
	for _, mount := range tmpfs {
		if strings.HasPrefix(mount, "/state/home") {
			found = true
		}
	}
	if !found {
		t.Fatalf("open-knowledge must tmpfs /state/home — the passwd-home registry cannot mkdir on the read-only rootfs: %v", tmpfs)
	}
}

// The service joins only the space-private internal agent net. The shared
// hub net is never joined: nothing dials the service by name now that the
// runtime MCP route stays retired. A ui_port adds exactly one dedicated
// publishable network (the agent net is internal and drops published
// ports) — the browser-publish pattern, minus any alias.
func TestOKPrivateNetOnly(t *testing.T) {
	nets := func(exec string, uiPort int) M {
		s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
			Model: "test", ModelURL: "http://model.invalid/v1", ExecutionMode: exec, UIPort: uiPort,
			CapabilityProfileID: "managed-dev", CapabilityGeneration: 1,
			Tools: testTools("knowledge_ui", "memory_vault"), Ingress: testIngress()}
		svc := Compose(s, t.TempDir(), t.TempDir())["services"].(M)["open-knowledge"].(M)
		raw, _ := svc["networks"].(M)
		return raw
	}
	managed := nets("", 0)
	if len(managed) != 1 {
		t.Fatalf("managed mode without ui_port must join exactly one network: %v", managed)
	}
	for name := range managed {
		if !strings.HasPrefix(name, "hermes-hub-agent-") {
			t.Fatalf("managed mode must join only the internal agent network: %v", managed)
		}
	}
	published := nets("supervisor", 8443)
	if len(published) != 2 || published["knowledge-publish"] == nil {
		t.Fatalf("managed ui_port must add the publishable network to the agent net: %v", published)
	}
	rendered := Compose(Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1", CapabilityProfileID: "managed-dev", CapabilityGeneration: 1, UIPort: 8443,
		Tools: testTools("knowledge_ui", "memory_vault"), Ingress: testIngress()}, t.TempDir(), t.TempDir())
	def, ok := rendered["networks"].(M)["knowledge-publish"].(M)
	if !ok || def["internal"] == true {
		t.Fatalf("knowledge-publish must be a rendered non-internal network: %v", rendered["networks"])
	}
}

// OK's project root is the vault — upstream confines every path arg to that
// root, and the service mounts nothing else, so no OK call can reach a host
// path outside spaces/<id>/vault.
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

// R10.1: an opted-in ui_port publishes exactly one loopback binding and
// re-points the canonical external URL at the browser origin — never a
// non-loopback address, never a second port.
func TestUIPortLoopbackOnly(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1", UIPort: 8443,
		Tools: testTools("knowledge_ui", "memory_vault"), Ingress: testIngress()}
	svc := Compose(s, t.TempDir(), t.TempDir())["services"].(M)["open-knowledge"].(M)
	ports, _ := svc["ports"].([]string)
	if len(ports) != 1 || ports[0] != "127.0.0.1:8443:8080" {
		t.Fatalf("ui_port must publish exactly 127.0.0.1:<ui_port>:8080: %v", ports)
	}
	env := svc["environment"].(M)
	if env["OK_EXTERNAL_URL"] != "http://localhost:8443" {
		t.Fatalf("OK_EXTERNAL_URL must be the loopback browser origin under ui_port: %v", env)
	}

	// An explicit ::1 bind publishes on IPv6 loopback — the only other
	// address validation admits, so it must render rather than default.
	s.UIBind = "::1"
	svc = Compose(s, t.TempDir(), t.TempDir())["services"].(M)["open-knowledge"].(M)
	ports, _ = svc["ports"].([]string)
	if len(ports) != 1 || ports[0] != "[::1]:8443:8080" {
		t.Fatalf("ui_bind ::1 must publish [::1]:<ui_port>:8080: %v", ports)
	}
}

// R10.1/red-team: a non-loopback bind would put the unauthenticated editor
// on the LAN — every spelling of a wider bind is a config validation error.
func TestUIPortRejectsNonLoopback(t *testing.T) {
	for _, bind := range []string{"0.0.0.0", "192.168.1.10", "example.internal", "localhost"} {
		s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
			Model: "test", ModelURL: "http://model.invalid/v1", UIPort: 8443, UIBind: bind,
			Tools: testTools("knowledge_ui", "memory_vault"), Ingress: testIngress()}
		if err := s.Validate(); err == nil {
			t.Fatalf("ui_bind %q must be refused — only loopback may publish", bind)
		}
	}
}

// R10.1: ui_port is diagnosed exactly like the other host ports — out of
// range, colliding with browser/oauth/slack events ports, or declared
// without the feature it publishes are all validation errors, never a
// silent remap.
func TestUIPortCollisionDiagnostic(t *testing.T) {
	base := Settings{CapabilityProfileID: "me-default", CapabilityGeneration: 1, Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model-relay:8318/v1",
		Tools: testTools("knowledge_ui", "memory_vault"), Ingress: testIngress()}
	for _, port := range []int{80, 1023, 70000, 6080, 8000} {
		s := base
		s.UIPort = port
		if err := s.Validate(); err == nil {
			t.Fatalf("ui_port %d must be refused (range/collision)", port)
		}
	}
	s := base
	s.UIPort = 8443
	if err := s.Validate(); err != nil {
		t.Fatalf("a free ui_port must validate: %v", err)
	}
	s.Tools = testTools("memory_vault")
	if err := s.Validate(); err == nil {
		t.Fatal("ui_port without knowledge_ui must be refused — nothing would publish it")
	}
}

// R10.3: ui_expose is the reserved M18 edge key — setting it fails honestly
// today rather than silently doing nothing, and when M18 lands the same key
// drives the named edge service / one-time ?t= link without a vocabulary
// change.
func TestUIExposeReservedForM18(t *testing.T) {
	s := Settings{CapabilityProfileID: "me-default", CapabilityGeneration: 1, Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model-relay:8318/v1", UIPort: 8443, UIExpose: "named-edge",
		Tools: testTools("knowledge_ui", "memory_vault"), Ingress: testIngress()}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "M18") {
		t.Fatalf("ui_expose must fail as reserved-for-M18, got %v", err)
	}
}

// The runtime mcp_servers map never carries the open_knowledge bypass
// route — the human editor surface does not re-admit it (ADR-0095 D2
// amendment; #422 stays deferred). Neither the unmanaged render nor the
// managed render may register a direct dial.
func TestKnowledgeUINoRuntimeRoute(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools("knowledge_ui", "memory_vault"),
		Ingress: testIngress()}
	s.gov = &toolGovernance{decisions: map[string]api.PolicyDecision{
		"native:knowledge_ui": {Effect: api.RuleAllow},
	}}
	if servers := Config(s)["mcp_servers"].(M); len(servers) != 0 {
		if _, ok := servers["open_knowledge"]; ok {
			t.Fatalf("unmanaged render must not emit open_knowledge: %v", servers)
		}
	}
	if _, ok := managedMCPServers(s)["open_knowledge"]; ok {
		t.Fatalf("managed render must not emit open_knowledge: %v", managedMCPServers(s))
	}
}

// R9.2: the deferred write grant has no vocabulary — every tools spelling of
// `knowledge_ui_write` is refused at validation, which is the standard deny
// path the owner meets until per-path scoping lands upstream (#422).
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

// R8.5: feature off → zero render effects; and even when on, the hub-side
// flag never lands on the agent's native toolset list.
func TestOKDisabledRendersNothing(t *testing.T) {
	services, s := knowledgeSpace(t)
	if _, ok := services["open-knowledge"]; ok {
		t.Fatal("open-knowledge rendered with the feature off")
	}
	// Revocation is fail-closed — with the feature off no endpoint exists for
	// a direct MCP call to reach at all.
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

// A published stack passport declaring open-knowledge while the feature is
// off fails the declared-but-unrendered check — the declaration follows the
// wish, never the other way round.
func TestOKDeclaredButUnrendered(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools: testTools("memory_vault"), Ingress: testIngress(),
		PassportServices: map[string]string{"open-knowledge": "per-space"}}
	if _, err := ComposeChecked(s, t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "open-knowledge") {
		t.Fatalf("declared open-knowledge with the feature off must fail the check: %v", err)
	}
	s.Tools = testTools("knowledge_ui", "memory_vault")
	if _, err := ComposeChecked(s, t.TempDir(), t.TempDir()); err != nil {
		t.Fatalf("declared + feature on must render: %v", err)
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

// R10.2/R10.3: the runbook must document the loopback edge — the ssh tunnel
// form, the no-public-exposure default, and the reserved ui_expose key whose
// only contract today is the M18 one-time ?t= link design note.
func TestM18HookDocumented(t *testing.T) {
	data, err := os.ReadFile("../../docs/operations.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ui_port", "ssh -L", "ui_bind", "ui_expose", "M18", "?t="} {
		if !strings.Contains(string(data), want) {
			t.Errorf("operations.md lost the UI-edge/M18 clause %q", want)
		}
	}
}

// native:knowledge_ui must be an inventoried deny-default section — without
// it a real space's tools wish evaluates to "section not in the tool
// inventory" and can never be granted, and the first-Read migration seed has
// nothing to preserve.
func TestKnowledgeUIInventoried(t *testing.T) {
	for _, s := range api.SeedInventory() {
		if s.ID == "native:knowledge_ui" {
			if s.Default != api.RuleDeny {
				t.Fatalf("native:knowledge_ui must be deny-default like every pseudo entry, got %s", s.Default)
			}
			return
		}
	}
	t.Fatal("native:knowledge_ui missing from SeedInventory — the wish can never be admitted")
}

// R10.4/R10.2: doctor is the status surface — it must name the published
// edge (bind, port, tunnel hint), probe loopback reachability honestly, and
// refuse to bless a non-loopback bind even if one ever slipped past Validate.
func TestDoctorUIPortChecks(t *testing.T) {
	s := Settings{Tools: testTools("knowledge_ui", "memory_vault")}
	if lines := KnowledgeDoctor(s); lines == nil || !strings.Contains(lines[0], "network-only") {
		t.Fatalf("ui_port unset must report network-only: %v", lines)
	}
	s.UIBind = "0.0.0.0"
	s.UIPort = 8443
	if lines := KnowledgeDoctor(s); lines == nil || !strings.Contains(lines[0], "not loopback") {
		t.Fatalf("a non-loopback bind must be refused, not probed: %v", lines)
	}
	s.UIBind = ""
	lines := KnowledgeDoctor(s)
	if len(lines) == 0 || !strings.Contains(lines[0], "localhost:8443") {
		t.Fatalf("status must show the ui_port edge: %v", lines)
	}
	down := lines[len(lines)-1]
	if !strings.Contains(down, "nothing listening") {
		t.Fatalf("a free port must report the stack down, not imply health: %v", lines)
	}
}

// The reachability probe is real: something already bound on the declared
// port flips the last line to "listening" — TCP-level honesty, since a
// squatter is indistinguishable from the editor here (a true conflict fails
// loudly at compose publish). Feature off must report nothing at all.
func TestDoctorUIPortReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind loopback: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	s := Settings{Tools: testTools("knowledge_ui", "memory_vault"), UIPort: port}
	lines := KnowledgeDoctor(s)
	if last := lines[len(lines)-1]; !strings.Contains(last, "listening") {
		t.Fatalf("an occupied port must report listening: %v", lines)
	}
	if off := KnowledgeDoctor(Settings{UIPort: port}); off != nil {
		t.Fatalf("feature off must be silent: %v", off)
	}
}
