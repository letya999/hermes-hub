package toolhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

// /v1/tool-call is the control-plane path deterministic probes use: the
// caller's own bearer or the control-plane bearer + X-Hub-Principal resolves
// the envelope, and only read-effect projected tools may be invoked through
// it (SPEC-0054 R3.1).
func TestToolCallReturnsText(t *testing.T) {
	store, auth, _ := seededStore(t)
	backend := stubBackend{text: "price: 42"}
	g := &Gateway{
		Store:        store,
		ControlToken: "control-plane-token-which-is-32-chars!",
		Backend:      RoutingBackend{MCP: backend},
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
	tool := ProjectedToolName("google-work", "1.0.0", "search")
	call := func(token, principal, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/tool-call", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if principal != "" {
			req.Header.Set("X-Hub-Principal", principal)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	w := call("alice-principal-token-0000000000000", "", `{"tool":"`+tool+`","arguments":{"q":"price"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("tool call: %d %s", w.Code, w.Body)
	}
	var out struct {
		Text    string `json:"text"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Text != "price: 42" || out.IsError {
		t.Fatalf("tool call body: %s", w.Body)
	}
	// The control-plane bearer reaches the same tool through X-Hub-Principal.
	w = call("control-plane-token-which-is-32-chars!", "alice", `{"tool":"`+tool+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("control tool call: %d %s", w.Code, w.Body)
	}
}

// A write-effect tool name must never reach the backend through this
// endpoint — probes are read-only by contract (red-team).
func TestToolCallRejectsWriteEffect(t *testing.T) {
	store, auth, _ := seededStore(t)
	g := &Gateway{
		Store:        store,
		ControlToken: "control-plane-token-which-is-32-chars!",
		Backend:      RoutingBackend{MCP: stubBackend{text: "must not run"}},
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
	write := ProjectedToolName("google-work", "1.0.0", "send")
	req := httptest.NewRequest(http.MethodPost, "/v1/tool-call", strings.NewReader(`{"tool":"`+write+`"}`))
	req.Header.Set("Authorization", "Bearer alice-principal-token-0000000000000")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("write tool call: %d %s", w.Code, w.Body)
	}
}

// X-Hub-Principal is honored only with the control-plane bearer: a principal
// token + a foreign principal header still resolves the token's own envelope
// and cannot name another owner's tools.
func TestToolCallRejectsForeignHeaderAndUnknown(t *testing.T) {
	store, auth, _ := seededStore(t)
	bob := identity.TelegramEnvelope("bob", 8, "runtime-bob", "policy-1")
	g := &Gateway{
		Store:        store,
		ControlToken: "control-plane-token-which-is-32-chars!",
		Backend:      RoutingBackend{MCP: stubBackend{text: "nope"}},
		Tokens: map[string]identity.Envelope{
			"alice-principal-token-0000000000000": auth,
			"bob-principal-token-000000000000000": bob,
		},
		projections: map[string]*gatewayProjection{
			"alice-principal-token-0000000000000": {auth: auth},
			"bob-principal-token-000000000000000": {auth: bob},
		},
	}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	tool := ProjectedToolName("google-work", "1.0.0", "search")
	call := func(token, principal, body, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/tool-call", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if principal != "" {
			req.Header.Set("X-Hub-Principal", principal)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	// Bob's bearer cannot cross into alice's projection via the header.
	if w := call("bob-principal-token-000000000000000", "alice", `{"tool":"`+tool+`"}`, ""); w.Code != http.StatusForbidden {
		t.Fatalf("foreign header tool call: %d %s", w.Code, w.Body)
	}
	// Unknown tool names fail closed even for the owner.
	if w := call("alice-principal-token-0000000000000", "", `{"tool":"hub-nope"}`, ""); w.Code != http.StatusForbidden {
		t.Fatalf("unknown tool: %d %s", w.Code, w.Body)
	}
	// Browser origins are forbidden.
	if w := call("alice-principal-token-0000000000000", "", `{"tool":"`+tool+`"}`, "https://evil.example"); w.Code != http.StatusForbidden {
		t.Fatalf("origin tool call: %d", w.Code)
	}
	// Control-plane bearer + unknown principal is a 401, never a silent owner.
	if w := call("control-plane-token-which-is-32-chars!", "mallory", `{"tool":"`+tool+`"}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown principal: %d", w.Code)
	}
}

type stubBackend struct {
	text    string
	isError bool
	err     error
}

func (b stubBackend) Call(_ context.Context, _ EffectiveBinding, _ ToolSpec, arguments map[string]any) (BackendResult, error) {
	if b.err != nil {
		return BackendResult{}, b.err
	}
	return BackendResult{Text: b.text, IsError: b.isError}, nil
}
