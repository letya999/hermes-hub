package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestResumeObservesOriginalRunAndRejectsOtherConversation(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "resume-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "resume", Text: "private prompt"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.bindJobGeneration(request, runtime.Generation)
	m.finishJobGeneration(request, hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session-1", RunID: "original-run", RuntimeGeneration: runtime.Generation, Status: "uncertain"}, "uncertain", runtime.Generation)
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/observe" {
			t.Fatalf("resubmitted instead of observing: %s", r.URL.Path)
		}
		var ref hubruntime.RunReference
		if json.NewDecoder(r.Body).Decode(&ref) != nil {
			t.Fatal("bad reference")
		}
		if ref.Text != "" || ref.RunID != "original-run" {
			t.Fatalf("reference leaked prompt or changed run: %+v", ref)
		}
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		event := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: ref.SessionID, RunID: ref.RunID, RuntimeGeneration: ref.RuntimeGeneration, Status: "running", LastEvent: "run.admitted", EventID: "admitted"}
		_ = json.NewEncoder(w).Encode(event)
		w.(http.Flusher).Flush()
		// A stream must outlive the former two-second readiness client timeout.
		time.Sleep(2200 * time.Millisecond)
		if err := m.Reap(context.Background(), time.Now().Add(10*time.Minute)); err != nil {
			t.Error(err)
		}
		if held, _, err := m.Status(b); err != nil || held.State != Busy || held.Leases == 0 || held.Generation != runtime.Generation {
			t.Errorf("active stream lost its runtime: %+v %v", held, err)
		}
		event.Status, event.LastEvent, event.EventID, event.Text = "completed", "run.completed", "terminal:completed", "answer"
		_ = json.NewEncoder(w).Encode(event)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	m.mu.Unlock()
	call := func(reqData hubruntime.ExecuteRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(reqData)
		req := httptest.NewRequest(http.MethodPost, "/v1/resume", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, req)
		return rec
	}
	other := request
	observationLock := m.lockFor("observation:" + executeJobKey(request))
	observationLock.Lock()
	if rec := call(request); rec.Code != http.StatusTooEarly || calls != 0 {
		t.Fatalf("parallel observation=%d calls=%d", rec.Code, calls)
	}
	observationLock.Unlock()
	other.ConversationID = "another"
	if rec := call(other); rec.Code != http.StatusConflict || calls != 0 {
		t.Fatalf("other conversation reached runtime: %d calls=%d", rec.Code, calls)
	}
	rec := call(request)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "answer") || calls != 1 {
		t.Fatalf("resume=%d %s calls=%d", rec.Code, rec.Body.String(), calls)
	}
	// A replay uses the saved terminal receipt and cannot start another observation.
	rec = call(request)
	if rec.Code != http.StatusOK || calls != 1 {
		t.Fatalf("terminal replay=%d calls=%d", rec.Code, calls)
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	m.mu.Unlock()
	if record.Status != "completed" || record.Request.Text != "" {
		t.Fatalf("record=%+v", record)
	}
}

