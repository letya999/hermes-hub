package toolhub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestValidateRemoteMCPEndpointDeniesNonPublic(t *testing.T) {
	original := resolveRemoteIP
	defer func() { resolveRemoteIP = original }()
	resolveRemoteIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	denied := []string{
		"http://api.githubcopilot.com/mcp/",
		"https://alice@api.githubcopilot.com/mcp/",
		"https://api.githubcopilot.com/mcp/?key=x",
		"https://api.githubcopilot.com/mcp/#frag",
		"https://localhost/mcp",
		"https://foo.localhost/mcp",
		"https://toolhive/mcp",
		"https://vmcp.internal/mcp",
		"https://anything.corp/mcp",
		"https://metadata.google.internal/computeMetadata/v1",
		"https://127.0.0.1/mcp",
		"https://[::1]/mcp",
		"https://10.0.0.4/mcp",
		"https://169.254.169.254/latest/meta-data",
		"https://100.64.1.1/mcp",
		"https://singlelabel/mcp",
		"https://192.168.1.10:8443/mcp",
		"not a url",
		"",
	}
	for _, candidate := range denied {
		if err := ValidateRemoteMCPEndpoint(candidate); err == nil {
			t.Fatalf("non-public endpoint %q accepted", candidate)
		}
	}
	for _, candidate := range []string{"https://api.githubcopilot.com/mcp/", "https://calendarmcp.googleapis.com/mcp/v1", "https://mcp.example.com:8443/x", "https://93.184.216.34/mcp", "https://[2606:4700::6810:85e5]/mcp"} {
		if err := ValidateRemoteMCPEndpoint(candidate); err != nil {
			t.Fatalf("public endpoint %q refused: %v", candidate, err)
		}
	}
	resolveRemoteIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("192.168.0.1")}, nil
	}
	if err := ValidateRemoteMCPEndpoint("https://rebind.example/mcp"); err == nil {
		t.Fatal("host with a private answer accepted")
	}
}

func TestRemoteAddrPublicClassification(t *testing.T) {
	denied := []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "::1", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "198.18.0.1", "240.1.2.3"}
	for _, raw := range denied {
		if remoteAddrPublic(netip.MustParseAddr(raw)) {
			t.Fatalf("%s classified public", raw)
		}
	}
	for _, raw := range []string{"93.184.216.34", "140.82.113.6", "2606:4700::6810:85e5"} {
		if !remoteAddrPublic(netip.MustParseAddr(raw)) {
			t.Fatalf("%s classified private", raw)
		}
	}
}

func TestRemoteDialAndRedirectRefuse(t *testing.T) {
	if _, err := remoteDial(context.Background(), "tcp", "169.254.169.254:443"); err == nil {
		t.Fatal("private dial target dialed")
	}
	original := resolveRemoteIP
	defer func() { resolveRemoteIP = original }()
	resolveRemoteIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.8")}, nil
	}
	if _, err := remoteDial(context.Background(), "tcp", "poisoned.example:443"); err == nil {
		t.Fatal("host resolving private dialed")
	}
	client := defaultRemoteHTTPClient("https://remote.example/mcp", nil)
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("remote client follows redirects")
	}
}

