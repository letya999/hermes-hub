package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestStaticRuntimeUsesExistingExecuteContract(t *testing.T) {
	calls := 0
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/execute" || r.Header.Get("Authorization") != "Bearer control" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var request hubruntime.ExecuteRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.JobID != "routine-static" || request.IdempotencyKey != "stable-key" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls++
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: request.JobID, Status: "failed"})
			return
		}
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: request.JobID, Status: "completed", Text: "done"})
	}))
	defer server.Close()
	client := &HTTPSupervisorClient{URL: server.URL, Token: "control", JobPath: "/v1/execute"}
	job := commodel.Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "routine-static", Text: "work"}
	if result, err := client.RunJob(context.Background(), "stable-key", job); err != nil || result.Status != "completed" || calls != 1 {
		t.Fatalf("static runtime execute: status=%q calls=%d err=%v", result.Status, calls, err)
	}
	fail = true
	if result, err := client.RunJob(context.Background(), "stable-key", job); err != nil || result.Status != "failed" || calls != 2 {
		t.Fatalf("static runtime terminal failure: status=%q calls=%d err=%v", result.Status, calls, err)
	}
	if _, err := client.RunScript(context.Background(), commodel.RoutineOccurrence{Job: job}); err == nil || calls != 2 {
		t.Fatal("static runtime received an unsupported script request")
	}
}