func TestJobLeaseOwnershipRollsBackWhenPersistenceFails(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	lease, _, err := m.Acquire(context.Background(), b, LeaseUncertain)
	if err != nil {
		t.Fatal(err)
	}
	m.statePath = root
	if err = m.attachJobLease(lease.ID, "job-one"); err == nil {
		t.Fatal("ownership persistence failure ignored")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	actual := m.items[runtimeKey(b)].leases[lease.ID]
	if actual.Owner != lease.Owner || actual.Kind != LeaseUncertain || m.items[runtimeKey(b)].Leases != 1 {
		t.Fatalf("ownership or hold changed: %+v", actual)
	}
}

func TestObservationGenerationRebindIsDurableAndFenced(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	runtime, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "rebind", IdempotencyKey: "rebind", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.bindJobGeneration(request, "original-generation")
	m.mu.Lock()
	m.items[runtimeKey(binding(root))].Generation = "original-generation"
	m.mu.Unlock()
	response := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: "original-generation", Status: "uncertain"}
	if err = m.finishJobGeneration(request, response, response.Status, "original-generation"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	expected := m.jobs[request.JobID]
	m.items[runtimeKey(binding(root))].Generation = runtime.Generation
	m.mu.Unlock()
	path := m.statePath
	m.statePath = root
	if _, err = m.rebindJobObservation(request, expected, runtime); err == nil {
		t.Fatal("failed durable rebind accepted")
	}
	m.mu.Lock()
	actual := m.jobs[request.JobID]
	m.mu.Unlock()
	if actual.Generation != "original-generation" {
		t.Fatal("failed rebind changed memory")
	}
	m.statePath = path
	bad := expected
	bad.Response.RunID = "another"
	if _, err = m.rebindJobObservation(request, bad, runtime); err == nil {
		t.Fatal("changed run accepted")
	}
	actual, err = m.rebindJobObservation(request, expected, runtime)
	if err != nil || actual.Generation != runtime.Generation || actual.Response.RunID != "original" {
		t.Fatalf("rebind=%+v %v", actual, err)
	}
	restored, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored.mu.Lock()
	saved := restored.jobs[request.JobID]
	restored.mu.Unlock()
	if saved.Generation != runtime.Generation {
		t.Fatal("generation not durable")
	}
}

func TestRelayRejectsOldGenerationAndKeepsUncertain(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	runtime, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{JobID: "relay-job", IdempotencyKey: "relay"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.bindJobGeneration(request, runtime.Generation)
	event := hubruntime.ExecuteResponse{JobID: request.JobID, RuntimeGeneration: "stale", RunID: "r", SessionID: "s", Status: "completed", Text: "stale answer"}
	body, _ := json.Marshal(event)
	rec := httptest.NewRecorder()
	m.relayRunStream(rec, request, runtime, strings.NewReader(string(body)+"\n"))
	m.mu.Lock()
	record := m.jobs[request.JobID]
	m.mu.Unlock()
	if record.Status != "uncertain" || strings.Contains(rec.Body.String(), "stale answer") {
		t.Fatalf("record=%+v body=%s", record, rec.Body.String())
	}
}

func TestResumeRejectsMalformedAndUnknownAdmissionBeforeStart(t *testing.T) {
	starts := 0
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { starts++; return nil, nil }, nil)
	for _, tc := range []struct {
		method, body string
		status       int
	}{{http.MethodGet, "", http.StatusMethodNotAllowed}, {http.MethodPost, "invalid", http.StatusBadRequest}, {http.MethodPost, "{}", http.StatusConflict}} {
		req := httptest.NewRequest(tc.method, "/v1/resume", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.status || starts != 0 {
			t.Fatalf("status=%d want=%d starts=%d", rec.Code, tc.status, starts)
		}
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "unknown-admission", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "unknown"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/v1/resume", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || starts != 0 {
		t.Fatalf("unknown admission restarted: %d starts=%d", rec.Code, starts)
	}
	if err := m.attachJobLease("missing", "unknown"); err == nil {
		t.Fatal("attached missing lease")
	}
}

func TestTerminalRelayRequiresDurableSupervisorWrite(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	runtime, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{JobID: "durable-relay", IdempotencyKey: "durable"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.bindJobGeneration(request, runtime.Generation)
	event := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: runtime.Generation, Status: "completed", LastEvent: "run.completed", Text: "answer"}
	body, _ := json.Marshal(event)
	path := m.statePath
	m.statePath = filepath.Join(t.TempDir(), "missing", "state.json")
	rec := httptest.NewRecorder()
	m.relayRunStream(rec, request, runtime, strings.NewReader(string(body)+"\n"))
	m.mu.Lock()
	record := m.jobs[request.JobID]
	m.mu.Unlock()
	if record.Status == "completed" || strings.Contains(rec.Body.String(), "answer") {
		t.Fatalf("non-durable result published: %+v %s", record, rec.Body.String())
	}
	m.statePath = path
	rec = httptest.NewRecorder()
	m.relayRunStream(rec, request, runtime, strings.NewReader(string(body)+"\n"))
	if !strings.Contains(rec.Body.String(), "answer") {
		t.Fatalf("durable result not published: %s", rec.Body.String())
	}
}

func TestDurableTerminalIdentityAndTextCannotBeReplaced(t *testing.T) {
	m, _ := testManager(t, nil, nil)
	request := hubruntime.ExecuteRequest{JobID: "immutable-final", IdempotencyKey: "immutable-final"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(request, "gen-1"); err != nil {
		t.Fatal(err)
	}
	final := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: "gen-1", Status: "completed", Text: "first answer"}
	if err := m.finishJobGeneration(request, final, "completed", "gen-1"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"job", "run", "session", "generation", "text"} {
		changed := final
		switch field {
		case "job":
			changed.JobID = "another"
		case "run":
			changed.RunID = "another"
		case "session":
			changed.SessionID = "another"
		case "generation":
			changed.RuntimeGeneration = "another"
		case "text":
			changed.Text = "another"
		}
		if err := m.finishJobGeneration(request, changed, "completed", "gen-1"); err == nil {
			t.Fatalf("replaced durable final %s", field)
		}
	}
	if err := m.finishJobGeneration(request, hubruntime.ExecuteResponse{}, "completed", "gen-1"); err != nil {
		t.Fatal(err)
	}
	restored, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.jobs[request.JobID].Response; !reflect.DeepEqual(got, final) {
		t.Fatalf("authoritative final changed: %+v", got)
	}
}

func TestReadinessBodyHasIndependentDeadlineAndSizeLimit(t *testing.T) {
	for _, mode := range []string{"stalled", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := testManager(t, nil, nil)
			cancelled := make(chan time.Duration, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "oversized" {
					_, _ = w.Write([]byte(strings.Repeat("x", 64*1024+2)))
					return
				}
				start := time.Now()
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				select {
				case cancelled <- time.Since(start):
				default:
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() { finished <- m.ready(ctx, server.URL, "test-auth") }()
			if mode == "stalled" {
				select {
				case duration := <-cancelled:
					if duration > 3*time.Second {
						t.Errorf("readiness inherited long stream deadline: %s", duration)
					}
				case <-time.After(5 * time.Second):
					t.Error("readiness body exceeded probe deadline")
				}
			} else {
				select {
				case err := <-finished:
					if err == nil {
						t.Fatal("oversized readiness body accepted")
					}
					return
				case <-time.After(200 * time.Millisecond):
				}
			}
			cancel()
			select {
			case err := <-finished:
				if err == nil {
					t.Error("incomplete/oversized readiness body accepted")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("readiness did not return after cancellation")
			}
		})
	}
}

func TestLateJSONFinalCannotCrossRegisteredRuntimeGeneration(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "late-json-final", IdempotencyKey: "late-json-final"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	event := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: runtime.Generation, Status: "running"}
	if err := m.finishJobGeneration(request, event, "running", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(b)].Generation = "replacement"
	m.mu.Unlock()
	event.Status, event.Text = "completed", "obsolete JSON final"
	if err := m.finishJobGeneration(request, event, "completed", runtime.Generation); err == nil {
		t.Fatal("obsolete producer persisted a terminal result after replacement")
	}
	restored, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.jobs[request.JobID].Response; got.Text != "" || got.RunID != "original" {
		t.Fatalf("obsolete JSON result became durable: %+v", got)
	}
}

func TestResumeTransportFailuresRetainOriginalRunAndHold(t *testing.T) {
	for _, mode := range []string{"status", "content-type", "transport", "binding"} {
		t.Run(mode, func(t *testing.T) {
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				return []byte("running"), nil
			}, func(context.Context, string, string) error { return nil })
			b := binding(root)
			lease, runtime, err := m.Acquire(context.Background(), b, LeaseStream)
			if err != nil {
				t.Fatal(err)
			}
			request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "resume-failure", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "resume-failure", Text: "original input"}
			if mode == "binding" {
				request.ScopeID = "invalid-scope"
			}
			if _, _, err := m.beginJob(request); err != nil {
				t.Fatal(err)
			}
			if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
				t.Fatal(err)
			}
			if err := m.attachJobLease(lease.ID, executeJobKey(request)); err != nil {
				t.Fatal(err)
			}
			known := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: runtime.Generation, Status: "uncertain"}
			if err := m.finishJobGeneration(request, known, "uncertain", runtime.Generation); err != nil {
				t.Fatal(err)
			}
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/observe" {
					t.Error("original execution replayed")
				}
				if mode == "transport" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				if mode == "status" {
					w.WriteHeader(503)
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer api.Close()
			m.mu.Lock()
			m.items[runtimeKey(b)].Address = api.URL
			m.mu.Unlock()
			body, _ := json.Marshal(request)
			req := httptest.NewRequest(http.MethodPost, "/v1/resume", strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer secret")
			recorder := httptest.NewRecorder()
			m.Handler().ServeHTTP(recorder, req)
			if recorder.Code/100 == 2 || (mode == "binding" && calls != 0) {
				t.Fatalf("unsafe recovery accepted: %d calls=%d", recorder.Code, calls)
			}
			state, _, err := m.Status(b)
			if err != nil || state.Leases == 0 || m.jobs[request.JobID].Response.RunID != "original" || terminalRunStatus(m.jobs[request.JobID].Status) {
				t.Fatalf("failure lost original observation or freed runtime: %+v %v", state, err)
			}
		})
	}
}
