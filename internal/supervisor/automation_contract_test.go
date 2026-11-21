package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

func TestAutomationClientUsesSupervisorJobsContract(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	job := commodel.Job{
		Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"),
		ID:       "automation-job", OrganizationID: "personal", UserID: "alice",
		ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot",
		Trigger: "test", Text: "run",
	}
	request := hubruntime.ExecuteRequest{
		Envelope: job.Envelope, JobID: job.ID, OrganizationID: job.OrganizationID,
		UserID: job.UserID, ActorID: job.ActorID, ScopeID: job.ScopeID,
		Channel: job.Channel, Trigger: job.Trigger, IdempotencyKey: "stable-key", Text: job.Text,
	}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.finishJob(request, hubruntime.ExecuteResponse{JobID: job.ID, Status: "completed", Text: "done"}, "completed")
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	client := automation.HTTPSupervisorClient{URL: srv.URL, Token: "secret"}
	result, err := client.RunJob(context.Background(), "stable-key", job)
	if err != nil || result.JobID != job.ID || result.Text != "done" {
		t.Fatalf("supervisor replay result: %+v, %v", result, err)
	}
	client.Token = "wrong"
	if _, err := client.RunJob(context.Background(), "stable-key", job); err == nil {
		t.Fatal("supervisor accepted the wrong bearer")
	}
}

func TestAutomationClientRunsThroughSupervisorJobsHandler(t *testing.T) {
	var calls atomic.Int32
	var generation string
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request hubruntime.ExecuteRequest
		if r.URL.Path != "/v1/execute" || r.Header.Get("Authorization") != "Bearer secret" || json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{
			JobID: request.JobID, RuntimeGeneration: generation,
			RunID: "run-1", SessionID: "session-1", Text: "done",
		})
	}))
	defer runtimeAPI.Close()
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b, err := m.normalize(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(b)]
	entry.Address, entry.State, entry.auth = runtimeAPI.URL, Ready, "secret"
	generation = entry.Generation
	m.mu.Unlock()
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	job := commodel.Job{
		Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"),
		ID:       "fresh-automation-job", OrganizationID: "personal", UserID: "alice",
		ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot",
		Trigger: "test", Text: "run",
	}
	client := automation.HTTPSupervisorClient{URL: srv.URL, Token: "secret"}
	for i := 0; i < 2; i++ {
		result, err := client.RunJob(context.Background(), "stable-fresh-key", job)
		if err != nil || result.Status != "completed" || result.Text != "done" {
			t.Fatalf("run %d: %+v, %v", i, result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("supervisor executed replay %d times", calls.Load())
	}
}