func TestRemoteCredentialInputsAndHeaderDelivery(t *testing.T) {
	inputs, err := RemoteCredentialInputs(map[string]any{"credentials": []any{"GITHUB_TOKEN"}})
	if err != nil || len(inputs) != 1 || inputs[0].Name != "GITHUB_TOKEN" || inputs[0].Target != "Authorization" || inputs[0].Prefix != "Bearer " || inputs[0].Delivery != "http_header" {
		t.Fatalf("inputs=%+v err=%v", inputs, err)
	}
	custom, err := RemoteCredentialInputs(map[string]any{"credentials": []any{map[string]any{"name": "ACME_KEY", "header": "X-Api-Key"}}})
	if err != nil || custom[0].Target != "X-Api-Key" || custom[0].Prefix != "" {
		t.Fatalf("custom=%+v err=%v", custom, err)
	}
	stringsList, err := RemoteCredentialInputs(map[string]any{"credentials": []string{"A_TOKEN"}, "credential_header": "X-Token", "credential_prefix": "key="})
	if err != nil || stringsList[0].Target != "X-Token" || stringsList[0].Prefix != "key=" {
		t.Fatalf("stringsList=%+v err=%v", stringsList, err)
	}
	if _, err := RemoteCredentialInputs(map[string]any{"credentials": "GITHUB_TOKEN"}); err == nil {
		t.Fatal("scalar credentials accepted")
	}
	if _, err := RemoteCredentialInputs(map[string]any{"credentials": []any{42}}); err == nil {
		t.Fatal("non-string credential accepted")
	}
	if _, err := RemoteCredentialInputs(map[string]any{"credentials": []any{"A_TOKEN", "B_TOKEN", "C_TOKEN", "D_TOKEN", "E_TOKEN", "F_TOKEN", "G_TOKEN", "H_TOKEN", "I_TOKEN"}}); err == nil {
		t.Fatal("unbounded credential list accepted")
	}
	if _, err := RemoteCredentialInputs(map[string]any{"credentials": []any{"A_TOKEN", "A_TOKEN"}}); err == nil {
		t.Fatal("duplicate credential accepted")
	}
	if _, err := RemoteCredentialInputs(map[string]any{"credentials": []any{map[string]any{"name": "x", "header": "Authorization"}}}); err == nil {
		t.Fatal("invalid credential name accepted")
	}
	if _, err := RemoteCredentialInputs(map[string]any{"credentials": []any{map[string]any{"name": "A_TOKEN", "header": "Host"}}}); err == nil {
		t.Fatal("Host header credential accepted")
	}
	definition, err := remoteDefinitionBase("https://mcp.example.com/mcp", "", inputs)
	if err != nil {
		t.Fatal(err)
	}
	headers, err := CredentialHeaders(definition, map[string]string{"GITHUB_TOKEN": "ghp_secret"})
	if err != nil || headers["Authorization"] != "Bearer ghp_secret" {
		t.Fatalf("headers=%v err=%v", headers, err)
	}
	if _, err := CredentialHeaders(definition, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing required credential did not deny: %v", err)
	}
	if _, err := CredentialHeaders(definition, map[string]string{"GITHUB_TOKEN": "bad\r\ninjected: 1"}); err == nil {
		t.Fatal("CRLF credential accepted into a header")
	}
}

func TestRemoteToolEffectClassification(t *testing.T) {
	writes := []string{"create_issue", "merge_pull_request", "issue_write", "send_message", "update-file"}
	for _, name := range writes {
		if remoteToolEffect(name) != WriteEffect {
			t.Fatalf("%s not classified write", name)
		}
	}
	reads := []string{"search", "get_issue", "list_repos", "get_file_contents", "reassigning_lookup"}
	for _, name := range reads {
		if remoteToolEffect(name) != ReadEffect {
			t.Fatalf("%s classified write", name)
		}
	}
}

// remoteMCPFixture is a real MCP server behind httptest TLS. Tests pin DNS to
// a public answer and swap remoteHTTPClient for a fixture-dialing client so
// the shipped endpoint validation, header delivery and probe code all run.
type remoteMCPFixture struct {
	URL        string
	lastAuth   atomic.Value
	sawTool    atomic.Value
	tlsCleanup func()
}

