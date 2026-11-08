package toolhub

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
)

// r5ExecutorDefinition carries one member per remote executor family the
// catalog r5 declares: the facade, the pinned platform layer and a flow
// executor that has no hub-side backend yet.
func r5ExecutorDefinition() api.ToolDefinition {
	return api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "hub-agent-tools", Version: "7.0.0", Transport: api.AgentTools,
		Source:    api.DefinitionSource{},
		Workload:  api.WorkloadPolicy{Class: api.PerUser, Rationale: "in-runtime executor inside the owning runtime"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    api.HealthProbe{Kind: "exec", Value: "hubctl", TimeoutSeconds: 5},
		Tools: []api.ToolSpec{
			{Name: "connector_list", Effect: api.ReadEffect, CapabilityID: "connectors.list", Executor: "runtime:connectors", Requires: []string{"connectors.discover"}, Audience: api.AudienceModel,
				Description: "List connectors visible to this space with their status."},
			{Name: "platform_status", Effect: api.ReadEffect, CapabilityID: "platform.status", Executor: "runtime:toolhub", Requires: []string{"platform.status"}, Audience: api.AudienceModel,
				Description: "Show own health and diagnostics with allowlisted fields only."},
			{Name: "platform_guide", Effect: api.ReadEffect, CapabilityID: "platform.guide", Executor: "runtime:toolhub", Requires: []string{"platform.guide"}, Audience: api.AudienceModel,
				Description: "Orient in this space."},
			{Name: "platform_bootstrap", Effect: api.WriteEffect, CapabilityID: "platform.bootstrap", Executor: "runtime:toolhub", Requires: []string{"platform.bootstrap"}, Audience: api.AudienceModel,
				Description: "Show or complete pending first-run setup steps.",
				Arguments:   []api.CLIArgument{{Name: "action", Type: "string"}, {Name: "step", Type: "string"}}},
			{Name: "platform_usage", Effect: api.ReadEffect, CapabilityID: "platform.usage", Executor: "runtime:toolhub", Requires: []string{"platform.usage"}, Audience: api.AudienceModel,
				Description: "Show space consumption.", Arguments: []api.CLIArgument{{Name: "period", Type: "string"}}},
			{Name: "tool_search", Effect: api.ReadEffect, CapabilityID: "platform.search", Executor: "runtime:toolhub", Requires: []string{"tools.list"}, Audience: api.AudienceModel,
				Description: "Search the extended tool inventory.", Arguments: []api.CLIArgument{{Name: "query", Type: "string"}, {Name: "name", Type: "string"}, {Name: "limit", Type: "integer"}}},
			{Name: "tool_call", Effect: api.WriteEffect, CapabilityID: "platform.call", Executor: "runtime:toolhub", Requires: []string{"tools.use"}, Audience: api.AudienceModel,
				Description: "Invoke one extended tool by name.", Arguments: []api.CLIArgument{{Name: "tool", Type: "string", Required: true}, {Name: "arguments", Type: "object", Required: true}}},
			{Name: "research_create", Effect: api.WriteEffect, CapabilityID: "research.create", Executor: "flow:deep_research", Requires: []string{"research.create"}, Audience: api.AudienceModel,
				Description: "Start an async deep-research job.",
				Arguments:   []api.CLIArgument{{Name: "topic", Type: "string", Required: true}}},
			{Name: "model_list", Effect: api.ReadEffect, CapabilityID: "models.list", Executor: "runtime:models", Requires: []string{"models.list"}, Audience: api.AudienceModel,
				Description: "List granted model routes."},
			{Name: "browser_act", Effect: api.WriteEffect, CapabilityID: "browser.act", Executor: "connector:browser", Requires: []string{"browser.act"}, Audience: api.AudienceModel,
				Description: "Drive one browser action.",
				Arguments:   []api.CLIArgument{{Name: "action", Type: "string", Required: true}}},
		},
	}
}

