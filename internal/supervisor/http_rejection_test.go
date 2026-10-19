package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestUntypedRuntimeHTTPFailureHasDurableOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		status string
	}{
		{"invalid", 400, "failed"}, {"unauthorized", 401, "failed"}, {"forbidden", 403, "failed"}, {"unprocessable", 422, "failed"}, {"conflict", 409, "uncertain"}, {"server", 500, "uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				return []byte("running"), nil
			}, func(context.Context, string, string) error { return nil })
			b := binding(root)
			runtime, err := m.Ensure(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.ReleaseBinding(b); err != nil {
				t.Fatal(err)
			}
			calls := 0
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(`{"error":"private runtime detail"}`))
			}))
			defer backend.Close()
			m.mu.Lock()
			m.items[runtimeKey(b)].Address = backend.URL
			m.mu.Unlock()
			request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "rejected-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "rejected", Text: "hello"}
			body, _ := json.Marshal(request)
			call := func(manager *Manager) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(string(body)))
				r.Header.Set("Authorization", "Bearer secret")
				w := httptest.NewRecorder()
				manager.Handler().ServeHTTP(w, r)
				return w
			}
			response := call(m)
			var result hubruntime.ExecuteResponse
			if response.Code != tc.code || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Status != tc.status || result.JobID != request.JobID || result.RuntimeGeneration != runtime.Generation || strings.Contains(response.Body.String(), "private runtime detail") {
				t.Fatalf("response=%d %s", response.Code, response.Body.String())
			}
			restored, err := New(m.cfg)
			if err != nil {
				t.Fatal(err)
			}
			record := restored.jobs[request.JobID]
			if record.Status != tc.status || record.Response.Status != tc.status || record.Unconfirmed != (tc.status == "uncertain") {
				t.Fatalf("persisted outcome=%+v", record)
			}
			if tc.status == "failed" {
				replayed := call(restored)
				if json.Unmarshal(replayed.Body.Bytes(), &result) != nil || result.Status != "failed" || calls != 1 {
					t.Fatalf("replay=%d %s calls=%d", replayed.Code, replayed.Body.String(), calls)
				}
			}
		})
	}
}
