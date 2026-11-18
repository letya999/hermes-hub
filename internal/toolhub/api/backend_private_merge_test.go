package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The private classifier admits only loopback and RFC-1918/ULA destinations:
// public, link-local (cloud metadata), CGNAT and reserved space all refuse —
// a credential-bearing hop can never land there.
func TestBackendAddrAllowedClassifier(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.5.4", "172.31.255.1", "192.168.9.1", "fc00::1", "fd12:3456::1", "::ffff:10.0.0.1"} {
		ip, err := netip.ParseAddr(raw)
		if err != nil || !BackendAddrAllowed(ip) {
			t.Fatalf("private %q refused: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"8.8.8.8", "1.1.1.1", "169.254.169.254", "169.254.1.1", "100.64.0.1",
		"192.0.2.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "0.0.0.0",
		"255.255.255.255", "fe80::1", "2001:4860:4860::8888", "172.15.0.1", "172.32.0.1",
	} {
		ip, _ := netip.ParseAddr(raw)
		if ip.IsValid() && BackendAddrAllowed(ip) {
			t.Fatalf("non-private %q admitted", raw)
		}
	}
	var invalid netip.Addr
	if BackendAddrAllowed(invalid) {
		t.Fatal("invalid addr admitted")
	}
}

// BackendDial re-resolves at dial time: a name that flips to a public answer
// after registration is refused before any packet leaves.
func TestBackendDialFence(t *testing.T) {
	orig := ResolveRemoteIP
	defer func() { ResolveRemoteIP = orig }()
	ResolveRemoteIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "rebound.example":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		case "meta.example":
			return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
		case "mixed.example":
			return []netip.Addr{netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("8.8.8.8")}, nil
		case "nx.example":
			return nil, errors.New("nxdomain")
		}
		return nil, nil
	}
	if _, err := BackendDial(context.Background(), "tcp", "rebound.example:443"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("rebound-public dial admitted: %v", err)
	}
	if _, err := BackendDial(context.Background(), "tcp", "meta.example:443"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("metadata dial admitted: %v", err)
	}
	if _, err := BackendDial(context.Background(), "tcp", "8.8.8.8:443"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("public literal dial admitted: %v", err)
	}
	if _, err := BackendDial(context.Background(), "tcp", "nx.example:443"); err == nil {
		t.Fatal("unresolvable dial succeeded")
	}
	// A mixed answer still dials: the private half is reachable, the public
	// half is filtered out before dialing.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	ResolveRemoteIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := BackendDial(ctx, "tcp", "filtered.example:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("private half of mixed answer refused: %v", err)
	}
	conn.Close()
}

// CanonicalBackendURL folds scheme+host case and keeps port/path — nothing
// else — so a connection cannot smuggle credentials, a different query or a
// rewritten endpoint past the immutable pin.
func TestCanonicalBackendURL(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ in, want string }{
		{"http://127.0.0.1:8080/mcp", "http://127.0.0.1:8080/mcp"},
		{"HTTP://LOCALHOST:9000/MCP", "http://localhost:9000/MCP"},
		{"https://10.0.0.2/x", "https://10.0.0.2/x"},
	} {
		got, err := CanonicalBackendURL(test.in)
		if err != nil || got != test.want {
			t.Fatalf("%q → %q, %v", test.in, got, err)
		}
	}
	for _, bad := range []string{
		"http://user:pw@127.0.0.1:8080/mcp", "http://127.0.0.1:8080/mcp?x=1",
		"http://127.0.0.1:8080/mcp#frag", "ftp://127.0.0.1/", "http://", "", "not a url",
	} {
		if got, err := CanonicalBackendURL(bad); err == nil {
			t.Fatalf("%q canonicalized to %q", bad, got)
		}
	}
}

