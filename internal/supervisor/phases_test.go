package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// SPEC-0053 R3.1: the job phase machine is explicit — every legal transition is
// in the table, everything else is rejected so a late or stale event cannot
// silently move a job backwards or resurrect a terminal one.
func TestJobPhaseTransitions(t *testing.T) {
	legal := map[string][]string{
		phaseAccepted:        {phaseWaking, phaseAdmitted, phaseCancelled, phaseInterrupted, phaseFailed},
		phaseWaking:          {phaseAdmitted, phaseRunning, phaseCancelled, phaseInterrupted, phaseFailed},
		phaseAdmitted:        {phaseRunning, phaseCancelled, phaseInterrupted, phaseFailed},
		phaseRunning:         {phaseWaitingApproval, phaseCompleted, phaseFailed, phaseCancelled, phaseInterrupted},
		phaseWaitingApproval: {phaseRunning, phaseCompleted, phaseFailed, phaseCancelled, phaseInterrupted},
	}
	phases := []string{phaseAccepted, phaseWaking, phaseAdmitted, phaseRunning, phaseWaitingApproval, phaseCompleted, phaseFailed, phaseCancelled, phaseInterrupted}
	for _, from := range phases {
		for _, to := range phases {
			if from == to {
				continue
			}
			_, ok := nextPhase(from, phaseEvent{To: to})
			allowed := false
			for _, want := range legal[from] {
				if want == to {
					allowed = true
				}
			}
			if ok != allowed {
				t.Fatalf("nextPhase(%q -> %q) = %v, want %v", from, to, ok, allowed)
			}
		}
	}
	// Terminal phases never leave; the same-terminal idempotent repeat is ok.
	for _, terminal := range []string{phaseCompleted, phaseFailed, phaseCancelled, phaseInterrupted} {
		if _, ok := nextPhase(terminal, phaseEvent{To: terminal}); !ok {
			t.Fatalf("same-terminal repeat %q must be allowed", terminal)
		}
	}
}

// Legacy records persisted before phases existed are mapped on load: phase is
// derived from status/binding position and uncertainty becomes the flag.
func TestLegacyRecordsMapToPhases(t *testing.T) {
	run := func(id string) hubruntime.ExecuteResponse {
		return hubruntime.ExecuteResponse{RunID: id}
	}
	cases := []struct {
		name            string
		record          jobRecord
		wantPhase       string
		wantUnconfirmed bool
	}{
		{"accepted", jobRecord{Status: "running"}, phaseAccepted, false},
		{"admitted", jobRecord{Status: "running", Generation: "gen-1"}, phaseAdmitted, false},
		{"running", jobRecord{Status: "running", Generation: "gen-1", Response: run("run-1")}, phaseRunning, false},
		{"waiting approval", jobRecord{Status: "waiting_for_approval", Generation: "gen-1"}, phaseWaitingApproval, false},
		{"queued", jobRecord{Status: "queued"}, phaseAccepted, false},
		{"uncertain bound", jobRecord{Status: "uncertain", Generation: "gen-1", Response: run("run-1")}, phaseRunning, true},
		{"uncertain unbound", jobRecord{Status: "uncertain"}, phaseAccepted, true},
		{"completed", jobRecord{Status: "completed"}, phaseCompleted, false},
		{"failed", jobRecord{Status: "failed"}, phaseFailed, false},
		{"cancelled", jobRecord{Status: "cancelled"}, phaseCancelled, false},
		{"interrupted", jobRecord{Status: "interrupted"}, phaseInterrupted, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := tc.record
			record.UpdatedAt = time.Now().UTC()
			mapLegacyPhase(&record)
			if record.Phase != tc.wantPhase {
				t.Fatalf("phase = %q, want %q", record.Phase, tc.wantPhase)
			}
			if record.Unconfirmed != tc.wantUnconfirmed {
				t.Fatalf("unconfirmed = %v, want %v", record.Unconfirmed, tc.wantUnconfirmed)
			}
			if tc.wantUnconfirmed && record.UnconfirmedSince.IsZero() {
				t.Fatal("unconfirmed record must carry a since timestamp")
			}
		})
	}
}

