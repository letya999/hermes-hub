package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/routines"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// testChannel is a real OccurrenceChannel for the harness: occurrence
// admission validation plus blocked-notice retargeting, no I/O.
type testChannel struct{ id string }

func (c testChannel) ValidateSchedule(commodel.Schedule) error {
	if c.id == "reject" {
		return errors.New("channel rejected schedule")
	}
	return nil
}

func (c testChannel) ValidateOccurrence(o commodel.RoutineOccurrence, caller identity.Envelope) error {
	if o.Job.PrincipalID == "" || caller.PrincipalID == "" {
		return errors.New("occurrence identity missing")
	}
	return nil
}

func (c testChannel) RouteBlockedNotice(item commodel.Schedule, d *commodel.Delivery) {
	d.ChatID = item.ChatID
}

// supervisorServer is a real-HTTP admission endpoint implementing the
// supervisor contract slice the host needs: idempotent job admission keyed
// by the durable intent key. Submissions and logical jobs are tracked
// separately so a lost acknowledgment is observable as a re-POST.
type supervisorServer struct {
	mu           sync.Mutex
	submissions  map[string]int
	logical      map[string]commodel.Job
	scriptCalls  atomic.Int32
	fail         atomic.Bool
	dropAck      atomic.Bool
	failRuns     atomic.Bool
	epoch        atomic.Uint64
	holdActive   atomic.Bool
	epochRejects atomic.Int32
	blockAlice   <-chan struct{}
	srv          *httptest.Server
}

func newSupervisorServer(t *testing.T) *supervisorServer {
	s := &supervisorServer{submissions: map[string]int{}, logical: map[string]commodel.Job{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/holds" && r.Method == http.MethodGet && r.Header.Get("Authorization") == "Bearer control" {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"principal_id": "alice", "context_id": "alice", "epoch": s.epoch.Load(), "active": s.holdActive.Load()}})
			return
		}
		if r.URL.Path == "/v1/script" {
			var request hubruntime.ScriptRequest
			if r.Header.Get("Authorization") != "Bearer control" || json.NewDecoder(r.Body).Decode(&request) != nil || request.JobID == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.scriptCalls.Add(1)
			if request.Text == `emit("drop")` {
				_ = json.NewEncoder(w).Encode([]string{"drop"})
				return
			}
			_ = json.NewEncoder(w).Encode([]string{"script result"})
			return
		}
		if s.fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/v1/jobs" || r.Header.Get("Authorization") != "Bearer control" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var in hubruntime.ExecuteRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.JobID == "" || in.IdempotencyKey == "" {
			http.Error(w, "bad admit", http.StatusBadRequest)
			return
		}
		if in.PrincipalID == "alice" && (s.holdActive.Load() || in.OperationalEpoch != s.epoch.Load()) {
			s.epochRejects.Add(1)
			w.WriteHeader(http.StatusConflict)
			return
		}
		if in.PrincipalID == "alice" && s.blockAlice != nil {
			<-s.blockAlice
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.submissions[in.IdempotencyKey]++
		if _, dup := s.logical[in.IdempotencyKey]; dup {
			if s.dropAck.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: in.JobID, Status: "completed", Text: "done"})
			return
		}
		s.logical[in.IdempotencyKey] = commodel.Job{Envelope: in.Envelope, ID: in.JobID, IdempotencyKey: in.IdempotencyKey, UserID: in.UserID, Text: in.Text, OperationalEpoch: in.OperationalEpoch}
		if s.dropAck.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if s.failRuns.Load() {
			_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: in.JobID, Status: "failed", Text: "boom"})
			return
		}
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: in.JobID, Status: "completed", Text: "done"})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *supervisorServer) onlyJob() commodel.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.logical {
		return j
	}
	return commodel.Job{}
}

func (s *supervisorServer) counts() (submissions, logical int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.submissions {
		submissions += n
	}
	return submissions, len(s.logical)
}

// commServer is a real-HTTP delivery-intake endpoint; `down` simulates a
// communication outage without touching durable host state.
type commServer struct {
	mu         sync.Mutex
	deliveries []commodel.Delivery
	down       atomic.Bool
	srv        *httptest.Server
}

func newCommServer(t *testing.T) *commServer {
	c := &commServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/v1/automation/deliveries" || r.Header.Get("Authorization") != "Bearer control" || r.Header.Get("X-Hub-Principal") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var d commodel.Delivery
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			http.Error(w, "bad delivery", http.StatusBadRequest)
			return
		}
		d.OwnerPrincipalID = r.Header.Get("X-Hub-Principal")
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, seen := range c.deliveries {
			if seen.IdempotencyKey == d.IdempotencyKey && seen.IdempotencyKey != "" {
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		c.deliveries = append(c.deliveries, d)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *commServer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.deliveries)
}

type harness struct {
	root  string
	env   string
	store *toolhub.Store
	gov   *toolhub.GovernanceStore
	sup   *supervisorServer
	comm  *commServer
	now   time.Time
	seq   int
}

