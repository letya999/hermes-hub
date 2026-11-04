package toolhub

import (
	"context"
	"encoding/json"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// platform_status reports allowlisted platform diagnostics only (ADR-0094
// M31.8): service identity plus the caller's own admitted tool counts. It
// never exposes environment contents, auth files, credential-store keys or
// other secret-bearing fields — the field list is closed, like every other
// diagnostic surface in this repository.
func (g *Gateway) addPlatformStatusTool(server *mcp.Server, auth identity.Envelope) {
	if g == nil || g.Store == nil || server == nil {
		return
	}
	server.AddTool(&mcp.Tool{
		Name:        "platform_status",
		Description: "Report bounded platform health for this session: ToolHub identity, how many tools are admitted for you and how many sit beyond the visible core. Diagnostics only; no account data, environment or credentials.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body, err := g.platformStatus(auth)
		if err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(body)
		return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
	})
}

func (g *Gateway) platformStatus(auth identity.Envelope) (map[string]any, error) {
	projected, err := g.Store.ListProjectedTools(auth)
	if err != nil {
		return nil, err
	}
	if projected, err = g.filterProjectedTools(auth, projected); err != nil {
		return nil, err
	}
	core, err := g.Store.CoreToolNames(auth)
	if err != nil {
		return nil, err
	}
	coreVisible := 0
	for _, tool := range projected {
		if core == nil || core[tool.Name] {
			coreVisible++
		}
	}
	return map[string]any{
		"service":        "hermes-toolhub",
		"tools_admitted": len(projected),
		"tools_visible":  coreVisible,
		"tools_extended": len(projected) - coreVisible,
	}, nil
}