// M31.9: cataloged tools dispatch through their declared executor. Remote
// executors never reach the in-runtime handler channel: runtime:connectors
// runs the connector facade, runtime:toolhub serves the pinned platform
// layer with direct-call parity, and executors without a hub backend fail
// honestly — never as "unknown tool" from the exec channel.
func TestRemoteExecutorDispatch(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_MODEL_RELAY_URL", "http://model-relay-alice:8318")
	t.Setenv("HUB_MODEL_RELAY_ADMIN", "fixture-admin")
	s, auth, policy, profile := managedStore(t)
	policy.Revision++
	policy.RightCeiling = append(policy.RightCeiling, api.RightRule{Capability: "models", Operation: "list"})
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	profile.PolicyRevision = policy.Revision
	definition := r5ExecutorDefinition()
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
	profile.Connectors = api.ConnectorsNone // explicit tool selection grants the facade without the legacy bundle
	for _, tool := range definition.Tools {
		profile.Selections = append(profile.Selections, api.CapabilitySelection{
			CapabilityID: tool.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
			ImplementationDigest: digest, ToolName: tool.Name, Name: tool.Name})
	}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	grantDefinitionRights(t, s, auth, definition)
	var backendCalls []string
	g := &Gateway{Store: s, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Control: &ControlPlane{Store: s, Now: time.Now},
		ModelHTTP: &http.Client{Transport: modelTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "model-relay-alice:8318" || r.Header.Get("Authorization") != "Bearer fixture-admin" {
				t.Errorf("model relay route or credential mismatch")
			}
			body := `{"models":[{"name":"fast"}],"accounts":[],"switch":"none"}`
			if r.URL.Path == "/v1/model" {
				body = `{"model":"fast"}`
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
		Backend: backendFunc(func(_ context.Context, _ api.EffectiveBinding, tool api.ToolSpec, _ map[string]any) (api.BackendResult, error) {
			backendCalls = append(backendCalls, tool.Name)
			return api.BackendResult{Text: "backend"}, nil
		})}
	attachGatewayAudit(t, g)
	auditWrite := g.AuditWrite
	g.AuditWrite = func(event string, fields map[string]string) error {
		if event == "admit" && fields["capability_id"] == "connectors.list" {
			if !s.mu.TryLock() {
				t.Error("connector facade was called under the registry admission lock")
			} else {
				s.mu.Unlock()
			}
		}
		return auditWrite(event, fields)
	}
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

	// runtime:connectors dispatches to the connector facade — a real domain
	// answer, never the in-runtime backend.
	res, err := call("connector_list", map[string]any{})
	if err != nil {
		t.Fatalf("connector_list: %v", err)
	}
	if body := text(res); !strings.Contains(body, "connector") {
		t.Fatalf("connector_list did not reach the facade: %s", body)
	}
	res, err = call("model_list", map[string]any{})
	if err != nil || !strings.Contains(text(res), `"fast"`) {
		t.Fatalf("model_list did not reach the owner relay: %v %v", res, err)
	}

	// runtime:toolhub members run the same handlers the pinned surface
	// serves — a projected call is admission- and result-identical to the
	// direct wire.
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"platform_status", map[string]any{}, "tools_admitted"},
		{"platform_guide", map[string]any{}, "Hermes"},
		{"platform_bootstrap", map[string]any{}, "steps"},
		{"platform_usage", map[string]any{}, "unavailable"},
		{"tool_search", map[string]any{"query": "connector"}, "connector_list"},
		{"tool_call", map[string]any{"tool": "connector_list", "arguments": map[string]any{}}, "connector"},
	} {
		res, err = call(tc.name, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if body := text(res); !strings.Contains(body, tc.want) {
			t.Fatalf("projected %s lost platform semantics: %s", tc.name, body)
		}
	}

	// Executors without a hub backend report unavailable honestly and the
	// message hints at which backend the family needs (#443/#444).
	for _, tc := range []struct {
		name string
		args map[string]any
		hint string
	}{
		{"research_create", map[string]any{"topic": "t"}, "deep-research"},
		{"browser_act", map[string]any{"action": "click"}, "browser"},
	} {
		res, err = call(tc.name, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if body := text(res); !strings.Contains(body, "unavailable") || !strings.Contains(body, tc.hint) {
			t.Fatalf("unconfigured executor must report unavailable with a hint, got: %s", body)
		}
	}

	for _, name := range backendCalls {
		t.Fatalf("remote-executor tool %q reached the in-runtime backend", name)
	}
	// Exercise the cataloged delegate itself, beyond the MCP control facade.
	platformAuditWrite := g.AuditWrite
	g.AuditWrite = func(event string, fields map[string]string) error {
		if event == "admit" && fields["capability_id"] == "platform.call" {
			if !s.mu.TryLock() {
				t.Error("cataloged delegate retains the outer registry fence")
			} else {
				s.mu.Unlock()
			}
		}
		return platformAuditWrite(event, fields)
	}
	res, err = g.CallAuthorized(t.Context(), auth, "tool_call", map[string]any{"tool": "connector_list", "arguments": map[string]any{}})
	if err != nil || res == nil || res.IsError || !strings.Contains(text(res), "connector") {
		t.Fatalf("cataloged delegation failed: %v", err)
	}
}

// TestPlatformStatusAllowlist (M31.8/#445): platform_status may only ever
// emit its closed diagnostic field set — a red-team pin so a future field
// addition is a deliberate, reviewed change, not a silent leak.
func TestPlatformStatusAllowlist(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	s, auth, _, _ := managedStore(t)
	g := &Gateway{Store: s}
	out, err := g.platformStatus(auth)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"service": true, "tools_admitted": true, "tools_visible": true, "tools_extended": true}
	for key := range out {
		if !allowed[key] {
			t.Fatalf("platform_status emits non-allowlisted field %q — diagnostic surfaces stay closed (ADR-0094)", key)
		}
	}
}
