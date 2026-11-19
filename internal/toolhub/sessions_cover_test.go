package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/memory/prune"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// sessionStub answers /v1/sessions like the communication control plane and
// records each payload so execSession's argument translation is observable.
type sessionStub struct {
	srv     *httptest.Server
	payload map[string]any
	status  int
}

func newSessionStub(t *testing.T, status int) *sessionStub {
	t.Helper()
	s := &sessionStub{status: status}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions" || r.Header.Get("X-Hub-Principal") == "" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&s.payload)
		if s.status != http.StatusOK {
			w.WriteHeader(s.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func sessionEnvelope() identity.Envelope {
	return identity.Envelope{PrincipalID: "alice"}
}

func TestExecSessionTranslatesToolArguments(t *testing.T) {
	stub := newSessionStub(t, http.StatusOK)
	g := &Gateway{CommunicationURL: stub.srv.URL, ControlToken: "service"}
	auth := sessionEnvelope()

	cases := []struct {
		tool string
		args map[string]any
		want map[string]any
	}{
		{"session_list", map[string]any{"cursor": "3", "limit": float64(5)}, map[string]any{"action": "list", "cursor": "3", "limit": float64(5)}},
		{"session_search", map[string]any{"query": "needle", "limit": float64(2)}, map[string]any{"action": "search", "query": "needle", "limit": float64(2)}},
		{"session_create", map[string]any{"title": "fresh"}, map[string]any{"action": "create", "title": "fresh"}},
		{"session_read", map[string]any{"id": "task-1", "offset": float64(4)}, map[string]any{"action": "read", "id": "task-1", "offset": float64(4)}},
		{"session_switch", map[string]any{"id": "task-2"}, map[string]any{"action": "switch", "id": "task-2"}},
		{"session_remove", map[string]any{"id": "task-3"}, map[string]any{"action": "remove", "id": "task-3"}},
		{"session_edit", map[string]any{"id": "task-4", "action": "archive"}, map[string]any{"action": "edit", "id": "task-4", "edit_action": "archive"}},
	}
	for _, tc := range cases {
		result, err := g.execSession(context.Background(), auth, api.ToolSpec{Name: tc.tool}, tc.args)
		if err != nil || result.IsError {
			t.Fatalf("%s failed: %v %+v", tc.tool, err, result)
		}
		for key, want := range tc.want {
			if stub.payload[key] != want {
				t.Fatalf("%s payload[%s] = %v, want %v (%v)", tc.tool, key, stub.payload[key], want, stub.payload)
			}
		}
	}
}

func TestExecSessionValidationAndStatusMapping(t *testing.T) {
	g := &Gateway{CommunicationURL: "http://127.0.0.1:1", ControlToken: "service"}
	auth := sessionEnvelope()
	ctx := context.Background()

	// Local validation failures never reach the wire.
	for _, tc := range []struct {
		tool string
		args map[string]any
		code string
	}{
		{"session_search", map[string]any{"query": ""}, "invalid_query"},
		{"session_search", map[string]any{"query": strings.Repeat("q", 300)}, "invalid_query"},
		{"session_create", map[string]any{"title": ""}, "invalid_title"},
		{"session_edit", map[string]any{"id": "t", "action": "nuke"}, "invalid_action"},
		{"session_read", map[string]any{}, "invalid_id"},
		{"session_read", map[string]any{"id": strings.Repeat("i", 90)}, "invalid_id"},
	} {
		result, err := g.execSession(ctx, auth, api.ToolSpec{Name: tc.tool}, tc.args)
		if err != nil || !result.IsError || result.Structured.(map[string]any)["error"] != tc.code {
			t.Fatalf("%s want %s: %v %+v", tc.tool, tc.code, err, result)
		}
	}
	// Unknown session tool is a hard invalid.
	if _, err := g.execSession(ctx, auth, api.ToolSpec{Name: "session_nuke"}, nil); err == nil {
		t.Fatal("unknown session tool accepted")
	}

	// Upstream status classes map to bounded result codes.
	for _, tc := range []struct {
		status int
		code   string
	}{{http.StatusNotFound, "not_found"}, {http.StatusBadRequest, "invalid_request"}, {http.StatusUnauthorized, "forbidden"}, {http.StatusForbidden, "forbidden"}, {http.StatusConflict, "unavailable"}} {
		stub := newSessionStub(t, tc.status)
		g := &Gateway{CommunicationURL: stub.srv.URL, ControlToken: "service"}
		result, err := g.execSession(ctx, auth, api.ToolSpec{Name: "session_list"}, nil)
		if err != nil || !result.IsError || result.Structured.(map[string]any)["error"] != tc.code {
			t.Fatalf("status %d want %s: %v %+v", tc.status, tc.code, err, result)
		}
	}
	// Unreachable communication is unavailable, not a panic.
	stub := newSessionStub(t, http.StatusOK)
	stub.srv.Close()
	g = &Gateway{CommunicationURL: stub.srv.URL, ControlToken: "service"}
	result, err := g.execSession(ctx, auth, api.ToolSpec{Name: "session_list"}, nil)
	if err != nil || !result.IsError || result.Structured.(map[string]any)["error"] != "unavailable" {
		t.Fatalf("unreachable communication: %v %+v", err, result)
	}
}

type dreamingSignerFunc func(context.Context, contract.ServiceRequest) (string, error)

func (f dreamingSignerFunc) IssueDreamingModelService(ctx context.Context, scope contract.ServiceRequest) (string, error) {
	return f(ctx, scope)
}

func TestCallNightRelayContract(t *testing.T) {
	scope := contract.ServiceRequest{Space: "alice", Environment: "dev", PolicyRevision: contract.PolicyDigest([]byte("synthetic policy"))}
	var signed contract.ServiceRequest
	signer := dreamingSignerFunc(func(_ context.Context, request contract.ServiceRequest) (string, error) {
		signed = request
		return "synthetic-proof", nil
	})
	var gotAuth, gotProof, gotBody string
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotAuth = r.Header.Get("Authorization")
		gotProof = r.Header.Get(contract.ModelServiceTokenHeader)
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"night answer"}}]}`))
	}))
	t.Cleanup(srv.Close)

	answer, err := callNightRelay(t.Context(), srv.URL, signer, scope, "sys", "usr")
	if err != nil || answer != "night answer" {
		t.Fatalf("relay answer = %q %v", answer, err)
	}
	expected := scope
	expected.RequestDigest = contract.PolicyDigest([]byte(gotBody))
	if signed != expected || gotAuth != "" || gotProof != "synthetic-proof" || !strings.Contains(gotBody, `"system"`) || !strings.Contains(gotBody, `"usr"`) {
		t.Fatal("relay did not send the exact request covered by its proof")
	}
	deny := dreamingSignerFunc(func(context.Context, contract.ServiceRequest) (string, error) {
		return "", errors.New("synthetic signing denial")
	})
	if _, err := callNightRelay(t.Context(), srv.URL, deny, scope, "sys", "usr"); err == nil || calls != 1 {
		t.Fatal("denied issuance reached the relay")
	}

	// Non-200, malformed and choice-less responses all collapse to unavailable.
	for _, resp := range []string{`{}`, `{`, `{"choices":[]}`} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(resp))
		}))
		if _, err := callNightRelay(t.Context(), bad.URL, signer, scope, "s", "u"); err == nil {
			t.Fatalf("malformed relay response accepted: %s", resp)
		}
		bad.Close()
	}
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	if _, err := callNightRelay(t.Context(), refused.URL, signer, scope, "s", "u"); err == nil {
		t.Fatal("refused relay accepted")
	}
	refused.Close()
	if _, err := callNightRelay(t.Context(), "http://127.0.0.1:1", signer, scope, "s", "u"); err == nil {
		t.Fatal("unreachable relay accepted")
	}
}

func TestNightPruneLineRendersAllRows(t *testing.T) {
	report := prune.Report{
		Folded:  []prune.FoldResult{{Day: "2026-11-01", Before: 100, After: 40, Stubs: 2}},
		Expired: []string{"/vault/2026-10-01.md", "/vault/2026-10-02.md"},
		Kept:    []prune.KeptDay{{Day: "2026-10-03", Reason: "day summary missing"}, {Day: "2026-10-04", Reason: "incomplete rollup"}},
	}
	out := nightPruneLine("digest-line", report, locale.LangEN)
	for _, want := range []string{"digest-line", "2026-11-01", "2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04"} {
		if !strings.Contains(out, want) {
			t.Fatalf("prune line missing %q: %q", want, out)
		}
	}
	if nightPruneLine("d", prune.Report{}, locale.LangEN) != "d" {
		t.Fatal("empty report changed the digest")
	}
}
