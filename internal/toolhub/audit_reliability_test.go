package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPostEffectAuditFailureNeverRepeatsAction(t *testing.T) {
	store, auth, _ := seededStore(t)
	var calls atomic.Int32
	g := &Gateway{Store: store, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		calls.Add(1)
		return api.BackendResult{Text: "private fixture result"}, nil
	}), AuditWrite: func(event string, _ map[string]string) error {
		if event == "allow" {
			return errors.New("fixture completion failure")
		}
		return nil
	}}
	name := api.ProjectedToolName("google-work", "1.0.0", "search")
	if result, err := g.call(context.Background(), auth, name, nil); !errors.Is(err, audit.ErrUncertain) || result != nil {
		t.Errorf("post-effect result=%+v error=%v", result, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("backend effects=%d", calls.Load())
	}
}

func TestBackendErrorIsNotAuthorizationDeny(t *testing.T) {
	store, auth, _ := seededStore(t)
	denied := 0
	g := &Gateway{Store: store, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		return api.BackendResult{}, errors.New("fixture backend failure")
	}), AuditWrite: func(event string, _ map[string]string) error {
		if event == "deny" {
			denied++
		}
		return nil
	}}
	name := api.ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := g.call(context.Background(), auth, name, nil); !errors.Is(err, audit.ErrUncertain) {
		t.Errorf("backend failure=%v", err)
	}
	if denied != 0 {
		t.Fatalf("execution failure recorded as %d authorization denials", denied)
	}
}

func TestCheckpointFailureIsUncertainWithoutPayload(t *testing.T) {
	store, auth, _ := seededStore(t)
	calls := 0
	g := &Gateway{Store: store, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		calls++
		return api.BackendResult{Text: "private fixture result"}, nil
	}), AuditWrite: func(string, map[string]string) error { return nil }, Injector: func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
		return api.CredentialInjection{Checkpoint: func() error { return errors.New("fixture checkpoint failure") }}, nil
	}}
	if result, err := g.call(context.Background(), auth, api.ProjectedToolName("google-work", "1.0.0", "search"), nil); !errors.Is(err, audit.ErrUncertain) || result != nil {
		t.Errorf("checkpoint result=%+v error=%v", result, err)
	}
	if calls != 1 {
		t.Fatalf("backend effects=%d", calls)
	}
}

func TestDurableToolAdmissionAndRecoveredReplayFence(t *testing.T) {
	store, auth, _ := seededStore(t)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := api.WithCallCorrelation(context.Background(), api.CallCorrelation{JobID: "job-1", HermesRunID: "run-1", ToolCallID: "call-1"})
	calls := 0
	record := privateRunRecord(auth, "job-1", "run-1")
	g := &Gateway{Store: store, AuditWrite: LedgerAuditWriter(ledger), Runs: runAuthorityFunc(func(context.Context, string) (api.RunContext, error) {
		return record, nil
	}), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		calls++
		pending, err := ledger.Pending(audit.Scope{PrincipalID: auth.PrincipalID}, 1)
		if err != nil || len(pending) != 1 || pending[0].Authorization != "allow" || pending[0].Execution != "unknown" || pending[0].JobID != "job-1" {
			t.Fatalf("effect preceded durable admission: %+v %v", pending, err)
		}
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		return api.BackendResult{Text: "private fixture payload"}, nil
	})}
	name := api.ProjectedToolName("google-work", "1.0.0", "search")
	result, err := g.mcpCall(ctx, auth, name, nil)
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("uncertain is not an application result: %+v %v", result, err)
	}
	data, ok := result.StructuredContent.(map[string]any)
	if !ok || data["status"] != "uncertain" || data["retryable"] != false || !strings.HasPrefix(data["operation_id"].(string), "tool-") {
		t.Fatalf("missing outcome contract: %+v", result)
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok && strings.Contains(text.Text, "private fixture") {
			t.Fatal("private backend result escaped")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	recovered, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	g.AuditWrite = LedgerAuditWriter(recovered)
	if _, err := g.call(ctx, auth, name, map[string]any{"changed": "arguments"}); !errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("recovered replay allowed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("backend effects=%d", calls)
	}
}

func TestBackendFailurePersistsUnknownNotDeny(t *testing.T) {
	store, auth, _ := seededStore(t)
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: store, AuditWrite: LedgerAuditWriter(ledger), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		return api.BackendResult{}, errors.New("private backend error")
	})}
	if _, err := g.call(context.Background(), auth, api.ProjectedToolName("google-work", "1.0.0", "search"), nil); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal(err)
	}
	events, err := ledger.List(auth.PrincipalID)
	if err != nil || len(events) != 2 || events[1].Phase != "uncertain" || events[1].Authorization != "allow" || events[1].Outcome != "backend-error" {
		t.Fatalf("wrong durable outcome: %+v %v", events, err)
	}
	pending, err := ledger.Pending(audit.Scope{PrincipalID: auth.PrincipalID}, 1)
	if err != nil || len(pending) != 1 {
		t.Fatalf("unknown intent lost: %+v %v", pending, err)
	}
}

