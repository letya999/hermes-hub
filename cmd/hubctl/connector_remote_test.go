package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// remoteFixture is a real MCP server on loopback: BackendDial admits
// 127.0.0.1 directly, so connector remote drives the shipped probe and
// dial path unstubbed.
type remoteFixture struct {
	URL      string
	lastAuth atomic.Value
}

func newRemoteFixture(t *testing.T, requiredToken string) *remoteFixture {
	t.Helper()
	fixture := &remoteFixture{}
	server := mcp.NewServer(&mcp.Implementation{Name: "remote-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "search", Description: "Search", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "create_issue", Description: "Create", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

func remoteAuth() identity.Envelope {
	return identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", ExternalIdentityID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-1"}
}

// The shipped registration path: probe freezes the tool set, the definition
// and owner connection land owner-scoped in the store, and a repeated call
// is a no-op while a mutated one conflicts.
func TestConnectRemotePrivateRegistration(t *testing.T) {
	ctx := context.Background()
	fixture := newRemoteFixture(t, "")
	key, _ := credstore.GenerateKey()
	backend, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "secrets.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := toolhub.NewStore()
	auth := remoteAuth()
	registryPath := filepath.Join(t.TempDir(), "registry.json")

	if err := connectRemotePrivate(ctx, store, backend, auth, "corp-tools", fixture.URL, "", false, "", registryPath); err != nil {
		t.Fatalf("register: %v", err)
	}
	definition, err := store.Definition("remote-corp-tools", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if definition.Source.TLSMode != api.TLSModeOperatorPinned || len(definition.Tools) != 2 {
		t.Fatalf("definition: %+v", definition)
	}
	// Registration is user-scoped: another principal cannot enable it, the
	// owner can — the credential-less connection binds on its endpoint alone.
	bob := identity.TelegramEnvelope("bob", 8, "runtime", "policy-1")
	if _, err := store.Enable(bob, definition.DefinitionID, definition.Version); err == nil {
		t.Fatal("cross-principal enable succeeded")
	}
	binding, err := store.Enable(auth, definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatalf("owner enable: %v", err)
	}
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Connection == nil || effective.Connection.Metadata["mcp_endpoint"] != fixture.URL {
		t.Fatalf("resolved binding lost the pinned endpoint: %+v", effective.Connection)
	}
	if _, err := os.Stat(registryPath); err != nil {
		t.Fatalf("registry not persisted: %v", err)
	}
	// Re-running the identical registration is a no-op, mutating is a conflict.
	if err := connectRemotePrivate(ctx, store, backend, auth, "corp-tools", fixture.URL, "", false, "", registryPath); err != nil {
		t.Fatalf("idempotent re-register: %v", err)
	}
	if err := connectRemotePrivate(ctx, store, backend, auth, "corp-tools", fixture.URL, "search", false, "", registryPath); err == nil {
		t.Fatal("tool-set mutation admitted on an immutable registration")
	}
}

// --tools narrows the frozen set but can never widen it; --bearer lands the
// token in the credstore, never in metadata or stdout-shaped output.
func TestConnectRemotePrivateAllowlistAndBearer(t *testing.T) {
	ctx := context.Background()
	fixture := newRemoteFixture(t, "conn-secret")
	key, _ := credstore.GenerateKey()
	secretPath := filepath.Join(t.TempDir(), "secrets.enc")
	backend, err := credstore.Open(credstore.Options{Path: secretPath, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := toolhub.NewStore()
	auth := remoteAuth()
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("conn-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := connectRemotePrivate(ctx, store, backend, auth, "corp-auth", fixture.URL, "search,missing", true, tokenFile, registryPath); err == nil {
		t.Fatal("wider-than-probe allowlist admitted")
	}
	if err := connectRemotePrivate(ctx, store, backend, auth, "corp-auth", fixture.URL, "search", true, tokenFile, registryPath); err != nil {
		t.Fatalf("credentialed register: %v", err)
	}
	definition, err := store.Definition("remote-corp-auth", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Tools) != 1 || definition.Tools[0].Name != "search" {
		t.Fatalf("allowlist did not narrow the frozen set: %+v", definition.Tools)
	}
	// The credential lives only in the credstore behind its reference.
	connection, reference, err := store.OwnedConnection(auth, api.DeterministicID("conn", "alice", "remote-corp-auth", fixture.URL))
	if err != nil {
		t.Fatal(err)
	}
	if reference.Locator == "" || connection.Metadata["mcp_endpoint"] != fixture.URL {
		t.Fatalf("connection/reference shape: %+v %+v", connection, reference)
	}
	values, err := backend.Get(reference.Locator, "alice")
	if err != nil || values["REMOTE_BEARER"] != "conn-secret" {
		t.Fatalf("credstore: %v %+v", err, values)
	}
	if got := fixture.lastAuth.Load(); got != "Bearer conn-secret" {
		t.Fatalf("bearer reached origin as %q", got)
	}
	raw, _ := os.ReadFile(registryPath)
	if strings.Contains(string(raw), "conn-secret") {
		t.Fatal("bearer token persisted into the registry file")
	}
}

// Registration refuses public endpoints and any registration aimed away from
// loopback/private space — the command is the private twin of
// prepare_source remote_url, not a second public path.
func TestConnectRemotePrivatePublicRefused(t *testing.T) {
	ctx := context.Background()
	key, _ := credstore.GenerateKey()
	backend, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "secrets.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := toolhub.NewStore()
	auth := remoteAuth()
	for _, endpoint := range []string{
		"https://mcp.example.com/mcp", "https://8.8.8.8/mcp",
		"http://example.com/mcp", "https://169.254.169.254/latest",
	} {
		if err := connectRemotePrivate(ctx, store, backend, auth, "corp-pub", endpoint, "", false, "", filepath.Join(t.TempDir(), "r.json")); err == nil {
			t.Fatalf("public endpoint %q admitted", endpoint)
		}
	}
}
