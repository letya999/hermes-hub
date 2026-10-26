package toolhub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// One run may not fan out unbounded tool calls: the gateway counts calls per
// (job,run) correlation and refuses past the host ceiling; a different run is
// unaffected.
func TestToolCallRunCeiling(t *testing.T) {
	t.Setenv("HUB_TOOL_CALLS_PER_RUN", "2")
	store, auth, _ := seededStore(t)
	g := &Gateway{
		Store:        store,
		ControlToken: "control-plane-token-which-is-32-chars!",
		Backend:      api.RoutingBackend{MCP: stubBackend{text: "ok"}},
		Tokens: map[string]identity.Envelope{
			"alice-principal-token-0000000000000": auth,
		},
		projections: map[string]*gatewayProjection{
			"alice-principal-token-0000000000000": {auth: auth},
		},
	}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	tool := api.ProjectedToolName("google-work", "1.0.0", "search")
	call := func(jobID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/tool-call", strings.NewReader(`{"tool":"`+tool+`"}`))
		req.Header.Set("Authorization", "Bearer alice-principal-token-0000000000000")
		if jobID != "" {
			req.Header.Set(api.HeaderJobID, jobID)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := call("job-1"); w.Code != http.StatusOK {
		t.Fatalf("call 1: %d", w.Code)
	}
	if w := call("job-1"); w.Code != http.StatusOK {
		t.Fatalf("call 2: %d", w.Code)
	}
	if w := call("job-1"); w.Code == http.StatusOK {
		t.Fatal("run ceiling not enforced")
	}
	if w := call("job-2"); w.Code != http.StatusOK {
		t.Fatalf("different run refused: %d %s", w.Code, w.Body)
	}
	// Uncorrelated control-path calls carry no run and are never counted.
	for i := 0; i < 4; i++ {
		if w := call(""); w.Code != http.StatusOK {
			t.Fatalf("uncorrelated call refused: %d", w.Code)
		}
	}
}

// The MCP transport must apply the same per-run ceiling: correlation headers
// reach the tool handler through RequestExtra.Header (the streamable session
// detaches per-request context), not the outer request context.
func TestToolCallRunCeilingMCP(t *testing.T) {
	t.Setenv("HUB_TOOL_CALLS_PER_RUN", "2")
	store, auth, _ := seededStore(t)
	g := &Gateway{
		Store:        store,
		ControlToken: "control-plane-token-which-is-32-chars!",
		Backend:      api.RoutingBackend{MCP: stubBackend{text: "ok"}},
		Tokens: map[string]identity.Envelope{
			"alice-principal-token-0000000000000": auth,
		},
		projections: map[string]*gatewayProjection{
			"alice-principal-token-0000000000000": {auth: auth},
		},
	}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	tool := api.ProjectedToolName("google-work", "1.0.0", "search")
	post := func(body, jobID, sessionID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, DefaultEndpointPath, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer alice-principal-token-0000000000000")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if jobID != "" {
			req.Header.Set(api.HeaderJobID, jobID)
		}
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	init := post(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`, "", "")
	sessionID := init.Header().Get("Mcp-Session-Id")
	if sessionID == "" || init.Code != http.StatusOK {
		t.Fatalf("initialize: %d", init.Code)
	}
	call := func(jobID string) (int, bool, string) {
		payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{}}}`
		w := post(payload, jobID, sessionID)
		body, err := io.ReadAll(w.Result().Body)
		if err != nil {
			t.Fatal(err)
		}
		return w.Code, strings.Contains(string(body), "ceiling"), string(body)
	}
	if code, denied, body := call("mcp-job-1"); code != http.StatusOK || denied {
		t.Fatalf("call 1: %d denied=%v body=%s", code, denied, body)
	}
	if code, denied, _ := call("mcp-job-1"); code != http.StatusOK || denied {
		t.Fatalf("call 2: %d denied=%v", code, denied)
	}
	if _, denied, body := call("mcp-job-1"); !denied {
		t.Fatalf("run ceiling not enforced on the MCP path: %s", body)
	}
	if _, denied, _ := call("mcp-job-2"); denied {
		t.Fatal("different run refused on the MCP path")
	}
}
