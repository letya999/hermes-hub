package hermes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type lineageBackendFunc func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error)

func (f lineageBackendFunc) Call(ctx context.Context, effective api.EffectiveBinding, tool api.ToolSpec, args map[string]any) (api.BackendResult, error) {
	return f(ctx, effective, tool, args)
}

// The external package supplies the real server without a runtime/server test cycle.
var TestLineageEndpoint func(*testing.T, identity.Envelope, api.ToolBackend) http.Handler

// The full managed chain — Hermes MCP call → runtime proxy → /mcp-run →
// supervisor record → backend — runs on the same objects production wires:
// the runtime stamps job/run from its admitted bindings, the endpoint marker
// makes them mandatory, and RunAuthority decides from the durable record.
// Header claims alone admit nothing.
func TestManagedToolCallLineageVerifiedEndToEnd(t *testing.T) {
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
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
	handler := TestLineageEndpoint(t, auth, backend)
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
