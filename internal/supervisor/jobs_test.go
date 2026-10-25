package supervisor

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestJobRecordHasNoPromptText(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "known", IdempotencyKey: "known", Text: "PRIVATE_INPUT_CANARY"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.finishJob(request, hubruntime.ExecuteResponse{JobID: request.JobID, Text: "final", Status: "completed"}, "completed")
	persisted, err := os.ReadFile(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "PRIVATE_INPUT_CANARY") {
		t.Fatal("prompt text persisted in job record")
	}
	var state persistedManager
	if err := json.Unmarshal(persisted, &state); err != nil {
		t.Fatal(err)
	}
	record, ok := state.Jobs[executeJobKey(request)]
	if !ok {
		t.Fatal("job record not persisted")
	}
	if record.Request.Text != "" {
		t.Fatal("request text persisted")
	}
	if !record.Request.Envelope.Equal(request.Envelope) {
		t.Fatal("durable identity lost")
	}
}

func TestProgressEventDoesNotPersist(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "known", IdempotencyKey: "known", Text: "secret"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	progress := hubruntime.ExecuteResponse{JobID: request.JobID, Status: "running", LastEvent: "run.progress", Text: "PROGRESS_MEMORY_ONLY"}
	if err := m.finishJobGeneration(request, progress, "running", ""); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("progress event rewrote durable state")
	}
	m.mu.Lock()
	record := m.jobs[executeJobKey(request)]
	m.mu.Unlock()
	if record.Response.Text != "PROGRESS_MEMORY_ONLY" || record.Status != "running" {
		t.Fatal("progress event not visible in memory")
	}
}

func TestLeaseChangeWhileRunningPersists(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "approval-job", IdempotencyKey: "approval-job"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	waiting := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: runtime.Generation, Status: "waiting_for_approval", ApprovalID: "one", ApprovalChoices: []string{"once", "deny"}}
	if err := m.finishJobGeneration(request, waiting, waiting.Status, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	waiting.ApprovalID = "two"
	if err := m.finishJobGeneration(request, waiting, waiting.Status, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	restored, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored.mu.Lock()
	defer restored.mu.Unlock()
	if restored.jobs[request.JobID].Response.ApprovalID != "two" {
		t.Fatal("rotated approval did not persist")
	}
	if _, ok := restored.items[runtimeKey(b)].leases["approval:"+request.JobID+":two"]; !ok {
		t.Fatal("rotated approval lease not durable")
	}
}

func TestTerminalJobsCompacted(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	old := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "old", IdempotencyKey: "old"}
	if _, _, err := m.beginJob(old); err != nil {
		t.Fatal(err)
	}
	m.finishJob(old, hubruntime.ExecuteResponse{JobID: "old", RunID: "run-old", Text: "result", Status: "completed"}, "completed")
	fresh := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "fresh", IdempotencyKey: "fresh"}
	if _, _, err := m.beginJob(fresh); err != nil {
		t.Fatal(err)
	}
	m.finishJob(fresh, hubruntime.ExecuteResponse{JobID: "fresh", RunID: "run-fresh", Text: "result", Status: "completed"}, "completed")
	live := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "live", IdempotencyKey: "live"}
	if _, _, err := m.beginJob(live); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	aged := m.jobs[executeJobKey(old)]
	aged.UpdatedAt = time.Now().UTC().Add(-25 * time.Hour)
	m.jobs[executeJobKey(old)] = aged
	_ = m.persistLocked()
	m.mu.Unlock()
	m.CompactJobs(time.Now().UTC())
	restored, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored.mu.Lock()
	defer restored.mu.Unlock()
	compacted := restored.jobs[executeJobKey(old)]
	if compacted.Status != "completed" || compacted.Response.RunID != "run-old" || compacted.UpdatedAt != aged.UpdatedAt {
		t.Fatalf("compacted record lost status/run_id/finished_at: %+v", compacted)
	}
	if !compacted.Request.Envelope.Equal(identity.Envelope{}) || compacted.Response.Text != "" {
		t.Fatalf("compacted record retained identity/content: %+v", compacted)
	}
	if got := restored.jobs[executeJobKey(fresh)]; got.Request.Envelope.Equal(identity.Envelope{}) || got.Response.Text != "result" {
		t.Fatal("recent terminal record compacted early")
	}
	if got := restored.jobs[executeJobKey(live)]; got.Request.Envelope.Equal(identity.Envelope{}) {
		t.Fatal("live record compacted")
	}
}

func TestSweepCompactsTerminalJobs(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "old", IdempotencyKey: "old"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.finishJob(request, hubruntime.ExecuteResponse{JobID: "old", RunID: "run-old", Status: "completed"}, "completed")
	m.mu.Lock()
	aged := m.jobs[executeJobKey(request)]
	aged.UpdatedAt = time.Now().UTC().Add(-25 * time.Hour)
	m.jobs[executeJobKey(request)] = aged
	m.mu.Unlock()
	m.sweep(context.Background(), time.Now().UTC())
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.jobs[executeJobKey(request)].Request.Envelope.Equal(identity.Envelope{}) {
		t.Fatal("sweep did not compact aged terminal job")
	}
}

func TestJobsStatusUsesStoredAuthorityAndHidesPrompt(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "known", IdempotencyKey: "known", Text: "PRIVATE_INPUT_CANARY"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.finishJob(request, hubruntime.ExecuteResponse{JobID: request.JobID, Text: "final", Status: "completed"}, "completed")
	for _, tc := range []struct {
		method, path, auth string
		status             int
	}{{"GET", "/v1/jobs/known", "secret", 200}, {"GET", "/v1/jobs/known", "other", 401}, {"GET", "/v1/jobs/missing", "secret", 404}, {"POST", "/v1/jobs/known", "secret", 405}, {"GET", "/v1/jobs/known/other", "secret", 400}, {"GET", "/v1/jobs", "secret", 405}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.auth)
		response := httptest.NewRecorder()
		m.Handler().ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, response.Code)
		}
		if strings.Contains(response.Body.String(), "PRIVATE_INPUT_CANARY") {
			t.Fatal("prompt disclosed")
		}
		if response.Code == 200 {
			var payload struct {
				Identity identity.Envelope `json:"identity"`
			}
			if json.Unmarshal(response.Body.Bytes(), &payload) != nil || !payload.Identity.Equal(request.Envelope) {
				t.Fatal("stored audience lost")
			}
		}
	}
}
