package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if fmt.Sprint(env["OK_ALLOW_EXTERNAL"]) != "1" {
		t.Fatalf("ok must bind its container interface for the private net: %v", svc["environment"])
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

// R8.5: feature off → zero render effects; and even when on, the hub-side
// flag never lands on the agent's native toolset list.
func TestOKDisabledRendersNothing(t *testing.T) {
	services, _ := knowledgeSpace(t)
	if _, ok := services["open-knowledge"]; ok {
		t.Fatal("open-knowledge rendered with the feature off")
	}
	// On: the pseudo toolset is a hub subsystem flag, not an agent toolset.
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
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
