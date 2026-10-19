package api

import (
	"strings"
	"testing"
)

// S8.1: a browser workload declares egress class "web" — arbitrary HTTPS
// through the per-workload proxy with a deny list of private, loopback,
// link-local, metadata and hub-internal destinations.
func TestWebEgressClassValidation(t *testing.T) {
	base := func() ToolDefinition {
		return ToolDefinition{
			Schema: SchemaVersion, DefinitionID: "browser", Version: "1.0.0", Transport: ContainerMCP,
			Source:    DefinitionSource{Image: "ghcr.io/o/browser", Digest: "sha256:" + strings.Repeat("a", 64)},
			Workload:  WorkloadPolicy{Class: PerUser, Stateful: true, Rationale: "browser profile", ToolHiveVersion: "v0.12.1", SidecarImages: []string{"ghcr.io/o/squid@sha256:" + strings.Repeat("b", 64)}},
			Execution: ExecutionPolicy{TimeoutSeconds: 300, OutputBytes: 8192, CPUMillis: 1000, MemoryMiB: 2048, MaxPIDs: 256, Egress: []string{"metadata.example.com"}, EgressClass: "web"},
			Tools:     []ToolSpec{{Name: "navigate", Effect: ReadEffect}},
			Health:    HealthProbe{Kind: "exec", Value: "true", TimeoutSeconds: 5},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("web egress class rejected: %v", err)
	}
	bogus := base()
	bogus.Execution.EgressClass = "unbounded"
	if err := bogus.Validate(); err == nil {
		t.Fatal("unknown egress class accepted")
	}
	remote := base()
	remote.Transport = RemoteMCP
	remote.Source = DefinitionSource{URL: "https://browser.example.com/mcp", TLSMode: "required"}
	remote.Health = HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5}
	if err := remote.Validate(); err == nil {
		t.Fatal("web egress on remote transport accepted")
	}
}

// S8.1: the web proxy config denies private, metadata and hub-internal
// destinations before the allow-all tail; deny order matters.
func TestWebProxyConfigDenyList(t *testing.T) {
	cfg, err := WebProxyConfig([]string{"ads.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	denyIdx := strings.Index(cfg, "http_access deny CONNECT web_deny")
	allowIdx := strings.Index(cfg, "http_access allow CONNECT")
	if denyIdx < 0 || allowIdx < 0 || denyIdx > allowIdx {
		t.Fatalf("deny must precede allow:\n%s", cfg)
	}
	for _, want := range []string{"10.0.0.0/8", "192.168.0.0/16", "169.254.169.254", "127.0.0.0/8", "localhost", "ads.example.com"} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("missing deny entry %q:\n%s", want, cfg)
		}
	}
}

// S8.1: a name that resolves to a private address (DNS rebinding) is denied
// by the dst ACL — Squid resolves at request time.
func TestWebProxyConfigRebindingFixture(t *testing.T) {
	cfg, err := WebProxyConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "web_deny_ip") || !strings.Contains(cfg, "deny CONNECT web_deny_ip") {
		t.Fatalf("dst ACL missing — rebound names would pass:\n%s", cfg)
	}
}

// S8.2: a web-class plan renders through the deny-first config while a
// bounded allowlist plan keeps the existing renderer; per-connection
// refinements narrow the allow tail and extend the deny list.
func TestProxyConfigForDispatch(t *testing.T) {
	web := ControllerPlan{Execution: ExecutionPolicy{EgressClass: EgressWeb, Egress: []string{"ads.example.com"}}}
	cfg, err := ProxyConfigFor(web)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "deny CONNECT web_deny") {
		t.Fatalf("web plan did not render deny-first config:\n%s", cfg)
	}
	bounded := ControllerPlan{Execution: ExecutionPolicy{Egress: []string{"api.slack.com"}}}
	cfg, err = ProxyConfigFor(bounded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg, "web_deny") || !strings.Contains(cfg, "acl hermes_0 dstdomain .api.slack.com") {
		t.Fatalf("bounded plan lost its allowlist:\n%s", cfg)
	}
	if _, err := ProxyConfigFor(ControllerPlan{EgressAllow: []string{"x.com"}, Execution: ExecutionPolicy{Egress: []string{"api.slack.com"}}}); err == nil {
		t.Fatal("per-connection refinements accepted on a bounded plan")
	}
}