func pendingDispatches(t *testing.T, host *Host, principal string) int {
	t.Helper()
	n, err := host.PendingDispatches(principal)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func pendingDeliveries(t *testing.T, host *Host, principal string) int {
	t.Helper()
	n, err := host.PendingDeliveries(principal)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	store := toolhub.NewStore()
	store.RightsDoc = api.RightsDoc{
		Release: "test", DefinitionID: "hub-agent-rights",
		Capabilities: map[string]api.RightCapability{
			"tasks": {Description: "fixture", Operations: map[string]api.RightOperation{
				"schedule": {Subject: "self", Scope: "none", Issuers: []string{"platform", "operator"}, Effect: "control"},
				"observe":  {Subject: "self", Scope: "none", Issuers: []string{"platform", "operator"}, Effect: "control"},
				"delegate": {Subject: "self", Scope: "none", Issuers: []string{"platform", "operator"}, Effect: "control"},
			}},
		},
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(root, "governance.json"))
	store.RightsLedger = gov.Load
	return &harness{
		root: root, env: "prod", store: store, gov: gov,
		sup: newSupervisorServer(t), comm: newCommServer(t),
		now: time.Now().UTC(),
	}
}

func (h *harness) envelope(t *testing.T, principal string) identity.Envelope {
	t.Helper()
	// Non-managed verified envelope: environment pinning binds envelopes
	// that carry an Environment value; Decide validates identity always.
	return identity.TelegramEnvelope(principal, 11, "rt-"+principal, "policy-1")
}

func (h *harness) grant(t *testing.T, principal, _ string) {
	t.Helper()
	for _, operation := range []string{"schedule", "observe", "delegate"} {
		h.grantRight(t, principal, operation)
	}
}

func (h *harness) grantRight(t *testing.T, principal, op string) {
	t.Helper()
	h.seq++
	grant := api.NewRightGrant(fmt.Sprintf("rg-%s-%s-%d", principal, op, h.seq), principal, "tasks", op, "", "fixture", "operator", h.now.Add(time.Hour))
	if err := api.Confirm(&grant, "operator", h.now); err != nil {
		t.Fatal(err)
	}
	if err := h.gov.GrantRight(grant, h.store.RightsDoc, h.now); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) revoke(t *testing.T, principal, _ string) {
	t.Helper()
	grants, err := h.gov.RightGrants()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if g.PrincipalID == principal {
			if err := h.gov.RevokeRight(g.GrantID, "operator", "revoked", h.now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func (h *harness) revokeRight(t *testing.T, principal, operation string) {
	t.Helper()
	grants, err := h.gov.RightGrants()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if g.PrincipalID == principal && g.Operation == operation {
			if err := h.gov.RevokeRight(g.GrantID, "operator", "revoked", h.now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestHostUsesCatalogRightsAndFencesRevokedWatch(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grantRight(t, "alice", "observe")
	host := h.host(t)
	watch := draft("watch-right", alice)
	watch.JobKind, watch.Delivery, watch.Sink = "event", "stream", "agent"
	watch.Expression = "0 10 * * *"
	watch.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait", CursorArg: "after"}
	created, err := host.CreateSchedule(context.Background(), alice, watch, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.LoadSchedule(context.Background(), alice, created.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if _, err := host.CreateSchedule(context.Background(), alice, draft("routine-denied", alice), ""); err == nil {
		t.Fatal("observe right admitted a scheduled routine")
	}
	if _, _, err := host.CreateAssignment(context.Background(), alice, commodel.Assignment{AssignmentID: "assignment-denied", Envelope: alice, Intent: "watch", Title: "watch"}); err == nil {
		t.Fatal("observe right admitted an assignment")
	}
	h.grantRight(t, "alice", "schedule")
	job := commodel.Job{Envelope: alice, Trigger: "cron"}
	if err := host.authorizeJob(job, created.ScheduleID); err != nil {
		t.Fatal(err)
	}
	h.revokeRight(t, "alice", "observe")
	if err := host.authorizeJob(job, created.ScheduleID); err == nil {
		t.Fatal("revoked observe right admitted a due watch despite schedule right")
	}
}

func (h *harness) host(t *testing.T) *Host {
	return h.hostWithOwners(t, "")
}

func (h *harness) hostWithOwners(t *testing.T, ownersDir string) *Host {
	t.Helper()
	host, err := NewHost(Config{
		Root:        h.root,
		OwnersDir:   ownersDir,
		Environment: h.env,
		Store:       h.store,
		AuthForPrincipal: func(principal string) (identity.Envelope, error) {
			return identity.TelegramEnvelope(principal, 11, "rt-"+principal, "policy-1"), nil
		},
		Now:              func() time.Time { return h.now },
		Channels:         map[string]routines.OccurrenceChannel{"telegram_bot": testChannel{id: "telegram_bot"}},
		Supervisor:       &HTTPSupervisorClient{URL: h.sup.srv.URL, Token: "control"},
		Communication:    &HTTPCommunicationClient{URL: h.comm.srv.URL, Token: "control"},
		AuthorizeChannel: func(context.Context, string, commodel.Job) error { return nil },
		WorkspaceDir:     func(principal string) string { return filepath.Join(h.root, "workspace", principal) },
	})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { host.Close() })
	return host
}

func draft(id string, env identity.Envelope) commodel.Schedule {
	return commodel.Schedule{
		ScheduleID: id, Envelope: env, UserID: env.PrincipalID, ActorID: env.PrincipalID,
		ScopeID: "user:" + env.PrincipalID, Channel: "telegram_bot", ChatID: 11,
		Timezone: "UTC", Expression: "every:1m", Input: "work " + id,
		JobKind: "agent", Enabled: true,
	}
}

func TestSampleWatchUsesOwnerHostWithoutAdvancingCursor(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	bob := h.envelope(t, "bob")
	for _, op := range []string{"read", "write"} {
		h.grant(t, "alice", op)
		h.grant(t, "bob", op)
	}
	host := h.host(t)
	var cursor any
	host.cfg.ToolCall = func(_ context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		if principal != "alice" {
			t.Errorf("source called for %q", principal)
		}
		cursor = probe.Args["after"]
		return []byte(`{"events":[{"id":"m1","text":"hello","chat_id":7}],"next_cursor":"m2"}`), nil
	}
	schedule := draft("rt-sample-host", alice)
	schedule.JobKind, schedule.Delivery, schedule.Sink = "event", "stream", "agent"
	schedule.Expression = "0 10 * * *"
	schedule.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait", CursorArg: "after"}
	if _, err := host.CreateSchedule(context.Background(), alice, schedule, ""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := ControlClient{URL: server.URL, Token: "control"}
	var result watchSample
	if err := client.Call(context.Background(), bob, "schedule.sample", map[string]any{"id": schedule.ScheduleID}, &result); err == nil {
		t.Fatal("foreign owner sampled watch")
	}
	if err := client.Call(context.Background(), alice, "schedule.sample", map[string]any{"id": schedule.ScheduleID, "filter": `emit("drop")`}, &result); err != nil {
		t.Fatal(err)
	}
	if cursor != "0" || len(result.Sources) != 1 || len(result.Sources[0].Events) != 1 || result.Sources[0].Verdicts["m1"] != "drop" || !strings.Contains(result.Sources[0].Events[0].Raw, `"chat_id":7`) {
		t.Fatalf("sample lost shape, cursor, or verdict: %+v cursor=%v", result, cursor)
	}
	if h.sup.scriptCalls.Load() != 1 {
		t.Fatal("candidate filter did not run once")
	}
	if _, err := os.Stat(filepath.Join(h.root, "events")); !os.IsNotExist(err) {
		t.Fatalf("sample wrote event state: %v", err)
	}
	if submissions, logical := h.sup.counts(); submissions != 0 || logical != 0 || h.comm.count() != 0 {
		t.Fatal("sample dispatched or delivered")
	}
}

func TestHostUsesChannelScheduleValidation(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	missing := host.cfg
	missing.Channels = nil
	if _, err := NewHost(missing); err == nil {
		t.Fatal("host accepted no channel validators")
	}
	host.cfg.Channels["telegram_bot"] = testChannel{id: "reject"}
	if _, err := host.CreateSchedule(context.Background(), alice, draft("rt-reject", alice), ""); err == nil {
		t.Fatal("channel-specific schedule validation was skipped")
	}
	if _, err := os.Stat(filepath.Join(h.root, "users", "alice", "schedules", "rt-reject.json")); !os.IsNotExist(err) {
		t.Fatalf("rejected schedule reached storage: %v", err)
	}
}

func TestManagedPolicyIdentityCannotBeBypassedByChannelEnvelope(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	item, err := host.CreateSchedule(context.Background(), alice, draft("rt-before-policy-change", alice), "")
	if err != nil {
		t.Fatal(err)
	}
	managed := alice
	managed.CapabilityProfile, managed.Environment, managed.Generation = "missing-profile", "prod", 1
	host.cfg.AuthForPrincipal = func(string) (identity.Envelope, error) { return managed, nil }
	if _, err := host.CreateSchedule(context.Background(), alice, draft("rt-denied", alice), ""); err == nil {
		t.Fatal("channel envelope bypassed the missing managed profile")
	}
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	if _, jobs := h.sup.counts(); jobs != 0 {
		t.Fatalf("background dispatch bypassed managed profile: %d jobs", jobs)
	}
}

// The host must reload durable routines across a restart: a schedule
// admitted before the restart still fires exactly once afterward, and a
// delivery intent persisted while communication was down flushes on the
// next tick — no replay, no loss.
func TestToolHubAutomationHostRestart(t *testing.T) {
	h := newHarness(t)
	env := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")

	host := h.host(t)
	item, err := host.CreateSchedule(context.Background(), env, draft("rt-restart", env), "")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	// Communication is down for the first dispatch: execution must still be
	// admitted durably and the delivery intent must stay pending.
	h.comm.down.Store(true)
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, logical := h.sup.counts(); logical != 1 {
		t.Fatalf("expected one admitted job, got %d", logical)
	}
	// Execution result persists while communication is down; the delivery
	// intent stays durable and pending.
	job := h.sup.onlyJob()
	if err := host.ReportResult(context.Background(), env.PrincipalID, job.ID, "completed", "done"); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if n := h.comm.count(); n != 0 {
		t.Fatalf("delivery reached communication while down: %d", n)
	}
	if pending := pendingDeliveries(t, host, env.PrincipalID); pending != 1 {
		t.Fatalf("expected one durable pending delivery intent, got %d", pending)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Restart over the same root: durable schedules and intents reload.
	h.comm.down.Store(false)
	host2 := h.host(t)
	if err := host2.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := host2.Tick(context.Background(), h.now.Add(30*time.Second)); err != nil {
		t.Fatalf("Tick after restart: %v", err)
	}
	if _, logical := h.sup.counts(); logical != 1 {
		t.Fatalf("restart replayed the admitted job: %d logical", logical)
	}
	if pending := pendingDeliveries(t, host2, env.PrincipalID); pending != 0 {
		t.Fatalf("delivery intent still pending after recovery: %d", pending)
	}
	if n := h.comm.count(); n != 1 {
		t.Fatalf("expected exactly one flushed delivery, got %d", n)
	}
}

// Assignment and watch transitions are owned by the ToolHub host through
// the same verified envelope + grant gate as routine CRUD.
func TestAssignmentWatchTransitionsOwnedByToolHub(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	bob := h.envelope(t, "bob")
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")
	h.grant(t, "bob", "write")
	h.grant(t, "bob", "read")

	host := h.host(t)
	ctx := context.Background()

	asg, created, err := host.CreateAssignment(ctx, alice, commodel.Assignment{
		AssignmentID: "asg-1", Envelope: alice, Intent: "watch the page", Title: "watch",
		State: "draft",
		Plan: &commodel.AssignmentPlan{Revision: 1, Sources: []commodel.AssignmentSource{
			{Name: "page", Schedule: commodel.RoutineDraft{JobKind: "watch", Channel: "telegram_bot", Probe: &commodel.WatchProbe{URL: "https://example.com"}}},
		}},
		Grants: commodel.AssignmentGrants{MaxWakesPerDay: 10},
	})
	if err != nil || created {
		t.Fatalf("CreateAssignment: existing=%v err=%v (new assignment must not replay)", created, err)
	}
	if err := host.PauseAssignment(ctx, alice, asg.AssignmentID); err != nil {
		t.Fatalf("PauseAssignment: %v", err)
	}
	if err := host.ResumeAssignment(ctx, alice, asg.AssignmentID); err != nil {
		t.Fatalf("ResumeAssignment: %v", err)
	}
	if _, err := host.CompleteAssignment(ctx, alice, asg.AssignmentID); err != nil {
		t.Fatalf("CompleteAssignment: %v", err)
	}

	// Watch baseline transitions under the same owner.
	item, err := host.CreateSchedule(ctx, alice, draft("w-owned", alice), "")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if err := host.SeedWatchBaseline(ctx, alice, item.ScheduleID, "v1", h.now); err != nil {
		t.Fatalf("SeedWatchBaseline: %v", err)
	}
	state, err := host.LoadWatchState(ctx, alice, item.ScheduleID)
	if err != nil {
		t.Fatalf("LoadWatchState: %v", err)
	}
	if state.Value != "v1" {
		t.Fatalf("watch baseline not persisted: %+v", state)
	}

	// Cross-user: bob's engine root cannot expose alice's objects — a read
	// either denies or returns an empty state, never alice's baseline.
	if bstate, err := host.LoadWatchState(ctx, bob, item.ScheduleID); err == nil && bstate.Value != "" {
		t.Fatalf("cross-user watch read leaked alice's baseline: %+v", bstate)
	}
	if err := host.PauseAssignment(ctx, bob, asg.AssignmentID); err == nil {
		t.Fatal("cross-user assignment mutation must deny")
	}
	if list, err := host.ListSchedules(ctx, bob); err != nil || len(list) != 0 {
		t.Fatalf("cross-user schedule list leaked: %d err=%v", len(list), err)
	}
}

func TestHostPreservesRevisionPreconditions(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")
	host := h.host(t)
	ctx := context.Background()

	schedule, err := host.CreateSchedule(ctx, alice, draft("rt-cas", alice), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.PauseSchedule(ctx, alice, schedule.ScheduleID, schedule.Revision+1); !errors.Is(err, routines.ErrRoutineRevision) {
		t.Fatalf("stale schedule revision: %v", err)
	}
	if err := host.PauseSchedule(ctx, alice, schedule.ScheduleID, schedule.Revision); err != nil {
		t.Fatalf("current schedule revision: %v", err)
	}

	assignment, _, err := host.CreateAssignment(ctx, alice, commodel.Assignment{
		AssignmentID: "asg-cas", Envelope: alice, Intent: "check revisions", Title: "check revisions", State: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.PauseAssignment(ctx, alice, assignment.AssignmentID, assignment.Revision+1); !errors.Is(err, routines.ErrRoutineRevision) {
		t.Fatalf("stale assignment revision: %v", err)
	}
	if err := host.PauseAssignment(ctx, alice, assignment.AssignmentID, assignment.Revision); err != nil {
		t.Fatalf("current assignment revision: %v", err)
	}
}

func TestChannelRebindBlocksSupervisorDispatch(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")
	host := h.host(t)
	item, err := host.CreateSchedule(context.Background(), alice, draft("rt-channel-fence", alice), "")
	if err != nil {
		t.Fatal(err)
	}
	allowed := false
	host.cfg.AuthorizeChannel = func(_ context.Context, scheduleID string, job commodel.Job) error {
		if scheduleID != item.ScheduleID || job.PrincipalID != "alice" {
			return errors.New("unexpected channel request")
		}
		if !allowed {
			return errors.New("channel re-bound")
		}
		return nil
	}
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	if _, logical := h.sup.counts(); logical != 0 {
		t.Fatal("re-bound channel dispatched a job")
	}
	// Materialize-time authorization fences before staging: the durable
	// record of the denial is the blocked schedule, not a pending intent.
	if pendingDispatches(t, host, "alice") != 0 {
		t.Fatal("channel denial staged a dispatch intent for unauthorized work")
	}
	blocked, err := host.LoadSchedule(context.Background(), alice, item.ScheduleID)
	if err != nil || blocked.BlockedReason == "" {
		t.Fatalf("channel denial did not durably block the schedule: %+v %v", blocked, err)
	}
	allowed = true
	resumed, err := host.EditRoutine(context.Background(), alice, item.ScheduleID, map[string]json.RawMessage{}, true, nil)
	if err != nil {
		t.Fatalf("owner unblock: %v", err)
	}
	h.now = resumed.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	if _, logical := h.sup.counts(); logical != 1 {
		t.Fatal("restored channel binding did not dispatch after owner unblock")
	}
}

func TestChannelRebindStopsScriptAndProbeIO(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	script := draft("rt-script-channel", alice)
	script.JobKind = "script"
	script.Input = `emit("done")`
	watch := draft("rt-probe-channel", alice)
	watch.JobKind = "event"
	watch.Probe = &commodel.WatchProbe{Kind: "http", URL: "https://example.test/feed"}
	var fetched atomic.Int32
	host.cfg.Fetch = func(context.Context, string) ([]byte, error) {
		fetched.Add(1)
		return []byte(`{"events":[]}`), nil
	}
	var due time.Time
	for _, item := range []commodel.Schedule{script, watch} {
		created, err := host.CreateSchedule(context.Background(), alice, item, "")
		if err != nil {
			t.Fatal(err)
		}
		if created.NextDue.After(due) {
			due = created.NextDue
		}
	}
	var checked atomic.Int32
	host.cfg.AuthorizeChannel = func(context.Context, string, commodel.Job) error {
		checked.Add(1)
		return errors.New("channel re-bound")
	}
	h.now = due.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	if checked.Load() < 2 || h.sup.scriptCalls.Load() != 0 || fetched.Load() != 0 {
		t.Fatal("re-bound channel performed script or probe I/O")
	}
}

// A due occurrence is admitted durably before supervisor I/O. When the
// first acknowledgment is lost, recovery re-submits the same stable key;
// the supervisor converges to exactly one logical job.
func TestRetiredScheduleStopsRetriesWithoutInventingRunOutcome(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(fmt.Sprintf("retired=%t", retire), func(t *testing.T) {
			h := newHarness(t)
			env := h.envelope(t, "alice")
			h.grant(t, "alice", "write")
			h.grant(t, "alice", "read")
			host := h.host(t)
			ctx := context.Background()
			item, err := host.CreateSchedule(ctx, env, draft("retire-pending", env), "")
			if err != nil {
				t.Fatal(err)
			}
			h.sup.dropAck.Store(true)
			h.now = item.NextDue.Add(time.Second)
			if err := host.Tick(ctx, h.now); err != nil {
				t.Fatal(err)
			}
			if got := pendingDispatches(t, host, "alice"); got != 1 {
				t.Fatalf("pending before retirement=%d", got)
			}
			job := h.sup.onlyJob()
			if retire {
				err = host.DeleteSchedule(ctx, env, item.ScheduleID)
			} else {
				err = host.PauseSchedule(ctx, env, item.ScheduleID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if retire {
				eng, err := host.engineFor("alice")
				if err != nil {
					t.Fatal(err)
				}
				path := eng.CancelPath(item.ScheduleID)
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var foreign commodel.Schedule
				if err := json.Unmarshal(original, &foreign); err != nil {
					t.Fatal(err)
				}
				foreign.Envelope.PrincipalID = "bob"
				forged, err := json.Marshal(foreign)
				if err != nil {
					t.Fatal(err)
				}
				for _, invalid := range [][]byte{[]byte("invalid"), forged} {
					if err := os.WriteFile(path, invalid, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := host.Recover(ctx); err != nil {
						t.Fatal(err)
					}
					if got := pendingDispatches(t, host, "alice"); got != 1 {
						t.Fatal("invalid or foreign deletion discarded pending work")
					}
				}
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			submissions, _ := h.sup.counts()
			if err := host.Close(); err != nil {
				t.Fatal(err)
			}
			host = h.host(t)
			if err := host.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			want := 1
			if retire {
				want = 0
			}
			if got := pendingDispatches(t, host, "alice"); got != want {
				t.Fatalf("pending after restart=%d, want %d", got, want)
			}
			if got, _ := h.sup.counts(); got != submissions {
				t.Fatalf("inactive source resubmitted: %d -> %d", submissions, got)
			}
			mapping, err := host.queue.loadMapping("alice", job.ID)
			if err != nil || commodel.TerminalStatus(mapping.Status) {
				t.Fatalf("invented terminal outcome: %s, %v", mapping.Status, err)
			}
			if _, err := host.queue.recordResult("alice", job.ID, "completed", "actual late result", h.now); err != nil {
				t.Fatalf("late outcome lost: %v", err)
			}
		})
	}
}

func TestDueOccurrenceDispatchRecoversOnce(t *testing.T) {
	h := newHarness(t)
	env := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")
	host := h.host(t)
	ctx := context.Background()

	item, err := host.CreateSchedule(ctx, env, draft("rt-once", env), "")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	// Supervisor admits the job but loses its first acknowledgment.
	h.sup.dropAck.Store(true)
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(ctx, h.now); err != nil {
		t.Fatalf("Tick with lost acknowledgment must keep durable intent: %v", err)
	}
	if subs, logical := h.sup.counts(); subs == 0 || logical != 1 {
		t.Fatalf("lost acknowledgment should leave one accepted job: submissions=%d jobs=%d", subs, logical)
	}
	if got := pendingDispatches(t, host, env.PrincipalID); got != 1 {
		t.Fatalf("expected one durable pending dispatch, got %d", got)
	}

	// Recovery: the intent re-submits under the same stable key.
	h.sup.dropAck.Store(false)
	if err := host.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	subs, logical := h.sup.counts()
	if logical != 1 {
		t.Fatalf("expected exactly one logical job, got %d", logical)
	}
	if subs < 2 {
		t.Fatalf("expected repeat submission after lost acknowledgment, got %d", subs)
	}
	if got := pendingDispatches(t, host, env.PrincipalID); got != 0 {
		t.Fatalf("dispatch intent still pending: %d", got)
	}

	// A repeat tick at the same due window must not re-admit.
	if err := host.Tick(ctx, h.now.Add(30*time.Second)); err != nil {
		t.Fatalf("repeat Tick: %v", err)
	}
	if _, logical := h.sup.counts(); logical != 1 {
		t.Fatalf("repeat dispatch admitted a second job")
	}
}

func TestSlowPrincipalDoesNotBlockOtherDispatch(t *testing.T) {
	h := newHarness(t)
	for _, principal := range []string{"alice", "bob"} {
		h.grant(t, principal, "write")
	}
	host := h.host(t)
	at := h.now
	for _, principal := range []string{"alice", "bob"} {
		env := h.envelope(t, principal)
		item, err := host.CreateSchedule(context.Background(), env, draft("rt-"+principal, env), "")
		if err != nil {
			t.Fatal(err)
		}
		if item.NextDue.After(at) {
			at = item.NextDue
		}
	}
	h.now = at.Add(time.Second)
	block := make(chan struct{})
	h.sup.blockAlice = block
	done := make(chan error, 1)
	go func() { done <- host.Tick(context.Background(), h.now) }()
	deadline := time.After(3 * time.Second)
	for {
		h.sup.mu.Lock()
		bob := false
		for _, job := range h.sup.logical {
			bob = bob || job.PrincipalID == "bob"
		}
		h.sup.mu.Unlock()
		if bob {
			break
		}
		select {
		case <-deadline:
			close(block)
			<-done
			t.Fatal("alice's slow supervisor call starved bob")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSlowProbeDoesNotBlockOtherPrincipal(t *testing.T) {
	h := newHarness(t)
	for _, principal := range []string{"alice", "bob"} {
		h.grant(t, principal, "write")
	}
	host := h.host(t)
	alice := h.envelope(t, "alice")
	watch := draft("rt-slow-probe", alice)
	watch.JobKind = "event"
	watch.Probe = &commodel.WatchProbe{Kind: "http", URL: "https://example.com/slow"}
	watchItem, err := host.CreateSchedule(context.Background(), alice, watch, "")
	if err != nil {
		t.Fatal(err)
	}
	bob := h.envelope(t, "bob")
	bobItem, err := host.CreateSchedule(context.Background(), bob, draft("rt-bob-fast", bob), "")
	if err != nil {
		t.Fatal(err)
	}
	h.now = watchItem.NextDue.Add(time.Second)
	if bobItem.NextDue.After(watchItem.NextDue) {
		h.now = bobItem.NextDue.Add(time.Second)
	}
	block := make(chan struct{})
	host.cfg.Fetch = func(_ context.Context, url string) ([]byte, error) {
		if url == watch.Probe.URL {
			<-block
		}
		return []byte("event"), nil
	}
	done := make(chan error, 1)
	go func() { done <- host.Tick(context.Background(), h.now) }()
	deadline := time.After(3 * time.Second)
	for {
		h.sup.mu.Lock()
		found := false
		for _, job := range h.sup.logical {
			found = found || job.PrincipalID == "bob"
		}
		h.sup.mu.Unlock()
		if found {
			break
		}
		select {
		case <-deadline:
			close(block)
			<-done
			t.Fatal("alice's slow probe starved bob's dispatch")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Identity, environment, and grant revocation gate every operation; a
// paused schedule produces no dispatch.
func TestAutomationHostIsolation(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	ctx := context.Background()

	wrongEnv := alice
	wrongEnv.Environment = "dev"
	if _, err := host.CreateSchedule(ctx, wrongEnv, draft("rt-env", wrongEnv), ""); err == nil {
		t.Fatal("wrong-environment envelope must deny")
	}
	if _, err := host.CreateSchedule(ctx, identity.Envelope{}, draft("rt-noname", identity.Envelope{}), ""); err == nil {
		t.Fatal("empty principal must deny")
	}
	item, err := host.CreateSchedule(ctx, alice, draft("rt-rev", alice), "")
	if err != nil {
		t.Fatalf("CreateSchedule with active grant: %v", err)
	}
	h.revoke(t, "alice", "write")
	if _, err := host.CreateSchedule(ctx, alice, draft("rt-rev2", alice), ""); err == nil {
		t.Fatal("revoked grant must deny")
	}

	// Paused schedule: no dispatch on tick.
	h.grant(t, "alice", "write")
	item2, err := host.CreateSchedule(ctx, alice, draft("rt-paused", alice), "")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if err := host.PauseSchedule(ctx, alice, item2.ScheduleID); err != nil {
		t.Fatalf("PauseSchedule: %v", err)
	}
	h.now = item2.NextDue.Add(time.Hour)
	if err := host.Tick(ctx, h.now); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, logical := h.sup.counts(); logical != 0 {
		t.Fatalf("paused schedule dispatched")
	}
	_ = item
}

func TestOnceAgentDispatchAfterScheduleSettles(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "schedule")
	host := h.host(t)
	item := draft("once-agent", alice)
	item.Expression = "once:" + h.now.Add(time.Minute).Format(time.RFC3339)
	created, err := host.CreateSchedule(t.Context(), alice, item, "")
	if err != nil {
		t.Fatal(err)
	}
	h.now = created.NextDue
	h.sup.fail.Store(true)
	if err := host.Tick(t.Context(), h.now); err != nil {
		t.Fatal(err)
	}
	settled, err := host.LoadSchedule(t.Context(), alice, created.ScheduleID)
	if err != nil || settled.Enabled || !settled.Paused || settled.LastOccurrenceID == "" {
		t.Fatalf("once schedule did not settle with a pending occurrence: %+v %v", settled, err)
	}
	if _, logical := h.sup.counts(); logical != 0 {
		t.Fatal("unavailable supervisor accepted the job")
	}
	h.sup.fail.Store(false)
	if err := host.Tick(t.Context(), h.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, logical := h.sup.counts(); logical != 1 || h.comm.count() != 1 {
		t.Fatalf("settled once occurrence did not finish exactly once: jobs=%d deliveries=%d", logical, h.comm.count())
	}
	if err := host.Tick(t.Context(), h.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, logical := h.sup.counts(); logical != 1 || h.comm.count() != 1 {
		t.Fatal("settled once occurrence replayed")
	}
}

func TestAutomationHostRejectsUnsafeOwnerStorage(t *testing.T) {
	h := newHarness(t)
	host := h.host(t)
	if _, err := host.engineFor("../escape"); err == nil {
		t.Fatal("path-like principal accepted")
	}
	users := filepath.Join(h.root, "users")
	if err := os.MkdirAll(users, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(users, "alice"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.engineFor("alice"); err == nil {
		t.Fatal("non-directory owner storage accepted")
	}
}

// Routine CRUD beyond create is owned through the same gate: update,
// snooze, list, cancel, delete — each against the caller's own engine.
func TestRoutineCRUDOwnedByToolHub(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")
	host := h.host(t)
	ctx := context.Background()

	item, err := host.CreateSchedule(ctx, alice, draft("rt-crud", alice), "")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	updated, err := host.UpdateSchedule(ctx, alice, item.ScheduleID, "every:5m", "changed input", nil)
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if updated.Input != "changed input" {
		t.Fatalf("update not applied: %+v", updated)
	}
	if err := host.SnoozeSchedule(ctx, alice, item.ScheduleID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SnoozeSchedule: %v", err)
	}
	list, err := host.ListSchedules(ctx, alice)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSchedules: %d err=%v", len(list), err)
	}
	if !list[0].SnoozedUntil.After(h.now) {
		t.Fatalf("snooze not persisted: %+v", list[0])
	}

	asg, _, err := host.CreateAssignment(ctx, alice, commodel.Assignment{
		AssignmentID: "asg-crud", Envelope: alice, Intent: "crud assignment", Title: "crud",
		State: "draft", Grants: commodel.AssignmentGrants{MaxWakesPerDay: 5},
	})
	if err != nil {
		t.Fatalf("CreateAssignment: %v", err)
	}
	asgs, err := host.ListAssignments(ctx, alice)
	if err != nil || len(asgs) != 1 {
		t.Fatalf("ListAssignments: %d err=%v", len(asgs), err)
	}
	if err := host.CancelAssignment(ctx, alice, asg.AssignmentID); err != nil {
		t.Fatalf("CancelAssignment: %v", err)
	}
	if err := host.DeleteSchedule(ctx, alice, item.ScheduleID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if list, err := host.ListSchedules(ctx, alice); err != nil || len(list) != 0 {
		t.Fatalf("delete did not remove schedule: %d", len(list))
	}
}

// Event-kind schedules fan probe results out to the watch destination as
// delivery intents; the durable seen-set dedupes replays — polling the same
// body twice delivers once.
func TestWatchEventFanoutOwnedByToolHub(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")
	var body atomic.Value
	body.Store(`{"ev":1}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()
	host := h.host(t)
	host.cfg.Fetch = func(ctx context.Context, url string) ([]byte, error) {
		resp, err := http.Get(url)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return io.ReadAll(resp.Body)
	}
	ctx := context.Background()
	sc := draft("rt-events", alice)
	sc.JobKind = "event"
	sc.Probe = &commodel.WatchProbe{Kind: "http", URL: srv.URL}
	item, err := host.CreateSchedule(ctx, alice, sc, "")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(ctx, h.now); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := h.comm.count(); got != 1 {
		t.Fatalf("first poll delivered %d events, want 1", got)
	}
	if err := host.Tick(ctx, h.now.Add(3*time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := h.comm.count(); got != 1 {
		t.Fatalf("replay delivered %d events, want still 1 (dedupe)", got)
	}
	body.Store(`{"ev":2}`)
	if err := host.Tick(ctx, h.now.Add(4*time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := h.comm.count(); got != 2 {
		t.Fatalf("changed body delivered %d events, want 2", got)
	}
}

func TestMCPEventEnvelopeFansOutByEventID(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	host.cfg.ToolCall = func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"one","text":"first"},{"id":"two","text":"second"}],"next_cursor":"3"}`), nil
	}
	sc := draft("rt-mcp-events", alice)
	sc.JobKind = "event"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.read"}
	item, err := host.CreateSchedule(context.Background(), alice, sc, "")
	if err != nil {
		t.Fatal(err)
	}
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	if got := h.comm.count(); got != 2 {
		t.Fatalf("two source items produced %d deliveries", got)
	}
	occ := commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}}
	state, _, err := host.events.load(occ)
	if err != nil || len(state.Seen) != 2 || state.Cursor != "3" {
		t.Fatalf("event identities or cursor not checkpointed: %+v, %v", state, err)
	}
	if err := host.Tick(context.Background(), h.now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := h.comm.count(); got != 2 {
		t.Fatalf("replayed source items produced %d deliveries", got)
	}
}

func TestEventCursorFeedsNextProbeWithoutMutatingSchedule(t *testing.T) {
	s := &eventStore{dir: t.TempDir()}
	o := commodel.RoutineOccurrence{ScheduleID: "cursor", Job: commodel.Job{Envelope: identity.Envelope{PrincipalID: "alice"}}}
	_, path, err := s.load(o)
	if err != nil || writeJSONAtomic(path, eventState{ScheduleID: o.ScheduleID, Cursor: "next-42"}) != nil {
		t.Fatal(err)
	}
	probe := &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.read", CursorArg: "after", Args: map[string]any{"limit": 5}}
	_, err = s.acquire(context.Background(), o, commodel.Schedule{Probe: probe}, func(_ context.Context, principal string, call *commodel.WatchProbe) ([]byte, error) {
		if principal != "alice" || call.Args["after"] != "next-42" || call.Args["limit"] != 5 {
			t.Fatalf("cursor not fed to owner probe: principal=%q args=%v", principal, call.Args)
		}
		return []byte(`{"events":[]}`), nil
	})
	if err != nil || probe.Args["after"] != nil {
		t.Fatalf("probe mutated or rejected: %v, args=%v", err, probe.Args)
	}
}

func TestMultiSourceAcquisitionKeepsCursorsAndEventNamespaces(t *testing.T) {
	s := &eventStore{dir: t.TempDir()}
	o := commodel.RoutineOccurrence{ScheduleID: "multi", Job: commodel.Job{Envelope: identity.Envelope{PrincipalID: "alice"}}}
	_, path, err := s.load(o)
	if err != nil || writeJSONAtomic(path, eventState{ScheduleID: o.ScheduleID, SourceCursors: map[string]string{"mail": "10", "tasks": "20"}}) != nil {
		t.Fatal(err)
	}
	probe := &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.read", CursorArg: "after", Args: map[string]any{"shared": true}, Sources: []commodel.WatchProbeSource{{Name: "mail", Args: map[string]any{"kind": "mail"}}, {Name: "tasks", Tool: "tasks.read", Args: map[string]any{"kind": "task"}}}}
	body, err := s.acquire(context.Background(), o, commodel.Schedule{Probe: probe}, func(_ context.Context, principal string, call *commodel.WatchProbe) ([]byte, error) {
		if principal != "alice" || call.Args["shared"] != true || len(call.Sources) != 0 {
			t.Errorf("source probe lost common binding: %+v", call)
		}
		switch call.Tool {
		case "events.read":
			if call.Args["after"] != "10" || call.Args["kind"] != "mail" {
				t.Errorf("mail cursor/args: %v", call.Args)
			}
			return []byte(`{"events":[{"id":"same","text":"mail event"}],"next_cursor":"11"}`), nil
		case "tasks.read":
			if call.Args["after"] != "20" || call.Args["kind"] != "task" {
				t.Errorf("task cursor/args: %v", call.Args)
			}
			return []byte(`{"events":[{"id":"same","text":"task event"}],"next_cursor":"21"}`), nil
		default:
			return nil, errors.New("unexpected source")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	events, _, cursors, sourceErrors, err := extractSourceBatch(probe, body)
	if err != nil || len(events) != 2 || events[0].ID != "mail|same" || events[1].ID != "tasks|same" || cursors["mail"] != "11" || cursors["tasks"] != "21" || len(sourceErrors) != 0 {
		t.Fatalf("multi-source shape: events=%+v cursors=%v errors=%v err=%v", events, cursors, sourceErrors, err)
	}
	if probe.Args["after"] != nil || probe.Args["kind"] != nil {
		t.Fatalf("source call mutated authored probe: %+v", probe.Args)
	}
}

func TestEventToolCallsBoundedAcrossStreams(t *testing.T) {
	s := &eventStore{dir: t.TempDir()}
	probe := &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait", Sources: make([]commodel.WatchProbeSource, 32)}
	var active, peak atomic.Int32
	call := func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		n := active.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		return []byte(`{"events":[]}`), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o := commodel.RoutineOccurrence{ScheduleID: fmt.Sprintf("stream-%d", i), Job: commodel.Job{Envelope: identity.Envelope{PrincipalID: "alice"}}}
			if _, err := s.acquire(context.Background(), o, commodel.Schedule{Probe: probe}, call); err != nil {
				t.Errorf("stream %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if peak.Load() > 16 || active.Load() != 0 {
		t.Fatalf("source concurrency escaped global bound: peak=%d active=%d", peak.Load(), active.Load())
	}
}

func TestStreamTickFansOutOnceAndRecoversCursor(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	var calls atomic.Int32
	host.cfg.ToolCall = func(_ context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		calls.Add(1)
		if principal != "alice" || probe.Args["after"] != nil {
			t.Errorf("unexpected first source call: owner=%q args=%v", principal, probe.Args)
		}
		return []byte(`{"events":[{"id":"same","text":"one"}],"next_cursor":"1"}`), nil
	}
	sc := draft("rt-stream", alice)
	sc.JobKind, sc.Delivery, sc.Sink = "event", "stream", "notify"
	sc.Expression = "0 10 * * *"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait", CursorArg: "after", Sources: []commodel.WatchProbeSource{{Name: "mail"}, {Name: "tasks", Tool: "tasks.wait"}}}
	if _, err := host.CreateSchedule(context.Background(), alice, sc, ""); err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	if got := h.comm.count(); got != 2 || calls.Load() != 2 {
		t.Fatalf("stream fanout: deliveries=%d calls=%d", got, calls.Load())
	}
	restarted := h.host(t)
	restarted.cfg.ToolCall = func(_ context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		if probe.Args["after"] != "1" {
			t.Errorf("cursor lost after restart for %s: %v", probe.Tool, probe.Args)
		}
		return []byte(`{"events":[],"next_cursor":"1"}`), nil
	}
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(context.Background(), h.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := h.comm.count(); got != 2 {
		t.Fatalf("restart duplicated source events: %d", got)
	}
}

func TestStreamPauseAndCursorGap(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	var calls atomic.Int32
	host.cfg.ToolCall = func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"cursor_expired":true,"events":[]}`), nil
	}
	sc := draft("rt-gap", alice)
	sc.JobKind, sc.Delivery, sc.Sink = "event", "stream", "notify"
	sc.Expression = "0 10 * * *"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait", CursorArg: "after"}
	if _, err := host.CreateSchedule(context.Background(), alice, sc, ""); err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), h.now); err == nil {
		t.Fatal("expired cursor should report an event gap")
	}
	state, _, err := host.events.load(commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}})
	if err != nil || state.GapCount != 1 || state.Cursor != "" || calls.Load() != 1 {
		t.Fatalf("cursor gap not recorded: state=%+v calls=%d err=%v", state, calls.Load(), err)
	}
	if err := host.PauseSchedule(context.Background(), alice, sc.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), h.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("paused stream polled %d times", calls.Load())
	}
}

func TestAgentEventBatchAdmitsOneUntrustedJob(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	host.cfg.ToolCall = func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"one","text":"first"},{"id":"two","text":"second"}],"next_cursor":"2"}`), nil
	}
	sc := draft("rt-agent-events", alice)
	sc.JobKind, sc.Delivery, sc.Sink = "event", "stream", "agent"
	sc.Expression = "0 10 * * *"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait", CursorArg: "after"}
	if _, err := host.CreateSchedule(context.Background(), alice, sc, ""); err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	completed, err := host.queue.completed()
	if err != nil || len(completed) != 1 || !completed[0].Job.Untrusted || !completed[0].Job.Probe || h.comm.count() != 1 {
		t.Fatalf("agent batch was not one guarded job: intents=%+v deliveries=%d err=%v", completed, h.comm.count(), err)
	}
	if err := host.Tick(context.Background(), h.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, logical := h.sup.counts(); logical != 1 {
		t.Fatalf("source replay caused %d logical agent jobs", logical)
	}
}

// The assignment wake bundle is model-facing: it pins the assignment contract
// (intent, specialist instructions, progress verb) and the owner's stored
// language like the per-run instructions do (ADR-0084 D5).
func TestAssignmentWakeBundleCarriesOwnerContractAndLanguage(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	host.cfg.LangOf = func(string) locale.Lang { return locale.LangES }
	host.cfg.ToolCall = func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"e1","text":"one"}]}`), nil
	}
	ctx := context.Background()
	if _, _, err := host.CreateAssignment(ctx, alice, commodel.Assignment{
		AssignmentID: "asg-1", Envelope: alice, Intent: "watch prices", Title: "prices",
		State: "active", AppliedRev: 1, Plan: &commodel.AssignmentPlan{Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}
	sc := draft("rt-asg-wake", alice)
	sc.JobKind, sc.Delivery, sc.Sink = "event", "stream", "agent"
	sc.Expression = "0 10 * * *"
	sc.AssignmentID, sc.AssignmentPlanRevision, sc.WakePrompt = "asg-1", 1, "report changes"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait"}
	if _, err := host.CreateSchedule(ctx, alice, sc, ""); err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(ctx, h.now); err != nil {
		t.Fatal(err)
	}
	job := h.sup.onlyJob()
	for _, want := range []string{
		"Standing assignment asg-1 (revision 1)",
		"Owner intent: watch prices",
		"Specialist instructions: report changes",
		"call assignment_read then assignment_edit action=progress",
		"language is Español (es)",
		"untrusted event evidence",
	} {
		if !strings.Contains(job.Text, want) {
			t.Fatalf("wake bundle missing %q: %q", want, job.Text)
		}
	}
	if strings.Contains(job.Text, "call assignment_progress") || strings.Contains(job.Text, "call assignment_get") {
		t.Fatal("wake bundle instructs retired assignment tools")
	}
}

func TestAssignmentEventWakeBudgetSurvivesLostAdmissionAck(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	body := `{"events":[{"id":"first","text":"one"}]}`
	host.cfg.ToolCall = func(context.Context, string, *commodel.WatchProbe) ([]byte, error) { return []byte(body), nil }
	ctx := context.Background()
	if _, _, err := host.CreateAssignment(ctx, alice, commodel.Assignment{AssignmentID: "asg-budget", Envelope: alice, Intent: "watch", State: "active", AppliedRev: 1, Plan: &commodel.AssignmentPlan{Revision: 1}, Grants: commodel.AssignmentGrants{MaxWakesPerDay: 1}}); err != nil {
		t.Fatal(err)
	}
	sc := draft("rt-assignment-budget", alice)
	sc.JobKind, sc.Delivery, sc.Sink = "event", "stream", "agent"
	sc.AssignmentID, sc.AssignmentPlanRevision = "asg-budget", 1
	sc.Expression = "0 10 * * *"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait"}
	if _, err := host.CreateSchedule(ctx, alice, sc, ""); err != nil {
		t.Fatal(err)
	}
	admit := host.events.admit
	host.events.admit = func(job commodel.Job, source string) (bool, error) {
		if job.AssignmentID != "asg-budget" || job.AssignmentPlanRevision != 1 || !job.Untrusted || !job.Probe {
			t.Errorf("event wake lost assignment provenance: id=%q revision=%d untrusted=%t probe=%t", job.AssignmentID, job.AssignmentPlanRevision, job.Untrusted, job.Probe)
		}
		_, err := admit(job, source)
		if err != nil {
			return false, err
		}
		return false, errors.New("lost admission acknowledgement")
	}
	if err := host.Tick(ctx, h.now); err == nil {
		t.Fatal("lost acknowledgement was not observed")
	}
	a, err := host.LoadAssignment(ctx, alice, "asg-budget")
	if err != nil || a.Checkpoint.WakesToday != 1 || a.Checkpoint.LLMCalls != 1 {
		t.Fatalf("event wake bypassed assignment budget: wakes=%d calls=%d err=%v", a.Checkpoint.WakesToday, a.Checkpoint.LLMCalls, err)
	}
	restarted := h.host(t)
	restarted.cfg.ToolCall = host.cfg.ToolCall
	if err := restarted.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(ctx, h.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	body = `{"events":[{"id":"second","text":"two"}]}`
	if err := restarted.Tick(ctx, h.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	a, err = restarted.LoadAssignment(ctx, alice, "asg-budget")
	_, logical := h.sup.counts()
	st, _, stateErr := restarted.events.load(commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}})
	if err != nil || stateErr != nil || logical != 1 || a.Checkpoint.WakesToday != 1 || a.Checkpoint.LLMCalls != 1 || st.Pending == nil || len(st.Seen) != 1 {
		t.Fatalf("retry or next event escaped wake cap: jobs=%d wakes=%d calls=%d pending=%t seen=%d err=%v stateErr=%v", logical, a.Checkpoint.WakesToday, a.Checkpoint.LLMCalls, st.Pending != nil, len(st.Seen), err, stateErr)
	}
}

func TestAgentEventBatchReplaysPlannedJobsAfterAdmissionFailure(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	items := make([]map[string]string, 9)
	for i := range items {
		items[i] = map[string]string{"id": fmt.Sprintf("id-%d", i), "text": strings.Repeat("x", 1000)}
	}
	response, _ := json.Marshal(map[string]any{"events": items, "next_cursor": "9"})
	host.cfg.ToolCall = func(context.Context, string, *commodel.WatchProbe) ([]byte, error) { return response, nil }
	sc := draft("rt-agent-replay", alice)
	sc.JobKind, sc.Delivery, sc.Sink = "event", "stream", "agent"
	sc.Expression = "0 10 * * *"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait", CursorArg: "after"}
	if _, err := host.CreateSchedule(context.Background(), alice, sc, ""); err != nil {
		t.Fatal(err)
	}
	actualAdmit := host.events.admit
	calls := 0
	host.events.admit = func(job commodel.Job, sourceID string) (bool, error) {
		calls++
		if calls == 1 {
			return false, errors.New("injected admission outage")
		}
		return actualAdmit(job, sourceID)
	}
	if err := host.Tick(context.Background(), h.now); err == nil {
		t.Fatal("agent admission failure was not reported")
	}
	st, _, err := host.events.load(commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}})
	if err != nil || st.Pending == nil || len(st.Pending.AgentJobs) != 2 || st.Cursor != "" {
		t.Fatalf("agent plan not durable before admission: %+v, %v", st, err)
	}
	restarted := h.host(t)
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	st, _, err = restarted.events.load(commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}})
	_, logical := h.sup.counts()
	if err != nil || st.Pending == nil || st.Cursor != "" || st.Overflow != 1 || logical != 1 || h.comm.count() != 1 {
		t.Fatalf("first agent wake did not checkpoint remaining event: state=%+v logical=%d deliveries=%d err=%v", st, logical, h.comm.count(), err)
	}
	if err := restarted.Tick(context.Background(), h.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	st, _, err = restarted.events.load(commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}})
	_, logical = h.sup.counts()
	if err != nil || st.Pending != nil || st.Cursor != "9" || logical != 2 || h.comm.count() != 2 {
		t.Fatalf("remaining agent event was lost: state=%+v logical=%d deliveries=%d err=%v", st, logical, h.comm.count(), err)
	}
}

func TestStreamDailyCapLeavesExcessEventUnseen(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	polls := 0
	host.cfg.ToolCall = func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		polls++
		if polls != 1 {
			return nil, errors.New("source receipt must drain before repoll")
		}
		return []byte(`{"events":[{"id":"one","text":"first"},{"id":"two","text":"second"}],"next_cursor":"2"}`), nil
	}
	sc := draft("rt-daily-cap", alice)
	sc.JobKind, sc.Delivery, sc.Sink, sc.DailyLimit = "event", "stream", "notify", 1
	sc.Expression = "0 10 * * *"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.wait", CursorArg: "after"}
	if _, err := host.CreateSchedule(context.Background(), alice, sc, ""); err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	st, _, err := host.events.load(commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}})
	if err != nil || h.comm.count() != 1 || st.NotifyCount != 1 || len(st.Seen) != 1 || st.Pending == nil || st.Cursor != "" {
		t.Fatalf("daily cap failed: state=%+v deliveries=%d err=%v", st, h.comm.count(), err)
	}
	h.now = h.now.Add(24 * time.Hour)
	h.grant(t, "alice", "write")
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	st, _, err = host.events.load(commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}})
	if err != nil || h.comm.count() != 2 || polls != 1 || st.Pending != nil || st.Cursor != "2" {
		t.Fatalf("saved event did not drain on next local day: state=%+v deliveries=%d polls=%d err=%v", st, h.comm.count(), polls, err)
	}
}

func TestEventProbeRevisionDoesNotDiscardPendingBatch(t *testing.T) {
	st := eventState{ScheduleID: "source", ProbeRevision: 1, Cursor: "old", Seen: []string{"one"}, GapCount: 2, Pending: &eventBatch{JobID: "job", Events: []commodel.Event{{ID: "two"}}}}
	schedule := commodel.Schedule{ScheduleID: "source", ProbeRevision: 2}
	if err := rebindEventState(&st, schedule); err == nil || st.Pending == nil || st.Cursor != "old" {
		t.Fatalf("probe change silently discarded unfinished source batch: %+v err=%v", st, err)
	}
	st.Pending = nil
	if err := rebindEventState(&st, schedule); err != nil || st.ProbeRevision != 2 || st.Cursor != "" || len(st.Seen) != 0 || st.GapCount != 2 {
		t.Fatalf("completed source did not reset on probe change: %+v err=%v", st, err)
	}
}

func TestRecoverySubmitsAllPendingJobsForOnePrincipal(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	first, err := host.CreateSchedule(context.Background(), alice, draft("rt-first", alice), "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.CreateSchedule(context.Background(), alice, draft("rt-second", alice), "")
	if err != nil {
		t.Fatal(err)
	}
	h.sup.fail.Store(true)
	h.now = first.NextDue
	if second.NextDue.After(h.now) {
		h.now = second.NextDue
	}
	h.now = h.now.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	if pendingDispatches(t, host, "alice") != 2 {
		t.Fatal("two due jobs were not durable before supervisor recovery")
	}
	h.sup.fail.Store(false)
	restarted := h.host(t)
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, logical := h.sup.counts(); logical != 2 || pendingDispatches(t, restarted, "alice") != 0 || h.comm.count() != 2 {
		t.Fatalf("one-principal recovery lost a job: logical=%d pending=%d deliveries=%d", logical, pendingDispatches(t, restarted, "alice"), h.comm.count())
	}
}

func TestTerminalFailureUsesOwnerLanguage(t *testing.T) {
	h := newHarness(t)
	workspace := filepath.Join(h.root, "workspace", "alice")
	if err := locale.StorePrefs(workspace, locale.Prefs{Lang: locale.LangRU, Source: locale.SourceChosen}); err != nil {
		t.Fatal(err)
	}
	alice := h.envelope(t, "alice")
	host := h.host(t)
	job := commodel.Job{Envelope: alice, ID: "localized-job", IdempotencyKey: "localized-job", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Trigger: "cron", Text: "work", CreatedAt: h.now}
	if _, err := host.admitJob(job); err != nil {
		t.Fatal(err)
	}
	if err := host.ReportResult(context.Background(), "alice", job.ID, "failed", "raw internal error"); err != nil {
		t.Fatal(err)
	}
	if err := locale.StorePrefs(workspace, locale.Prefs{Lang: locale.LangEN, Source: locale.SourceChosen}); err != nil {
		t.Fatal(err)
	}
	if err := host.ReportResult(context.Background(), "alice", job.ID, "failed", "raw internal error"); err != nil {
		t.Fatalf("terminal replay changed with locale preference: %v", err)
	}
	h.comm.mu.Lock()
	defer h.comm.mu.Unlock()
	if len(h.comm.deliveries) != 1 || h.comm.deliveries[0].Text != locale.T(locale.LangRU, "job.failed", job.ID) {
		t.Fatalf("terminal message ignored owner language: %+v", h.comm.deliveries)
	}
}

func TestMCPEventFilterDropsBeforeDelivery(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	host.cfg.ToolCall = func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"private","text":"private event"}]}`), nil
	}
	sc := draft("rt-filter", alice)
	sc.JobKind = "event"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.read", Filter: `emit("drop")`}
	item, err := host.CreateSchedule(context.Background(), alice, sc, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), item.NextDue.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := h.comm.count(); got != 0 {
		t.Fatalf("dropped source event delivered %d times", got)
	}
	state, _, err := host.events.load(commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}})
	if err != nil || len(state.Seen) != 1 || state.Seen[0] != "private" {
		t.Fatalf("drop not checkpointed: %+v, %v", state, err)
	}
}

func TestPendingEventBatchDrainsAfterRestart(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	host.cfg.ToolCall = func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"one","text":"first"},{"id":"two","text":"second"}],"next_cursor":"2"}`), nil
	}
	sc := draft("rt-pending", alice)
	sc.JobKind = "event"
	sc.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "events.read"}
	item, err := host.CreateSchedule(context.Background(), alice, sc, "")
	if err != nil {
		t.Fatal(err)
	}
	actualDeliver := host.events.deliver
	host.events.deliver = func(principal string, d commodel.Delivery, now time.Time) error {
		if d.Text == "work rt-pending\nsecond" {
			return errors.New("injected handoff outage")
		}
		return actualDeliver(principal, d, now)
	}
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err == nil {
		t.Fatal("first drain should stop after partial handoff")
	}
	o := commodel.RoutineOccurrence{ScheduleID: sc.ScheduleID, Job: commodel.Job{Envelope: alice}}
	state, _, err := host.events.load(o)
	if err != nil || state.Pending == nil || state.Cursor != "" {
		t.Fatalf("source batch or cursor moved before drain: %+v, %v", state, err)
	}
	restarted := h.host(t)
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	state, _, err = restarted.events.load(o)
	if err != nil || state.Pending != nil || state.Cursor != "2" || len(state.Seen) != 2 || h.comm.count() != 2 {
		t.Fatalf("pending source batch did not drain once: %+v deliveries=%d err=%v", state, h.comm.count(), err)
	}
}

func TestScriptOccurrenceUsesSupervisorContract(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	sc := draft("rt-script", alice)
	sc.JobKind, sc.Input = "script", `emit("script result")`
	item, err := host.CreateSchedule(context.Background(), alice, sc, "")
	if err != nil {
		t.Fatal(err)
	}
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	if h.comm.count() != 1 {
		t.Fatal("script output did not reach the delivery outbox")
	}
}

// A right revoked between admission and the due tick stops execution: the
// background gate re-decides on the persisted job envelope, so the schedule
// is blocked rather than dispatched and no job reaches the supervisor.
func TestRevokedRightStopsDueDispatch(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")
	host := h.host(t)
	ctx := context.Background()

	item, err := host.CreateSchedule(ctx, alice, draft("rt-revoke", alice), "")
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	h.revoke(t, "alice", "write")
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(ctx, h.now); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, logical := h.sup.counts(); logical != 0 {
		t.Fatalf("revoked principal dispatched %d jobs, want 0", logical)
	}
}

func TestPendingIntentRechecksRightAndPause(t *testing.T) {
	for _, fence := range []string{"revoke", "pause"} {
		t.Run(fence, func(t *testing.T) {
			h := newHarness(t)
			alice := h.envelope(t, "alice")
			h.grant(t, "alice", "write")
			host := h.host(t)
			item, err := host.CreateSchedule(context.Background(), alice, draft("rt-fence", alice), "")
			if err != nil {
				t.Fatal(err)
			}
			h.sup.fail.Store(true)
			h.now = item.NextDue.Add(time.Second)
			if err := host.Tick(context.Background(), h.now); err != nil {
				t.Fatal(err)
			}
			if pendingDispatches(t, host, "alice") != 1 {
				t.Fatal("expected a durable intent before the fence")
			}
			if fence == "revoke" {
				h.revoke(t, "alice", "write")
			} else if err := host.PauseSchedule(context.Background(), alice, item.ScheduleID); err != nil {
				t.Fatal(err)
			}
			h.sup.fail.Store(false)
			if err := host.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, logical := h.sup.counts(); logical != 0 {
				t.Fatalf("%s did not fence pending dispatch: %d jobs", fence, logical)
			}
		})
	}
}

func TestTaskPortListsOnlyOwnerAndRestoresTopic(t *testing.T) {
	s := &taskStore{dir: t.TempDir()}
	for _, task := range []commodel.Task{
		{TaskID: "a", PrincipalID: "alice", ChatID: 11, TopicID: 7},
		{TaskID: "b", PrincipalID: "bob", ChatID: 11, TopicID: 7},
	} {
		if err := s.put(task); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := s.list("alice")
	if err != nil || len(tasks) != 1 || tasks[0].TaskID != "a" {
		t.Fatalf("owner task list: %+v, %v", tasks, err)
	}
	if id, err := s.topic("alice", 11, 7); err != nil || id != "a" {
		t.Fatalf("owner topic binding: %q, %v", id, err)
	}
	if id, err := s.topic("bob", 11, 7); err != nil || id != "b" {
		t.Fatalf("other owner topic binding: %q, %v", id, err)
	}
}

func TestTaskBindingPrecedesDispatchIntent(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	item, err := host.CreateSchedule(context.Background(), alice, draft("rt-task-failure", alice), "")
	if err != nil {
		t.Fatal(err)
	}
	at := item.NextDue.Add(time.Second)
	engine, err := host.engineFor("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.TickSchedules(at, "", func(_ string, job commodel.Job) error { return host.authorizeJob(job) }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(h.root, "users", "alice", "occurrences"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one due occurrence: %d, %v", len(entries), err)
	}
	var occurrence commodel.RoutineOccurrence
	if err := loadJSON(filepath.Join(h.root, "users", "alice", "occurrences", entries[0].Name()), &occurrence); err != nil {
		t.Fatal(err)
	}
	taskPath := host.tasks.path("alice", occurrence.Job.ID)
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), at); err == nil {
		t.Fatal("dispatch ignored a failed task binding")
	}
	if pendingDispatches(t, host, "alice") != 0 {
		t.Fatal("dispatch intent became visible before task binding")
	}
	if _, logical := h.sup.counts(); logical != 0 {
		t.Fatal("supervisor received a job without a durable task")
	}
	if body, err := os.ReadFile(taskPath); err != nil || string(body) != "corrupt" {
		t.Fatalf("failed binding was overwritten: %q, %v", body, err)
	}
}

func TestTaskBindingReplayPreservesExistingRecord(t *testing.T) {
	s := &taskStore{dir: t.TempDir()}
	original := commodel.Task{TaskID: "task-1", PrincipalID: "alice", ContextID: "ctx", Channel: "telegram_bot", ChatID: 11, Name: "owner title"}
	if err := s.put(original); err != nil {
		t.Fatal(err)
	}
	replay := original
	replay.Name = "cron"
	if err := s.put(replay); err != nil {
		t.Fatal(err)
	}
	got, err := s.load("alice", "task-1")
	if err != nil || got.Name != original.Name {
		t.Fatalf("replay replaced existing task: %+v, %v", got, err)
	}
	replay.ChatID = 22
	if err := s.put(replay); err == nil {
		t.Fatal("changed task routing was accepted")
	}
}

func TestDispatchMappingIsPrincipalScoped(t *testing.T) {
	q := &dispatchQueue{dir: t.TempDir()}
	for _, principal := range []string{"alice", "bob"} {
		job := commodel.Job{Envelope: identity.TelegramEnvelope(principal, 11, "rt-"+principal, "policy-1"), ID: "same", IdempotencyKey: "same", UserID: principal}
		if _, err := q.admit(job, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, principal := range []string{"alice", "bob"} {
		mapping, err := q.loadMapping(principal, "same")
		if err != nil || mapping.PrincipalID != principal {
			t.Fatalf("%s mapping resolved to %s: %v", principal, mapping.PrincipalID, err)
		}
	}
}

func TestCorruptIntentIsNotOverwritten(t *testing.T) {
	q := &dispatchQueue{dir: t.TempDir()}
	job := commodel.Job{Envelope: identity.TelegramEnvelope("alice", 11, "rt-alice", "policy-1"), ID: "same", IdempotencyKey: "same", UserID: "alice"}
	path := q.intentPath("alice", "same")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := q.admit(job, time.Now()); err == nil {
		t.Fatal("corrupt durable intent must stop admission")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "broken" {
		t.Fatalf("corrupt record was replaced: %q, %v", body, err)
	}
}

func TestResultUsesAdmittedTaskBinding(t *testing.T) {
	h := newHarness(t)
	host := h.host(t)
	job := commodel.Job{Envelope: h.envelope(t, "alice"), ID: "job-1", IdempotencyKey: "occurrence-1", UserID: "alice", TaskID: "task-1", Channel: "telegram_bot", TopicID: 7}
	if _, err := host.queue.admit(job, h.now); err != nil {
		t.Fatal(err)
	}
	if err := host.tasks.put(commodel.Task{TaskID: "task-1", PrincipalID: "alice", Channel: "telegram_bot", TopicID: 7}); err != nil {
		t.Fatal(err)
	}
	h.comm.down.Store(true)
	if err := host.ReportResult(context.Background(), "alice", "job-1", "completed", "done"); err != nil {
		t.Fatal(err)
	}
	if pendingDeliveries(t, host, "alice") != 1 {
		t.Fatal("bound task result did not persist delivery")
	}
	items, err := host.outbox.pending()
	if err != nil || len(items) != 1 || items[0].Delivery.ID != "result-job-1" || items[0].Delivery.TopicID != 7 {
		t.Fatalf("result lost delivery identity or task binding: %+v, %v", items, err)
	}
	if err := host.ReportResult(context.Background(), "alice", "job-1", "completed", "done"); err != nil {
		t.Fatalf("stable result replay failed: %v", err)
	}
}

func TestDeliveryOutboxRejectsConflictingReplay(t *testing.T) {
	o := &deliveryOutbox{dir: t.TempDir()}
	if _, err := o.enqueue("alice", commodel.Delivery{ID: "one", IdempotencyKey: "same", Text: "first"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := o.enqueue("alice", commodel.Delivery{ID: "two", IdempotencyKey: "same", Text: "second"}, time.Now()); err == nil {
		t.Fatal("different delivery reused a durable idempotency key")
	}
	if _, err := o.enqueue("alice", commodel.Delivery{IdempotencyKey: "missing-id"}, time.Now()); err == nil {
		t.Fatal("delivery without an outbox ID was accepted")
	}
	items, err := o.pending()
	if err != nil || len(items) != 1 || items[0].Delivery.Text != "first" {
		t.Fatalf("conflict changed the original delivery: %+v, %v", items, err)
	}
}

func TestResultCheckpointRestoresDeliveryAfterRestart(t *testing.T) {
	h := newHarness(t)
	host := h.host(t)
	job := commodel.Job{Envelope: h.envelope(t, "alice"), ID: "job-1", IdempotencyKey: "job-1", UserID: "alice", Channel: "telegram_bot"}
	if _, err := host.queue.admit(job, h.now); err != nil {
		t.Fatal(err)
	}
	if err := host.queue.markAcked("alice", "job-1"); err != nil {
		t.Fatal(err)
	}
	if err := host.tasks.put(commodel.Task{TaskID: "job-1", PrincipalID: "alice", Channel: "telegram_bot"}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.queue.recordResult("alice", "job-1", "completed", "done", h.now); err != nil {
		t.Fatal(err)
	}
	h.comm.down.Store(true)
	restarted := h.host(t)
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pendingDeliveries(t, restarted, "alice") != 1 {
		t.Fatal("terminal result checkpoint did not recreate delivery intent")
	}
	if err := restarted.ReportResult(context.Background(), "alice", "job-1", "failed", "different"); err == nil {
		t.Fatal("conflicting result replay changed terminal result")
	}
}

func TestUnimplementedEventFilterFailsClosed(t *testing.T) {
	events := &eventStore{dir: t.TempDir(), deliver: func(string, commodel.Delivery, time.Time) error {
		t.Fatal("unfiltered event was delivered")
		return nil
	}}
	_, err := events.settle(nil, commodel.RoutineOccurrence{}, commodel.Schedule{Probe: &commodel.WatchProbe{Filter: `emit("drop")`}}, []byte("private event"), time.Now(), nil, nil)
	if err == nil {
		t.Fatal("missing event filter executor must fail closed")
	}
}

func TestRecoverRejectsCorruptDurableIntents(t *testing.T) {
	for _, kind := range []string{"dispatch", "delivery"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			host := h.host(t)
			path := host.queue.intentPath("alice", "job-1")
			if kind == "delivery" {
				path = host.outbox.recordPath("alice", "delivery-1")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := host.Recover(context.Background()); err == nil {
				t.Fatal("recovery silently skipped a corrupt durable intent")
			}
			if body, err := os.ReadFile(path); err != nil || string(body) != "broken" {
				t.Fatalf("recovery changed corrupt record: %q, %v", body, err)
			}
		})
	}
}

// Recovery reads the engine's own durable layout, not a host-private
// directory: a schedule written by a bare routines.Engine at the per-user
// root must be visible to a recovered host.
func TestRecoverReadsEngineLayout(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "read")
	root := filepath.Join(h.root, "users", "alice")
	for _, dir := range []string{"occurrences", "schedules"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	eng := routines.NewEngine(root, routines.Deps{
		Hooks: routines.DraftHooks{
			Channel: func(in commodel.Schedule) error {
				if in.Channel != "telegram_bot" {
					return errors.New("unknown channel")
				}
				return nil
			},
			Script: routines.ScriptSyntaxError,
		},
		Storage: routines.StoragePort{
			WriteJSONAtomic: writeJSONAtomic,
			WriteBytes:      func(path string, body []byte) error { return os.WriteFile(path, body, 0o600) },
			MkdirAll:        func(path string, _ bool) error { return os.MkdirAll(path, 0o700) },
			Rename:          os.Rename,
			Remove:          os.Remove,
			ReadFile:        os.ReadFile,
			ReadDir:         readDirNames,
			Lstat:           os.Lstat,
		},
	})
	if _, err := eng.CreateSchedule(draft("rt-foreign", alice), alice, ""); err != nil {
		t.Fatalf("seed engine: %v", err)
	}
	host := h.host(t)
	if err := host.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	list, err := host.ListSchedules(context.Background(), alice)
	if err != nil || len(list) != 1 || list[0].ScheduleID != "rt-foreign" {
		t.Fatalf("recovered host lost the engine-written schedule: %+v err=%v", list, err)
	}
}

func TestCutoverReadsOwnerRecordsInPlace(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "read")
	owners := filepath.Join(h.root, "existing-owners")
	root := filepath.Join(owners, "alice")
	for _, dir := range []string{"occurrences", "schedules", "assignments", "watchers", "digests"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(owners, "bob"), 0o700); err != nil {
		t.Fatal(err)
	}
	item := draft("rt-existing", alice)
	path := filepath.Join(root, "schedules", item.ScheduleID+".json")
	if err := writeJSONAtomic(path, item); err != nil {
		t.Fatal(err)
	}
	assignment := commodel.Assignment{AssignmentID: "as-existing", Envelope: alice, State: "paused", Revision: 3}
	if err := writeJSONAtomic(filepath.Join(root, "assignments", "as-existing.json"), assignment); err != nil {
		t.Fatal(err)
	}
	watch := routines.WatchState{ScheduleID: item.ScheduleID, ProbeRevision: item.ProbeRevision, Baseline: "existing-baseline", Value: "existing-value"}
	if err := writeJSONAtomic(filepath.Join(root, "watchers", item.ScheduleID+".json"), watch); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	host := h.hostWithOwners(t, owners)
	if err := host.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := host.ListSchedules(context.Background(), alice)
	if err != nil || len(got) != 1 || got[0].ScheduleID != item.ScheduleID {
		t.Fatalf("existing schedule not visible: %+v, %v", got, err)
	}
	if assignments, err := host.ListAssignments(context.Background(), alice); err != nil || len(assignments) != 1 || assignments[0].AssignmentID != assignment.AssignmentID {
		t.Fatalf("existing assignment not visible: %+v, %v", assignments, err)
	}
	if state, err := host.LoadWatchState(context.Background(), alice, item.ScheduleID); err != nil || state.Value != watch.Value {
		t.Fatalf("existing watch state not visible: %+v, %v", state, err)
	}
	bob := h.envelope(t, "bob")
	h.grant(t, "bob", "read")
	if got, err := host.ListSchedules(context.Background(), bob); err != nil || len(got) != 0 {
		t.Fatalf("new owner saw another owner's records: %+v, %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("cutover changed existing record: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "users")); !os.IsNotExist(err) {
		t.Fatalf("cutover created a parallel owner root: %v", err)
	}
}

func TestCutoverRejectsActivatedLayoutWithoutChangingRecords(t *testing.T) {
	h := newHarness(t)
	owners := filepath.Join(h.root, "existing-owners")
	root := filepath.Join(owners, "alice")
	for _, dir := range []string{"occurrences", "schedules"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "records.activated")
	if err := os.WriteFile(path, []byte("existing marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := h.hostWithOwners(t, owners)
	if err := host.Recover(context.Background()); err == nil {
		t.Fatal("activated layout was silently accepted")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "existing marker" {
		t.Fatalf("rejected layout was changed: %v", err)
	}
}

func TestCutoverRequiresOwnerMount(t *testing.T) {
	h := newHarness(t)
	owners := filepath.Join(h.root, "missing-owners")
	host := h.hostWithOwners(t, owners)
	if err := host.Recover(context.Background()); !os.IsNotExist(err) {
		t.Fatalf("missing owner mount did not fail startup: %v", err)
	}
	if _, err := os.Stat(owners); !os.IsNotExist(err) {
		t.Fatalf("startup created missing owner mount: %v", err)
	}
}

func TestCutoverRequiresEveryEnrolledOwnerBeforeWriting(t *testing.T) {
	h := newHarness(t)
	owners := filepath.Join(h.root, "existing-owners")
	alice := filepath.Join(owners, "alice")
	if err := os.MkdirAll(alice, 0o700); err != nil {
		t.Fatal(err)
	}
	host := h.hostWithOwners(t, owners)
	host.cfg.RequiredPrincipals = []string{"alice", "bob"}
	if err := host.ValidateStorage(); err == nil {
		t.Fatal("missing enrolled owner was accepted")
	}
	if err := host.Recover(context.Background()); err == nil {
		t.Fatal("recovery ignored missing enrolled owner")
	}
	if _, err := os.Stat(filepath.Join(alice, "schedules")); !os.IsNotExist(err) {
		t.Fatalf("recovery wrote before checking every owner: %v", err)
	}
}

func TestCutoverRejectsSharedLegacySpoolBeforeWriting(t *testing.T) {
	h := newHarness(t)
	owners := filepath.Join(h.root, "existing-owners")
	alice := filepath.Join(owners, "alice")
	if err := os.MkdirAll(alice, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(h.root, "legacy-spool")
	if err := os.MkdirAll(filepath.Join(legacy, "schedules"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(legacy, "schedules", "rt-existing.json")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := h.hostWithOwners(t, owners)
	host.cfg.LegacySpoolDir = legacy
	if err := host.Recover(context.Background()); err == nil {
		t.Fatal("shared legacy automation records were accepted")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "existing" {
		t.Fatalf("legacy record changed: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(alice, "schedules")); !os.IsNotExist(err) {
		t.Fatalf("recovery wrote before checking legacy spool: %v", err)
	}
}

func TestCutoverRejectsLegacyEventStateBeforeCreatingOwnerClasses(t *testing.T) {
	h := newHarness(t)
	owners := filepath.Join(h.root, "existing-owners")
	alice := filepath.Join(owners, "alice")
	bob := filepath.Join(owners, "bob")
	if err := os.MkdirAll(alice, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bob, "event-state"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bob, "event-state", "source.json")
	if err := os.WriteFile(path, []byte(`{"cursor":"existing"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	host := h.hostWithOwners(t, owners)
	if err := host.Recover(context.Background()); err == nil {
		t.Fatal("unsupported legacy event state was accepted")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != `{"cursor":"existing"}` {
		t.Fatalf("rejected state changed: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(alice, "schedules")); !os.IsNotExist(err) {
		t.Fatalf("first owner's classes were created before all owners validated: %v", err)
	}
}

// Send-time re-authorization keeps the old communication-side
// occurrenceRefAuthorizedLocked contract under the new owner: a delivery may
// only send while its occurrence is still dispatched, current and authorized.
func TestAuthorizeOccurrenceReauthorizesAtSendTime(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	bob := h.envelope(t, "bob")
	for _, op := range []string{"read", "write"} {
		h.grant(t, "alice", op)
	}
	host := h.host(t)
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := ControlClient{URL: server.URL, Token: "control"}
	item := draft("rem-fence", alice)
	item.JobKind = "reminder"
	item.Expression = "once:" + h.now.Add(time.Minute).Format(time.RFC3339)
	created, err := host.CreateSchedule(context.Background(), alice, item, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), created.NextDue); err != nil {
		t.Fatal(err)
	}
	if h.comm.count() != 1 {
		t.Fatalf("reminder did not reach intake: %d", h.comm.count())
	}
	h.comm.mu.Lock()
	occurrenceID := h.comm.deliveries[0].OccurrenceID
	h.comm.mu.Unlock()
	if occurrenceID == "" {
		t.Fatal("intake delivery lost its occurrence id")
	}
	// A live dispatched occurrence authorizes its delivery.
	if err := client.Call(context.Background(), alice, "occurrence.authorize", map[string]any{"id": occurrenceID}, nil); err != nil {
		t.Fatalf("live occurrence denied: %v", err)
	}
	// A foreign principal cannot authorize another owner's occurrence.
	if err := client.Call(context.Background(), bob, "occurrence.authorize", map[string]any{"id": occurrenceID}, nil); err == nil {
		t.Fatal("foreign principal authorized alice's occurrence")
	}
	// Losing the automation right between dispatch and send blocks the delivery.
	h.revoke(t, "alice", "write")
	if err := client.Call(context.Background(), alice, "occurrence.authorize", map[string]any{"id": occurrenceID}, nil); err == nil {
		t.Fatal("revoked owner authorized at send time")
	}
	h.grant(t, "alice", "write")
	// Deleting the schedule retires the pending delivery with it.
	if err := host.DeleteSchedule(context.Background(), alice, created.ScheduleID, created.Revision); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(context.Background(), alice, "occurrence.authorize", map[string]any{"id": occurrenceID}, nil); err == nil {
		t.Fatal("deleted schedule's occurrence still authorized")
	}
	// Unknown ids fail closed rather than letting stray refs send.
	if err := client.Call(context.Background(), alice, "occurrence.authorize", map[string]any{"id": "occ-missing"}, nil); err == nil {
		t.Fatal("missing occurrence authorized")
	}
}

// A routine-failure notice references its occurrence after it settled — a
// dead probe marks it "failed", not "dispatched". The send-time fence must
// still authorize the notice while its schedule stands at the recorded
// revision, or no watch-failure notice could ever reach the owner.
func TestAuthorizeOccurrenceAuthorizesSettledFailureNotice(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "read")
	h.grant(t, "alice", "write")
	host := h.host(t)
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := ControlClient{URL: server.URL, Token: "control"}
	host.cfg.Fetch = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("source exploded")
	}
	item := draft("failing-watch-source", alice)
	item.JobKind = "watcher"
	item.Probe = &commodel.WatchProbe{Kind: "http", URL: "https://example.test/feed"}
	created, err := host.CreateSchedule(context.Background(), alice, item, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), created.NextDue); err != nil {
		t.Fatal(err)
	}
	h.comm.mu.Lock()
	var notice commodel.Delivery
	for _, d := range h.comm.deliveries {
		if d.Kind == "routine-failure" {
			notice = d
		}
	}
	h.comm.mu.Unlock()
	if notice.OccurrenceID == "" {
		t.Fatal("failing probe produced no routine-failure notice")
	}
	// The failed occurrence authorizes its routine-failure notice...
	if err := client.Call(context.Background(), alice, "occurrence.authorize", map[string]any{"id": notice.OccurrenceID, "kind": "routine-failure"}, nil); err != nil {
		t.Fatalf("settled failure occurrence denied its notice: %v", err)
	}
	// ...but not as an ordinary routine payload: the dispatched check still
	// fences a settled occurrence out of the reminder/agent send path.
	if err := client.Call(context.Background(), alice, "occurrence.authorize", map[string]any{"id": notice.OccurrenceID}, nil); err == nil {
		t.Fatal("settled occurrence authorized as a live dispatch")
	}
	// Once the schedule is gone the notice is stale and must not send.
	if err := host.DeleteSchedule(context.Background(), alice, created.ScheduleID, created.Revision); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(context.Background(), alice, "occurrence.authorize", map[string]any{"id": notice.OccurrenceID, "kind": "routine-failure"}, nil); err == nil {
		t.Fatal("deleted schedule's failure notice still authorized")
	}
}

// A routine that keeps failing must not spam the owner: identical failures
// dedupe to one notice; a success resets the digest so a later failure
// notifies again.
func TestRoutineRepeatedFailureNotifiesOnce(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "read")
	h.grant(t, "alice", "write")
	host := h.host(t)
	item := draft("failing-routine", alice)
	item.Expression = "every:1m"
	if _, err := host.CreateSchedule(context.Background(), alice, item, ""); err != nil {
		t.Fatal(err)
	}
	h.sup.failRuns.Store(true)
	for i := 0; i < 10; i++ {
		h.now = h.now.Add(time.Minute)
		if err := host.Tick(context.Background(), h.now); err != nil {
			t.Fatal(err)
		}
	}
	h.comm.mu.Lock()
	delivered := len(h.comm.deliveries)
	h.comm.mu.Unlock()
	if delivered != 1 {
		t.Fatalf("10 identical failures produced %d deliveries", delivered)
	}
	// A delivered notice promotes its digest; the next identical failure is
	// suppressed until a success clears it.
	h.comm.mu.Lock()
	first := h.comm.deliveries[0]
	h.comm.mu.Unlock()
	if first.Kind != "routine-failure" || first.OccurrenceID == "" {
		t.Fatalf("failure notice lost its dedupe marker: %+v", first)
	}
	if err := host.RecordDeliveryOutcome(context.Background(), alice, first.OccurrenceID, "delivered", first.Text); err != nil {
		t.Fatal(err)
	}
	// A success resets the digest: the next identical failure notifies again.
	h.sup.failRuns.Store(false)
	h.now = h.now.Add(time.Minute)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	h.sup.failRuns.Store(true)
	h.now = h.now.Add(time.Minute)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	h.comm.mu.Lock()
	delivered = len(h.comm.deliveries)
	h.comm.mu.Unlock()
	if delivered != 3 {
		t.Fatalf("failure after success produced %d deliveries, want result+failure", delivered)
	}
}
