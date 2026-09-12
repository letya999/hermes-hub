package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
)

func TestControlPersistsBeforeDispatchAndDeduplicatesDecision(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "control-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "control", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.bindJobGeneration(request, runtime.Generation)
	outcome := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session", RunID: "original", RuntimeGeneration: runtime.Generation, Status: "waiting_for_approval", ApprovalID: "approval-1", ApprovalChoices: []string{"once", "deny"}}
	if err = m.finishJobGeneration(request, outcome, outcome.Status, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var control hubruntime.RunControl
		_ = json.NewDecoder(r.Body).Decode(&control)
		m.mu.Lock()
		saved := m.jobs[request.JobID].Controls["approve:approval-1"]
		m.mu.Unlock()
		if control.Action == "approve" && saved.State != "uncertain" {
			t.Errorf("dispatch before durable intent: %+v", saved)
		}
		outcome.Status = "running"
		if control.Action == "cancel" {
			outcome.Status = "cancelled"
		}
		response := outcome
		response.ApprovalID, response.ApprovalChoices = "", nil
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	m.mu.Unlock()
	control := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: request, RunID: "original", SessionID: "session", RuntimeGeneration: runtime.Generation}, Action: "approve", RequestID: "approval-1", Choice: "once"}
	call := func(c hubruntime.RunControl) int {
		body, _ := json.Marshal(c)
		req := httptest.NewRequest(http.MethodPost, "/v1/control", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	bad := control
	bad.ConversationID = "other"
	if code := call(bad); code != 409 || calls != 0 {
		t.Fatalf("ownership %d calls %d", code, calls)
	}
	for _, action := range []string{"approve", "cancel"} {
		for name, change := range map[string]func(*hubruntime.RunControl){
			"principal":    func(c *hubruntime.RunControl) { c.PrincipalID = "bob" },
			"context":      func(c *hubruntime.RunControl) { c.ContextID = "bob" },
			"conversation": func(c *hubruntime.RunControl) { c.ConversationID = "other" },
			"delivery":     func(c *hubruntime.RunControl) { c.DeliveryTargetID = "other" },
			"external":     func(c *hubruntime.RunControl) { c.ExternalIdentityID = "other" },
			"schema":       func(c *hubruntime.RunControl) { c.Schema++ },
			"runtime":      func(c *hubruntime.RunControl) { c.RuntimeID = "bob" },
			"generation":   func(c *hubruntime.RunControl) { c.RuntimeGeneration = "obsolete" },
			"run":          func(c *hubruntime.RunControl) { c.RunID = "other" },
			"session":      func(c *hubruntime.RunControl) { c.SessionID = "other" },
			"actor":        func(c *hubruntime.RunControl) { c.ActorID = "bob" },
			"org":          func(c *hubruntime.RunControl) { c.OrganizationID = "other" },
			"scope":        func(c *hubruntime.RunControl) { c.ScopeID = "user:bob" },
			"policy":       func(c *hubruntime.RunControl) { c.PolicyVersion = "other" },
			"idempotency":  func(c *hubruntime.RunControl) { c.IdempotencyKey = "other" },
		} {
			bad := control
			bad.Action = action
			change(&bad)
			if code := call(bad); code != 409 || calls != 0 {
				t.Fatalf("%s/%s accepted: code=%d calls=%d", action, name, code, calls)
			}
		}
	}
	if code := call(control); code != 200 {
		t.Fatal(code)
	}
	if code := call(control); code != 200 || calls != 1 {
		t.Fatalf("duplicate %d calls %d", code, calls)
	}
	bad = control
	bad.Choice = "deny"
	if code := call(bad); code != 409 || calls != 1 {
		t.Fatalf("conflict %d calls %d", code, calls)
	}
	m.mu.Lock()
	for _, lease := range m.items[runtimeKey(b)].leases {
		if lease.Kind == LeaseApproval {
			t.Error("applied approval still held")
		}
	}
	m.mu.Unlock()
	outcome.Status = "waiting_for_approval"
	outcome.ApprovalID = "approval-2"
	if err = m.finishJobGeneration(request, outcome, outcome.Status, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for id, lease := range m.items[runtimeKey(b)].leases {
		if lease.Kind == LeaseApproval && id != "approval:"+request.JobID+":approval-2" {
			t.Error("obsolete approval still holds runtime")
		}
	}
	m.mu.Unlock()
	m.mu.Lock()
	record := m.jobs[request.JobID]
	record.ApprovalDeadline = time.Now().Add(-time.Minute)
	m.jobs[request.JobID] = record
	held := false
	for _, lease := range m.items[runtimeKey(b)].leases {
		if lease.Kind == LeaseApproval {
			held = true
		}
	}
	m.mu.Unlock()
	if !held {
		t.Fatal("pending approval has no lease")
	}
	m.ExpireApprovals(context.Background(), time.Now())
	m.ExpireApprovals(context.Background(), time.Now())
	if calls != 2 {
		t.Fatalf("expiry not idempotent: %d", calls)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs[request.JobID].Status != "cancelled" {
		t.Fatal("expiry did not confirm cancellation")
	}
	for _, lease := range m.items[runtimeKey(b)].leases {
		if lease.Kind == LeaseApproval {
			t.Fatal("terminal expiry retained approval")
		}
	}
}

func TestControlPreservesConcurrentTerminalResult(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "race-control", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "race", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.bindJobGeneration(request, runtime.Generation)
	response := hubruntime.ExecuteResponse{JobID: request.JobID, SessionID: "session", RunID: "original", RuntimeGeneration: runtime.Generation, Status: "waiting_for_approval", ApprovalID: "one", ApprovalChoices: []string{"once", "deny"}}
	if err = m.finishJobGeneration(request, response, response.Status, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		completed := response
		completed.Status, completed.Text = "completed", "saved answer"
		completed.ApprovalID, completed.ApprovalChoices = "", nil
		if err := m.finishJobGeneration(request, completed, completed.Status, runtime.Generation); err != nil {
			t.Error(err)
		}
		completed.Status, completed.Text = "running", ""
		_ = json.NewEncoder(w).Encode(completed)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	m.mu.Unlock()
	control := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: request, RunID: response.RunID, SessionID: response.SessionID, RuntimeGeneration: runtime.Generation}, Action: "approve", RequestID: "one", Choice: "once"}
	body, _ := json.Marshal(control)
	req := httptest.NewRequest(http.MethodPost, "/v1/control", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "saved answer") {
		t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.jobs[request.JobID]
	if record.Status != "completed" || record.Response.Text != "saved answer" || record.Controls["approve:one"].Response.Status != "completed" {
		t.Fatalf("terminal result overwritten: %+v", record)
	}
}

func TestUncertainDecisionClosesWithoutClaimingApprovalApplied(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	request := hubruntime.ExecuteRequest{JobID: "uncertain-control", IdempotencyKey: "uncertain-control", Text: "private"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.bindJobGeneration(request, "original-generation")
	m.mu.Lock()
	record := m.jobs[request.JobID]
	record.Controls = map[string]controlRecord{"approve:one": {Choice: "once", State: "uncertain"}}
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	response := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: "original-generation", Status: "interrupted"}
	if err := m.finishJobGeneration(request, response, response.Status, response.RuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	restored, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored.mu.Lock()
	defer restored.mu.Unlock()
	control := restored.jobs[request.JobID].Controls["approve:one"]
	if control.State != "closed" || control.Response.Status != "interrupted" || control.Choice != "once" {
		t.Fatalf("uncertainty falsely resolved as applied: %+v", control)
	}
}

func TestPreAdmissionCancellationIsDurableAndDoesNotInventNativeCompletion(t *testing.T) {
	for _, dispatching := range []bool{false, true} {
		t.Run(fmt.Sprint(dispatching), func(t *testing.T) {
			m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, os.ErrNotExist }, func(context.Context, string, string) error { return nil })
			request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "starting-cancel", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "starting-cancel", Text: "private"}
			_ = binding(root)
			if _, _, err := m.beginJob(request); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			record := m.jobs[request.JobID]
			record.Dispatching = dispatching
			m.jobs[request.JobID] = record
			m.mu.Unlock()
			body, _ := json.Marshal(hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: request}, Action: "cancel"})
			for i := 0; i < 2; i++ {
				response := httptest.NewRecorder()
				m.control(response, httptest.NewRequest("POST", "/v1/control", strings.NewReader(string(body))))
				if response.Code != 202 {
					t.Fatalf("cancel: %d %s", response.Code, response.Body.String())
				}
			}
			m.mu.Lock()
			record = m.jobs[request.JobID]
			m.mu.Unlock()
			if !record.CancelRequested || (!dispatching && record.Status != "cancelled") || (dispatching && terminalRunStatus(record.Status)) {
				t.Fatalf("invalid cancellation: %+v", record)
			}
			if !dispatching {
				result, replay, err := m.beginJob(request)
				if err != nil || !replay || result.Status != "cancelled" {
					t.Fatalf("cancelled job re-admitted: %+v %v %v", result, replay, err)
				}
			}
		})
	}
}

