package toolhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewEndpointHandlerLaunchesTwiceWithControlStatus(t *testing.T) {
	definition := catalogReadDefinition()
	runLaunch := func(t *testing.T, principal, token string, grant bool) (string, map[string]any) {
		t.Helper()
		store := NewStore()
		if err := store.RegisterDefinition(definition); err != nil {
			t.Fatal(err)
		}
		if grant {
			if err := store.PutGrant(OperatorGrant(GrantCatalogDefault, principal, "", "")); err != nil {
				t.Fatal(err)
			}
		}
		auth := identity.TelegramEnvelope(principal, 7, principal+"-runtime", "policy-1")
		path := filepath.Join(t.TempDir(), "store.json")
		if err := store.Save(path); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewEndpointHandler(EndpointConfig{Token: token, Auth: auth, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			return BackendResult{Text: "ok"}, nil
		}), Listen: "127.0.0.1:0"}, loaded)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		client := mcp.NewClient(&mcp.Implementation{Name: "launch", Version: "1"}, nil)
		session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: token}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		prepared, err := callControl(t, session, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "launch"})
		if !grant {
			if err == nil {
				t.Fatal("ungranted launch prepare succeeded")
			}
			return server.URL, nil
		}
		if err != nil {
			t.Fatal(err)
		}
		status, err := callControl(t, session, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
		if err != nil || status["phase"] == "" {
			t.Fatalf("status=%v err=%v", status, err)
		}
		if _, err := callControl(t, session, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); err != nil {
			t.Fatal(err)
		}
		enabled, err := callControl(t, session, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]})
		if err != nil || enabled["binding_id"] == "" || enabled["phase"] != PhaseEnabled {
			t.Fatalf("enable=%v err=%v", enabled, err)
		}
		if _, err := callControl(t, session, "prepare_source", map[string]any{"user_id": "other", "definition_id": definition.DefinitionID, "version": definition.Version}); err == nil {
			t.Fatal("forged user_id accepted on launched handler")
		}
		if _, err := callControl(t, session, "revoke", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
			t.Fatal(err)
		}
		name := ProjectedToolName(definition.DefinitionID, definition.Version, "search")
		if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "x"}}); err == nil {
			t.Fatal("revoked call admitted on launched handler")
		}
		raw, _ := json.Marshal(enabled)
		if strings.Contains(string(raw), "token=") {
			t.Fatalf("launch body leaked secret: %s", raw)
		}
		return server.URL, enabled
	}
	url1, body1 := runLaunch(t, "alice", strings.Repeat("a", 32), true)
	url2, body2 := runLaunch(t, "bob", strings.Repeat("b", 32), true)
	if url1 == "" || url2 == "" || body1["binding_id"] == body2["binding_id"] {
		t.Fatalf("launch bodies=%v %v", body1, body2)
	}
	t.Logf("launch-1 alice url=%s phase=%v status=%v binding_id=%v", url1, body1["phase"], body1["status"], body1["binding_id"])
	t.Logf("launch-2 bob url=%s phase=%v status=%v binding_id=%v", url2, body2["phase"], body2["status"], body2["binding_id"])
	_, _ = runLaunch(t, "carol", strings.Repeat("c", 32), false)
	t.Logf("launch-carol ungranted prepare denied")
}
