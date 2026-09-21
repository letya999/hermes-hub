package toolhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOfficialWorkspaceDiscoveryAndShippedGateway(t *testing.T) {
	for product, names := range workspaceReadTools {
		t.Run(product, func(t *testing.T) {
			calls := 0
			server := mcp.NewServer(&mcp.Implementation{Name: "official-shaped", Version: "1.0.0"}, nil)
			for _, name := range append(append([]string{}, names...), "delete_everything") {
				server.AddTool(&mcp.Tool{Name: product + "." + name, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"resource": map[string]any{"type": "string"}}, "required": []string{"resource"}, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					calls++
					return &mcp.CallToolResult{StructuredContent: map[string]any{"ok": true}, Content: []mcp.Content{&mcp.TextContent{Text: "read"}}}, nil
				})
			}
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer owner-token" {
					t.Error("wrong credential")
				}
				mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true}).ServeHTTP(w, r)
			}))
			defer fixture.Close()
			httpClient := &http.Client{Transport: calendarFixtureTransport{fixture.URL, http.DefaultTransport}}
			d, err := DiscoverGoogleWorkspace(context.Background(), httpClient, product, "owner-token")
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Tools) != len(names) {
				t.Fatal(d.Tools)
			}
			for _, tool := range d.Tools {
				if !json.Valid(tool.InputSchema) || tool.Effect != ReadEffect {
					t.Fatal(tool)
				}
			}
			store := NewStore()
			if err := store.RegisterDefinition(d); err != nil {
				t.Fatal(err)
			}
			auth := identity.TelegramEnvelope("alice", 1, "alice", "policy-1")
			ref := CredentialReference{Schema: 1, CredentialRefID: "reference", ConnectionID: "google", Revision: 1, Backend: "local", Locator: "loc", Keys: []string{"ACCESS_TOKEN", "OAUTH_SCOPE"}, Status: ActiveStatus}
			if err := store.PutCredentialReference(ref); err != nil {
				t.Fatal(err)
			}
			if err := store.PutConnection(Connection{Schema: 1, ConnectionID: "google", Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: ActiveStatus, Metadata: map[string]string{"google_sub": "subject"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Enable(auth, d.DefinitionID, d.Version); err != nil {
				t.Fatal(err)
			}
			scopes, _ := GoogleWorkspaceScopes(product)
			gateway := &Gateway{Store: store, Backend: RoutingBackend{MCP: MCPBackend{HTTPClient: httpClient}}, Injector: func(context.Context, EffectiveBinding) (CredentialInjection, error) {
				return CredentialInjection{Environment: map[string]string{"ACCESS_TOKEN": "owner-token", "OAUTH_SCOPE": strings.Join(scopes, " ")}}, nil
			}}
			name := ProjectedToolName(d.DefinitionID, d.Version, d.Tools[0].Name)
			if _, err := gateway.CallAuthorized(context.Background(), auth, name, map[string]any{"resource": "one"}); err != nil {
				t.Fatal(err)
			}
			before := calls
			for _, args := range []map[string]any{{"connection_id": "other"}, {"wrong": "x"}, {"resource": 1}} {
				if result, err := gateway.CallAuthorized(context.Background(), auth, name, args); err == nil && !result.IsError {
					t.Fatal("invalid input admitted")
				}
			}
			// Provider schema rejects malformed input before invoking the tool.
			if calls != before {
				t.Fatal("malformed input reached tool")
			}
			if _, err := gateway.CallAuthorized(context.Background(), identity.TelegramEnvelope("bob", 2, "bob", "policy-1"), name, map[string]any{"resource": "one"}); err == nil {
				t.Fatal("cross-owner")
			}
			if err := store.SetConnectionStatus("google", RevokedStatus); err != nil {
				t.Fatal(err)
			}
			if _, err := gateway.CallAuthorized(context.Background(), auth, name, map[string]any{"resource": "one"}); err == nil {
				t.Fatal("revoked")
			}
			if calls != before {
				t.Fatal("denied call reached backend")
			}
		})
	}
}

func TestWorkspaceInvalidGrantAndSchema(t *testing.T) {
	if _, err := GoogleWorkspaceScopes("unknown"); err == nil {
		t.Fatal("unknown service")
	}
	if _, err := workspaceSession(context.Background(), nil, "gmail", ""); err == nil {
		t.Fatal("missing token")
	}
	if _, err := DiscoverGoogleWorkspace(context.Background(), nil, "unknown", "token"); err == nil {
		t.Fatal("unknown discovery")
	}
	if _, err := (GoogleWorkspaceBackend{}).CallEnv(context.Background(), EffectiveBinding{}, ToolSpec{}, nil, nil); err == nil {
		t.Fatal("missing binding")
	}
	d := CalendarDefinition(false)
	d.Tools[0].InputSchema = json.RawMessage(`{}`)
	if d.Validate() == nil {
		t.Fatal("REST schema accepted")
	}
	d.Transport = RemoteMCP
	d.Tools[0].InputSchema = json.RawMessage(`bad`)
	if d.Validate() == nil {
		t.Fatal("bad MCP schema accepted")
	}
}