// A restart over state written before phases existed maps every record: the
// running->uncertain restore keeps its real position as the phase while the
// flag records that confirmation was lost.
func TestLoadRepairsLegacyInterruptedReplay(t *testing.T) {
	for _, responseStatus := range []string{"", "uncertain"} {
		t.Run("response="+responseStatus, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "supervisor.json")
			stamp := time.Unix(100, 0).UTC()
			old := jobRecord{Request: hubruntime.ExecuteRequest{JobID: "old-interrupted"}, Status: "interrupted", Phase: phaseInterrupted, UpdatedAt: stamp, Response: hubruntime.ExecuteResponse{JobID: "old-interrupted", RunID: "original-run", SessionID: "original-session", Status: responseStatus, LastEvent: "run.unknown"}}
			body, err := json.Marshal(persistedManager{Jobs: map[string]jobRecord{"old-interrupted": old}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			m, err := New(Config{SpacesRoot: root, StateDir: root, RuntimeAuth: "control", Image: "hermes:test"})
			if err != nil {
				t.Fatal(err)
			}
			got := m.jobs["old-interrupted"]
			if got.Response.Status != "interrupted" || got.Response.LastEvent != "run.interrupted" || got.Response.RunID != old.Response.RunID || got.Response.SessionID != old.Response.SessionID || !got.UpdatedAt.Equal(stamp) {
				t.Fatalf("legacy interrupted replay not repaired in place: %+v", got)
			}
			body, err = os.ReadFile(path)
			var saved persistedManager
			if err != nil || json.Unmarshal(body, &saved) != nil || saved.Jobs["old-interrupted"].Response.Status != "interrupted" {
				t.Fatal("legacy replay repair was not durable")
			}
		})
	}
}

