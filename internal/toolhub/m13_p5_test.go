package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
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

// S6.3: a proxy-delivered credential never reaches the workload env — the
// injector emits the "hub-managed" stub and the egress-proxy plan carries
// the credential reference plus its allowlisted host.
func TestProxyDeliveryStubAndPlan(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_1", CredentialID: "credential_1"})
		case "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Env: map[string]string{"ACME_API_KEY": "real-secret-token"}})
		case "/v1/runtime/leases/lease_1/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	effective := api.EffectiveBinding{
		Binding:    api.ToolBinding{ToolBindingID: "binding_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1"},
		WorkloadID: "workload_1",
		Definition: api.ToolDefinition{
			DefinitionID: "acme-api",
			Credentials:  []api.CredentialInput{{Name: "ACME_API_KEY", Required: true, Delivery: "proxy", Target: "api.acme.example"}},
		},
		Credential: &api.CredentialReference{CredentialRefID: "cred_acme_1", Backend: "credential-broker", BrokerGrantID: "grant_1", Keys: []string{"ACME_API_KEY"}, BrokerEnv: map[string]string{"ACME_API_KEY": "ACME_API_KEY"}},
	}
	injection, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective)
	if err != nil {
		t.Fatal(err)
	}
	if got := injection.Environment["ACME_API_KEY"]; got != api.ProxyStubValue {
		t.Fatalf("workload env=%q, want the %q stub", got, api.ProxyStubValue)
	}
	if len(injection.Proxy) != 1 || injection.Proxy[0].EnvName != "ACME_API_KEY" ||
		injection.Proxy[0].Reference != "cred_acme_1" ||
		!slices.Equal(injection.Proxy[0].Hosts, []string{"api.acme.example"}) {
		t.Fatalf("proxy plan=%+v", injection.Proxy)
	}
}

// S6.3: the local credential path stubs proxy inputs the same way — the
// workload env never carries the real value there either.
func TestProxyDeliveryLocalInjectorStub(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "secrets.enc")
	keyPath := filepath.Join(t.TempDir(), "store.key")
	if err := credstore.WriteKeyFile(keyPath, key); err != nil {
		t.Fatal(err)
	}
	backend, err := credstore.Open(credstore.Options{Path: storePath, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Put("loc_acme", "alice", map[string]string{"ACME_API_KEY": "real-secret-token"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_CREDENTIAL_STORE", storePath)
	t.Setenv("HUB_CREDENTIAL_KEY_FILE", keyPath)
	_, injector, err := credentialServicesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	effective := api.EffectiveBinding{
		Binding: api.ToolBinding{ToolBindingID: "binding_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1"},
		Definition: api.ToolDefinition{
			DefinitionID: "acme-api",
			Credentials:  []api.CredentialInput{{Name: "ACME_API_KEY", Required: true, Delivery: "proxy", Target: "api.acme.example"}},
		},
		Credential: &api.CredentialReference{CredentialRefID: "cred_acme_1", Locator: "loc_acme", Keys: []string{"ACME_API_KEY"}},
	}
	injection, err := injector(t.Context(), effective)
	if err != nil {
		t.Fatal(err)
	}
	if got := injection.Environment["ACME_API_KEY"]; got != api.ProxyStubValue {
		t.Fatalf("local injector env=%q, want the %q stub", got, api.ProxyStubValue)
	}
	if len(injection.Proxy) != 1 || injection.Proxy[0].Reference != "cred_acme_1" ||
		!slices.Equal(injection.Proxy[0].Hosts, []string{"api.acme.example"}) {
		t.Fatalf("local proxy plan=%+v", injection.Proxy)
	}
}