// The operator-pinned source relaxes exactly the scheme requirement — http is
// legal on a private hop — but keeps every other cleanliness rule, and only
// for RemoteMCP.
func TestOperatorPinnedSourceContract(t *testing.T) {
	t.Parallel()
	good := DefinitionSource{URL: "http://127.0.0.1:8080/mcp", TLSMode: TLSModeOperatorPinned}
	if err := good.Validate(RemoteMCP); err != nil {
		t.Fatalf("pinned http source refused: %v", err)
	}
	for _, u := range []string{
		"http://user:pw@127.0.0.1/mcp", "http://127.0.0.1/mcp?q=1",
		"http://127.0.0.1/mcp#f", "ftp://127.0.0.1/mcp", "http://",
	} {
		if err := (DefinitionSource{URL: u, TLSMode: TLSModeOperatorPinned}).Validate(RemoteMCP); err == nil {
			t.Fatalf("%q admitted as operator-pinned source", u)
		}
	}
	// Not a RemoteMCP escape hatch: provider definitions still demand required TLS.
	if err := good.Validate(ProviderAPI); err == nil {
		t.Fatal("operator-pinned leaked into provider validation")
	}
	if err := (DefinitionSource{URL: "http://127.0.0.1/mcp", TLSMode: "required"}).Validate(RemoteMCP); err == nil {
		t.Fatal("cleartext remote source admitted without the pinned mode")
	}
}

