package toolhub

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// S5.4: a definition carrying toolsets projects only the default-enabled
// set on install; enabling a toolset on the binding adds its tools.
func TestToolsetDefaultProjection(t *testing.T) {
	store := NewStore()
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "c", DeliveryTargetID: "c", PolicyVersion: "policy-1"}
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "github-mcp", Version: "1.0.0",
		Transport: api.AgentTools, Source: api.DefinitionSource{},
		Workload:  api.WorkloadPolicy{Class: api.PerUser, Rationale: "test"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 120, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.github.com"}},
		Health:    api.HealthProbe{Kind: "exec", Value: "true", TimeoutSeconds: 5},
		Tools: []api.ToolSpec{
			{Name: "read_repo", Effect: api.ReadEffect},
			{Name: "list_issues", Effect: api.ReadEffect},
			{Name: "create_issue", Effect: api.WriteEffect},
		},
		Toolsets:        map[string][]string{"issues": {"list_issues", "create_issue"}},
		DefaultEnabled:  []string{"read_repo"},
		DefaultExcluded: []string{"create_issue"},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		tools, err := store.ListProjectedTools(auth)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(tools))
		for _, tool := range tools {
			out = append(out, tool.Tool.Name)
		}
		slices.Sort(out)
		return out
	}
	if got := names(); !slices.Equal(got, []string{"read_repo"}) {
		t.Fatalf("default projection=%v", got)
	}
	if err := store.EnableToolsets(auth, binding.ToolBindingID, []string{"issues"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"create_issue", "list_issues", "read_repo"}) {
		t.Fatalf("toolset projection=%v", got)
	}
}

// S5.4: connector_enable accepts a toolsets list and records it on the
// resolved binding so the next projection includes that set.
func TestConnectorEnableToolsets(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	enabled := m13P3EnabledOnboarding(t, store, auth)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_enable", map[string]any{"target": "google-work", "toolsets": []any{"issues"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != api.OpStateDone {
		t.Fatalf("enable result: %+v", result)
	}
	onboarding, err := store.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	binding := store.bindings[onboarding.BindingID]
	if !slices.Equal(binding.EnabledToolsets, []string{"issues"}) {
		t.Fatalf("enabled toolsets=%v", binding.EnabledToolsets)
	}
}

// CHG-0133 S6.1: recipe-declared format and length limits are enforced at
// the submit boundary — a wrong-shaped token never reaches the store.
func TestCredentialFormatValidation(t *testing.T) {
	hints := []api.CredentialHint{
		{Name: "GH_TOKEN", Type: "token", Secret: true, Delivery: "env", Format: `^(ghp_|github_pat_)[A-Za-z0-9_]{8,}$`, MaxLength: 255},
		{Name: "API_URL", Delivery: "env", MaxLength: 10},
	}
	if _, err := filterSubmittedCredentials(hints, map[string]string{"GH_TOKEN": "not-a-token", "API_URL": "ok"}); err == nil {
		t.Fatal("format-violating token accepted")
	}
	if _, err := filterSubmittedCredentials(hints, map[string]string{"GH_TOKEN": "ghp_abcdefgh1234", "API_URL": "01234567890"}); err == nil {
		t.Fatal("over-length value accepted")
	}
	if _, err := filterSubmittedCredentials(hints, map[string]string{"GH_TOKEN": "ghp_abcdefgh1234", "API_URL": "ok"}); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
}

// S6.1: recipe-declared format/length reach the rendered hint, so the same
// validation drives both the protected form and the submit path.
func TestRecipeFieldFormatReachesHint(t *testing.T) {
	recipe := api.ConnectionRecipe{Fields: []api.ConnectionField{
		{Name: "GH_TOKEN", Type: "token", Required: true, Secret: true, Delivery: "env", Format: `^ghp_`, MaxLength: 255},
	}}
	hints := credentialHintsForRecipe(api.ToolDefinition{}, recipe)
	if len(hints) != 1 || hints[0].Format != `^ghp_` || hints[0].MaxLength != 255 {
		t.Fatalf("hint=%+v", hints)
	}
	definition := api.ToolDefinition{Credentials: recipe.CredentialInputs()}
	fallback := credentialHints(definition)
	if len(fallback) != 1 || fallback[0].Format != `^ghp_` || fallback[0].MaxLength != 255 {
		t.Fatalf("input hint=%+v", fallback)
	}
	optional := optionalCredentialHints(api.ToolDefinition{Credentials: []api.CredentialInput{
		{Name: "API_URL", Format: `^https://`, MaxLength: 64},
	}})
	if len(optional) != 1 || optional[0].Format != `^https://` || optional[0].MaxLength != 64 {
		t.Fatalf("optional hint=%+v", optional)
	}
}
