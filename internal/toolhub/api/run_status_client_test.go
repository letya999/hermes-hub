package api

import (
	"context"
	"encoding/json"
	"errors"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The supervisor run-context client is the seam the memory admission gate
// consults: it must call the same authenticated control-plane surface as the
// restart notifier (/v1/jobs/<id> under the control bearer), decode only the
// allowlisted binding fields, and treat any non-200 or malformed answer as an
// error — the gate denies on error, never on absence.
func TestSupervisorRunAuthorityContract(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"job_id": "job-1", "run_id": "run-1", "session_id": "s-1", "status": "running",
			"runtime_generation": "gen-1", "current_generation": "gen-1", "environment": "dev",
			"scope_id": "user:alice", "trigger": "message", "channel": "telegram_bot",
			"identity": map[string]any{"principal_id": "alice", "context_id": "alice", "runtime_id": "alice"},
			"outcome":  map[string]any{"text": "ignored"},
		})
	}))
	defer server.Close()
	a := task.NewHTTPClient(server.URL, "tok", server.Client())
	rc, err := a.Run(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if gotPath != "/v1/jobs/job-1" || gotAuth != "Bearer tok" {
		t.Fatalf("path=%q auth=%q", gotPath, gotAuth)
	}
	if rc.JobID != "job-1" || rc.RunID != "run-1" || rc.SessionID != "s-1" || rc.Status != "running" ||
		rc.RuntimeGeneration != "gen-1" || rc.CurrentGeneration != "gen-1" || rc.Environment != "dev" ||
		rc.ScopeID != "user:alice" || rc.Trigger != "message" || rc.Channel != "telegram_bot" ||
		rc.Envelope.PrincipalID != "alice" || rc.Envelope.ContextID != "alice" || rc.Envelope.RuntimeID != "alice" {
		t.Fatalf("decoded run context wrong: %+v", rc)
	}
	if Kind(rc) != RunKindPrivate {
		t.Fatalf("private message run classified %q", Kind(rc))
	}
}

// The authority is a deny path input: unknown jobs, refused bearers, upstream
// faults and malformed bodies must all surface as errors so the gate denies —
// a silent zero record would let a mismatched claim pass on empty fields.
func TestSupervisorRunAuthorityDeniesOnNonOK(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{{404, task.ErrNotFound}, {401, task.ErrForbidden}, {500, task.ErrUnavailable}, {204, task.ErrUnavailable}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
		}))
		a := task.NewHTTPClient(server.URL, "tok", server.Client())
		if _, err := a.Run(context.Background(), "job-x"); !errors.Is(err, tc.want) {
			t.Fatalf("status %d: want %v, got %v", tc.code, tc.want, err)
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()
	a := task.NewHTTPClient(server.URL, "tok", server.Client())
	if _, err := a.Run(context.Background(), "job-x"); err == nil {
		t.Fatal("malformed body did not error")
	}
}