func TestCancellationBeforeWarmRuntimeAdmissionDoesNotExecute(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	warm, err := m.Ensure(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseBinding(b); err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "cancel-warm-ready", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "cancel-warm-ready", Text: "never dispatch"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: request}, Action: "cancel"})
	for range 2 {
		response := httptest.NewRecorder()
		m.control(response, httptest.NewRequest("POST", "/v1/control", bytes.NewReader(body)))
		if response.Code != 202 {
			t.Fatal(response.Code)
		}
	}
	body, _ = json.Marshal(request)
	response := httptest.NewRecorder()
	m.execute(response, httptest.NewRequest("POST", "/v1/execute", bytes.NewReader(body)))
	var outcome hubruntime.ExecuteResponse
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &outcome) != nil || outcome.Status != "cancelled" || outcome.RunID != "" {
		t.Fatalf("warm admission escaped cancellation: %s", response.Body.String())
	}
	current, _, err := m.Status(b)
	if err != nil || current.Generation != warm.Generation || current.Leases != 0 {
		t.Fatalf("warm cancellation changed runtime: %+v %v", current, err)
	}
}

func TestCancellationWhileRuntimeStartsPreventsNativeDispatchAndReleasesHold(t *testing.T) {
	started, ready := make(chan struct{}), make(chan struct{})
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(ctx context.Context, _, _ string) error {
		close(started)
		select {
		case <-ready:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "cancel-during-start", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "cancel-during-start", Text: "private"}
	body, _ := json.Marshal(request)
	result := httptest.NewRecorder()
	finished := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go func() {
		defer close(finished)
		m.execute(result, httptest.NewRequest("POST", "/v1/execute", strings.NewReader(string(body))).WithContext(ctx))
	}()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("runtime did not start")
	}
	controlBody, _ := json.Marshal(hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: request}, Action: "cancel"})
	response := httptest.NewRecorder()
	m.control(response, httptest.NewRequest("POST", "/v1/control", strings.NewReader(string(controlBody))))
	close(ready)
	defer func() { cancel(); <-finished }()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("execution did not finish")
	}
	var outcome hubruntime.ExecuteResponse
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &outcome) != nil || outcome.Status != "cancelled" || outcome.RunID != "" || outcome.RuntimeGeneration == "" {
		t.Fatalf("native work not prevented: %d %s", result.Code, result.Body.String())
	}
	runtime, ok, err := m.Status(binding(root))
	if err != nil || !ok || runtime.Leases != 0 {
		t.Fatalf("cancelled startup retained hold: %+v %v", runtime, err)
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	m.mu.Unlock()
	if record.Dispatching {
		t.Fatal("cancelled startup crossed dispatch fence")
	}
}

