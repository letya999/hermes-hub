package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// workspaceTestClient rewrites requests meant for the fixed Google endpoint
// onto a loopback MCP server so the real streamable transport runs end to end.
func workspaceTestClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host = target.Scheme, target.Host
		r2.Host = target.Host
		return http.DefaultTransport.RoundTrip(r2)
	})}
}

func workspaceServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "workspace-fixture", Version: "1"}, nil)
	type in struct {
		Q string `json:"q,omitempty"`
	}
	for _, name := range []string{"calendar.list_calendars", "calendar.evil_write"} {
		mcp.AddTool(server, &mcp.Tool{Name: name}, func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, in, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "cal"}}}, a, nil
		})
	}
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(srv.Close)
	return srv
}

// DiscoverGoogleWorkspace keeps only the approved read tools for the product
// and builds a remote definition pinned to the fixed endpoint.
func TestDiscoverGoogleWorkspaceLoopback(t *testing.T) {
	t.Parallel()
	client := workspaceTestClient(t, workspaceServer(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d, err := DiscoverGoogleWorkspace(ctx, client, "calendar", "tok")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if d.DefinitionID != "google-workspace-calendar-read" || d.Transport != RemoteMCP || d.Source.TLSMode != "required" || len(d.Execution.Egress) != 1 {
		t.Fatalf("definition shape: %+v", d)
	}
	if len(d.Tools) != 1 || d.Tools[0].Name != "calendar.list_calendars" || d.Tools[0].Effect != ReadEffect {
		t.Fatalf("only approved read tools may be imported: %+v", d.Tools)
	}
}

// CallEnv reaches the provider tool through the scope-checked session and
// returns the text payload.
func TestGoogleWorkspaceCallEnvLoopback(t *testing.T) {
	t.Parallel()
	client := workspaceTestClient(t, workspaceServer(t))
	scopes, err := GoogleWorkspaceScopes("calendar")
	if err != nil {
		t.Fatal(err)
	}
	e := EffectiveBinding{
		Definition: ToolDefinition{
			DefinitionID: "google-workspace-calendar-read",
			Transport:    RemoteMCP,
			Source:       DefinitionSource{URL: workspaceEndpoint("calendar")},
		},
		Connection: &Connection{Metadata: map[string]string{"google_sub": "sub-1"}},
	}
	env := map[string]string{"OAUTH_SCOPE": strings.Join(scopes, " ") + " openid", "ACCESS_TOKEN": "tok"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := (GoogleWorkspaceBackend{HTTP: client}).CallEnv(ctx, e, ToolSpec{Name: "calendar.list_calendars", Effect: ReadEffect}, nil, env)
	if err != nil || result.Text != "cal" || result.IsError {
		t.Fatalf("call: %v %+v", err, result)
	}
}