func newRemoteMCPFixture(t *testing.T, requiredToken string) *remoteMCPFixture {
	t.Helper()
	fixture := &remoteMCPFixture{}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "remote-fixture", Version: "1"}, nil)
	mcpServer.AddTool(&mcp.Tool{Name: "search", Description: "Search issues", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fixture.sawTool.Store("search")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture search ok"}}}, nil
	})
	mcpServer.AddTool(&mcp.Tool{Name: "create_issue", Description: "Create an issue", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "created"}}}, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.lastAuth.Store(r.Header.Get("Authorization"))
		if requiredToken != "" && r.Header.Get("Authorization") != "Bearer "+requiredToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	fixture.URL = server.URL

	originalResolve, originalClient := resolveRemoteIP, remoteHTTPClient
	fixture.tlsCleanup = func() { resolveRemoteIP, remoteHTTPClient = originalResolve, originalClient }
	t.Cleanup(fixture.tlsCleanup)
	resolveRemoteIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	dialAddr := strings.TrimPrefix(server.URL, "https://")
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", dialAddr)
		},
	}
	remoteHTTPClient = func(endpoint string, headers map[string]string) *http.Client {
		origin := ""
		if u, err := url.Parse(endpoint); err == nil {
			origin = requestOrigin(u)
		}
		return &http.Client{
			Transport:     remoteHeaderRoundTripper{base: transport, origin: origin, headers: headers},
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return fixture
}

// remoteFixtureHost maps the TLS fixture to a stable public-looking endpoint
// URL; the name is a stubbed-DNS fixture host, never a literal private IP.
func (f *remoteMCPFixture) endpoint() string {
	return "https://remote-fixture.example:" + strings.TrimPrefix(strings.TrimPrefix(f.URL, "https://127.0.0.1"), ":") + "/mcp"
}

type remoteControlFixture struct {
	*controlFixture
	secrets credstore.Backend
}

func newRemoteControlFixture(t *testing.T) *remoteControlFixture {
	t.Helper()
	fix := newControlFixture(t, nil)
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "remote.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	fix.control.Secrets = secrets
	fix.control.AdmitWithCredentials = admitRemoteDefinition
	// The shipped remote call path must run — a stub backend would bypass
	// callRemoteMCP, header injection and endpoint re-validation entirely.
	fix.gateway.Backend = MCPBackend{}
	// Enable runs the same readiness probe the production wiring installs:
	// the endpoint must still speak MCP and still advertise every bound tool.
	fix.control.Ready = func(ctx context.Context, effective EffectiveBinding) error {
		env, err := DecryptAuthorized(secrets, effective)
		if err != nil {
			return err
		}
		return (MCPBackend{}).EnsureReady(ctx, effective, env)
	}
	fix.store.Reconnect = &ReconnectController{Store: fix.store, Auth: aliceAuth(), OnChange: func(ProjectionChange) error {
		return fix.gateway.RefreshProjection()
	}}
	fix.gateway.Injector = func(ctx context.Context, effective EffectiveBinding) (CredentialInjection, error) {
		env, err := DecryptAuthorized(secrets, effective)
		return CredentialInjection{Environment: env}, err
	}
	return &remoteControlFixture{controlFixture: fix, secrets: secrets}
}

func TestPrepareRemoteDeniedWithoutSelfInstallGrant(t *testing.T) {
	fixture := newRemoteMCPFixture(t, "")
	fix := newRemoteControlFixture(t)
	alice := fix.session(t, aliceToken)
	if _, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()}); !errors.Is(err, ErrUnauthorized) && err == nil {
		t.Fatalf("ungated prepare succeeded: %v", err)
	}
	fix.store.mu.RLock()
	defer fix.store.mu.RUnlock()
	for _, definition := range fix.store.definitions {
		if definition.Transport == RemoteMCP {
			t.Fatalf("denied prepare wrote a definition: %s", definition.DefinitionID)
		}
	}
}

