package toolhub

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEndpointHandlerUsesOneAuthenticatedIdentity(t *testing.T) {
	store, auth, _ := seededStore(t)
	config := EndpointConfig{Token: strings.Repeat("t", 32), Auth: auth, Backend: backendFunc(func(_ context.Context, _ EffectiveBinding, _ ToolSpec, _ map[string]any) (BackendResult, error) {
		return BackendResult{}, nil
	})}
	if _, err := NewEndpointHandler(config, store); err != nil {
		t.Fatal(err)
	}
	bad := config
	bad.Auth = identity.Envelope{Schema: identity.Schema}
	if _, err := NewEndpointHandler(bad, store); err == nil {
		t.Fatal("invalid endpoint identity accepted")
	}
	if _, err := NewEndpointHandler(config, nil); err == nil {
		t.Fatal("nil store accepted")
	}
	bad = config
	bad.Token = "short"
	if _, err := NewEndpointHandler(bad, store); err == nil {
		t.Fatal("short endpoint token accepted")
	}
	bad = config
	bad.Backend = nil
	if _, err := NewEndpointHandler(bad, store); err == nil {
		t.Fatal("nil endpoint backend accepted")
	}
}

func TestReadinessBackendRouting(t *testing.T) {
	backend := MCPBackend{}
	for _, candidate := range []ToolBackend{
		backend, &backend,
		RoutingBackend{MCP: backend}, &RoutingBackend{MCP: backend},
	} {
		if readinessBackend(candidate) == nil {
			t.Fatalf("readiness hook missing for %T", candidate)
		}
	}
	var nilMCP *MCPBackend
	var nilRouting *RoutingBackend
	if readinessBackend(nilMCP) != nil || readinessBackend(nilRouting) != nil || readinessBackend(nil) != nil {
		t.Fatal("nil readiness backend accepted")
	}
}

func TestEndpointHandlerRunsReadinessBeforeProjection(t *testing.T) {
	store := NewStore()
	definition := statefulContainerDefinition()
	definition.DefinitionID = "ready-container"
	definition.Credentials = nil
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := store.PutGrant(OperatorGrant(GrantCatalogDefault, auth.PrincipalID, "", "")); err != nil {
		t.Fatal(err)
	}
	var admissions atomic.Int32
	root := t.TempDir()
	t.Setenv("HUB_STATE", root)
	t.Setenv("HUB_CREDENTIAL_STORE", "")
	config := EndpointConfig{
		Token: strings.Repeat("r", 32), Auth: auth, Listen: "127.0.0.1:8090",
		Backend: MCPBackend{Root: root, AdmissionVerifier: func(_ context.Context, effective EffectiveBinding) (AdmissionReceipt, error) {
			admissions.Add(1)
			return AdmissionReceipt{WorkloadID: effective.WorkloadID, State: "running", Enforced: true, ImageDigest: definition.Source.Digest, SidecarImages: definition.Workload.SidecarImages, Execution: definition.Execution}, nil
		}},
	}
	handler, err := NewEndpointHandler(config, store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	session := mcpConnect(t, server.URL, config.Token)
	prepared, err := callControl(t, session, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "ready-handler"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := callControl(t, session, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, session, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); err != nil {
		t.Fatal(err)
	}
	if got := admissions.Load(); got != 1 {
		t.Fatalf("readiness admissions=%d", got)
	}
	tools, err := session.ListTools(context.Background(), nil)
	projected := ProjectedToolName(definition.DefinitionID, definition.Version, "read")
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == projected {
			found = true
			break
		}
	}
	if err != nil || !found {
		t.Fatalf("projection tools=%v err=%v", tools, err)
	}
}

func TestEndpointConfigFromEnvUsesRuntimeIdentityDefaults(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "")
	t.Setenv("HUB_RUNTIME_ID", "")
	t.Setenv("HUB_POLICY_VERSION", "")
	t.Setenv("HUB_TOOLHUB_TOKEN_ENV", "")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	config, err := EndpointConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "127.0.0.1:8090" || config.Auth.PrincipalID != "alice" || config.Auth.ContextID != "alice" || config.Auth.RuntimeID != "alice" || config.Backend == nil {
		t.Fatalf("unexpected default endpoint config: %+v", config)
	}
	routing, ok := config.Backend.(RoutingBackend)
	if !ok {
		t.Fatalf("endpoint backend=%T", config.Backend)
	}
	mcp, ok := routing.MCP.(MCPBackend)
	if !ok || mcp.Root != "/state" {
		t.Fatalf("MCP backend root=%q ok=%v", mcp.Root, ok)
	}
	t.Setenv("HUB_TOOLHUB_TOKEN_ENV", "CUSTOM_TOKEN")
	t.Setenv("CUSTOM_TOKEN", strings.Repeat("b", 32))
	t.Setenv("HUB_CONTEXT_ID", "context")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9090")
	config, err = EndpointConfigFromEnv()
	if err != nil || config.Token != strings.Repeat("b", 32) || config.Auth.ContextID != "context" || config.Listen != "127.0.0.1:9090" {
		t.Fatalf("custom endpoint config failed: %+v %v", config, err)
	}
	t.Setenv("HUB_TOOLHUB_TOKEN_ENV", "bad-name")
	if _, err := EndpointConfigFromEnv(); err == nil {
		t.Fatal("invalid token environment name accepted")
	}
}

