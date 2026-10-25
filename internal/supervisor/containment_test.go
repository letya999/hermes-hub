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
)

func TestMasterHoldSurvivesRestart(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "synthetic-admin-authority")
	calls := 0
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { calls++; return nil, nil }, func(context.Context, string, string) error { return nil })
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/holds", strings.NewReader(`{"principal_id":"alice","context_id":"alice","reason":"manual"}`))
	request.Header.Set("Authorization", "Bearer synthetic-admin-authority")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, request)
	if w.Code != http.StatusOK {
		t.Fatalf("admin could not persist hold: %d %s", w.Code, w.Body.String())
	}
	var receipt struct {
		PrincipalID string    `json:"principal_id"`
		ContextID   string    `json:"context_id"`
		Reason      string    `json:"reason"`
		Active      bool      `json:"active"`
		Epoch       uint64    `json:"epoch"`
		UpdatedAt   time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || receipt.PrincipalID != "alice" || receipt.ContextID != "alice" || receipt.Reason != "manual" || !receipt.Active || receipt.Epoch != 1 || !receipt.UpdatedAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("hold has no safe versioned incident receipt: %+v %v", receipt, err)
	}
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Ensure(t.Context(), binding(root)); err == nil {
		t.Fatal("held user revived after restart")
	}
	if calls != 0 {
		t.Fatalf("held admission reached Docker boundary: %d calls", calls)
	}
	request = httptest.NewRequest(http.MethodDelete, "/v1/admin/holds", strings.NewReader(`{"principal_id":"alice","context_id":"alice","reason":"manual"}`))
	request.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	restarted.Handler().ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("ordinary service can remove master hold: %d", w.Code)
	}
	if _, err := restarted.Ensure(t.Context(), binding(root)); err == nil {
		t.Fatal("unauthorized resume changed hold")
	}
}

func TestMasterResumeDoesNotReviveFencedDemand(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "synthetic-admin")
	calls := 0
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { calls++; return nil, os.ErrPermission }, func(context.Context, string, string) error { return nil })
	job := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "old-job", IdempotencyKey: "old-key", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal"}
	if _, _, err := m.beginJob(job); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"POST", "DELETE"} {
		r := httptest.NewRequest(method, "/v1/admin/holds", strings.NewReader(`{"principal_id":"alice","context_id":"alice","reason":"manual","expected_epoch":1}`))
		r.Header.Set("Authorization", "Bearer synthetic-admin")
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("hold transition: %d", w.Code)
		}
	}
	record := m.jobs[job.JobID]
	record.Generation = "gen-old"
	record.UnconfirmedSince = time.Now().Add(-time.Minute)
	record.Response = hubruntime.ExecuteResponse{JobID: job.JobID, RunID: "run-old", SessionID: "session-old", RuntimeGeneration: "gen-old", Status: "uncertain"}
	m.jobs[job.JobID] = record
	entry := &runtimeEntry{Runtime: Runtime{PrincipalID: job.PrincipalID, ContextID: job.ContextID, RuntimeID: job.RuntimeID, RuntimeMode: "gateway", Generation: "gen-old", State: Stopped, RuntimeHealth: "missing", Ownership: "absent"}, binding: binding(root), leases: map[string]Lease{}}
	m.items[runtimeKey(binding(root))] = entry
	if m.runtimeDesiredLocked(entry) {
		t.Error("fenced uncertain job became desired after resume")
	}
	m.reconcileJob(t.Context(), record)
	m.RecoverMissingWork(t.Context())
	m.reconcileUnconfirmed(t.Context(), time.Now())
	observation := m.lockFor("observation:" + job.JobID)
	observation.Lock()
	observation.Unlock()
	if calls != 0 {
		t.Errorf("fenced recovery reached Docker: %d calls", calls)
	}
}