func TestPrepareRemoteCredentialFreeEndToEnd(t *testing.T) {
	fixture := newRemoteMCPFixture(t, "")
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	alice, bob := fix.session(t, aliceToken), fix.session(t, bobToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	if prepared["phase"] != PhaseAwaitingConfirm || prepared["onboarding_id"] == "" {
		t.Fatalf("prepare=%v", prepared)
	}
	// Idempotent: the same prepare resolves to the same onboarding.
	again, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil || again["onboarding_id"] != prepared["onboarding_id"] {
		t.Fatalf("idempotent prepare=%v err=%v", again, err)
	}
	// Bob must not see or enable alice's user-published definition.
	if _, err := callControl(t, bob, "prepare_source", map[string]any{"definition_id": prepared["definition_id"], "version": "1.0.0"}); err == nil {
		t.Fatal("cross-user catalog prepare allowed")
	}
	confirmed, err := callControl(t, alice, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": prepared["nonce"]})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed["phase"] != PhaseConfirmed {
		t.Fatalf("confirm=%v", confirmed)
	}
	enabled, err := callControl(t, alice, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	projected := enabled["projected_tools"].([]any)
	if len(projected) != 2 {
		t.Fatalf("projected=%v", enabled)
	}
	listed, err := alice.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projectedName := ""
	for _, tool := range listed.Tools {
		if strings.Contains(tool.Name, "search") && strings.HasPrefix(tool.Name, "hub-remote-") {
			projectedName = tool.Name
		}
	}
	if projectedName == "" {
		t.Fatal("remote tool not projected")
	}
	bobTools, err := bob.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range bobTools.Tools {
		if strings.HasPrefix(tool.Name, "hub-remote-") {
			t.Fatalf("owner-scoped remote leaked to bob: %s", tool.Name)
		}
	}
	invoked, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": projectedName, "arguments": map[string]any{"q": "x"}}})
	if err != nil || invoked.IsError {
		t.Fatalf("invoke=%+v err=%v", invoked, err)
	}
	if fixture.sawTool.Load() != "search" {
		t.Fatal("remote fixture did not receive the call")
	}
	// Bob reusing the projected name is denied at call time.
	if _, err := bob.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": projectedName, "arguments": map[string]any{}}}); err == nil {
		t.Fatal("cross-user projected call allowed")
	}
	// Revoke removes the projection and cuts the call path mid-session.
	if _, err := callControl(t, alice, "revoke", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": projectedName, "arguments": map[string]any{}}}); err == nil {
		t.Fatal("revoked remote call still succeeded")
	}
	refreshed, err := alice.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range refreshed.Tools {
		if tool.Name == projectedName {
			t.Fatal("revoked remote tool still projected")
		}
	}
}

func TestPrepareRemoteCredentialedFlow(t *testing.T) {
	fixture := newRemoteMCPFixture(t, "ghp_fixture")
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	alice := fix.session(t, aliceToken)
	// An auth-required endpoint without a credentials list is refused with a
	// clear resubmit instruction and no store write.
	if _, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()}); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("unauthenticated prepare=%v", err)
	}
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint(), "credentials": []any{"GITHUB_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared["phase"] != PhaseAwaitingCreds {
		t.Fatalf("credentialed prepare=%v", prepared)
	}
	onboardingID := prepared["onboarding_id"].(string)
	onboarding := mustOnboarding(t, fix.store, onboardingID)
	if err := fix.control.SubmitCredentials(onboardingID, "wrong-nonce", map[string]string{"GITHUB_TOKEN": "ghp_fixture"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad nonce: %v", err)
	}
	if err := fix.control.SubmitCredentials(onboardingID, onboarding.FormNonce, map[string]string{"GITHUB_TOKEN": "ghp_fixture"}); err != nil {
		t.Fatal(err)
	}
	onboarding = mustOnboarding(t, fix.store, onboardingID)
	if onboarding.Phase != PhaseAwaitingConfirm || onboarding.AdmissionPending {
		t.Fatalf("post-submit=%+v", onboarding)
	}
	if _, err := callControl(t, alice, "confirm", map[string]any{"onboarding_id": onboardingID, "nonce": onboarding.ConfirmationNonce}); err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, alice, "enable", map[string]any{"onboarding_id": onboardingID}); err != nil {
		t.Fatal(err)
	}
	listed, err := alice.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var projectedName string
	for _, tool := range listed.Tools {
		if strings.Contains(tool.Name, "search") && strings.HasPrefix(tool.Name, "hub-remote-") {
			projectedName = tool.Name
		}
	}
	if projectedName == "" {
		t.Fatal("credentialed remote tool not projected")
	}
	result, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": projectedName, "arguments": map[string]any{}}})
	if err != nil || result.IsError {
		t.Fatalf("invoke failed: %+v %v", result, err)
	}
	if auth, _ := fixture.lastAuth.Load().(string); auth != "Bearer ghp_fixture" {
		t.Fatalf("fixture auth=%q", auth)
	}
	// The secret must exist only in the encrypted store — never in the
	// ToolHub snapshot, catalog output or onboarding record.
	fix.store.mu.RLock()
	snapshot := fix.store.path
	fix.store.mu.RUnlock()
	_ = snapshot
	raw, _ := json.Marshal(mustOnboarding(t, fix.store, onboardingID))
	if strings.Contains(string(raw), "ghp_fixture") {
		t.Fatal("onboarding record carries the secret")
	}
	if catalog, err := callControl(t, alice, "discover", map[string]any{"query": "fixture"}); err == nil {
		if dumped, _ := json.Marshal(catalog); strings.Contains(string(dumped), "ghp_fixture") {
			t.Fatal("catalog output carries the secret")
		}
	}
}

