package toolhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSystemGuideAvailableWithoutConnectorGrants(t *testing.T) {
	g := &Gateway{Store: NewStore(), Tokens: map[string]identity.Envelope{aliceToken: aliceAuth(), bobToken: bobAuth()}, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		t.Fatal("reference read executed a provider tool")
		return api.BackendResult{}, nil
	})}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, token := range []string{aliceToken, bobToken} {
		client := mcp.NewClient(&mcp.Implementation{Name: "guide-test", Version: "1"}, nil)
		session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: token}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		listed, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !toolListed(listed, "system_guide") {
			t.Fatal("mandatory reference missing without connector grants")
		}
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "system_guide", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			t.Fatalf("reference read failed: %v", err)
		}
		if len(result.Content) != 1 {
			t.Fatal("reference must be one text document")
		}
		body, ok := result.Content[0].(*mcp.TextContent)
		if !ok || !strings.Contains(body.Text, "name: hermes-hub-system-guide") {
			t.Fatal("canonical skill not returned")
		}
		if err := g.RefreshProjection(); err != nil {
			t.Fatal(err)
		}
		listed, err = session.ListTools(t.Context(), nil)
		if err != nil || !toolListed(listed, "system_guide") {
			t.Fatal("refresh removed mandatory reference")
		}
	}
}