func TestAdmissionAndGenerationWriteFailuresRollBack(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, os.ErrNotExist }, func(context.Context, string, string) error { return nil })
	request := hubruntime.ExecuteRequest{JobID: "write-failure", IdempotencyKey: "write-failure"}
	original := m.statePath
	m.statePath = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, _, err := m.beginJob(request); err == nil {
		t.Fatal("non-durable admission accepted")
	}
	if _, ok := m.jobs[request.JobID]; ok {
		t.Fatal("failed admission left in-memory job")
	}
	m.statePath = original
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.statePath = filepath.Join(t.TempDir(), "missing", "state.json")
	if err := m.bindJobGeneration(request, "gen-new"); err == nil {
		t.Fatal("non-durable generation accepted")
	}
	if m.jobs[request.JobID].Generation != "" {
		t.Fatal("failed generation changed memory")
	}
}

func TestUncertainApprovalReconcilesReadOnlyWithoutClaimingApplied(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "uncertain-approval", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "uncertain-approval", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	outcome := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: runtime.Generation, Status: "waiting_for_approval", ApprovalID: "one", ApprovalChoices: []string{"once", "deny"}}
	if err = m.finishJobGeneration(request, outcome, outcome.Status, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	mutations, reads := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c hubruntime.RunControl
		_ = json.NewDecoder(r.Body).Decode(&c)
		if !c.Reconcile {
			mutations++
			w.WriteHeader(502)
			return
		}
		reads++
		outcome.Status = "running"
		outcome.ApprovalID = ""
		outcome.ApprovalChoices = nil
		_ = json.NewEncoder(w).Encode(outcome)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	m.mu.Unlock()
	control := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: request, RunID: outcome.RunID, SessionID: outcome.SessionID, RuntimeGeneration: runtime.Generation}, Action: "approve", RequestID: "one", Choice: "once"}
	call := func() int {
		body, _ := json.Marshal(control)
		rec := httptest.NewRecorder()
		m.control(rec, httptest.NewRequest("POST", "/v1/control", strings.NewReader(string(body))))
		return rec.Code
	}
	if code := call(); code != 502 {
		t.Fatalf("lost response: %d", code)
	}
	if code := call(); code != 409 || mutations != 1 {
		t.Fatal("unknown approval repeated")
	}
	control.Reconcile = true
	if code := call(); code != 200 || reads != 1 || mutations != 1 {
		t.Fatalf("reconcile: %d reads=%d mutations=%d", code, reads, mutations)
	}
	if code := call(); code != 200 || reads != 1 {
		t.Fatal("closed decision not idempotent")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs[request.JobID].Controls["approve:one"].State != "closed" {
		t.Fatal("inactive approval falsely applied")
	}
	for _, lease := range m.items[runtimeKey(b)].leases {
		if lease.Kind == LeaseApproval {
			t.Fatal("inactive request retained approval hold")
		}
	}
}