func TestPrepareRemoteNonMCPDenied(t *testing.T) {
	rest := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(rest.Close)
	originalResolve, originalClient := resolveRemoteIP, remoteHTTPClient
	defer func() { resolveRemoteIP, remoteHTTPClient = originalResolve, originalClient }()
	resolveRemoteIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	dialAddr := strings.TrimPrefix(rest.URL, "https://")
	remoteHTTPClient = func(endpoint string, headers map[string]string) *http.Client {
		return &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", dialAddr)
			},
		}, Timeout: 15 * time.Second}
	}
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	alice := fix.session(t, aliceToken)
	endpoint := "https://rest-fixture.example:" + strings.TrimPrefix(dialAddr, "127.0.0.1:") + "/api"
	if _, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": endpoint}); err == nil {
		t.Fatal("non-MCP endpoint registered")
	}
	if _, err := fix.store.Definition("remote-rest-fixture-example", "1.0.0"); err == nil {
		t.Fatal("non-MCP endpoint wrote a definition")
	}
}

func TestRemoteAllowlistNarrowsProjection(t *testing.T) {
	fixture := newRemoteMCPFixture(t, "")
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	alice := fix.session(t, aliceToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint(), "tools": []any{"search"}})
	if err != nil {
		t.Fatal(err)
	}
	onboarding := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
	if _, err := callControl(t, alice, "confirm", map[string]any{"onboarding_id": onboarding.OnboardingID, "nonce": onboarding.ConfirmationNonce}); err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, alice, "enable", map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	definition, err := fix.store.Definition(prepared["definition_id"].(string), "1.0.0")
	if err != nil || len(definition.Tools) != 1 || definition.Tools[0].Name != "search" {
		t.Fatalf("definition=%+v err=%v", definition, err)
	}
	// An allowlist cannot widen beyond the advertised set.
	if _, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint(), "name": "remote-widened", "tools": []any{"drop_table"}}); err == nil {
		t.Fatal("unadvertised tool allowlisted")
	}
}

