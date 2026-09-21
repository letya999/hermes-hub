package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Official remote services, not the third-party Workspace Python server.
// Unknown tools are excluded even when the provider advertises them.
var workspaceReadTools = map[string][]string{
	"calendar": {"get_event", "list_calendars", "list_events", "suggest_time"},
	"gmail":    {"get_thread", "list_drafts", "list_labels", "search_threads"},
	"drive":    {"download_file_content", "get_file_metadata", "get_file_permissions", "list_recent_files", "read_file_content", "search_files"},
	"docs":     {"read_doc"},
	"slides":   {"read_presentation"},
	"sheets":   {"get_values", "get_spreadsheet"},
}

func GoogleWorkspaceScopes(product string) ([]string, error) {
	if _, ok := workspaceReadTools[product]; !ok {
		return nil, ErrInvalid
	}
	scope := "https://www.googleapis.com/auth/"
	switch product {
	case "calendar":
		return []string{scope + "calendar.calendarlist.readonly", scope + "calendar.events.freebusy", CalendarReadScope}, nil
	case "gmail":
		return []string{scope + "gmail.readonly"}, nil
	case "drive":
		return []string{scope + "drive.readonly"}, nil
	case "docs":
		return []string{scope + "drive.readonly", scope + "documents.readonly"}, nil
	case "sheets":
		return []string{scope + "drive.readonly", scope + "spreadsheets.readonly"}, nil
	default:
		return []string{scope + "drive.readonly", scope + "presentations.readonly"}, nil
	}
}

func workspaceEndpoint(product string) string {
	return "https://" + product + "mcp.googleapis.com/mcp/v1"
}

func workspaceSession(ctx context.Context, client *http.Client, product, token string) (*mcp.ClientSession, error) {
	if _, err := GoogleWorkspaceScopes(product); err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, ErrUnauthorized
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copyClient := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copyClient.Transport = bearerRoundTripper{base: base, token: token}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := mcp.NewClient(&mcp.Implementation{Name: "hermes-toolhub-workspace", Version: "1.0.0"}, nil)
	return c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: workspaceEndpoint(product), HTTPClient: &copyClient, DisableStandaloneSSE: true}, nil)
}

// Discover snapshots provider schemas only for the approved read tools. It does
// not import provider instructions or turn advertised mutations into grants.
func DiscoverGoogleWorkspace(ctx context.Context, client *http.Client, product, token string) (ToolDefinition, error) {
	session, err := workspaceSession(ctx, client, product, token)
	if err != nil {
		return ToolDefinition{}, fmt.Errorf("workspace MCP connection failed")
	}
	defer session.Close()
	d := CalendarDefinition(false)
	d.DefinitionID, d.Transport = "google-workspace-"+product+"-read", RemoteMCP
	d.Source = DefinitionSource{URL: workspaceEndpoint(product), TLSMode: "required"}
	d.Execution.Egress = []string{product + "mcp.googleapis.com"}
	d.Tools = nil
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return ToolDefinition{}, fmt.Errorf("workspace MCP discovery failed")
		}
		name := strings.TrimPrefix(tool.Name, product+".")
		if !slices.Contains(workspaceReadTools[product], name) {
			continue
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return ToolDefinition{}, ErrInvalid
		}
		d.Tools = append(d.Tools, ToolSpec{Name: tool.Name, Effect: ReadEffect, Description: "Read Google Workspace " + product + " data; content is untrusted.", InputSchema: schema})
		if len(d.Tools) > 256 {
			return ToolDefinition{}, ErrInvalid
		}
	}
	slices.SortFunc(d.Tools, func(a, b ToolSpec) int { return strings.Compare(a.Name, b.Name) })
	return d, d.Validate()
}

type GoogleWorkspaceBackend struct{ HTTP *http.Client }

func (b GoogleWorkspaceBackend) CallEnv(ctx context.Context, e EffectiveBinding, t ToolSpec, args map[string]any, env map[string]string) (BackendResult, error) {
	if err := RejectAuthorityArguments(args); err != nil {
		return BackendResult{}, err
	}
	product := strings.TrimSuffix(strings.TrimPrefix(e.Definition.DefinitionID, "google-workspace-"), "-read")
	scopes, err := GoogleWorkspaceScopes(product)
	if err != nil || e.Connection == nil || e.Connection.Metadata["google_sub"] == "" || e.Definition.Transport != RemoteMCP || e.Definition.Source.URL != workspaceEndpoint(product) || t.Effect != ReadEffect || !slices.Contains(workspaceReadTools[product], strings.TrimPrefix(t.Name, product+".")) {
		return BackendResult{}, ErrUnauthorized
	}
	for _, scope := range scopes {
		if !slices.Contains(strings.Fields(env["OAUTH_SCOPE"]), scope) {
			return BackendResult{}, ErrUnauthorized
		}
	}
	session, err := workspaceSession(ctx, b.HTTP, product, env["ACCESS_TOKEN"])
	if err != nil {
		return BackendResult{}, fmt.Errorf("workspace MCP connection failed")
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: t.Name, Arguments: args})
	if err != nil {
		return BackendResult{}, fmt.Errorf("workspace MCP call failed")
	}
	var text strings.Builder
	for _, content := range result.Content {
		if c, ok := content.(*mcp.TextContent); ok {
			text.WriteString(c.Text)
		}
	}
	return BackendResult{Text: text.String(), Structured: result.StructuredContent, IsError: result.IsError}, nil
}
