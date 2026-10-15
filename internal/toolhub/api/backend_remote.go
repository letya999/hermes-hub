package api

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"time"
)

// remoteReady proves the endpoint is still an MCP server and still advertises
// every bound tool before a binding goes active.
func (b MCPBackend) remoteReady(ctx context.Context, effective EffectiveBinding, environment map[string]string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probe, err := ProbeRemoteMCP(probeCtx, effective.Definition, environment)
	if err != nil {
		return fmt.Errorf("%w: remote MCP readiness: %v", ErrIsolation, sanitizeRemoteError(err))
	}
	advertised := map[string]bool{}
	for _, tool := range probe.Tools {
		advertised[tool.Name] = true
	}
	for _, tool := range effective.Definition.Tools {
		if !advertised[tool.Name] {
			return fmt.Errorf("%w: remote endpoint no longer serves %q", ErrStale, tool.Name)
		}
	}
	return nil
}

// callRemoteMCP projects one tools/call onto the owner's remote endpoint. The
// endpoint is re-validated at call time and credentials arrive only as the
// declared HTTP headers — no ToolHive admission, no credential files.
func (b MCPBackend) CallRemoteMCP(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, environment map[string]string) (BackendResult, error) {
	endpoint := effective.Definition.Source.URL
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		return BackendResult{}, err
	}
	headers, err := CredentialHeaders(effective.Definition, environment)
	if err != nil {
		return BackendResult{}, err
	}
	session, err := connectRemoteMCP(ctx, endpoint, RemoteHTTPClient(endpoint, headers))
	if err != nil {
		return BackendResult{}, fmt.Errorf("%w: remote MCP session: %v", ErrIsolation, sanitizeRemoteError(err))
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: arguments})
	closeErr := session.Close()
	if err != nil {
		return BackendResult{}, fmt.Errorf("%w: remote MCP call: %v", ErrIsolation, sanitizeRemoteError(err))
	}
	if closeErr != nil {
		return BackendResult{}, fmt.Errorf("%w: remote MCP close: %v", ErrIsolation, sanitizeRemoteError(closeErr))
	}
	var text strings.Builder
	for _, content := range result.Content {
		if value, ok := content.(*mcp.TextContent); ok {
			text.WriteString(value.Text)
		}
	}
	return BackendResult{Text: text.String(), Content: result.Content, Structured: result.StructuredContent, IsError: result.IsError}, nil
}