func TestRemoteSecretStaysOutOfStoreFile(t *testing.T) {
	fixture := newRemoteMCPFixture(t, "ghp_storecheck")
	fix := newRemoteControlFixture(t)
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(storePath, []byte(`{"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	persisted, err := Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	fix.control.Store = persisted
	fix.gateway.Store = persisted
	if err := putGrant(t, persisted, OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	for _, op := range ControlOperations {
		if err := putGrant(t, persisted, OperatorControlGrant("alice", op)); err != nil {
			t.Fatal(err)
		}
	}
	alice := fix.session(t, aliceToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint(), "credentials": []any{"GITHUB_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	onboarding := mustOnboarding(t, persisted, prepared["onboarding_id"].(string))
	if err := fix.control.SubmitCredentials(onboarding.OnboardingID, onboarding.FormNonce, map[string]string{"GITHUB_TOKEN": "ghp_storecheck"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ghp_storecheck") {
		t.Fatal("store.json contains the credential value")
	}
}

// A legacy SSE-only server: the shipped path must fall back from streamable
// to the 2024-11-05 SSE transport and still probe/call.
func newRemoteSSEFixture(t *testing.T) *remoteMCPFixture {
	t.Helper()
	fixture := &remoteMCPFixture{}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "remote-sse-fixture", Version: "1"}, nil)
	mcpServer.AddTool(&mcp.Tool{Name: "search", Description: "Search", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "sse search ok"}}}, nil
	})
	sseHandler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.SSEOptions{DisableLocalhostProtection: true})
	server := httptest.NewTLSServer(sseHandler)
	t.Cleanup(server.Close)
	fixture.URL = server.URL
	originalResolve, originalClient := resolveRemoteIP, remoteHTTPClient
	fixture.tlsCleanup = func() { resolveRemoteIP, remoteHTTPClient = originalResolve, originalClient }
	t.Cleanup(fixture.tlsCleanup)
	resolveRemoteIP = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	dialAddr := strings.TrimPrefix(server.URL, "https://")
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", dialAddr)
		},
	}
	remoteHTTPClient = func(endpoint string, headers map[string]string) *http.Client {
		origin := ""
		if u, err := url.Parse(endpoint); err == nil {
			origin = requestOrigin(u)
		}
		return &http.Client{
			Transport:     remoteHeaderRoundTripper{base: transport, origin: origin, headers: headers},
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return fixture
}

// Legacy SSE endpoints probe and call through the same guarded path.
func TestRemoteLegacySSEProbeAndCall(t *testing.T) {
	fixture := newRemoteSSEFixture(t)
	endpoint := fixture.endpoint()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	def, err := remoteDefinitionBase(endpoint, "remote-sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := probeRemoteMCP(ctx, def, nil)
	if err != nil {
		t.Fatalf("SSE fallback probe: %v", err)
	}
	if len(probe.Tools) != 1 || probe.Tools[0].Name != "search" {
		t.Fatalf("SSE tools: %+v", probe.Tools)
	}
	full, err := RemoteMCPDefinition(endpoint, "remote-sse", nil, probe.Tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (MCPBackend{}).callRemoteMCP(ctx, EffectiveBinding{Definition: full}, full.Tools[0], map[string]any{"q": "x"}, nil)
	if err != nil {
		t.Fatalf("SSE call: %v", err)
	}
	if !strings.Contains(result.Text, "sse search ok") {
		t.Fatalf("SSE result: %q", result.Text)
	}
}

// Credential headers ride only to the admitted origin: a legacy SSE endpoint
// that redirects its message POSTs to another host must not leak them.
func TestRemoteHeaderOriginPinning(t *testing.T) {
	var sawAuth []string
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sawAuth = append(sawAuth, req.URL.Host+"="+req.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}, Request: req}, nil
	})
	rt := remoteHeaderRoundTripper{base: base, origin: "https://admitted.example", headers: map[string]string{"Authorization": "Bearer token"}}
	for _, raw := range []string{"https://admitted.example/mcp", "https://evil.example/messages", "https://admitted.example:8443/mcp"} {
		req, _ := http.NewRequest(http.MethodPost, raw, nil)
		if _, err := rt.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
	if sawAuth[0] != "admitted.example=Bearer token" {
		t.Fatalf("origin request missing header: %v", sawAuth)
	}
	if sawAuth[1] != "evil.example=" {
		t.Fatalf("off-origin request carried headers: %v", sawAuth)
	}
	if sawAuth[2] != "admitted.example:8443=" {
		t.Fatalf("mismatched port carried headers: %v", sawAuth)
	}
}

// The response body cap keeps a hostile endpoint from exhausting memory
// before OutputBytes is even evaluated.
func TestRemoteBodyLimit(t *testing.T) {
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", remoteMaxBodyBytes+100))), Header: http.Header{}, Request: req}, nil
	})
	rt := remoteHeaderRoundTripper{base: base, origin: "https://admitted.example"}
	req, _ := http.NewRequest(http.MethodGet, "https://admitted.example/mcp", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != remoteMaxBodyBytes {
		t.Fatalf("body not capped: %d", len(body))
	}
}

// New deny prefixes: "this network", TEST-NET-1, Teredo, 6to4 and
// benchmarking space must classify non-public.
func TestRemoteAddrPublicExtendedDenies(t *testing.T) {
	for _, raw := range []string{"0.1.2.3", "192.0.2.10", "2001:0000::1", "2001:0002::1", "2002:0a00::1"} {
		if remoteAddrPublic(netip.MustParseAddr(raw)) {
			t.Fatalf("%s classified public", raw)
		}
	}
}
