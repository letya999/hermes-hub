package toolhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRevocationCascadesToAssignmentRuns(t *testing.T) {
	store, auth, binding := seededStore(t)
	store.RightsDoc = rightsVocabulary()
	store.RightsDoc.Capabilities["tasks"] = api.RightCapability{Operations: map[string]api.RightOperation{
		"delegate": {Subject: "self", Scope: "none", Issuers: []string{"platform"}, PEP: []string{"test"}, Effect: "write", Description: "fixture"},
	}}
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	const grantID = "rightgrant-assignment-delegate"
	mintRight(t, gov, store.RightsDoc, api.NewRightGrant(grantID, auth.PrincipalID, "tasks", "delegate", "", "fixture", "platform", time.Now().Add(time.Hour)))
	calls := 0
	g := &Gateway{Store: store,
		Runs: runAuthorityFunc(func(_ context.Context, id string) (api.RunContext, error) {
			r := privateRunRecord(auth, id, "run-"+id)
			if id != "interactive" {
				r.Assignment, r.AssignmentID, r.AssignmentPlanRevision, r.UserID = true, "asg-owner", 2, auth.PrincipalID
			}
			return r, nil
		}),
		authorizeAssignmentRun: func(owner identity.Envelope, id string, rev uint64) error {
			decision, err := store.Decide(owner, "tasks", "delegate", "", time.Now())
			if err != nil || !decision.Allow || id != "asg-owner" || rev != 2 {
				return api.ErrUnauthorized
			}
			return nil
		},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			calls++
			return api.BackendResult{Text: "ok"}, nil
		}),
	}
	attachGatewayAudit(t, g)
	call := func(id string) error {
		ctx := api.WithCallCorrelation(t.Context(), api.HeaderCorrelation(id, "run-"+id))
		name := api.ProjectedToolName(binding.DefinitionID, binding.DefinitionVersion, "search")
		_, err := g.CallAuthorized(ctx, auth, name, map[string]any{"query": "fixture"})
		return err
	}
	for _, id := range []string{"child-one", "child-two"} {
		if err := call(id); err != nil {
			t.Fatalf("active child refused: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("active children reached backend %d times", calls)
	}
	if err := gov.RevokeRight(grantID, "platform", "fixture revoke", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"child-one", "child-two"} {
		if err := call(id); !errors.Is(err, api.ErrUnauthorized) || calls != 2 {
			t.Fatalf("revoked child reached backend or returned wrong error: calls=%d err=%v", calls, err)
		}
	}
	if err := call("interactive"); err != nil || calls != 3 {
		t.Fatalf("assignment revocation affected an independent interactive run: calls=%d err=%v", calls, err)
	}
}

func TestRunMCPRouteUsesRequestCorrelationWithoutSession(t *testing.T) {
	store, auth, binding := seededStore(t)
	const token = "synthetic-run-route-token-000000"
	var seen api.CallCorrelation
	record := privateRunRecord(auth, "job-1", "run-1")
	g := &Gateway{Store: store, Tokens: map[string]identity.Envelope{token: auth}, Runs: runAuthorityFunc(func(_ context.Context, jobID string) (api.RunContext, error) {
		if jobID != "job-1" {
			return api.RunContext{}, errors.New("unknown job")
		}
		return record, nil
	}), Backend: backendFunc(func(ctx context.Context, _ api.EffectiveBinding, _ api.ToolSpec, _ map[string]any) (api.BackendResult, error) {
		seen = api.CallCorrelationFrom(ctx)
		return api.BackendResult{Text: "ok"}, nil
	})}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "run-route", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + RunEndpointPath,
		HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: token, jobID: "job-1", runID: "run-1"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.ID() != "" {
		t.Fatal("run route retained an MCP session")
	}
	name := api.ProjectedToolName(binding.DefinitionID, binding.DefinitionVersion, "search")
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "probe"}}); err != nil {
		t.Fatal(err)
	}
	if seen.JobID != "job-1" || seen.HermesRunID != "run-1" {
		t.Fatalf("run route lost request correlation: %+v", seen)
	}
}

