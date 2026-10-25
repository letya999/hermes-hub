package toolhub

import (
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