func TestFormOriginUsesLoopbackForWildcardListener(t *testing.T) {
	for listen, want := range map[string]string{
		"0.0.0.0:8090":   "http://127.0.0.1:8090",
		"[::]:8090":      "http://127.0.0.1:8090",
		"127.0.0.1:8090": "http://127.0.0.1:8090",
	} {
		if got := formOriginForListen(listen); got != want {
			t.Fatalf("form origin for %q = %q, want %q", listen, got, want)
		}
	}
}

func TestEndpointHandlerAuditLedgerFailClosed(t *testing.T) {
	store, auth, _ := seededStore(t)
	ledger := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("HUB_AUDIT_LEDGER", ledger)
	token := strings.Repeat("t", 32)
	config := EndpointConfig{Token: token, Auth: auth, Backend: backendFunc(func(_ context.Context, _ EffectiveBinding, _ ToolSpec, _ map[string]any) (BackendResult, error) {
		return BackendResult{Text: "ok"}, nil
	})}
	handler, err := NewEndpointHandler(config, store)
	if err != nil || handler == nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "audit-ledger", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	name := ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "ok"}}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(ledger)
	if err != nil || !strings.Contains(string(body), `"kind":"tool-call"`) || !strings.Contains(string(body), `"credential_revision":1`) {
		t.Fatalf("ledger=%s err=%v", body, err)
	}
	t.Setenv("HUB_AUDIT_LEDGER", "relative.jsonl")
	if _, err := NewEndpointHandler(config, store); err == nil {
		t.Fatal("relative ledger accepted")
	}
	t.Setenv("HUB_AUDIT_LEDGER", filepath.Join(t.TempDir(), "missing", "nested", "audit.jsonl"))
	if _, err := NewEndpointHandler(config, store); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_AUDIT_LEDGER", "")
	t.Setenv("HUB_CREDENTIAL_STORE", "relative.enc")
	if _, err := NewEndpointHandler(config, store); err == nil {
		t.Fatal("relative credential store accepted")
	}
}

