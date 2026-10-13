package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Use the runtime's existing owner-bound token, never the gateway's admin
// bearer: a successful control-plane request does not imply an agent grant.
func ownerHubSession(ctx context.Context, client *http.Client, config communication.Config, user communication.User) (*mcp.ClientSession, error) {
	if config.ToolHubURL == "" || config.ControlTokensFile == "" {
		return nil, errors.New("owner-bound ToolHub session unavailable")
	}
	f, err := os.Open(config.ControlTokensFile)
	if err != nil {
		return nil, errors.New("owner-bound ToolHub session unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("owner-bound ToolHub session unavailable")
	}
	var tokens map[string]identity.Envelope
	if json.Unmarshal(data, &tokens) != nil {
		return nil, errors.New("owner-bound ToolHub session unavailable")
	}
	token := ""
	for candidate, envelope := range tokens {
		if envelope.PrincipalID == user.ID && (user.RuntimeID == "" || envelope.RuntimeID == user.RuntimeID) && (user.PolicyVersion == "" || envelope.PolicyVersion == user.PolicyVersion) {
			if token != "" {
				return nil, errors.New("owner-bound ToolHub session unavailable")
			}
			token = candidate
		}
	}
	if token == "" {
		return nil, errors.New("owner-bound ToolHub session unavailable")
	}
	copy := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copy.Transport = ownerBearer{transport, token}
	c := mcp.NewClient(&mcp.Implementation{Name: "hub-e2e-admission", Version: "1"}, nil)
	session, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: strings.TrimRight(config.ToolHubURL, "/") + toolhub.DefaultEndpointPath, HTTPClient: &copy}, nil)
	if err != nil {
		return nil, errors.New("owner-bound ToolHub session unavailable")
	}
	return session, nil
}

func admittedHubTool(ctx context.Context, client *http.Client, config communication.Config, user communication.User, name string) bool {
	session, err := ownerHubSession(ctx, client, config, user)
	if err != nil {
		return false
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return false
	}
	definition := agenttools.HubToolsDefinition()
	projected := toolhub.ProjectedToolName(definition.DefinitionID, definition.Version, name)
	for _, tool := range listed.Tools {
		if tool.Name == name || tool.Name == projected {
			return true
		}
	}
	return false
}

type ownerBearer struct {
	transport http.RoundTripper
	token     string
}

func (b ownerBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.transport.RoundTrip(clone)
}