func TestUncertainCancellationReadsOriginalRunBeforeRetryingStop(t *testing.T) {
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "uncertain-cancel", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "uncertain-cancel", Text: "private"}
	if _, _, err = m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err = m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	outcome := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: runtime.Generation, Status: "waiting_for_approval", ApprovalID: "one", ApprovalChoices: []string{"once", "deny"}}
	if err = m.finishJobGeneration(request, outcome, outcome.Status, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	mutations, reads := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c hubruntime.RunControl
		_ = json.NewDecoder(r.Body).Decode(&c)
		if !c.Reconcile {
			mutations++
			if mutations == 1 {
				w.WriteHeader(502)
				return
			}
			if reads != 1 {
				t.Error("stop repeated without original state observation")
			}
			outcome.Status = "cancelled"
			outcome.ApprovalID = ""
			outcome.ApprovalChoices = nil
			_ = json.NewEncoder(w).Encode(outcome)
			return
		}
		reads++
		outcome.Status = "running"
		outcome.ApprovalID = ""
		outcome.ApprovalChoices = nil
		_ = json.NewEncoder(w).Encode(outcome)
	}))
	defer api.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = api.URL
	m.mu.Unlock()
	control := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: request, RunID: outcome.RunID, SessionID: outcome.SessionID, RuntimeGeneration: runtime.Generation}, Action: "cancel"}
	call := func() int {
		body, _ := json.Marshal(control)
		rec := httptest.NewRecorder()
		m.control(rec, httptest.NewRequest("POST", "/v1/control", strings.NewReader(string(body))))
		return rec.Code
	}
	if code := call(); code != 502 {
		t.Fatalf("lost response: %d", code)
	}
	if code := call(); code != 409 || mutations != 1 {
		t.Fatal("unknown stop blindly repeated")
	}
	control.Reconcile = true
	if code := call(); code != 200 || reads != 1 || mutations != 2 {
		t.Fatalf("reconcile: %d reads=%d mutations=%d", code, reads, mutations)
	}
	if code := call(); code != 200 || reads != 1 {
		t.Fatal("closed decision not idempotent")
	}
	// A lost expiry response is restored as uncertainty, then resolved through
	// an authoritative read of the same run before another idempotent stop.
	request.JobID, request.IdempotencyKey = "lost-expiry", "lost-expiry"
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	outcome.JobID, outcome.Status, outcome.ApprovalID = request.JobID, "waiting_for_approval", "expiry-request"
	outcome.ApprovalChoices = []string{"once", "deny"}
	if err := m.finishJobGeneration(request, outcome, outcome.Status, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	expiring := m.jobs[request.JobID]
	expiring.ApprovalDeadline = time.Now().Add(-time.Second)
	m.jobs[request.JobID] = expiring
	err = m.persistLocked()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	mutations, reads = 0, 0
	m.ExpireApprovals(context.Background(), time.Now())
	if mutations != 1 || reads != 0 {
		t.Fatal("expiry did not preserve lost stop outcome")
	}
	m, err = New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.ExpireApprovals(context.Background(), time.Now())
	m.ExpireApprovals(context.Background(), time.Now())
	if mutations != 2 || reads != 1 {
		t.Fatalf("expiry restart replay: mutations=%d reads=%d", mutations, reads)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs[request.JobID].Controls["cancel:"].State != "closed" {
		t.Fatal("uncertain stop not closed")
	}
	for _, lease := range m.items[runtimeKey(b)].leases {
		if lease.Kind == LeaseApproval {
			t.Fatal("confirmed cancellation retained approval hold")
		}
	}
}

func TestSupervisorBindingRejectsMalformedIdentityBeforeRuntimeAccess(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
		t.Fatal("malformed identity reached Docker")
		return nil, nil
	}, func(context.Context, string, string) error { t.Fatal("malformed identity reached runtime"); return nil })
	for _, schema := range []bool{false, true} {
		request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice"}
		if schema {
			request.Schema++
		} else {
			request.ExternalIdentityID = ""
		}
		if _, err := m.bindingFor(request); err == nil {
			t.Fatal("malformed identity accepted")
		}
	}
}

