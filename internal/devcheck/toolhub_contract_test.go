package devcheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolHubContractRequiresExplicitFixtureNames(t *testing.T) {
	for _, key := range []string{"TOOLHIVE_VMCP_ENDPOINT", "TOOLHIVE_REMOTE_TOOL", "TOOLHIVE_STATEFUL_TOOL"} {
		t.Setenv(key, "")
	}
	if err := ToolHubContract(context.Background()); err == nil {
		t.Fatal("unset ToolHive contract was accepted")
	}
}

func TestToolHubContractFixture(t *testing.T) {
	backend := mcp.NewServer(&mcp.Implementation{Name: "toolhive-fixture", Version: "1"}, nil)
	for _, name := range []string{"remote-read", "stateful-read"} {
		name := name
		backend.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name}}}, nil
		})
	}
	backendHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return backend }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backendHandler.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Setenv("TOOLHIVE_VMCP_ENDPOINT", server.URL+"/mcp")
	t.Setenv("TOOLHIVE_REMOTE_TOOL", "remote-read")
	t.Setenv("TOOLHIVE_STATEFUL_TOOL", "stateful-read")
	t.Setenv("TOOLHIVE_VMCP_TOKEN", "fixture-token")
	if err := ToolHubContract(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestToolHubGatewayContractFixture(t *testing.T) {
	backend := mcp.NewServer(&mcp.Implementation{Name: "toolhive-gateway-fixture", Version: "1"}, nil)
	for _, name := range []string{"remote-read", "stateful-read"} {
		name := name
		backend.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name}}}, nil
		})
	}
	backendHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return backend }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(backendHandler)
	defer server.Close()
	t.Setenv("TOOLHIVE_VMCP_ENDPOINT", server.URL+"/mcp")
	t.Setenv("TOOLHIVE_REMOTE_TOOL", "remote-read")
	t.Setenv("TOOLHIVE_STATEFUL_TOOL", "stateful-read")
	t.Setenv("TOOLHIVE_STATEFUL_IMAGE_DIGEST", "sha256:"+strings.Repeat("0", 64))
	digest := os.Getenv("TOOLHIVE_STATEFUL_IMAGE_DIGEST")
	definition := gatewayProbeDefinition("stateful-fixture", toolhub.ContainerMCP, "stateful-read", toolhub.PerUser, true, "", digest)
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "contract-user", ExternalIdentityID: "contract-transport", ContextID: "contract-user", RuntimeID: "contract-runtime", ConversationID: "contract-conversation", DeliveryTargetID: "contract-delivery", PolicyVersion: "contract-policy"}
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(toolhub.AdmissionReceipt{WorkloadID: toolhub.WorkloadInstanceID(definition.DefinitionID, definition.Workload.Class, auth.ContextID, ""), State: "running", Enforced: true, ImageDigest: definition.Source.Digest, SidecarImages: definition.Workload.SidecarImages, Execution: definition.Execution})
	}))
	defer controller.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", controller.URL)
	if err := ToolHubGatewayContract(context.Background()); err != nil {
		t.Fatal(err)
	}
}
