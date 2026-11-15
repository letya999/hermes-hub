package api

import (
	"context"
	"encoding/json"
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
	a := &SupervisorRunAuthority{URL: server.URL, Token: "tok", HTTP: server.Client()}
	rc, err := a.Lookup(context.Background(), "job-1")
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
	if rc.Kind() != RunKindPrivate {
		t.Fatalf("private message run classified %q", rc.Kind())
	}
}

// The authority is a deny path input: unknown jobs, refused bearers, upstream
// faults and malformed bodies must all surface as errors so the gate denies —
// a silent zero record would let a mismatched claim pass on empty fields.
func TestSupervisorRunAuthorityDeniesOnNonOK(t *testing.T) {
	for _, code := range []int{404, 401, 500} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		a := &SupervisorRunAuthority{URL: server.URL, Token: "tok", HTTP: server.Client()}
		if _, err := a.Lookup(context.Background(), "job-x"); err == nil {
			t.Fatalf("status %d did not error", code)
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()
	a := &SupervisorRunAuthority{URL: server.URL, Token: "tok", HTTP: server.Client()}
	if _, err := a.Lookup(context.Background(), "job-x"); err == nil {
		t.Fatal("malformed body did not error")
	}
}

// FromEnv mirrors the restart notifier's posture: the authority exists only
// when both halves of the rendered control channel are present.
func TestRunAuthorityFromEnv(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	if RunAuthorityFromEnv() != nil {
		t.Fatal("unconfigured env produced an authority")
	}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://supervisor:8876/")
	t.Setenv("HUB_COMMUNICATION_AUTH", " tok ")
	a := RunAuthorityFromEnv()
	if a == nil || a.URL != "http://supervisor:8876" || a.Token != "tok" {
		t.Fatalf("env authority wrong: %+v", a)
	}
}