func TestEndpointHandlerWiresReviewerOAuthAndElicitsFormURL(t *testing.T) {
	t.Setenv("HUB_STATE", "")
	t.Setenv("HUB_ARTIFACT_DIR", "")
	t.Setenv("HUB_BUILD_SECCOMP", "")
	t.Setenv("HUB_CREDENTIAL_STORE", "")
	t.Setenv("HUB_AUDIT_LEDGER", "")
	store := NewStore()
	if err := store.RegisterDefinition(remoteDefinition()); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := store.PutGrant(OperatorGrant(GrantDefinition, auth.PrincipalID, "google-work", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(OperatorGrant(GrantSelfInstall, auth.PrincipalID, "", "")); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("t", 32)
	handler, err := NewEndpointHandler(EndpointConfig{
		Token: token, Auth: auth, Listen: "127.0.0.1:8090",
		Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			return BackendResult{Text: "ok"}, nil
		}),
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	callback, err := http.Get(server.URL + "/oauth/callback?onboarding_id=onboard-missing")
	if err != nil {
		t.Fatal(err)
	}
	callback.Body.Close()
	if callback.StatusCode == http.StatusNotFound {
		t.Fatal("production oauth callback 404; broker not wired")
	}
	var elicited atomic.Value
	client := mcp.NewClient(&mcp.Implementation{Name: "prod-control", Version: "1"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}},
		ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			if req != nil && req.Params != nil {
				elicited.Store(req.Params.URL)
			}
			return &mcp.ElicitResult{Action: "accept"}, nil
		},
	})
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	github, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "prepare_source", Arguments: map[string]any{"source": githubCommitURL()}})
	text := toolCallText(github, err)
	if err == nil && (github == nil || !github.IsError) {
		t.Fatal("github prepare succeeded without import paths")
	}
	if strings.Contains(text, "source reviewer unavailable") {
		t.Fatalf("production reviewer missing: %s", text)
	}
	prepared, err := callControl(t, session, "prepare_source", map[string]any{"definition_id": "google-work", "version": "1.0.0", "request_key": "prod-form"})
	if err != nil {
		t.Fatal(err)
	}
	required, err := callControl(t, session, "required_credentials", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	formURL, _ := required["form_url"].(string)
	onboardingID, _ := prepared["onboarding_id"].(string)
	if formURL == "" || !strings.Contains(formURL, "/credentials/"+onboardingID) || !strings.Contains(formURL, "nonce=") || !strings.HasPrefix(formURL, "http://127.0.0.1:8090/") {
		t.Fatalf("production form_url=%q onboarding=%s", formURL, onboardingID)
	}
	got, _ := elicited.Load().(string)
	if got != formURL {
		t.Fatalf("elicit=%q form_url=%q", got, formURL)
	}
	assertRequiredCredentialsElicitsFormURL(t, store, auth, prepared["onboarding_id"].(string), formURL)
}

func assertRequiredCredentialsElicitsFormURL(t *testing.T, store *Store, auth identity.Envelope, onboardingID, wantURL string) {
	t.Helper()
	control := &ControlPlane{Store: store, Listen: "127.0.0.1:8090"}
	control.FormOrigin = control.origin()
	gateway := &Gateway{
		Store: store, Tokens: map[string]identity.Envelope{strings.Repeat("t", 32): auth},
		Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			return BackendResult{Text: "ok"}, nil
		}),
		Control: control,
	}
	mcpServer := gateway.serverFor(auth)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := mcpServer.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	var elicited atomic.Value
	client := mcp.NewClient(&mcp.Implementation{Name: "elicit", Version: "1"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}},
		ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			if req != nil && req.Params != nil {
				elicited.Store(req.Params.URL)
			}
			return &mcp.ElicitResult{Action: "accept"}, nil
		},
	})
	session, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	required, err := callControl(t, session, "required_credentials", map[string]any{"onboarding_id": onboardingID})
	if err != nil {
		t.Fatal(err)
	}
	formURL, _ := required["form_url"].(string)
	got, _ := elicited.Load().(string)
	if got == "" || got != formURL || got != wantURL {
		t.Fatalf("elicit=%q form_url=%q want=%q", got, formURL, wantURL)
	}
}

func TestPendingCredentialElicitRequiresFormURLAndURLCap(t *testing.T) {
	if pendingCredentialElicit(context.Background(), nil, map[string]any{"form_url": "http://127.0.0.1/credentials/x?nonce=n"}) != nil {
		t.Fatal("nil request elicited")
	}
	if pendingCredentialElicit(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}, map[string]any{}) != nil {
		t.Fatal("empty form_url elicited")
	}
	fallback := pendingCredentialElicit(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}, map[string]any{"form_url": "http://127.0.0.1/credentials/x?nonce=n"})
	if fallback == nil || !strings.Contains(toolCallText(fallback, nil), "http://127.0.0.1/credentials/x") {
		t.Fatal("unsupported client did not receive a protected form URL")
	}
	if clientSupportsURLElicitation(nil) {
		t.Fatal("nil request advertised url elicitation")
	}
}

