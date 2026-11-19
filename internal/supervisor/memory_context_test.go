package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// memoryInstructionsRequest builds the private-run dispatch request the
// memory lane sees: private scope, message trigger, a durable job id.
func memoryInstructionsRequest() hubruntime.ExecuteRequest {
	return hubruntime.ExecuteRequest{
		JobID: "job-1", PrincipalID: "alice", ContextID: "alice",
		ScopeID: "user:alice", Trigger: "message", Text: "earl grey supplier",
		Instructions: "base",
	}
}

// SPEC-0098 M5/#517: pre-dispatch memory context comes only from ToolHub —
// private/sensitive/kind gates and every failure mode either skip the call
// or degrade to the explicit unavailable marker, never a local vault read.
func TestMemoryInstructionsBoundary(t *testing.T) {
	var calls atomic.Int32
	var gotAuth, gotPrincipal, gotBody atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotAuth.Store(r.Header.Get("Authorization"))
		gotPrincipal.Store(r.Header.Get("X-Hub-Principal"))
		var claim map[string]string
		_ = json.NewDecoder(r.Body).Decode(&claim)
		gotBody.Store(claim)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "core": "core-block", "recall": "recall-block"})
	}))
	defer server.Close()

	newManager := func() *Manager {
		return &Manager{cfg: Config{RuntimeAuth: "control-secret"},
			jobs: map[string]jobRecord{"job-1": {AdmissionID: "adm-1"}}}
	}

	t.Setenv("HUB_TOOLHUB_URL_PATTERN", server.URL+"/%s")
	m := newManager()
	out := m.memoryInstructions(context.Background(), memoryInstructionsRequest())
	if !strings.Contains(out, "core-block") || !strings.Contains(out, "recall-block") || !strings.HasPrefix(out, "base") {
		t.Fatalf("memory context not injected: %q", out)
	}
	if calls.Load() != 1 {
		t.Fatalf("endpoint calls = %d", calls.Load())
	}
	assignment := memoryInstructionsRequest()
	assignment.Trigger, assignment.Assignment, assignment.UserID = "cron", true, "alice"
	if out := m.memoryInstructions(context.Background(), assignment); !strings.Contains(out, "recall-block") || calls.Load() != 2 {
		t.Fatalf("owner assignment missed ToolHub memory context: calls=%d", calls.Load())
	}
	if gotAuth.Load() != "Bearer control-secret" || gotPrincipal.Load() != "alice" {
		t.Fatalf("control auth/principal headers wrong: %v %v", gotAuth.Load(), gotPrincipal.Load())
	}
	claim := gotBody.Load().(map[string]string)
	if claim["job_id"] != "job-1" || claim["admission_id"] != "adm-1" || claim["text"] != "earl grey supplier" {
		t.Fatalf("claim fields wrong: %v", claim)
	}

	// Non-private kinds and sensitive turns never reach the endpoint at all.
	for name, mutate := range map[string]func(*hubruntime.ExecuteRequest){
		"group scope":        func(r *hubruntime.ExecuteRequest) { r.ScopeID = "group:alice" },
		"background kind":    func(r *hubruntime.ExecuteRequest) { r.Trigger = "goal" },
		"foreign assignment": func(r *hubruntime.ExecuteRequest) { r.Trigger, r.Assignment, r.UserID = "cron", true, "bob" },
		"sensitive request":  func(r *hubruntime.ExecuteRequest) { r.Sensitive = true },
	} {
		before := calls.Load()
		req := memoryInstructionsRequest()
		mutate(&req)
		if out := m.memoryInstructions(context.Background(), req); out != "base" || calls.Load() != before {
			t.Fatalf("%s reached the endpoint or changed instructions: %q", name, out)
		}
	}

	// Without a durable admission the run marks memory explicitly
	// unavailable — and still never calls the endpoint.
	mNoAdm := &Manager{cfg: Config{RuntimeAuth: "control-secret"}, jobs: map[string]jobRecord{"job-1": {}}}
	before := calls.Load()
	out = mNoAdm.memoryInstructions(context.Background(), memoryInstructionsRequest())
	if !strings.Contains(out, memoryUnavailable) || calls.Load() != before {
		t.Fatalf("missing admission did not mark unavailable: %q calls=%d", out, calls.Load())
	}
}

func TestMemoryInstructionsSignsTheAdmittedClaim(t *testing.T) {
	registry, _ := fixtureIdentity(t)
	if err := registry.EnsureSigningKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	keys, err := registry.SigningKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	request := memoryInstructionsRequest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		scope := contract.ServiceRequest{Space: request.ContextID, Environment: "dev", RequestDigest: contract.PolicyDigest(body)}
		token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if err != nil || !bearer || r.Header.Get("X-Hub-Principal") != "" ||
			contract.VerifyServiceToken(token, contract.MemoryContextServiceAudience, "system:hub-control", scope, keys, time.Now()) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "recall": "signed recall"})
	}))
	defer server.Close()
	t.Setenv("HUB_TOOLHUB_URL_PATTERN", server.URL+"/%s")
	m := &Manager{cfg: Config{Identity: registry, Environment: "dev", RuntimeAuth: "legacy-control"},
		jobs: map[string]jobRecord{request.JobID: {AdmissionID: "adm-1", PolicyRevision: contract.PolicyDigest([]byte("fixture-policy"))}}}
	if out := m.memoryInstructions(t.Context(), request); !strings.Contains(out, "signed recall") {
		t.Fatal("admitted recall did not carry a service signature")
	}
}

// Every ToolHub-side failure — non-200, malformed, dishonest sizes,
// unreachable — degrades to the one explicit unavailable marker. There is
// no local vault read to fall back to.
func TestMemoryInstructionsUnavailableFailsClosed(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"denied":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"malformed": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{")) },
		"bad status": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "injected"})
		},
		"oversized core": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "core": strings.Repeat("c", 4000)})
		},
		"oversized recall": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "recall": strings.Repeat("r", 3000)})
		},
	}
	for name, handler := range cases {
		server := httptest.NewServer(handler)
		t.Setenv("HUB_TOOLHUB_URL_PATTERN", server.URL+"/%s")
		m := &Manager{cfg: Config{RuntimeAuth: "x"}, jobs: map[string]jobRecord{"job-1": {AdmissionID: "adm-1"}}}
		out := m.memoryInstructions(context.Background(), memoryInstructionsRequest())
		if !strings.Contains(out, memoryUnavailable) {
			t.Fatalf("%s did not mark unavailable: %q", name, out)
		}
		server.Close()
	}
	// An unreachable endpoint and a missing pattern behave the same way:
	// unavailable marker vs. untouched instructions when no ToolHub is
	// configured at all (no injection point exists to mark).
	t.Setenv("HUB_TOOLHUB_URL_PATTERN", "http://127.0.0.1:1/%s")
	m := &Manager{cfg: Config{RuntimeAuth: "x"}, jobs: map[string]jobRecord{"job-1": {AdmissionID: "adm-1"}}}
	if out := m.memoryInstructions(context.Background(), memoryInstructionsRequest()); !strings.Contains(out, memoryUnavailable) {
		t.Fatalf("unreachable ToolHub did not mark unavailable: %q", out)
	}
	t.Setenv("HUB_TOOLHUB_URL_PATTERN", "")
	if out := m.memoryInstructions(context.Background(), memoryInstructionsRequest()); out != "base" {
		t.Fatalf("unconfigured ToolHub changed instructions: %q", out)
	}
}