// BackendMCPDefinition freezes the probed tool set, pins egress to the
// endpoint's own host:port and refuses both public destinations and an
// allowlist wider than the advertisement.
func TestBackendMCPDefinition(t *testing.T) {
	t.Parallel()
	endpoint := "http://127.0.0.1:8080/mcp"
	tools := []ToolSpec{
		{Name: "search", Effect: ReadEffect},
		{Name: "create_issue", Effect: WriteEffect},
	}
	creds := []CredentialInput{{Name: "REMOTE_BEARER", Required: true, Delivery: "http_header", Target: "Authorization", Prefix: "Bearer "}}
	def, err := BackendMCPDefinition(endpoint, "corp-tools", creds, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if def.DefinitionID != "remote-corp-tools" || def.Transport != RemoteMCP || def.Source.TLSMode != TLSModeOperatorPinned {
		t.Fatalf("definition shape: %+v", def)
	}
	if def.Source.URL != endpoint || len(def.Tools) != 2 || len(def.Execution.Egress) != 1 || def.Execution.Egress[0] != "127.0.0.1:8080" {
		t.Fatalf("pin/egress: %+v", def)
	}
	narrow, err := BackendMCPDefinition(endpoint, "corp-narrow", nil, tools, []string{"search"})
	if err != nil || len(narrow.Tools) != 1 || narrow.Tools[0].Name != "search" {
		t.Fatalf("narrowed allowlist: %v %+v", err, narrow.Tools)
	}
	if _, err := BackendMCPDefinition(endpoint, "corp-widen", nil, tools, []string{"missing_tool"}); err == nil {
		t.Fatal("allowlist wider than the probe admitted")
	}
	if _, err := BackendMCPDefinition("https://8.8.8.8/mcp", "corp-pub", nil, tools, nil); err == nil {
		t.Fatal("public destination admitted into a private-pinned definition")
	}
	if _, err := BackendMCPDefinition(endpoint, "corp-empty", nil, nil, nil); err == nil {
		t.Fatal("empty tool set admitted")
	}
}

// privateMCPFixture is a real MCP server on loopback: BackendDial admits
// 127.0.0.1 directly, so the shipped probe and call paths run unstubbed.
type privateMCPFixture struct {
	URL      string
	lastAuth atomic.Value
}

func newPrivateMCPFixture(t *testing.T, requiredToken string) *privateMCPFixture {
	t.Helper()
	fixture := &privateMCPFixture{}
	server := mcp.NewServer(&mcp.Implementation{Name: "private-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "search", Description: "Search", InputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "private search ok"}}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "create_issue", Description: "Create", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "created"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.lastAuth.Store(r.Header.Get("Authorization"))
		if requiredToken != "" && r.Header.Get("Authorization") != "Bearer "+requiredToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	fixture.URL = httpServer.URL + "/mcp"
	return fixture
}

// The shipped probe speaks real MCP over the private hop — initialize plus a
// bounded tools/list — and the per-connection bearer reaches the origin.
func TestProbeBackendMCP(t *testing.T) {
	ctx := context.Background()
	fixture := newPrivateMCPFixture(t, "")
	probed, err := ProbeBackendMCP(ctx, fixture.URL, nil, "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(probed.Tools) != 2 || probed.Tools[0].Name != "create_issue" || probed.Tools[1].Name != "search" {
		t.Fatalf("tools: %+v", probed.Tools)
	}
	if probed.Tools[0].Effect != WriteEffect || probed.Tools[1].Effect != ReadEffect {
		t.Fatalf("effects not classified: %+v", probed.Tools)
	}

	authed := newPrivateMCPFixture(t, "secret-token")
	if _, err := ProbeBackendMCP(ctx, authed.URL, nil, ""); err == nil {
		t.Fatal("credential-required endpoint probed without credentials")
	}
	probed, err = ProbeBackendMCP(ctx, authed.URL, map[string]string{"Authorization": "Bearer secret-token"}, "")
	if err != nil {
		t.Fatalf("credentialed probe: %v", err)
	}
	if got := authed.lastAuth.Load(); got != "Bearer secret-token" {
		t.Fatalf("authorization header %q reached origin", got)
	}

	if _, err := ProbeBackendMCP(ctx, "https://8.8.8.8/mcp", nil, ""); err == nil {
		t.Fatal("public endpoint admitted into the private probe")
	}
	if _, err := ProbeBackendMCP(ctx, "https://169.254.169.254/latest", nil, ""); err == nil {
		t.Fatal("metadata endpoint admitted into the private probe")
	}
}

// The call path the gateway runs: per-connection http_header credentials
// reach only the pinned origin; the shared backend bearer is strictly the
// credential-less fallback.
func TestPrivateCallEnvPerConnectionBearer(t *testing.T) {
	ctx := context.Background()
	fixture := newPrivateMCPFixture(t, "conn-token")
	creds := []CredentialInput{{Name: "REMOTE_BEARER", Required: true, Delivery: "http_header", Target: "Authorization", Prefix: "Bearer "}}
	def, err := BackendMCPDefinition(fixture.URL, "corp-tools", creds, []ToolSpec{{Name: "search", Effect: ReadEffect}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	effective := EffectiveBinding{
		Binding:    ToolBinding{PrincipalID: "alice", ContextID: "alice", ToolBindingID: "tb-alice-corp"},
		Definition: def,
		Connection: &Connection{Metadata: map[string]string{"mcp_endpoint": fixture.URL}},
	}
	backend := MCPBackend{Token: "shared-global-token", Root: t.TempDir()}
	if _, err := backend.CallEnv(ctx, effective, def.Tools[0], nil, map[string]string{"REMOTE_BEARER": "conn-token"}); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := fixture.lastAuth.Load(); got != "Bearer conn-token" {
		t.Fatalf("per-connection bearer did not reach origin: %q", got)
	}
	// Missing the required credential fails closed — it must not fall back to
	// the shared token and impersonate another tenant.
	if _, err := backend.CallEnv(ctx, effective, def.Tools[0], nil, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing credential fell through: %v", err)
	}
	// The credential-less path still honors the shared vMCP bearer.
	open := newPrivateMCPFixture(t, "shared-global-token")
	openDef, err := BackendMCPDefinition(open.URL, "corp-open", nil, []ToolSpec{{Name: "search", Effect: ReadEffect}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	openEffective := EffectiveBinding{Binding: ToolBinding{PrincipalID: "alice", ContextID: "alice", ToolBindingID: "tb-alice-open"}, Definition: openDef, Connection: &Connection{Metadata: map[string]string{"mcp_endpoint": open.URL}}}
	if _, err := backend.CallEnv(ctx, openEffective, openDef.Tools[0], nil, nil); err != nil {
		t.Fatalf("shared-token fallback call: %v", err)
	}
	if got := open.lastAuth.Load(); got != "Bearer shared-global-token" {
		t.Fatalf("shared bearer fallback: %q", got)
	}
}
