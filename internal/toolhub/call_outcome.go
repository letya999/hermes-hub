package toolhub

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
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
	// Agents see MCP-qualified names (mcp__toolhub__*) while the registry
	// admits bare projected names only — normalize at the single nested-call
	// funnel so the pinned control tool_call and the executor dispatch
	// resolve identically.
	name = strings.TrimPrefix(strings.TrimSpace(name), toolhubNamePrefix)
	result, err := g.call(ctx, auth, name, args)
	// Controls entries ("invoke") and this dispatcher's own name are grant or
	// surface labels, never projected tools — a nested {tool, arguments}
	// envelope under one unwraps once or the model sees a misleading
	// record-not-found. The denied outer call stays audited; the inner call
	// re-admits on its own name under a fresh call ID, so the ledger keeps
	// the pair as two operations. Admitted outer names never reach the retry.
	if errors.Is(err, api.ErrNotFound) && len(args) <= 2 {
		if inner, ok := args["tool"].(string); ok && strings.TrimSpace(inner) != "" {
			corr := api.CallCorrelationFrom(ctx)
			corr.ToolCallID = api.NewToolCallID()
			ctx = api.WithCallCorrelation(ctx, corr)
			innerArgs, _ := args["arguments"].(map[string]any)
			result, err = g.call(ctx, auth, strings.TrimPrefix(strings.TrimSpace(inner), toolhubNamePrefix), innerArgs)
		}
	}
	if data := uncertainCallData(err); data != nil {
		return &mcp.CallToolResult{IsError: true, StructuredContent: data, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
	}
	if errors.Is(err, api.ErrInvalid) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
	}
	return result, err
}
