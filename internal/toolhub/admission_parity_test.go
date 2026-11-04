package toolhub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
)

// M31.11 S3: admission parity — a direct projected call and the same call
// routed through `tool_call` produce identical results; a member the
// profile never selected is refused through both paths with nothing
// reaching any executor.
func TestDirectAndToolCallAdmissionParity(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	s, auth, _, profile := managedStore(t)
	definition := r5ExecutorDefinition()
	// A cataloged member the profile does not select — the extended-only
	// surface. It must be refused identically whether addressed directly or
	// through tool_call.
	definition.Tools = append(definition.Tools,
		api.ToolSpec{Name: "session_orphan", Effect: api.ReadEffect, CapabilityID: "sessions.orphan", Executor: "runtime:sessions", Requires: []string{"sessions.read"}, Audience: api.AudienceModel,
			Description: "Extended member never selected into the profile."})
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	for _, tool := range definition.Tools {
		if tool.Name == "session_orphan" {
			continue
		}
		profile.Selections = append(profile.Selections, api.CapabilitySelection{
			CapabilityID: tool.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
			ImplementationDigest: digest, ToolName: tool.Name, Name: tool.Name})
	}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	var backendCalls []string
	g := &Gateway{Store: s, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Control: &ControlPlane{Store: s, Now: time.Now},
		Backend: backendFunc(func(_ context.Context, _ api.EffectiveBinding, tool api.ToolSpec, _ map[string]any) (api.BackendResult, error) {
			backendCalls = append(backendCalls, tool.Name)
			return api.BackendResult{Text: "backend"}, nil
		})}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpConnect(t, server.URL, aliceToken)
	call := func(name string, args map[string]any) (*mcp.CallToolResult, error) {
		return client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	}
	text := func(res *mcp.CallToolResult) string {
		out := ""
		for _, content := range res.Content {
			if tc, ok := content.(*mcp.TextContent); ok {
				out += tc.Text
			}
		}
		return out
	}

	// Admitted member: direct and nested produce the same body.
	direct, err := call("platform_status", map[string]any{})
	if err != nil {
		t.Fatalf("direct platform_status: %v", err)
	}
	nested, err := call("tool_call", map[string]any{"tool": "platform_status", "arguments": map[string]any{}})
	if err != nil {
		t.Fatalf("nested platform_status: %v", err)
	}
	if d, n := text(direct), text(nested); d == "" || d != n {
		t.Fatalf("parity broken: direct=%q nested=%q", d, n)
	}

	// platform_usage: the pinned usage member must reach its real handler
	// through the projected path — parity on an identical "no handler" error
	// would be parity of a dead feature.
	usageDirect, err := call("platform_usage", map[string]any{})
	if err != nil {
		t.Fatalf("direct platform_usage: %v", err)
	}
	usageNested, err := call("tool_call", map[string]any{"tool": "platform_usage", "arguments": map[string]any{}})
	if err != nil {
		t.Fatalf("nested platform_usage: %v", err)
	}
	d, n := text(usageDirect), text(usageNested)
	if d == "" || d != n {
		t.Fatalf("platform_usage parity broken: direct=%q nested=%q", d, n)
	}
	if !strings.Contains(d, `"available"`) || strings.Contains(d, "no platform handler") {
		t.Fatalf("platform_usage never reached its handler: %q", d)
	}

	// Unselected member: refused through both paths, executor untouched.
	if _, err := call("session_orphan", map[string]any{}); err == nil {
		t.Fatal("unselected member callable directly")
	}
	res, err := call("tool_call", map[string]any{"tool": "session_orphan", "arguments": map[string]any{}})
	if err == nil && !res.IsError && !strings.Contains(text(res), "denied") && !strings.Contains(text(res), "unavailable") {
		t.Fatalf("unselected member callable through tool_call: %s", text(res))
	}
	for _, name := range backendCalls {
		if name == "session_orphan" {
			t.Fatal("unselected member reached the backend")
		}
	}
}