func TestMasterRejectsQueuedEpoch(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "synthetic-admin")
	calls := 0
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { calls++; return nil, nil }, func(context.Context, string, string) error { return nil })
	for _, method := range []string{"POST", "DELETE"} {
		r := httptest.NewRequest(method, "/v1/admin/holds", strings.NewReader(`{"principal_id":"alice","context_id":"alice","reason":"manual","expected_epoch":1}`))
		r.Header.Set("Authorization", "Bearer synthetic-admin")
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("hold transition: %d", w.Code)
		}
	}
	for _, epoch := range []uint64{0, 2} {
		raw, _ := json.Marshal(struct {
			hubruntime.ExecuteRequest
			Epoch uint64 `json:"operational_epoch"`
		}{hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: fmt.Sprintf("queued-%d", epoch), IdempotencyKey: fmt.Sprintf("queued-%d", epoch), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal"}, epoch})
		var job hubruntime.ExecuteRequest
		if err := json.Unmarshal(raw, &job); err != nil {
			t.Fatal(err)
		}
		_, _, err := m.beginJob(job)
		if epoch == 0 && err == nil {
			t.Error("queued old job admitted after resume")
		}
		if epoch == 2 && err != nil {
			t.Errorf("fresh epoch denied: %v", err)
		}
		if epoch == 0 {
			r := httptest.NewRequest("POST", "/v1/wake", bytes.NewReader(raw))
			r.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			m.Handler().ServeHTTP(w, r)
			if w.Code != 409 {
				t.Errorf("stale wake status %d, want 409", w.Code)
			}
		}
	}
	if calls != 0 {
		t.Errorf("stale wake reached Docker: %d calls", calls)
	}
}

func TestMasterHoldRejectsInvalidAuthorityAndInput(t *testing.T) {
	valid := `{"principal_id":"alice","context_id":"alice","reason":"manual"}`
	for _, tc := range []struct {
		name, admin, token, method, body string
		status                           int
	}{
		{"ordinary-service", "synthetic-admin", "secret", "POST", valid, 403},
		{"missing-bearer", "synthetic-admin", "synthetic-admin", "POST", valid, 403},
		{"empty-admin", "", "secret", "POST", valid, 403},
		{"shared-credential", "secret", "secret", "POST", valid, 403},
		{"wrong-method", "synthetic-admin", "synthetic-admin", "GET", valid, 405},
		{"malformed", "synthetic-admin", "synthetic-admin", "POST", "{", 400},
		{"oversized", "synthetic-admin", "synthetic-admin", "POST", `{"principal_id":"` + strings.Repeat("x", 4096) + `"}`, 400},
		{"invalid-principal", "synthetic-admin", "synthetic-admin", "POST", `{"principal_id":"../alice","context_id":"alice","reason":"manual"}`, 400},
		{"invalid-context", "synthetic-admin", "synthetic-admin", "POST", `{"principal_id":"alice","context_id":"../alice","reason":"manual"}`, 400},
		{"unknown-reason", "synthetic-admin", "synthetic-admin", "POST", `{"principal_id":"alice","context_id":"alice","reason":"arbitrary-content"}`, 400},
		{"unknown-key", "synthetic-admin", "synthetic-admin", "POST", `{"principal_id":"alice","context_id":"alice","reason":"manual","extra":true}`, 400},
		{"trailing-json", "synthetic-admin", "synthetic-admin", "POST", valid + " {}", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", tc.admin)
			m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) {
				t.Fatal("invalid hold touched Docker")
				return nil, nil
			}, nil)
			r := httptest.NewRequest(tc.method, "/v1/admin/holds", strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+tc.token)
			if tc.name == "missing-bearer" {
				r.Header.Set("Authorization", tc.token)
			}
			w := httptest.NewRecorder()
			m.Handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("boundary returned %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestMasterResumeRequiresCurrentEpoch(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "synthetic-admin")
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	call := func(method, body string) int {
		r := httptest.NewRequest(method, "/v1/admin/holds", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer synthetic-admin")
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if code := call("POST", `{"principal_id":"alice","context_id":"alice","reason":"manual"}`); code != 200 {
		t.Fatalf("hold: %d", code)
	}
	if code := call("DELETE", `{"principal_id":"alice","context_id":"alice","reason":"manual","expected_epoch":0}`); code != 409 {
		t.Errorf("stale resume: %d, want 409", code)
	}
	if code := call("DELETE", `{"principal_id":"alice","context_id":"alice","reason":"manual","expected_epoch":1}`); code != 200 {
		t.Errorf("current resume: %d, want 200", code)
	}
	if code := call("DELETE", `{"principal_id":"alice","context_id":"alice","reason":"manual","expected_epoch":1}`); code != 409 {
		t.Errorf("replayed resume: %d, want 409", code)
	}
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if h := restarted.holds[holdKey("alice", "alice")]; h.Active || h.Epoch != 2 {
		t.Fatalf("resume fence not persisted: %+v", h)
	}
	if h := restarted.holds[holdKey("alice", "alice")]; !h.NoticePending || h.NoticeID != "hold-alice-alice-1" {
		t.Errorf("resume lost unacknowledged owner notice: %+v", h)
	}
}

func TestMasterFencesLateEffects(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "synthetic-admin")
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	job := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "old-job", IdempotencyKey: "old-key", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal"}
	if _, _, err := m.beginJob(job); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(job, "gen-1"); err != nil {
		t.Fatal(err)
	}
	m.setJobPhase(job, phaseRunning)
	r := m.jobs[job.JobID]
	r.Dispatching = true
	m.jobs[job.JobID] = r
	other := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("bob", 2, "bob", "policy-1"), JobID: "other-job", IdempotencyKey: "other-key", UserID: "bob", ActorID: "bob", ScopeID: "user:bob", OrganizationID: "personal"}
	if _, _, err := m.beginJob(other); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(other, "gen-2"); err != nil {
		t.Fatal(err)
	}
	m.setJobPhase(other, phaseRunning)
	hold := func(method, body string) {
		req := httptest.NewRequest(method, "/v1/admin/holds", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer synthetic-admin")
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("hold transition: %d %s", w.Code, w.Body.String())
		}
	}
	hold("POST", `{"principal_id":"alice","context_id":"alice","reason":"manual"}`)
	next := job
	next.JobID = "blocked-job"
	next.IdempotencyKey = "blocked-key"
	if _, _, err := m.beginJob(next); err == nil {
		t.Error("hold admitted new durable work")
	}
	late := hubruntime.ExecuteResponse{JobID: job.JobID, RuntimeGeneration: "gen-1", Status: "completed", Text: "must not publish"}
	if err := m.finishJobGeneration(job, late, "completed", "gen-1"); err == nil {
		t.Error("hold accepted a late effect")
	}
	if r := m.jobs[job.JobID]; !r.CancelRequested || r.Status != "uncertain" || r.Response.Text != "" {
		t.Errorf("in-flight ambiguous effect not fenced: %+v", r)
	}
	hold("DELETE", `{"principal_id":"alice","context_id":"alice","reason":"manual","expected_epoch":1}`)
	if err := m.finishJobGeneration(job, late, "completed", "gen-1"); err == nil {
		t.Error("resume revived an old effect")
	}
	if err := m.finishJobGeneration(other, hubruntime.ExecuteResponse{JobID: other.JobID, Status: "completed", Text: "safe"}, "completed", ""); err != nil {
		t.Fatalf("hold damaged another principal: %v", err)
	}
}

func TestMasterContainsOnlyVerifiedScope(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "graceful", true: "forced"}[force], func(t *testing.T) {
			t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "synthetic-admin")
			var stopped, removed []string
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "inspect":
					return nil, os.ErrNotExist
				case "stop":
					stopped = append(stopped, args[len(args)-1])
					if force {
						return nil, os.ErrDeadlineExceeded
					}
				case "rm":
					removed = append(removed, args[len(args)-1])
				}
				return []byte("running"), nil
			}, func(context.Context, string, string) error { return nil })
			a, err := m.Ensure(t.Context(), binding(root))
			if err != nil {
				t.Fatal(err)
			}
			owned, err := m.ownedContainerID(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			foreignKey := runtimeKey(Binding{PrincipalID: "bob", ContextID: "bob", RuntimeMode: "gateway"})
			m.items[foreignKey] = &runtimeEntry{Runtime: Runtime{PrincipalID: "bob", ContextID: "bob", RuntimeID: "bob", RuntimeMode: "gateway", Generation: "gen-bob", Container: "foreign-shared", State: Ready}}
			r := httptest.NewRequest("POST", "/v1/admin/holds", strings.NewReader(`{"principal_id":"alice","context_id":"alice","reason":"manual"}`))
			r.Header.Set("Authorization", "Bearer synthetic-admin")
			w := httptest.NewRecorder()
			m.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("containment: %d %s", w.Code, w.Body.String())
			}
			if len(stopped) != 1 || stopped[0] != owned || len(removed) != 1 || removed[0] != owned {
				t.Errorf("verified tree not stopped with bounded fallback: stop=%v remove=%v expected=%s", stopped, removed, owned)
			}
			if m.items[runtimeKey(binding(root))].State != Stopped {
				t.Error("contained runtime remains active")
			}
			if m.items[foreignKey].State != Ready {
				t.Error("containment damaged a foreign consumer")
			}
		})
	}
}

