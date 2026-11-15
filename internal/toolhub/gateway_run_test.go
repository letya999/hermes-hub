package toolhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunMCPRouteUsesRequestCorrelationWithoutSession(t *testing.T) {
	store, auth, binding := seededStore(t)
	const token = "synthetic-run-route-token-000000"
	var seen api.CallCorrelation
	g := &Gateway{Store: store, Tokens: map[string]identity.Envelope{token: auth}, Backend: backendFunc(func(ctx context.Context, _ api.EffectiveBinding, _ api.ToolSpec, _ map[string]any) (api.BackendResult, error) {
		seen = api.CallCorrelationFrom(ctx)
		return api.BackendResult{Text: "ok"}, nil
	})}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "run-route", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + RunEndpointPath,
		HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: token, jobID: "job-1", runID: "run-1"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.ID() != "" {
		t.Fatal("run route retained an MCP session")
	}
	name := api.ProjectedToolName(binding.DefinitionID, binding.DefinitionVersion, "search")
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "probe"}}); err != nil {
		t.Fatal(err)
	}
	if seen.JobID != "job-1" || seen.HermesRunID != "run-1" {
		t.Fatalf("run route lost request correlation: %+v", seen)
	}
}