func TestSupervisorRestartMapsLegacyJobPhase(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".control")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	state := `{"jobs":{"job-1":{"request":{"job_id":"job-1","principal_id":"alice","context_id":"alice","idempotency_key":"idem-1"},"fingerprint":"fp","status":"running","updated_at":"2026-10-14T00:00:00Z"},"job-2":{"request":{"job_id":"job-2","principal_id":"alice","context_id":"alice","idempotency_key":"idem-2"},"fingerprint":"fp","status":"uncertain","generation":"gen-9","response":{"run_id":"run-9","status":"uncertain"},"updated_at":"2026-10-14T00:00:00Z"}}}`
	if err := os.WriteFile(filepath.Join(stateDir, "supervisor.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{SpacesRoot: root, RuntimeAuth: "control", Image: "hermes:test", StateDir: stateDir, Command: func(context.Context, ...string) ([]byte, error) { return nil, os.ErrNotExist }, Probe: func(context.Context, string, string) error { return nil }}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lost := m.jobs["job-1"]
	if lost.Status != "uncertain" || lost.Phase != phaseAccepted || !lost.Unconfirmed || lost.UnconfirmedSince.IsZero() {
		t.Fatalf("restored running job = %+v, want uncertain/accepted/unconfirmed", lost)
	}
	bound := m.jobs["job-2"]
	if bound.Phase != phaseRunning || !bound.Unconfirmed {
		t.Fatalf("restored uncertain bound job = %+v, want running/unconfirmed", bound)
	}
}

// Records persisted before the status vocabulary existed sit at status "".
// A dispatched one was admitted but never observed — it is unconfirmed, so the
// reconciler owns it from its UpdatedAt anchor and the budget branch settles
// the residue instead of leaving the runtime desired forever.
func TestLegacyEmptyStatusMarkedUnconfirmed(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".control")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * hubruntime.RunBudget()).UTC().Format(time.RFC3339Nano)
	state := fmt.Sprintf(`{"jobs":{"job-old":{"dispatching":true,"request":{"job_id":"job-old","principal_id":"alice","context_id":"alice","idempotency_key":"idem-old"},"fingerprint":"fp","status":"","generation":"gen-9","updated_at":%q}}}`, old)
	if err := os.WriteFile(filepath.Join(stateDir, "supervisor.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{SpacesRoot: root, RuntimeAuth: "control", Image: "hermes:test", StateDir: stateDir, Command: func(context.Context, ...string) ([]byte, error) { return nil, os.ErrNotExist }, Probe: func(context.Context, string, string) error { return nil }}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	record := m.jobs["job-old"]
	m.mu.Unlock()
	if !record.Unconfirmed || record.UnconfirmedSince.IsZero() || record.Status != "uncertain" {
		t.Fatalf("legacy empty-status record = %+v, want uncertain/unconfirmed", record)
	}
	// The anchor is past the run budget: the first reconcile names the failure
	// instead of re-executing stale side-effecting work.
	m.reconcileUnconfirmed(context.Background(), time.Now())
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		record = m.jobs["job-old"]
		m.mu.Unlock()
		if record.Status == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if record.Status != "failed" || record.Response.LastEvent != "run.timeout" {
		t.Fatalf("settled record = %+v, want failed/run.timeout", record)
	}
}

// The record moves through the machine as real events land: accepted at
// admission, admitted when a generation binds, running on first observation,
// terminal on the outcome. An uncertain event flags, it never repositions.
func TestJobPhaseProgressesThroughLifecycle(t *testing.T) {
	m := &Manager{jobs: map[string]jobRecord{}, items: map[string]*runtimeEntry{}, cfg: Config{StateDir: t.TempDir()}}
	m.statePath = filepath.Join(m.cfg.StateDir, "supervisor.json")
	request := hubruntime.ExecuteRequest{JobID: "job-1", PrincipalID: "alice", ContextID: "alice", IdempotencyKey: "idem-1"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	phase := func() string { return m.jobs["job-1"].Phase }
	if phase() != phaseAccepted {
		t.Fatalf("after begin: phase=%s", phase())
	}
	if err := m.bindJobGeneration(request, "gen-1"); err != nil {
		t.Fatal(err)
	}
	if phase() != phaseAdmitted {
		t.Fatalf("after bind: phase=%s", phase())
	}
	running := hubruntime.ExecuteResponse{JobID: "job-1", RunID: "run-1", Status: "running"}
	if err := m.finishJobGeneration(request, running, "running", "gen-1"); err != nil {
		t.Fatal(err)
	}
	if phase() != phaseRunning {
		t.Fatalf("after running event: phase=%s", phase())
	}
	uncertain := hubruntime.ExecuteResponse{JobID: "job-1", RunID: "run-1", Status: "uncertain"}
	if err := m.finishJobGeneration(request, uncertain, "uncertain", "gen-1"); err != nil {
		t.Fatal(err)
	}
	record := m.jobs["job-1"]
	if record.Phase != phaseRunning || !record.Unconfirmed || record.UnconfirmedSince.IsZero() {
		t.Fatalf("uncertain must flag not reposition: %+v", record)
	}
	done := hubruntime.ExecuteResponse{JobID: "job-1", RunID: "run-1", Status: "completed", Text: "answer"}
	if err := m.finishJobGeneration(request, done, "completed", "gen-1"); err != nil {
		t.Fatal(err)
	}
	record = m.jobs["job-1"]
	if record.Phase != phaseCompleted || record.Unconfirmed {
		t.Fatalf("terminal record = %+v, want completed/confirmed", record)
	}
}

// A job whose runtime is still starting sits in "waking" — the window between
// admission and generation binding is what the cold-start phase is for.
func TestJobPhaseWakingWhileRuntimeStarts(t *testing.T) {
	root := t.TempDir()
	ctxRoot := filepath.Join(root, "alice")
	for _, name := range []string{"runtime", "hermes", "workspace"} {
		if err := os.MkdirAll(filepath.Join(ctxRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, ctxRoot)
	gate := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	command := func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			once.Do(func() { close(started) })
			<-gate
			return []byte("ok"), nil
		}
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}
	cfg := Config{SpacesRoot: root, RuntimeAuth: "control", Image: "hermes:test", Command: command, Probe: func(context.Context, string, string) error { return nil }}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "job-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-1", Text: "hello"}
	body, _ := json.Marshal(request)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer control")
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, req)
		done <- rec
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("runtime start did not begin")
	}
	m.mu.Lock()
	phase := m.jobs["job-1"].Phase
	m.mu.Unlock()
	if phase != phaseWaking {
		t.Fatalf("phase during runtime start = %q, want waking", phase)
	}
	close(gate)
	select {
	case rec := <-done:
		if rec.Code == http.StatusOK {
			t.Fatalf("unexpected success without runtime backend: %s", rec.Body.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("execute did not finish after gate release")
	}
}

// The durable cancel path writes a terminal phase, not just a terminal status —
// /status and the reconciler read position from the same machine.
func TestCancelSetsTerminalPhase(t *testing.T) {
	m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "job-c", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-c", Text: "hello"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	control := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: request}, Action: "cancel"}
	body, _ := json.Marshal(control)
	req := httptest.NewRequest(http.MethodPost, "/v1/control", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	m.mu.Lock()
	record := m.jobs["job-c"]
	m.mu.Unlock()
	if record.Status != "cancelled" || record.Phase != phaseCancelled {
		t.Fatalf("record = %+v, want cancelled/cancelled", record)
	}
}

// SPEC-0053 R3.2: an unconfirmed job with a known run is re-observed through
// the runtime's stream; a terminal event resolves the record instead of
// leaving the outcome unknown.
func TestReconcileObservesKnownRun(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "job-obs", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-obs", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	if err = m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	var observed atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/observe" {
			observed.Add(1)
			w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
			event := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: runtime.Generation, Status: "completed", Text: "answer", LastEvent: "run.completed"}
			_ = json.NewEncoder(w).Encode(event)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	m.mu.Unlock()
	// The reconciler waits 30s before touching an unconfirmed job.
	m.reconcileUnconfirmed(context.Background(), time.Now())
	if observed.Load() != 0 {
		t.Fatal("fresh unconfirmed job reconciled too early")
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	record.UnconfirmedSince = time.Now().Add(-time.Minute)
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.reconcileUnconfirmed(context.Background(), time.Now())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		status := m.jobs[request.JobID].Status
		m.mu.Unlock()
		if status == "completed" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t.Fatalf("record after reconcile = %+v", m.jobs[request.JobID])
}

func TestReconcileReplaysIdempotencyKeyWhenRunIDUnknown(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "job-readmit", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-readmit", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	if err = m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	var readmitKey atomic.Value
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/readmit" {
			var replayed hubruntime.ExecuteRequest
			if json.NewDecoder(r.Body).Decode(&replayed) == nil {
				readmitKey.Store(replayed.IdempotencyKey)
			}
			w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
			event := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-2", RunID: "run-9", RuntimeGeneration: runtime.Generation, Status: "completed", Text: "answer", LastEvent: "run.completed"}
			_ = json.NewEncoder(w).Encode(event)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	record := m.jobs[request.JobID]
	record.UnconfirmedSince = time.Now().Add(-time.Minute)
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.reconcileUnconfirmed(context.Background(), time.Now())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		status := m.jobs[request.JobID].Status
		m.mu.Unlock()
		if status == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	finished := m.jobs[request.JobID]
	if finished.Status != "completed" {
		t.Fatalf("record after reconcile = %+v", finished)
	}
	if got := readmitKey.Load(); got != "idem-readmit" {
		t.Fatalf("readmit key = %v, want original idempotency key", got)
	}
	if finished.Response.RunID != "run-9" {
		t.Fatalf("run id = %q, want replayed run-9", finished.Response.RunID)
	}
}

// A pre-admission failure never wrote a runtime receipt, and the durable
// record strips the prompt by design — a readmit refusal (409) is permanent.
// The job must be named failed instead of re-asking the runtime forever.
func TestReconcileSettlesLostAdmission(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "job-lost", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-lost", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	if err = m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	var readmitCalls atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/readmit" {
			readmitCalls.Add(1)
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	record := m.jobs[request.JobID]
	record.UnconfirmedSince = time.Now().Add(-time.Minute)
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.reconcileUnconfirmed(context.Background(), time.Now())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		status := m.jobs[request.JobID].Status
		m.mu.Unlock()
		if status == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Lock()
	finished := m.jobs[request.JobID]
	m.mu.Unlock()
	if finished.Status != "failed" || finished.Response.LastEvent != "run.failed" {
		t.Fatalf("record after readmit refusal = %+v", finished)
	}
	// The verdict settles once: a later sweep must not re-ask the runtime.
	m.reconcileUnconfirmed(context.Background(), time.Now())
	time.Sleep(50 * time.Millisecond)
	if readmitCalls.Load() != 1 {
		t.Fatalf("readmit calls = %d, want exactly 1", readmitCalls.Load())
	}
}

// A still-unconfirmed observe must not accumulate a lease per attempt: the
// record itself pins the runtime as desired while its outcome is open.
func TestReconcileAttemptLeaseReleasedWhenUnresolved(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "job-stall", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-stall", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	if err = m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	var observed atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/observe" {
			observed.Add(1)
			w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
			event := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: runtime.Generation, Status: "running", LastEvent: "run.heartbeat"}
			_ = json.NewEncoder(w).Encode(event)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	record := m.jobs[request.JobID]
	record.UnconfirmedSince = time.Now().Add(-time.Minute)
	m.jobs[request.JobID] = record
	baseline := m.items[runtimeKey(b)].Leases
	m.mu.Unlock()
	m.reconcileUnconfirmed(context.Background(), time.Now())
	deadline := time.Now().Add(10 * time.Second)
	for observed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if observed.Load() == 0 {
		t.Fatal("reconcile never observed")
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		leases, holds := m.items[runtimeKey(b)].Leases, len(m.items[runtimeKey(b)].leases)
		m.mu.Unlock()
		if leases == baseline && holds == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.items[runtimeKey(b)]
	t.Fatalf("unresolved reconcile left holds: leases=%d map=%v, want %d/empty", entry.Leases, entry.leases, baseline)
}

func TestReconcileDeliversOnce(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "job-once", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-once", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	if err = m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	var observed atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/observe" {
			observed.Add(1)
			w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
			event := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: runtime.Generation, Status: "completed", Text: "answer", LastEvent: "run.completed"}
			_ = json.NewEncoder(w).Encode(event)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	record := m.jobs[request.JobID]
	record.UnconfirmedSince = time.Now().Add(-time.Minute)
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.reconcileUnconfirmed(context.Background(), time.Now())
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.Lock()
		settled := m.jobs[request.JobID].Status == "completed"
		m.mu.Unlock()
		if settled || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Lock()
	status := m.jobs[request.JobID].Status
	m.mu.Unlock()
	if status != "completed" {
		t.Fatalf("reconcile left job at %q", status)
	}
	// A late gateway re-observation replays the persisted outcome; the runtime
	// is not asked again and nothing double-executes.
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/v1/resume", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume replay = %d %s", rec.Code, rec.Body.String())
	}
	var replayed hubruntime.ExecuteResponse
	if err := json.NewDecoder(rec.Body).Decode(&replayed); err != nil || replayed.Status != "completed" {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	if observed.Load() != 1 {
		t.Fatalf("runtime re-observed %d times after terminal replay", observed.Load())
	}
}

func TestUnconfirmedPastBudgetFailsWithCause(t *testing.T) {
	now := time.Now().UTC()
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "job-budget", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-budget", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	if err = m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	var contacted atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	record := m.jobs[request.JobID]
	record.UnconfirmedSince = now.Add(-hubruntime.RunBudget())
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.reconcileUnconfirmed(context.Background(), now)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		status := m.jobs[request.JobID].Status
		m.mu.Unlock()
		if status == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	finished := m.jobs[request.JobID]
	if finished.Status != "failed" || finished.Response.LastEvent != "run.timeout" {
		t.Fatalf("record after budget = %+v", finished)
	}
	if finished.Unconfirmed {
		t.Fatal("timed-out job still carries the unconfirmed flag")
	}
	if contacted.Load() != 0 {
		t.Fatal("past-budget job re-contacted the runtime instead of failing with cause")
	}
}

func TestSweepReconcilesUnconfirmed(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "job-sweep", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-sweep", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	if err = m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/observe" {
			w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
			event := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: runtime.Generation, Status: "completed", Text: "answer", LastEvent: "run.completed"}
			_ = json.NewEncoder(w).Encode(event)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	record := m.jobs[request.JobID]
	record.UnconfirmedSince = time.Now().Add(-time.Minute)
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.sweep(context.Background(), time.Now())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		status := m.jobs[request.JobID].Status
		m.mu.Unlock()
		if status == "completed" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t.Fatalf("sweep left record at %+v", m.jobs[request.JobID])
}

func TestExpireUncertainDefersUnconfirmedWithinBudget(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "job-defer", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-defer", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	uncertain := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "uncertain", LastEvent: "run.unknown"}
	if err = m.finishJobGeneration(request, uncertain, "uncertain", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	// Leaseless and old enough to expire, but still inside the reconciliation
	// window: the reconciler owns this record until the budget lapses.
	old := time.Now().Add(-time.Hour)
	m.mu.Lock()
	record := m.jobs[request.JobID]
	record.UpdatedAt = old.UTC()
	record.UnconfirmedSince = time.Now().Add(-time.Minute)
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.ExpireUncertain(context.Background(), time.Now())
	m.mu.Lock()
	defer m.mu.Unlock()
	if got := m.jobs[request.JobID]; got.Status != "uncertain" || !got.Unconfirmed {
		t.Fatalf("unconfirmed record interrupted inside reconcile window: %+v", got)
	}
}

// Rejected transitions log a JSON record with the job key — `hubctl trace`
// joins on the job_id field (SPEC-0053 R3.5).
func TestRejectedTransitionLogsJSON(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)
	record := &jobRecord{Status: "completed", Phase: "completed"}
	transitionJobPhase("job-9", record, "running")
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line); err != nil {
		t.Fatalf("rejection log not JSON: %q", buf.String())
	}
	if line["job_id"] != "job-9" || line["component"] != "supervisor" {
		t.Fatalf("missing trace fields: %q", buf.String())
	}
}
