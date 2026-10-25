package toolhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The google-calendar-mcp server (Node MCP SDK) validates params.arguments as
// a record and rejects a literal null. EnsureReady probes it with no args, so
// the wire must carry "{}" or omit the field — never "null".
func TestEnsureReadyProbeNeverSendsNullArguments(t *testing.T) {
	testEnsureReadyOAuthProbe(t, false)
}

func TestEnsureReadyUsesNonCatalogOAuthProbe(t *testing.T) {
	testEnsureReadyOAuthProbe(t, true)
}

func TestEnsureReadyRejectsWriteOAuthProbe(t *testing.T) {
	testEnsureReadyOAuthProbe(t, true, true)
}

func TestWorkspaceMCPUsesContainerBackend(t *testing.T) {
	testEnsureReadyOAuthProbe(t, true, false, true)
}

func testEnsureReadyOAuthProbe(t *testing.T, fromRecipe bool, denyWrite ...bool) {
	calls := 0
	provider := mcp.NewServer(&mcp.Implementation{Name: "provider-fixture", Version: "1"}, nil)
	provider.AddTool(&mcp.Tool{Name: "list-calendars", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls++
		if fromRecipe && !strings.Contains(string(req.Params.Arguments), `"limit":1`) {
			t.Error("recipe probe args lost")
		}
		if string(req.Params.Arguments) == "null" {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: `invalid_type: expected record, received null`}}}, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"calendars":[]}`}}}, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return provider }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpServer.Close()
	definition := statefulContainerDefinition()
	definition.DefinitionID = "google-calendar-mcp"
	definition.Source.Repository = "https://github.com/nspady/google-calendar-mcp"
	definition.Source.CommitSHA = "7c788f58e8b00db66ddd0eccd81fdc3dda0ecf9b"
	if fromRecipe {
		entry, matched, err := api.PreparedForRepository(definition.Source.Repository, "")
		if err != nil || !matched {
			t.Fatal("OAuth fixture missing", err)
		}
		definition.OAuth = entry.OAuth
		definition.OAuth.ProbeArgs = map[string]any{"limit": float64(1)}
		definition.Source.Repository = "https://github.com/example/calendar-mcp"
		definition.Source.CommitSHA = strings.Repeat("a", 40)
	}
	definition.Tools = []api.ToolSpec{{Name: "list-calendars", Effect: api.ReadEffect}}
	if len(denyWrite) > 1 && denyWrite[1] {
		definition.DefinitionID = "google-workspace-mcp"
	}
	writeProbe := len(denyWrite) > 0 && denyWrite[0]
	if writeProbe {
		definition.Tools[0].Effect = api.WriteEffect
	}
	effective := api.EffectiveBinding{
		Definition: definition,
		WorkloadID: "work-probe",
		Connection: &api.Connection{Metadata: map[string]string{"mcp_endpoint": httpServer.URL + "/mcp"}},
	}
	backend := api.MCPBackend{
		HTTPClient: httpServer.Client(),
		AdmissionVerifier: func(context.Context, api.EffectiveBinding) (api.AdmissionReceipt, error) {
			return api.AdmissionReceipt{WorkloadID: "work-probe", State: "running", Enforced: true, ImageDigest: definition.Source.Digest, SidecarImages: definition.Workload.SidecarImages, Execution: definition.Execution}, nil
		},
		AdmissionRelease: func(context.Context, string) error { return nil },
	}
	err := backend.EnsureReady(context.Background(), effective, nil)
	if writeProbe {
		if !errors.Is(err, api.ErrUnauthorized) || calls != 0 {
			t.Fatalf("write probe executed: calls=%d err=%v", calls, err)
		}
		return
	}
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) && strings.Contains(err.Error(), "received null") {
			t.Fatalf("probe sent arguments:null on the wire: %v", err)
		}
		t.Fatalf("EnsureReady err=%v", err)
	}
	if calls != 1 {
		t.Fatal("provider read was not verified", calls)
	}
}
