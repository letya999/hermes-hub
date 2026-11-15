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

func TestSupervisorAdmissionPreservesAssignmentProvenance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request hubruntime.ExecuteRequest
		if r.URL.Path != "/v1/jobs" || json.NewDecoder(r.Body).Decode(&request) != nil ||
			!request.Assignment || !request.Probe || !request.Untrusted || request.Sensitive ||
			r.Header.Get("Accept") != hubruntime.RunStreamContentType {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "run-1", Status: "running", LastEvent: "run.admitted"})
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "run-1", Status: "completed", Text: "done"})
	}))
	defer server.Close()
	job := commodel.Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "event-one",
		AssignmentID: "asg-one", AssignmentPlanRevision: 1, Probe: true, Untrusted: true, Text: "event"}
	result, err := (&HTTPSupervisorClient{URL: server.URL, Token: "control"}).RunJob(context.Background(), "stable-key", job)
	if err != nil || result.Status != "completed" {
		t.Fatalf("assignment provenance not carried to supervisor: status=%q err=%v", result.Status, err)
	}
}

func TestScriptRequestsKeepOccurrenceAdmissionEpoch(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request hubruntime.ScriptRequest
		if r.URL.Path != "/v1/script" || r.Header.Get("Authorization") != "Bearer control" || json.NewDecoder(r.Body).Decode(&request) != nil || request.OperationalEpoch != 2 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		calls++
		_ = json.NewEncoder(w).Encode([]string{"ok"})
	}))
	defer server.Close()
	client := &HTTPSupervisorClient{URL: server.URL, Token: "control"}
	o := commodel.RoutineOccurrence{Job: commodel.Job{OperationalEpoch: 2}}
	if _, err := client.RunScript(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if result, err := client.ProbeScript(context.Background(), o, "pass", "", false, nil); err != nil || result != "ok" || calls != 2 {
		t.Fatalf("probe epoch: result=%q calls=%d err=%v", result, calls, err)
	}
}

func TestAdmissionEpochHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		epoch      uint64
		denied     bool
	}{
		{"empty", `[]`, 200, 0, false},
		{"owner", `[{"principal_id":"bob","context_id":"bob","epoch":9},{"principal_id":"alice","context_id":"alice","epoch":2}]`, 200, 2, false},
		{"active", `[{"principal_id":"alice","context_id":"alice","epoch":2,"active":true}]`, 200, 0, true},
		{"duplicate", `[{"principal_id":"alice","context_id":"alice","epoch":2},{"principal_id":"alice","context_id":"alice","epoch":4}]`, 200, 0, true},
		{"unavailable", `[]`, 503, 0, true},
		{"malformed", `{}`, 200, 0, true},
		{"trailing", `[] {}`, 200, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/holds" || r.Header.Get("Authorization") != "Bearer control" {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := &HTTPSupervisorClient{URL: server.URL, Token: "control"}
			epoch, err := client.AdmissionEpoch(context.Background(), identity.TelegramEnvelope("alice", 11, "alice", "policy-1"))
			if (err != nil) != tc.denied || epoch != tc.epoch {
				t.Fatalf("epoch=%d err=%v", epoch, err)
			}
		})
	}
}
