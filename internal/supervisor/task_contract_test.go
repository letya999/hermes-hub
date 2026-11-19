package supervisor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/contracts/task/v1/conformance"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestSupervisorTaskContractRequiresBearer(t *testing.T) {
	for _, tc := range []struct{ name, token, header string }{
		{"raw_token", "expected", "expected"},
		{"empty_config", "", "Bearer "},
		{"no_header", "expected", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &Manager{cfg: Config{RuntimeAuth: tc.token}}
			request := httptest.NewRequest(http.MethodGet, "/v1/jobs/job-1", nil)
			request.Header.Set("Authorization", tc.header)
			response := httptest.NewRecorder()
			manager.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("invalid authentication reached the job route: %d", response.Code)
			}
		})
	}
}

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	envelope := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	manager := &Manager{
		cfg: Config{Environment: "dev"},
		jobs: map[string]jobRecord{"job-1": {
			AdmissionID: "admission-1", RequestTextSHA256: "input-hash", Generation: "gen-1", Status: "running",
			Request:  hubruntime.ExecuteRequest{Envelope: envelope, UserID: "alice", ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot"},
			Response: hubruntime.ExecuteResponse{JobID: "job-1", RunID: "run-1", SessionID: "session-1"},
		}},
		items: map[string]*runtimeEntry{runtimeKey(Binding{PrincipalID: "alice", ContextID: "alice", RuntimeMode: "gateway"}): {Runtime: Runtime{Generation: "gen-1"}}},
	}
	server, ok := any(manager).(task.Client)
	if !ok {
		t.Fatal("supervisor does not implement the task contract")
	}
	known := task.RunRecord{JobID: "job-1", AdmissionID: "admission-1", RunID: "run-1", SessionID: "session-1",
		RuntimeGeneration: "gen-1", CurrentGeneration: "gen-1", Environment: "dev", Status: "running",
		ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot", UserID: "alice", TextSHA256: "input-hash",
		ResponseSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Envelope: envelope}
	conformance.Both(t, server, func(token string) http.Handler {
		manager.cfg.RuntimeAuth = token
		return manager.Handler()
	}, known)
}
