package stack

import (
	"fmt"
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

// M32/#520: knowledge_ui stays a valid feature but emits no data plane —
// ToolHub owns the vault and upstream OpenKnowledge cannot act as a governed
// client, so neither the vault-mounting service nor the runtime MCP route may
// render (ADR-0095 D2; SPEC-0098 #520: no direct vault access, no bypass
// routes, explicitly unavailable until adapted).
func TestKnowledgeUIRendersNoDataPlane(t *testing.T) {
	services, s := knowledgeSpace(t, "knowledge_ui")
	if _, ok := services["open-knowledge"]; ok {
		t.Fatalf("open-knowledge service must not render under ToolHub vault ownership: %#v", services["open-knowledge"])
	}
	for name, svc := range services {
		for _, vol := range svc.(M)["volumes"].([]any) {
			target := fmt.Sprint(vol.(M)["target"])
			if target == "/data" {
				t.Fatalf("service %s must not mount the vault as /data: %v", name, vol)
			}
		}
	}
	s.UIPort = 8080
	if err := s.Validate(); err != nil {
		t.Fatalf("ui_port config stays valid with no edge rendered: %v", err)
	}
}

// M32/#520: the runtime mcp_servers map never carries the open_knowledge
// bypass route — neither the unmanaged render nor the managed render may
// register a direct dial the ToolHub boundary never admitted.
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
	s.CapabilityMode = "managed"
	if _, ok := managedMCPServers(s)["open_knowledge"]; ok {
		t.Fatalf("managed render must not emit open_knowledge: %v", managedMCPServers(s))
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

// M32/#520: feature off → zero render effects, identical to feature on under
// the retired data plane — the hub-side flag never lands on the agent's
// native toolset list either way.
func TestKnowledgeUIRendersNothingWhenOff(t *testing.T) {
	services, _ := knowledgeSpace(t)
	if _, ok := services["open-knowledge"]; ok {
		t.Fatal("feature off must not render open-knowledge")
	}
}

// native:knowledge_ui stays an inventoried deny-default section — existing
// grants keep resolving to an explicit deny/allow decision instead of an
// unknown-section diagnostic, and the inventory is ready the day a
// governed-API knowledge surface returns.
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

// M32/#520: doctor is the status surface — with the data plane retired it
// must say the feature is unavailable, never imply a live editor edge.
func TestDoctorReportsUnavailable(t *testing.T) {
	s := Settings{Tools: testTools("knowledge_ui", "memory_vault")}
	lines := KnowledgeDoctor(s)
	if len(lines) != 1 || !strings.Contains(lines[0], "unavailable") {
		t.Fatalf("knowledge_ui on must report explicit unavailability: %v", lines)
	}
	if lines := KnowledgeDoctor(Settings{}); lines != nil {
		t.Fatalf("feature off still reports nothing: %v", lines)
	}
}

// R10.1/red-team: a non-loopback bind would put an unauthenticated editor on
// the LAN — every spelling of a wider bind stays a config validation error
// even though no edge currently renders.
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
	base := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
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
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1", UIPort: 8443, UIExpose: "named-edge",
		Tools: testTools("knowledge_ui", "memory_vault"), Ingress: testIngress()}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "M18") {
		t.Fatalf("ui_expose must fail as reserved-for-M18, got %v", err)
	}
}
