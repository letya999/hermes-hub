package toolhub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http/httptest"
)

// M31.11 S4 + docs/tool-design.md 6.4: MCP annotations are derived from the
// tool's declared effect — a read tool projects readOnlyHint, a removal
// projects destructiveHint. A projected tool with no annotations tells the
// model nothing honest about blast radius.
func TestProjectedToolAnnotationsMatchEffect(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	s, auth, _, profile := managedStore(t)
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "hub-agent-tools", Version: "7.0.0", Transport: api.AgentTools,
		Source:    api.DefinitionSource{},
		Workload:  api.WorkloadPolicy{Class: api.PerUser, Rationale: "in-runtime executor inside the owning runtime"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    api.HealthProbe{Kind: "exec", Value: "hubctl", TimeoutSeconds: 5},
		Tools: []api.ToolSpec{
			{Name: "platform_status", Effect: api.ReadEffect, CapabilityID: "platform.status", Executor: "runtime:toolhub", Requires: []string{"platform.status"}, Audience: api.AudienceModel,
				Description: "Show own health."},
			{Name: "session_read", Effect: api.ReadEffect, CapabilityID: "sessions.read", Executor: "runtime:sessions", Requires: []string{"sessions.read"}, Audience: api.AudienceModel,
				Description: "Read one session."},
			{Name: "session_remove", Effect: api.WriteEffect, CapabilityID: "sessions.remove", Executor: "runtime:sessions", Requires: []string{"sessions.remove"}, Audience: api.AudienceModel,
				Description: "Delete one session."},
		},
	}
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
		profile.Selections = append(profile.Selections, api.CapabilitySelection{
			CapabilityID: tool.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
			ImplementationDigest: digest, ToolName: tool.Name, Name: tool.Name})
	}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: s, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Control: &ControlPlane{Store: s, Now: time.Now},
		Backend: backendFunc(func(_ context.Context, _ api.EffectiveBinding, tool api.ToolSpec, _ map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		})}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpConnect(t, server.URL, aliceToken)
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := map[string]api.Effect{}
	for _, tool := range definition.Tools {
		effects[tool.Name] = tool.Effect
	}
	seen := map[string]bool{}
	for _, tool := range listed.Tools {
		effect, cataloged := effects[tool.Name]
		if !cataloged {
			continue
		}
		seen[tool.Name] = true
		if tool.Annotations == nil {
			t.Errorf("%s: projected without annotations — readOnlyHint/destructiveHint must derive from effect", tool.Name)
			continue
		}
		if want := effect == api.ReadEffect; tool.Annotations.ReadOnlyHint != want {
			t.Errorf("%s: readOnlyHint=%v want %v for effect %s", tool.Name, tool.Annotations.ReadOnlyHint, want, effect)
		}
		if strings.HasSuffix(tool.Name, "_remove") {
			if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Errorf("%s: removal projected without destructiveHint", tool.Name)
			}
		}
	}
	for name := range effects {
		if !seen[name] {
			t.Errorf("%s: cataloged but never projected", name)
		}
	}
}
