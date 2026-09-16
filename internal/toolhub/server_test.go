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
		Injector: func(context.Context, EffectiveBinding) (map[string]string, func() error, error) {
			injected.Store(true)
			return map[string]string{"GOOGLE_TOKEN": "injected"}, func() error { wiped.Store(true); return nil }, nil
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
