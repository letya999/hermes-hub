package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// sessionReadRequest builds the validated control-plane request handleSessions
// forwards after resolving the caller's session mapping.
func sessionReadRequest(t *testing.T, root string) hubruntime.SessionReadRequest {
	t.Helper()
	return hubruntime.SessionReadRequest{
		ExecuteRequest: hubruntime.ExecuteRequest{
			Envelope:       identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)),
			OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice",
		},
		SessionID: "hub-" + strings.Repeat("ab", 12),
		Limit:     10,
	}
}

func TestSessionReadProxiesVerifiedRuntime(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
		return []byte("ok"), nil
	}, func(context.Context, string, string) error { return nil })

	var upstreamAuth, upstreamPath string
	m.cfg.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamPath, upstreamAuth = r.URL.Path, r.Header.Get("Authorization")
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		recorder.WriteHeader(http.StatusOK)
		_, _ = recorder.WriteString(`{"declared_session_id":"hub-` + strings.Repeat("ab", 12) + `","messages":[]}`)
		return recorder.Result(), nil
	})}

	body, _ := json.Marshal(sessionReadRequest(t, root))
	r := httptest.NewRequest(http.MethodPost, "/v1/session-read", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("session read failed: %d %s", w.Code, w.Body.String())
	}
	if upstreamPath != "/v1/session-read" || upstreamAuth != "Bearer secret" {
		t.Fatalf("upstream contract drifted: %s %q", upstreamPath, upstreamAuth)
	}
	var decoded map[string]any
	if json.Unmarshal(w.Body.Bytes(), &decoded) != nil || decoded["declared_session_id"] != "hub-"+strings.Repeat("ab", 12) {
		t.Fatalf("response not proxied: %s", w.Body.String())
	}
}

func TestSessionReadRefusals(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
		return []byte("ok"), nil
	}, func(context.Context, string, string) error { return nil })

	// Wrong verb.
	r := httptest.NewRequest(http.MethodGet, "/v1/session-read", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET session-read: %d", w.Code)
	}
	// Malformed body and invalid session id.
	for _, payload := range []string{"{", `{"session_id":"nope"}`} {
		r = httptest.NewRequest(http.MethodPost, "/v1/session-read", strings.NewReader(payload))
		r.Header.Set("Authorization", "Bearer secret")
		w = httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("payload %q accepted: %d", payload, w.Code)
		}
	}
	// A valid shape whose envelope fails binding is a conflict, not a panic.
	request := sessionReadRequest(t, root)
	request.Envelope.PolicyVersion = "forged"
	body, _ := json.Marshal(request)
	r = httptest.NewRequest(http.MethodPost, "/v1/session-read", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	m.Handler().ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatalf("forged envelope passed binding: %s", w.Body.String())
	}
}

func TestSessionReadUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"non-200 upstream", http.StatusInternalServerError, `{}`, http.StatusBadGateway},
		{"invalid json", http.StatusOK, `{`, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
				return []byte("ok"), nil
			}, func(context.Context, string, string) error { return nil })
			m.cfg.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				recorder := httptest.NewRecorder()
				recorder.WriteHeader(tc.status)
				_, _ = recorder.WriteString(tc.body)
				return recorder.Result(), nil
			})}
			body, _ := json.Marshal(sessionReadRequest(t, root))
			r := httptest.NewRequest(http.MethodPost, "/v1/session-read", strings.NewReader(string(body)))
			r.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			m.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
	// Upstream transport error also maps to bad gateway.
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
		return []byte("ok"), nil
	}, func(context.Context, string, string) error { return nil })
	m.cfg.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, io.EOF
	})}
	body, _ := json.Marshal(sessionReadRequest(t, root))
	r := httptest.NewRequest(http.MethodPost, "/v1/session-read", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("transport error: %d %s", w.Code, w.Body.String())
	}
}