// Correlation headers select a supervisor record; they never supply its
// authority. Every run-scoped claim — forged, aliased, stale, foreign,
// finished or missing — denies before the backend, and a denied claim never
// reaches the per-job call ceiling it tried to spend.
func TestRunEndpointDeniesUnverifiedCorrelation(t *testing.T) {
	store, auth, binding := seededStore(t)
	const token = "synthetic-run-matrix-token-00000"
	records := map[string]api.RunContext{
		"job-a": privateRunRecord(auth, "job-a", "run-a"),
		"job-b": privateRunRecord(auth, "job-b", "run-b"),
	}
	stale := privateRunRecord(auth, "job-stale", "run-stale")
	stale.RuntimeGeneration, stale.CurrentGeneration = "gen-3", "gen-9"
	records[stale.JobID] = stale
	done := privateRunRecord(auth, "job-done", "run-done")
	done.Status = "completed"
	records[done.JobID] = done
	foreign := privateRunRecord(auth, "job-foreign", "run-foreign")
	foreign.Envelope.PrincipalID = "bob"
	foreign.Envelope.ContextID = "bob"
	records[foreign.JobID] = foreign
	otherEnv := privateRunRecord(auth, "job-env", "run-env")
	otherEnv.Environment = "prod"
	records[otherEnv.JobID] = otherEnv
	calls := 0
	g := &Gateway{Store: store, Tokens: map[string]identity.Envelope{token: auth}, Runs: runAuthorityFunc(func(_ context.Context, jobID string) (api.RunContext, error) {
		record, ok := records[jobID]
		if !ok {
			return api.RunContext{}, errors.New("unknown job")
		}
		return record, nil
	}), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		calls++
		return api.BackendResult{Text: "ok"}, nil
	})}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	name := api.ProjectedToolName(binding.DefinitionID, binding.DefinitionVersion, "search")
	post := func(path, jobID, runID string) *httptest.ResponseRecorder {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{"query":"x"}}}`
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if jobID != "" {
			req.Header.Set(api.HeaderJobID, jobID)
		}
		if runID != "" {
			req.Header.Set(api.HeaderRunID, runID)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	// Two independent valid runs of the same owner admit on their own records.
	for _, pair := range [][2]string{{"job-a", "run-a"}, {"job-b", "run-b"}} {
		if w := post(RunEndpointPath, pair[0], pair[1]); w.Code != http.StatusOK || calls == 0 {
			t.Fatalf("valid run %s denied: %d", pair[0], w.Code)
		}
	}
	before := calls
	for _, denied := range []struct {
		name, jobID, runID string
	}{
		{"forged job", "job-ghost", "run-x"},
		{"run alias across jobs", "job-a", "run-b"},
		{"superseded generation", "job-stale", "run-stale"},
		{"finished run", "job-done", "run-done"},
		{"foreign owner", "job-foreign", "run-foreign"},
		{"foreign environment", "job-env", "run-env"},
		{"missing correlation", "", ""},
		{"job without run", "job-a", ""},
		{"run without job", "", "run-a"},
	} {
		w := post(RunEndpointPath, denied.jobID, denied.runID)
		if calls != before {
			t.Fatalf("%s reached backend: %d", denied.name, w.Code)
		}
		if w.Code == http.StatusOK && !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("%s answered ok: %s", denied.name, w.Body.String())
		}
	}
	// The plain endpoint keeps a session; correlation headers still verify
	// against the same record — valid admits, forged denies.
	initReq := httptest.NewRequest(http.MethodPost, DefaultEndpointPath, strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	initReq.Header.Set("Authorization", "Bearer "+token)
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Accept", "application/json, text/event-stream")
	init := httptest.NewRecorder()
	handler.ServeHTTP(init, initReq)
	if sessionID := init.Header().Get("Mcp-Session-Id"); sessionID != "" {
		call := func(jobID, runID string) *httptest.ResponseRecorder {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{"query":"x"}}}`
			req := httptest.NewRequest(http.MethodPost, DefaultEndpointPath, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Mcp-Session-Id", sessionID)
			if jobID != "" {
				req.Header.Set(api.HeaderJobID, jobID)
			}
			if runID != "" {
				req.Header.Set(api.HeaderRunID, runID)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			return w
		}
		if w := call("job-ghost", "run-x"); calls != before {
			t.Fatalf("forged run on plain path reached backend: %d", w.Code)
		}
		if w := call("job-a", "run-a"); w.Code != http.StatusOK || calls != before+1 {
			t.Fatalf("verified run denied on plain path: %d calls=%d", w.Code, calls)
		}
	} else {
		t.Fatalf("initialize on plain path: %d", init.Code)
	}
	g.mu.Lock()
	charged := g.runCalls
	g.mu.Unlock()
	if charged["job-a"] != 2 || charged["job-b"] != 1 || len(charged) != 2 {
		t.Fatalf("denied claims spent foreign ceilings: %v", charged)
	}
}
