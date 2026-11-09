package toolhub

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type uncertainCallError struct{ operationID string }

func (e *uncertainCallError) Error() string {
	return audit.ErrUncertain.Error() + "; operation_id=" + e.operationID
}
func (e *uncertainCallError) Unwrap() error { return audit.ErrUncertain }

func uncertainCallData(err error) map[string]any {
	var uncertain *uncertainCallError
	if !errors.As(err, &uncertain) {
		return nil
	}
	return map[string]any{"status": "uncertain", "operation_id": uncertain.operationID, "retryable": false}
}

// An uncertain business result is an application error, not a JSON-RPC transport
// failure that clients may retry. Private backend payloads never enter it.
func (g *Gateway) mcpCall(ctx context.Context, auth identity.Envelope, name string, args map[string]any) (*mcp.CallToolResult, error) {
	result, err := g.call(ctx, auth, name, args)
	if data := uncertainCallData(err); data != nil {
		return &mcp.CallToolResult{IsError: true, StructuredContent: data, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
	}
	if errors.Is(err, api.ErrInvalid) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
	}
	return result, err
}
