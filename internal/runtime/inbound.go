package runtime

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fetchInbound(ctx context.Context, id string) (string, error) {
	if len(id) < 4 || len(id) > 32 || !strings.HasPrefix(id, "in-") {
		return "", errors.New("invalid inbound ID")
	}
	for _, c := range id[3:] {
		if c < '0' || c > '9' {
			return "", errors.New("invalid inbound ID")
		}
	}
	tokenEnv := os.Getenv("HUB_TOOLHUB_TOKEN_ENV")
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	client := &http.Client{Transport: inboundBearer{token: os.Getenv(tokenEnv), base: http.DefaultTransport}}
	call := func(name string) (*mcp.CallToolResult, error) {
		session, err := mcp.NewClient(&mcp.Implementation{Name: "hub-inbound", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: toolHubEndpoint(), HTTPClient: client}, nil)
		if err != nil {
			return nil, err
		}
		defer session.Close()
		return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"id": id}})
	}
	result, err := call("inbox_fetch")
	if err != nil || result == nil || result.IsError {
		// Managed profiles publish the tool under its canonical name; older
		// publications and unmanaged spaces expose the connector-style
		// projected name instead.
		definition := agenttools.HubToolsDefinition()
		result, err = call(api.ProjectedToolName(definition.DefinitionID, definition.Version, "inbox_fetch"))
		if err != nil || result == nil || result.IsError {
			return "", errors.New("inbound file could not be saved")
		}
	}
	data, _ := result.StructuredContent.(map[string]any)
	rel, _ := data["path"].(string)
	if path.Clean(rel) != rel || (rel != "inbox/"+id+"/files" && !strings.HasPrefix(rel, "inbox/"+id+"/files/")) {
		return "", errors.New("inbound file receipt is invalid")
	}
	return rel, nil
}

type inboundBearer struct {
	token string
	base  http.RoundTripper
}

func (b inboundBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(copy)
}
