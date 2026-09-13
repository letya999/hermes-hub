package devcheck

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolHubContract probes an operator-provided, private ToolHive/vMCP endpoint.
// It is intentionally opt-in: a local fake or an unset endpoint cannot be
// mistaken for remote/stateful MCP evidence.
func ToolHubContract(ctx context.Context) error {
	endpoint := os.Getenv("TOOLHIVE_VMCP_ENDPOINT")
	remoteTool := os.Getenv("TOOLHIVE_REMOTE_TOOL")
	statefulTool := os.Getenv("TOOLHIVE_STATEFUL_TOOL")
	if endpoint == "" || remoteTool == "" || statefulTool == "" {
		return fmt.Errorf("set TOOLHIVE_VMCP_ENDPOINT, TOOLHIVE_REMOTE_TOOL and TOOLHIVE_STATEFUL_TOOL")
	}
	if err := toolhub.ValidateBackendEndpoint(endpoint); err != nil {
		return err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hermes-toolhub-contract", Version: "0.2.0"}, nil)
	httpClient := &http.Client{Timeout: 30 * time.Second, Transport: contractBearerTransport{base: http.DefaultTransport, token: os.Getenv("TOOLHIVE_VMCP_TOKEN")}}
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, err := client.Connect(connectCtx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return fmt.Errorf("ToolHive/vMCP connect: %w", err)
	}
	defer session.Close()
	tools, err := session.ListTools(connectCtx, nil)
	if err != nil {
		return fmt.Errorf("ToolHive/vMCP tools/list: %w", err)
	}
	available := map[string]bool{}
	for _, candidate := range tools.Tools {
		available[candidate.Name] = true
	}
	for _, name := range []string{remoteTool, statefulTool} {
		if !available[name] {
			return fmt.Errorf("ToolHive/vMCP tool %q was not listed", name)
		}
		result, err := session.CallTool(connectCtx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil {
			return fmt.Errorf("ToolHive/vMCP call %q: %w", name, err)
		}
		if result.IsError {
			return fmt.Errorf("ToolHive/vMCP tool %q returned an MCP error", name)
		}
	}
	fmt.Printf("ToolHive/vMCP contract passed: tools=%d remote=%s stateful=%s\n", len(tools.Tools), remoteTool, statefulTool)
	return nil
}

type contractBearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t contractBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	if strings.TrimSpace(t.token) != "" {
		copyRequest.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(copyRequest)
}