func controlTestPolicy(t *testing.T, root string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte("schema: 1\nuser: alice\ntimezone: UTC\nbrowser_port: 9222\nfeatures: [workspace]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := stack.ReadEnvironment(root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	return stack.PolicyVersion(settings)
}

func TestApprovalRejectsChangedUnavailableAndExpandedPolicy(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
		t.Fatal("authorization reached Docker")
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	policy := controlTestPolicy(t, root)
	control := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", policy), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal"}}, Action: "approve"}
	if err := m.authorizeApproval(control); err != nil {
		t.Fatal(err)
	}
	forged := control
	forged.ActorID = "another"
	if err := m.authorizeApproval(forged); err == nil {
		t.Fatal("another actor authorized")
	}
	forged = control
	forged.OrganizationID = "acme"
	if err := m.authorizeApproval(forged); err == nil {
		t.Fatal("caller expanded organization")
	}
	path := filepath.Join(root, "settings.yaml")
	if err := os.WriteFile(path, []byte("schema: 1\nuser: alice\ntimezone: UTC\nbrowser_port: 9222\nfeatures: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.authorizeApproval(control); err == nil {
		t.Fatal("changed policy accepted old approval")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := m.authorizeApproval(control); err == nil {
		t.Fatal("missing policy authorized")
	}
}

func TestApprovalRechecksOrganizationMembership(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, os.ErrNotExist }, func(context.Context, string, string) error { return nil })
	userPath := filepath.Join(root, "settings.yaml")
	if err := os.WriteFile(userPath, []byte("schema: 1\nuser: alice\norganization: acme\ntimezone: UTC\nbrowser_port: 9222\nfeatures: [workspace]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	orgRoot := filepath.Join(m.cfg.SpacesRoot, "acme")
	if err := os.MkdirAll(filepath.Join(orgRoot, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	orgPath := filepath.Join(orgRoot, "scope.yaml")
	org := []byte("schema: 1\nkind: organization\nid: acme\norganization: acme\nmembers: {alice: member}\nfeatures: [workspace]\n")
	if err := os.WriteFile(orgPath, org, 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := stack.Read(userPath)
	if err != nil {
		t.Fatal(err)
	}
	organization, err := stack.ReadOrganization(orgRoot)
	if err != nil {
		t.Fatal(err)
	}
	settings, err = stack.ApplyOrganization(organization, settings, orgRoot)
	if err != nil {
		t.Fatal(err)
	}
	control := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", stack.PolicyVersion(settings)), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "acme"}}, Action: "approve"}
	if err := m.authorizeApproval(control); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orgPath, []byte("schema: 1\nkind: organization\nid: acme\norganization: acme\nmembers: {}\nfeatures: [workspace]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.authorizeApproval(control); err == nil {
		t.Fatal("removed member authorized approval")
	}
}

func TestScheduledExecutionReauthorizesPolicyBeforeRuntimeStart(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
		t.Fatal("revoked routine reached Docker")
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	policy := controlTestPolicy(t, root)
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", policy), JobID: "revoked-routine", IdempotencyKey: "revoked-routine", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Channel: "telegram_bot", Trigger: "cron", Text: "scheduled work"}
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte("schema: 1\nuser: alice\ntimezone: UTC\nbrowser_port: 9222\nfeatures: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(request)
	response := httptest.NewRecorder()
	m.execute(response, httptest.NewRequest("POST", "/v1/execute", strings.NewReader(string(body))))
	if response.Code != 409 || len(m.jobs) != 0 {
		t.Fatalf("revoked routine admitted: %d %+v", response.Code, m.jobs)
	}
}