func TestPreflightFailureSettlesWithoutEffect(t *testing.T) {
	store, auth, _ := seededStore(t)
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: store, AuditWrite: LedgerAuditWriter(ledger), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		t.Fatal("backend after failed injection")
		return api.BackendResult{}, nil
	}), Injector: func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
		return api.CredentialInjection{}, errors.New("fixture preflight failure")
	}}
	if _, err := g.call(context.Background(), auth, api.ProjectedToolName("google-work", "1.0.0", "search"), nil); err == nil || errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("pre-effect failure misclassified: %v", err)
	}
	events, err := ledger.List(auth.PrincipalID)
	if err != nil || len(events) != 2 || events[1].Execution != "not_started" || events[1].Authorization != "allow" {
		t.Fatalf("preflight=%+v %v", events, err)
	}
	pending, err := ledger.Pending(audit.Scope{PrincipalID: auth.PrincipalID}, 1)
	if err != nil || len(pending) != 0 {
		t.Fatalf("preflight left unknown effect: %+v %v", pending, err)
	}
}

func TestAuditLoggerDoesNotDumpDictionaryKeys(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(old)
	g := &Gateway{AuditWrite: func(string, map[string]string) error { return nil }}
	if err := g.audit("deny", map[string]string{"principal_id": "alice", "fixture-bearer-key": "private fixture data", "receipt": "private receipt"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "fixture-bearer-key") || strings.Contains(output.String(), "private") {
		t.Fatal("unallowlisted audit dictionary data in diagnostics")
	}
}

type failingEnvAuditBackend struct{ calls int }

func (b *failingEnvAuditBackend) Call(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
	return api.BackendResult{}, errors.New("unexpected non-env call")
}
func (b *failingEnvAuditBackend) CallEnv(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any, map[string]string) (api.BackendResult, error) {
	b.calls++
	return api.BackendResult{}, errors.New("fixture uncertain backend failure")
}

func TestUnknownEffectNeverRefreshesAndReplays(t *testing.T) {
	store, auth, _ := seededStore(t)
	store.mu.Lock()
	store.connections["google-work"].Metadata["oauth_provider"] = "fixture"
	store.mu.Unlock()
	backend := &failingEnvAuditBackend{}
	refreshes := 0
	g := &Gateway{Store: store, Backend: backend, AuditWrite: testAuditWriter(t), Injector: func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
		return api.CredentialInjection{}, nil
	}, OAuthRefresh: func(context.Context, api.EffectiveBinding) (bool, error) { refreshes++; return true, nil }}
	if _, err := g.call(context.Background(), auth, api.ProjectedToolName("google-work", "1.0.0", "search"), nil); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal(err)
	}
	if backend.calls != 1 || refreshes != 0 {
		t.Fatalf("uncertain replay: calls=%d refreshes=%d", backend.calls, refreshes)
	}
}

func TestHTTPUncertainOutcomeIsNonRetryableConflict(t *testing.T) {
	store, auth, _ := seededStore(t)
	token := "fixture-principal-token-0000000000000"
	g := &Gateway{Store: store, Tokens: map[string]identity.Envelope{token: auth}, AuditWrite: testAuditWriter(t), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		return api.BackendResult{}, errors.New("fixture business outcome lost")
	})}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/tool-call", strings.NewReader(`{"tool":"`+api.ProjectedToolName("google-work", "1.0.0", "search")+`"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body struct {
		Status      string `json:"status"`
		OperationID string `json:"operation_id"`
		Retryable   bool   `json:"retryable"`
	}
	if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Status != "uncertain" || body.Retryable || !strings.HasPrefix(body.OperationID, "tool-") {
		t.Fatalf("HTTP outcome=%d %s", response.Code, response.Body)
	}
}

func TestRemoteAuthenticationAfterDispatchIsNotBeforeEffect(t *testing.T) {
	var effects atomic.Int32
	serverMCP := mcp.NewServer(&mcp.Implementation{Name: "audit-effect-fixture", Version: "1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return serverMCP }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 16384))
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture read failure", 400)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Method == "tools/call" {
			effects.Add(1)
			http.Error(w, "fixture response lost after effect", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	fixture := &remoteMCPFixture{URL: server.URL}
	pinRemoteFixtureClient(t, fixture, server)
	effective := api.EffectiveBinding{Definition: api.ToolDefinition{Transport: api.RemoteMCP, Source: api.DefinitionSource{URL: fixture.endpoint()}}}
	_, err := (api.MCPBackend{}).CallRemoteMCP(context.Background(), effective, api.ToolSpec{Name: "fixture_action"}, nil, nil)
	if err == nil || errors.Is(err, api.ErrBeforeEffect) || errors.Is(err, api.ErrAuthBeforeEffect) || effects.Load() != 1 {
		t.Fatalf("post-dispatch classification: effects=%d err=%v", effects.Load(), err)
	}
}
