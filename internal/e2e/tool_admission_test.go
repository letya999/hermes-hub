package e2e

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAdmissionUsesOnlyExactOwnerRuntimeTokenAndActualToolList(t *testing.T) {
	definition := agenttools.HubToolsDefinition()
	projected := api.ProjectedToolName(definition.DefinitionID, definition.Version, "routine_list")
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: projected, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.Fatal("preflight invoked a tool")
		return nil, nil
	})
	server.AddTool(&mcp.Tool{Name: "routine_create", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.Fatal("preflight invoked a tool")
		return nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	var requests atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer owner-token" {
			t.Fatal("used admin or foreign token")
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	path := filepath.Join(t.TempDir(), "private-tokens.json")
	cfg := communication.Config{ControlAuth: "admin-token", ToolHubURL: httpServer.URL, ControlTokensFile: path}
	user := communication.User{ID: "alice", RuntimeID: "alice-runtime", PolicyVersion: "reviewed-policy"}
	owner := identity.Envelope{PrincipalID: user.ID, RuntimeID: user.RuntimeID, PolicyVersion: user.PolicyVersion}
	foreign := owner
	foreign.PrincipalID = "bob"
	for _, variant := range []string{"admitted", "managed-name", "not-granted", "foreign", "stale-runtime", "stale-policy", "ambiguous", "invalid"} {
		tokens := map[string]identity.Envelope{"owner-token": owner, "foreign-token": foreign}
		name := "routine_list"
		switch variant {
		case "managed-name":
			name = "routine_create"
		case "not-granted":
			name = "routine_snooze"
		case "foreign":
			delete(tokens, "owner-token")
		case "stale-runtime":
			wrong := owner
			wrong.RuntimeID = "other-runtime"
			tokens["owner-token"] = wrong
		case "stale-policy":
			wrong := owner
			wrong.PolicyVersion = "other-policy"
			tokens["owner-token"] = wrong
		case "ambiguous":
			tokens["duplicate-token"] = owner
		}
		data, _ := json.Marshal(tokens)
		if variant == "invalid" {
			data = []byte("invalid")
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		before := requests.Load()
		got := admittedHubTool(context.Background(), &http.Client{Timeout: time.Second}, cfg, user, name)
		if got != (variant == "admitted" || variant == "managed-name") {
			t.Fatal(variant, got)
		}
		if variant != "admitted" && variant != "managed-name" && variant != "not-granted" && requests.Load() != before {
			t.Fatal("wrong identity reached MCP", variant)
		}
	}
}