func TestMasterHoldReportsIncompleteContainment(t *testing.T) {
	for _, kind := range []string{"busy", "unverified", "remove-failure"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "synthetic-admin")
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				return []byte("running"), nil
			}, func(context.Context, string, string) error { return nil })
			if _, err := m.Ensure(t.Context(), binding(root)); err != nil {
				t.Fatal(err)
			}
			original := m.cfg.Command
			m.cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
				if kind == "unverified" {
					if args[0] != "inspect" && args[0] != "ps" {
						t.Errorf("unverified resource mutation: %s", args[0])
					}
					return nil, os.ErrPermission
				}
				if kind == "remove-failure" && args[0] == "rm" {
					return nil, os.ErrPermission
				}
				return original(ctx, args...)
			}
			lock := m.lockFor(runtimeKey(binding(root)))
			if kind == "busy" {
				lock.Lock()
			}
			r := httptest.NewRequest("POST", "/v1/admin/holds", strings.NewReader(`{"principal_id":"alice","context_id":"alice","reason":"manual"}`))
			r.Header.Set("Authorization", "Bearer synthetic-admin")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { m.Handler().ServeHTTP(w, r); close(done) }()
			timedOut := false
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				timedOut = true
			}
			if kind == "busy" {
				lock.Unlock()
			}
			if timedOut {
				<-done
				t.Fatal("containment blocked the control request on lifecycle work")
			}
			if w.Code != 202 {
				t.Errorf("unfinished stop reported %d, want 202: %s", w.Code, w.Body.String())
			}
			resume := httptest.NewRequest("DELETE", "/v1/admin/holds", strings.NewReader(`{"principal_id":"alice","context_id":"alice","reason":"manual","expected_epoch":1}`))
			resume.Header.Set("Authorization", "Bearer synthetic-admin")
			rw := httptest.NewRecorder()
			m.Handler().ServeHTTP(rw, resume)
			if rw.Code != 409 {
				t.Errorf("resume bypassed unfinished containment: %d", rw.Code)
			}
			restarted, err := New(m.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !restarted.holds[holdKey("alice", "alice")].Active {
				t.Error("failed containment lost its durable hold")
			}
		})
	}
}