func toolCallText(result *mcp.CallToolResult, err error) string {
	var b strings.Builder
	if err != nil {
		b.WriteString(err.Error())
	}
	if result == nil {
		return b.String()
	}
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func TestGatewayInjectorAndCallEnv(t *testing.T) {
	store, auth, _ := seededStore(t)
	name := ProjectedToolName("google-work", "1.0.0", "search")
	var injected atomic.Bool
	var wiped atomic.Bool
	gateway := Gateway{
		Store: store,
		Backend: backendFunc(func(_ context.Context, _ EffectiveBinding, _ ToolSpec, _ map[string]any) (BackendResult, error) {
			return BackendResult{Text: "ok"}, nil
		}),
		Injector: func(context.Context, EffectiveBinding) (CredentialInjection, error) {
			injected.Store(true)
			return CredentialInjection{Environment: map[string]string{"GOOGLE_TOKEN": "injected"}, Cleanup: func() error { wiped.Store(true); return nil }}, nil
		},
	}
	if _, err := gateway.call(context.Background(), auth, name, map[string]any{"query": "x"}); err != nil {
		t.Fatal(err)
	}
	if !injected.Load() || !wiped.Load() {
		t.Fatal("injector did not run")
	}
	cli := RoutingBackend{CLI: CLIRunner{}}
	if _, err := cli.CallEnv(context.Background(), EffectiveBinding{Definition: boundedCLIDefinition()}, ToolSpec{Name: "list"}, nil, nil); !errors.Is(err, ErrIsolation) && err == nil {
		t.Fatal("CLI without isolation accepted")
	}
}

func TestEndpointHandlerInjectsFromCredstore(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "credential.key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "store.enc")
	backend, err := credstore.Open(credstore.Options{Path: storePath, KeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	secret := "inject-secret-" + hex.EncodeToString([]byte("fixture-secret"))[:16]
	if err := backend.Put("local://alice/google/1", "alice", map[string]string{"GOOGLE_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	toolStore, auth, binding := seededStore(t)
	root := t.TempDir()
	var capturedBody []byte
	var envFile string
	backendServer := mcp.NewServer(&mcp.Implementation{Name: "inject-fixture", Version: "1"}, nil)
	backendServer.AddTool(&mcp.Tool{Name: "search", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	backendHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return backendServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = append(capturedBody, body...)
		r.Body = io.NopCloser(bytes.NewReader(body))
		path := filepath.Join(root, "per-user", "alice", "alice", "google-work", CredentialReferenceID("google-work", 0), "credentials.env")
		if data, err := os.ReadFile(path); err == nil {
			envFile = string(data)
		}
		if r.Header.Get("Authorization") != "Bearer backend-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backendHandler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	toolStore.mu.Lock()
	connection := toolStore.connections["google-work"]
	meta := map[string]string{}
	for key, value := range connection.Metadata {
		meta[key] = value
	}
	meta["mcp_endpoint"] = httpServer.URL + "/mcp"
	connection.Metadata = meta
	toolStore.connections["google-work"] = connection
	toolStore.mu.Unlock()
	_ = binding
	ledger := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("HUB_CREDENTIAL_STORE", storePath)
	t.Setenv("HUB_CREDENTIAL_KEY_FILE", keyFile)
	t.Setenv("HUB_CREDENTIAL_KEY", "")
	t.Setenv("HUB_AUDIT_LEDGER", ledger)
	t.Setenv("HUB_STATE", root)
	token := strings.Repeat("t", 32)
	config := EndpointConfig{
		Token: token,
		Auth:  auth,
		Backend: RoutingBackend{
			MCP: MCPBackend{HTTPClient: httpServer.Client(), Token: "backend-token", Root: root},
		},
	}
	handler, err := NewEndpointHandler(config, toolStore)
	if err != nil || handler == nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "inject-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: token, jobID: "job-alice", runID: "run-alice"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	name := ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "ok"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(envFile, secret) {
		t.Fatalf("credentials.env missing during MCP call: %q", envFile)
	}
	if strings.Contains(string(capturedBody), secret) {
		t.Fatalf("secret leaked into MCP HTTP: %s", capturedBody)
	}
	body, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"job_id":"job-alice"`) || !strings.Contains(text, `"hermes_run_id":"run-alice"`) || !strings.Contains(text, `"tool_call_id":"call-`) {
		t.Fatalf("ledger missing correlation: %s", body)
	}
	if strings.Contains(text, secret) {
		t.Fatal("ledger leaked secret")
	}
}

func TestNonLoopbackListenBoundary(t *testing.T) {
	for address, want := range map[string]bool{
		"127.0.0.1:8090": false,
		"localhost:8090": false,
		":8090":          true,
		"10.0.0.4:8090":  true,
		"toolhub:8090":   true,
		"bad-address":    false,
	} {
		if got := nonLoopbackListen(address); got != want {
			t.Fatalf("nonLoopbackListen(%q)=%v want %v", address, got, want)
		}
	}
}
