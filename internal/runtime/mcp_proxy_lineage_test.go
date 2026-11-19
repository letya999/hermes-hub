package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type lineageBackendFunc func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error)

func (f lineageBackendFunc) Call(ctx context.Context, effective api.EffectiveBinding, tool api.ToolSpec, args map[string]any) (api.BackendResult, error) {
	return f(ctx, effective, tool, args)
}

// lineageStore mirrors the toolhub fixture through its exported API: a remote
// definition plus its credential, connection, definition grant and binding —
// the smallest registry that projects one tool for the owner envelope.
func lineageStore(t *testing.T) (*toolhub.Store, identity.Envelope) {
	t.Helper()
	store := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:      api.DefinitionSource{URL: "https://example.com/mcp", TLSMode: "required"},
		Tools:       []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}},
		Credentials: []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}},
		Workload:    api.WorkloadPolicy{Class: api.PerUser, Rationale: "user-owned provider session"},
		Execution:   api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.com"}},
		Health:      api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	grant := api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)
	if err := api.Confirm(&grant, "", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(grant); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	return store, auth
}

// The full managed chain — Hermes MCP call → runtime proxy → /mcp-run →
// supervisor record → backend — runs on the same objects production wires:
// the runtime stamps job/run from its admitted bindings, the endpoint marker
// makes them mandatory, and RunAuthority decides from the durable record.
// Header claims alone admit nothing.
func TestManagedToolCallLineageVerifiedEndToEnd(t *testing.T) {
	store, auth := lineageStore(t)
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := api.RunContext{}
		if r.URL.Path == "/v1/jobs/job-live" {
			record = api.RunContext{
				JobID: "job-live", RunID: "run-live", SessionID: "session-live",
				RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
				Environment: auth.Environment, Status: "running",
				ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot",
				Envelope: identity.Envelope{Schema: auth.Schema, PrincipalID: auth.PrincipalID,
					ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion},
			}
		}
		if record.JobID == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(record)
	}))
	defer supervisor.Close()
	ledger := filepath.Join(t.TempDir(), "audit.jsonl")
	calls := 0
	backend := lineageBackendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		calls++
		return api.BackendResult{Text: "ok"}, nil
	})
	t.Setenv("HUB_AUDIT_LEDGER", ledger)
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", supervisor.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "control-token")
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("s", 32))
	handler, err := toolhub.NewEndpointHandler(toolhub.EndpointConfig{
		Token: strings.Repeat("s", 32), Auth: auth, Backend: backend,
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	toolhubServer := httptest.NewServer(handler)
	defer toolhubServer.Close()
	t.Setenv("HUB_TOOLHUB_ENDPOINT", toolhubServer.URL+"/mcp")
	rt := runtimeHandler().(*runtimeHTTP)
	if err := rt.bindRun("job-live", "run-live"); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(rt)
	defer proxy.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "lineage", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: proxy.URL + "/mcp",
		HTTPClient: &http.Client{Transport: proxyBearerTransport{RoundTripper: http.DefaultTransport, token: strings.Repeat("s", 32)}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	name := api.ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Meta: mcp.Meta{"x-hermes-hub-run-id": "run-live"}, Name: name, Arguments: map[string]any{}}); err != nil {
		t.Fatalf("admitted run call denied: %v", err)
	}
	if calls != 1 {
		t.Fatalf("backend calls=%d", calls)
	}
	body, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"job_id":"job-live"`) || !strings.Contains(text, `"hermes_run_id":"run-live"`) {
		t.Fatalf("ledger missing admitted lineage: %s", text)
	}
	// A run id the runtime never admitted is refused at the proxy — and a
	// forged pair can never spend another job's ceiling or audit attribution.
	for _, meta := range []mcp.Meta{nil, {"x-hermes-hub-run-id": "run-foreign"}} {
		if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Meta: meta, Name: name, Arguments: map[string]any{}}); err == nil {
			t.Fatalf("unbound run call accepted with meta %v", meta)
		}
	}
	if calls != 1 {
		t.Fatalf("forged run reached backend: calls=%d", calls)
	}
}