// S8.2: connection-level allow narrows the tail to the named hosts; deny
// entries always win over allow entries.
func TestWebProxyConnectionRefinement(t *testing.T) {
	cfg, err := WebProxyConfigFor(nil, []string{"docs.example.com"}, []string{"evil.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "web_allow") || !strings.Contains(cfg, ".docs.example.com") {
		t.Fatalf("connection allow not rendered:\n%s", cfg)
	}
	if !strings.Contains(cfg, ".evil.example.com") {
		t.Fatalf("connection deny not rendered:\n%s", cfg)
	}
	denyIdx := strings.Index(cfg, "deny CONNECT web_deny")
	allowIdx := strings.Index(cfg, "allow CONNECT web_allow")
	if allowIdx < 0 || denyIdx > allowIdx {
		t.Fatal("deny must still precede the narrowed allow")
	}
	// A host listed in both is denied — deny lines come first.
	cfg, err = WebProxyConfigFor(nil, []string{"evil.example.com"}, []string{"evil.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(cfg, "deny CONNECT web_deny") > strings.Index(cfg, "allow CONNECT web_allow") {
		t.Fatal("deny lost to allow ordering")
	}
	if _, err := WebProxyConfigFor(nil, []string{"host\ninject"}, nil); err == nil {
		t.Fatal("newline injection accepted")
	}
}

// S8.2: connection metadata lists are bounded and syntax-checked at
// admission — a malformed egress_allow/egress_deny value fails the record.
func TestConnectionEgressMetadataValidation(t *testing.T) {
	conn := func(meta map[string]string) Connection {
		return Connection{
			Schema: SchemaVersion, ConnectionID: "conn-01", DefinitionID: "browser",
			Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, Revision: 1,
			Status: ActiveStatus, Metadata: meta,
		}
	}
	if err := conn(map[string]string{"egress_allow": "docs.example.com, api.example.com:443"}).Validate(); err != nil {
		t.Fatalf("valid allow list rejected: %v", err)
	}
	for _, bad := range []string{"docs.example.com, ", "host\nname", strings.Repeat("a.example.com,", 40) + "x.example.com"} {
		if err := conn(map[string]string{"egress_deny": bad}).Validate(); err == nil {
			t.Fatalf("bad deny list %q accepted", bad)
		}
	}
}

// S8.2: the controller plan carries the connection's egress refinement only
// for web-class workloads.
func TestControllerPlanCarriesEgressRefinement(t *testing.T) {
	d := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "browser", Version: "1.0.0", Transport: ContainerMCP,
		Source:    DefinitionSource{Image: "ghcr.io/o/browser", Digest: "sha256:" + strings.Repeat("a", 64)},
		Workload:  WorkloadPolicy{Class: PerUser, Stateful: true, Rationale: "browser profile", ToolHiveVersion: "v0.12.1", SidecarImages: []string{"ghcr.io/o/squid@sha256:" + strings.Repeat("b", 64)}},
		Execution: ExecutionPolicy{TimeoutSeconds: 300, OutputBytes: 8192, CPUMillis: 1000, MemoryMiB: 2048, MaxPIDs: 256, EgressClass: EgressWeb},
		Tools:     []ToolSpec{{Name: "navigate", Effect: ReadEffect}},
		Health:    HealthProbe{Kind: "exec", Value: "true", TimeoutSeconds: 5},
	}
	plan, err := ControllerPlanFor(EffectiveBinding{
		WorkloadID: "w1", Definition: d,
		Connection: &Connection{Schema: SchemaVersion, ConnectionID: "conn-01", DefinitionID: "browser",
			Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, Revision: 1, Status: ActiveStatus,
			Metadata: map[string]string{"egress_allow": "docs.example.com", "egress_deny": "ads.example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.EgressAllow) != 1 || plan.EgressAllow[0] != "docs.example.com" || len(plan.EgressDeny) != 1 {
		t.Fatalf("plan refinements=%+v/%+v", plan.EgressAllow, plan.EgressDeny)
	}
	bad := d
	bad.Execution.EgressClass = ""
	plan, err = ControllerPlanFor(EffectiveBinding{
		WorkloadID: "w2", Definition: bad,
		Connection: &Connection{Schema: SchemaVersion, ConnectionID: "conn-02", DefinitionID: "browser",
			Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, Revision: 1, Status: ActiveStatus,
			Metadata: map[string]string{"egress_allow": "docs.example.com"}},
	})
	if err != nil || len(plan.EgressAllow) != 0 {
		t.Fatal("refinement carried on a bounded plan")
	}
}