func TestMasterContainmentRetriesAfterRestart(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "synthetic-admin")
	fail := true
	restarts := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "restart" {
			restarts++
		}
		if fail && (args[0] == "stop" || args[0] == "rm") {
			return nil, os.ErrPermission
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	if _, err := m.Ensure(t.Context(), binding(root)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/admin/holds", strings.NewReader(`{"principal_id":"alice","context_id":"alice","reason":"manual"}`))
	req.Header.Set("Authorization", "Bearer synthetic-admin")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatalf("failed initial stop: %d", w.Code)
	}
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	key := runtimeKey(binding(root))
	restarted.mu.Lock()
	desired := restarted.runtimeDesiredLocked(restarted.items[key])
	restarted.mu.Unlock()
	if desired {
		t.Error("retained demand overrides administrative hold")
	}
	fail = false
	restarted.ReconcileHealth(t.Context(), time.Unix(200, 0))
	if restarted.items[key].State != Stopped {
		t.Errorf("restart did not retry durable containment: %s", restarted.items[key].State)
	}
	if restarts != 0 {
		t.Fatalf("watchdog restarted a contained runtime %d times", restarts)
	}
}

func TestMasterNoticeWithoutRuntime(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "synthetic-admin")
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) {
		t.Fatal("notice must not start a runtime")
		return nil, nil
	}, nil)
	call := func(manager *Manager, method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		manager.Handler().ServeHTTP(w, r)
		return w
	}
	for range 2 {
		if w := call(m, "POST", "/v1/admin/holds", "synthetic-admin", `{"principal_id":"alice","context_id":"alice","reason":"manual"}`); w.Code != 200 {
			t.Fatalf("hold: %d", w.Code)
		}
	}
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(restarted, "GET", "/v1/holds", "wrong", ""); w.Code != 401 {
		t.Errorf("hold projection accepted wrong authority: %d", w.Code)
	}
	if w := call(restarted, "POST", "/v1/holds", "secret", ""); w.Code != 405 {
		t.Errorf("hold projection accepted wrong method: %d", w.Code)
	}
	w := call(restarted, "GET", "/v1/holds", "secret", "")
	var notices []struct {
		PrincipalID string `json:"principal_id"`
		Epoch       uint64 `json:"epoch"`
		NoticeID    string `json:"notice_id"`
		Pending     bool   `json:"notice_pending"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &notices) != nil || len(notices) != 1 {
		t.Fatalf("no durable owner notice: %d %s", w.Code, w.Body.String())
	}
	if n := notices[0]; n.PrincipalID != "alice" || n.Epoch != 1 || n.NoticeID == "" || !n.Pending {
		t.Fatalf("notice missing or not coalesced: %+v", n)
	}
	if w := call(restarted, "GET", "/v1/holds/ack", "secret", ""); w.Code != 405 {
		t.Errorf("ack method: %d", w.Code)
	}
	for _, bad := range []string{"{", `{}`, `{"notice_id":"../outside"}`, `{"notice_id":"x","extra":true}`, `{"notice_id":"x"} {}`, `{"notice_id":"` + strings.Repeat("x", 4096) + `"}`} {
		if w := call(restarted, "POST", "/v1/holds/ack", "secret", bad); w.Code != 400 {
			t.Errorf("invalid acknowledgement: %d, want 400", w.Code)
		}
	}
	w = call(restarted, "POST", "/v1/holds/ack", "secret", `{"notice_id":"foreign-notice"}`)
	if w.Code != 409 {
		t.Errorf("foreign notice ack: %d", w.Code)
	}
	body := `{"notice_id":"` + notices[0].NoticeID + `"}`
	statePath := restarted.statePath
	restarted.statePath = filepath.Join(restarted.cfg.StateDir, "missing", "supervisor.json")
	if w := call(restarted, "POST", "/v1/holds/ack", "secret", body); w.Code != 503 {
		t.Errorf("failed durable ack: %d, want 503", w.Code)
	}
	if !restarted.holds[holdKey("alice", "alice")].NoticePending {
		t.Error("failed ack lost pending notice")
	}
	restarted.statePath = statePath
	w = call(restarted, "POST", "/v1/holds/ack", "secret", body)
	if w.Code != 200 {
		t.Errorf("queued notice ack: %d", w.Code)
	}
	reloaded, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	w = call(reloaded, "GET", "/v1/holds", "secret", "")
	notices = nil
	if json.Unmarshal(w.Body.Bytes(), &notices) != nil || len(notices) != 1 || notices[0].Pending {
		t.Errorf("ack not durable: %s", w.Body.String())
	}
	if !reloaded.holds[holdKey("alice", "alice")].Active {
		t.Error("notice ack removed master hold")
	}
}
