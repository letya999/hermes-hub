package devcheck

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolHubGatewayContract exercises the opt-in path through the Go gateway and
// a real operator-provided ToolHive/vMCP endpoint. It does not alter the
// active runtime or persist the fixture catalog.
func ToolHubGatewayContract(ctx context.Context) error {
	endpoint := os.Getenv("TOOLHIVE_VMCP_ENDPOINT")
	remoteTool := os.Getenv("TOOLHIVE_REMOTE_TOOL")
	statefulTool := os.Getenv("TOOLHIVE_STATEFUL_TOOL")
	digest := os.Getenv("TOOLHIVE_STATEFUL_IMAGE_DIGEST")
	if endpoint == "" || remoteTool == "" || statefulTool == "" || digest == "" {
		return fmt.Errorf("set TOOLHIVE_VMCP_ENDPOINT, TOOLHIVE_REMOTE_TOOL, TOOLHIVE_STATEFUL_TOOL and TOOLHIVE_STATEFUL_IMAGE_DIGEST")
	}
	if err := api.ValidateBackendEndpoint(endpoint); err != nil {
		return err
	}
	admission, err := api.ControllerAdmissionVerifierFromEnv()
	if err != nil || admission == nil {
		return fmt.Errorf("set HUB_TOOLHIVE_ADMISSION_ENDPOINT to a controller returning an enforcement receipt: %v", err)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "contract-user", ExternalIdentityID: "contract-transport", ContextID: "contract-user", RuntimeID: "contract-runtime", ConversationID: "contract-conversation", DeliveryTargetID: "contract-delivery", PolicyVersion: "contract-policy"}
	store := toolhub.NewStore()
	remoteDefinition := gatewayProbeDefinition("remote-fixture", api.RemoteMCP, remoteTool, api.Shared, false, "https://remote-fixture.invalid/mcp", "")
	statefulDefinition := gatewayProbeDefinition("stateful-fixture", api.ContainerMCP, statefulTool, api.PerUser, true, "", digest)
	for _, definition := range []api.ToolDefinition{remoteDefinition, statefulDefinition} {
		if err := store.RegisterDefinition(definition); err != nil {
			return err
		}
	}
	remoteConnection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "remote-connection", Owner: api.OwnerRef{Type: api.ContextOwner, ID: auth.ContextID}, DefinitionID: remoteDefinition.DefinitionID, Revision: 1, Status: api.ActiveStatus, Metadata: map[string]string{"mcp_endpoint": endpoint}}
	statefulConnection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "stateful-connection", Owner: api.OwnerRef{Type: api.ContextOwner, ID: auth.ContextID}, DefinitionID: statefulDefinition.DefinitionID, Revision: 1, Status: api.ActiveStatus, Metadata: map[string]string{"mcp_endpoint": endpoint}}
	for _, connection := range []api.Connection{remoteConnection, statefulConnection} {
		if err := store.PutConnection(connection); err != nil {
			return err
		}
	}
	remoteBinding := gatewayProbeBinding(auth, remoteDefinition, remoteConnection)
	statefulBinding := gatewayProbeBinding(auth, statefulDefinition, statefulConnection)
	for _, binding := range []api.ToolBinding{remoteBinding, statefulBinding} {
		if err := store.PutBinding(binding); err != nil {
			return err
		}
	}

	gatewayToken := os.Getenv("TOOLHUB_GATEWAY_TOKEN")
	if gatewayToken == "" {
		gatewayToken = strings.Repeat("g", 32)
	}
	handler, err := (&toolhub.Gateway{Store: store, Backend: api.MCPBackend{Token: os.Getenv("TOOLHIVE_VMCP_TOKEN"), AdmissionVerifier: admission}, Tokens: map[string]identity.Envelope{gatewayToken: auth}}).Handler()
	if err != nil {
		return err
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "hermes-toolhub-gateway-contract", Version: "0.2.0"}, nil)
	connectCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	transport := &mcp.StreamableClientTransport{Endpoint: server.URL + toolhub.DefaultEndpointPath, HTTPClient: &http.Client{Timeout: 45 * time.Second, Transport: bearerTransport{token: gatewayToken}}}
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return fmt.Errorf("ToolHub gateway connect: %w", err)
	}
	defer session.Close()
	listed, err := session.ListTools(connectCtx, nil)
	if err != nil {
		return fmt.Errorf("ToolHub gateway tools/list: %w", err)
	}
	want := []struct {
		binding api.ToolBinding
		name    string
	}{
		{remoteBinding, api.ProjectedToolName(remoteDefinition.DefinitionID, remoteDefinition.Version, remoteTool)},
		{statefulBinding, api.ProjectedToolName(statefulDefinition.DefinitionID, statefulDefinition.Version, statefulTool)},
	}
	for _, candidate := range want {
		if !hasMCPTool(listed.Tools, candidate.name) {
			return fmt.Errorf("ToolHub gateway tool %q was not listed", candidate.name)
		}
		result, callErr := session.CallTool(connectCtx, &mcp.CallToolParams{Name: candidate.name, Arguments: map[string]any{}})
		if callErr != nil || result.IsError {
			return fmt.Errorf("ToolHub gateway call %q failed: %v", candidate.name, callErr)
		}
	}
	if _, err := session.CallTool(connectCtx, &mcp.CallToolParams{Name: want[0].name, Arguments: map[string]any{"context_id": "foreign-user"}}); err == nil {
		return fmt.Errorf("ToolHub gateway accepted a model-supplied authority argument")
	}
	if err := store.SetBindingStatus(statefulBinding.ToolBindingID, api.RevokedStatus); err != nil {
		return err
	}
	if _, err := session.CallTool(connectCtx, &mcp.CallToolParams{Name: want[1].name, Arguments: map[string]any{}}); err == nil {
		return fmt.Errorf("ToolHub gateway accepted a revoked binding")
	}
	fmt.Printf("ToolHub gateway contract passed: endpoint=%s tools=%d remote=%s stateful=%s\n", endpoint, len(listed.Tools), remoteTool, statefulTool)
	return nil
}

