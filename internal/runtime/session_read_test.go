package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSessionReadOwnerFenceAndMessageAllowlist(t *testing.T) {
	id := "hub-aaaaaaaaaaaaaaaaaaaaaaaa"
	var calls atomic.Int64
	hermes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/sessions/"+id+"/messages" || r.URL.Query().Get("limit") != "2" || r.Header.Get("Authorization") != "Bearer hermes-secret" {
			t.Error("Hermes read route escaped the validated session")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": id, "data": []map[string]string{{"role": "user", "content": "owner fact"}, {"role": "tool", "content": "private tool output"}}})
	}))
	defer hermes.Close()
	u, _ := url.Parse(hermes.URL)
	t.Setenv("HUB_HERMES_API_HOST", u.Hostname())
	t.Setenv("HUB_HERMES_API_PORT", u.Port())
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("API_SERVER_KEY", "hermes-secret")
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "alice")
	t.Setenv("HUB_POLICY_VERSION", "policy-1")
	request := SessionReadRequest{ExecuteRequest: validExecuteRequest("alice", "personal", "session-read", ""), SessionID: id, Limit: 2}
	call := func(auth string, in SessionReadRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(in)
		r := httptest.NewRequest(http.MethodPost, "/v1/session-read", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+auth)
		w := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(w, r)
		return w
	}
	if w := call("wrong", request); w.Code != http.StatusUnauthorized {
		t.Fatal("unauthorized read", w.Code)
	}
	foreign := request
	foreign.Envelope = validExecuteRequest("bob", "personal", "foreign", "").Envelope
	foreign.UserID, foreign.ScopeID = "bob", "user:bob"
	if w := call("runtime-secret", foreign); w.Code != http.StatusBadRequest {
		t.Fatal("foreign owner read", w.Code)
	}
	badID := request
	badID.SessionID = "../../foreign"
	if w := call("runtime-secret", badID); w.Code != http.StatusBadRequest {
		t.Fatal("foreign session path read", w.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("denied request reached Hermes")
	}
	w := call("runtime-secret", request)
	if w.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(w.Body.String(), "owner fact") || strings.Contains(w.Body.String(), "private tool output") {
		t.Fatal("bounded session read", w.Code, calls.Load())
	}
}