func gatewayProbeDefinition(id string, transport api.Transport, name string, class api.WorkloadClass, stateful bool, url, digest string) api.ToolDefinition {
	source := api.DefinitionSource{URL: url, TLSMode: "required"}
	health := api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5}
	if transport == api.ContainerMCP {
		source = api.DefinitionSource{Image: "hermes-toolhive-fixture", Digest: digest}
		health = api.HealthProbe{Kind: "exec", Value: "fixture", TimeoutSeconds: 5}
	}
	mounts := []api.Mount(nil)
	if stateful {
		mounts = []api.Mount{{Source: "connection-state", Target: "/var/lib/hermes-fixture"}}
	}
	workload := api.WorkloadPolicy{Class: class, Stateful: stateful, Rationale: "contract probe"}
	if transport == api.ContainerMCP {
		workload.ToolHiveVersion = "v0.48.0"
		workload.SidecarImages = []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	}
	return api.ToolDefinition{Schema: api.SchemaVersion, DefinitionID: id, Version: "1.0.0", Transport: transport, Source: source, Tools: []api.ToolSpec{{Name: name, Effect: api.ReadEffect}}, Workload: workload, Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 4096, CPUMillis: 250, MemoryMiB: 64, MaxPIDs: 32, Egress: []string{"127.0.0.1"}, Mounts: mounts}, Health: health}
}

func gatewayProbeBinding(auth identity.Envelope, definition api.ToolDefinition, connection api.Connection) api.ToolBinding {
	return api.ToolBinding{Schema: api.SchemaVersion, ToolBindingID: api.DeterministicBindingID(auth.PrincipalID, auth.ContextID, auth.RuntimeID, definition.DefinitionID, definition.Version, connection.ConnectionID, ""), PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
}

func hasMCPTool(tools []*mcp.Tool, name string) bool {
	for _, candidate := range tools {
		if candidate.Name == name {
			return true
		}
	}
	return false
}

type bearerTransport struct {
	token string
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyRequest.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(copyRequest)
}
