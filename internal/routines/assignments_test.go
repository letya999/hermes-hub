package routines

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

func assignmentCaller() identity.Envelope {
	return identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "tg-1", ContextID: "alice", RuntimeID: "rt-1", ConversationID: "conv-1", DeliveryTargetID: "tg-1", PolicyVersion: "p1"}
}

func assignmentForeignCaller() identity.Envelope {
	env := assignmentCaller()
	env.PrincipalID = "mallory"
	env.ContextID = "mallory"
	return env
}

func testEngine(t *testing.T) *Engine {
	t.Helper()
	root := t.TempDir()
	return NewEngine(root, Deps{
		Storage: StoragePort{
			WriteJSONAtomic: func(path string, value any) error {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					return err
				}
				tmp := path + ".tmp"
				f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
				if err != nil {
					return err
				}
				encErr := json.NewEncoder(f).Encode(value)
				syncErr := f.Sync()
				closeErr := f.Close()
				if encErr != nil {
					return encErr
				}
				if syncErr != nil {
					return syncErr
				}
				if closeErr != nil {
					return closeErr
				}
				return os.Rename(tmp, path)
			},
		},
		Delivery: DeliveryPort{
			NormalizeDelivery: func(d commodel.Delivery) (commodel.Delivery, error) { return d, nil },
			EnqueueDelivery:   func(commodel.Delivery) error { return nil },
		},
		Dispatch: DispatchPort{
			EnqueueJob: func(commodel.Job) (bool, error) { return true, nil },
		},
		Signal:     func() {},
		ChannelFor: func(string) OccurrenceChannel { return stubChannel{} },
		Hooks: DraftHooks{
			Channel: func(commodel.Schedule) error { return nil },
			Script:  func(string) error { return nil },
		},
	})
}

func watcherDraft(url string) commodel.RoutineDraft {
	return commodel.RoutineDraft{
		Timezone:   "UTC",
		Expression: "every:5m",
		JobKind:    "watcher",
		Probe:      &commodel.WatchProbe{Kind: "http", URL: url},
		Input:      "watch " + url,
		Channel:    "telegram_bot",
		NotifyMode: "agent",
	}
}

func newTestAssignment(id string) commodel.Assignment {
	return commodel.Assignment{
		AssignmentID: id,
		Envelope:     assignmentCaller(),
		Intent:       "watch the example page and tell me when it changes",
		Title:        "example watch",
		State:        "draft",
		Plan: &commodel.AssignmentPlan{
			Revision: 1,
			Sources: []commodel.AssignmentSource{
				{Name: "page", Schedule: watcherDraft("https://example.com/")},
			},
			Specialist: &commodel.SpecialistSpec{Instructions: "Summarize the change for the owner."},
		},
		Grants: commodel.AssignmentGrants{MaxWakesPerDay: 10},
	}
}

func TestAssignmentAgentEventRequiresFilter(t *testing.T) {
	plan := &commodel.AssignmentPlan{Revision: 1, Sources: []commodel.AssignmentSource{{Name: "feed", Schedule: commodel.RoutineDraft{JobKind: "event", Sink: "agent", Probe: &commodel.WatchProbe{Kind: "mcp_tool", Tool: "read_events"}}}}}
	if err := validateAssignmentSources(plan, commodel.AssignmentGrants{}); err == nil {
		t.Fatal("unfiltered event source may wake the agent")
	}
	plan.Sources[0].Schedule.Probe.Filter = "emit('drop')"
	if err := validateAssignmentSources(plan, commodel.AssignmentGrants{}); err != nil {
		t.Fatal(err)
	}
}

// TestAssignmentCreateIdempotent (SPEC-0080 A1.2 seed): a replayed create
// returns the existing record instead of a duplicate.
func TestAssignmentCreateIdempotent(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-test001")
	first, replayed, err := e.CreateAssignment(in, assignmentCaller())
	if err != nil || replayed {
		t.Fatalf("first create: %v replayed=%v", err, replayed)
	}
	second, replayed, err := e.CreateAssignment(in, assignmentCaller())
	if err != nil || !replayed || second.AssignmentID != first.AssignmentID {
		t.Fatalf("replayed create must return the same assignment: %v replayed=%v", err, replayed)
	}
	_ = strings.TrimSpace
}

func TestAssignmentApplyRejectsMissingPlan(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-no-plan")
	in.Plan = nil
	if _, _, err := e.CreateAssignment(in, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyAssignment(in.AssignmentID, assignmentCaller()); err == nil {
		t.Fatal("applying without a plan must be refused")
	}
}

func TestAssignmentApplyReconcilesChangedAndRemovedSources(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-reconcile")
	in.Plan.Sources = append(in.Plan.Sources, commodel.AssignmentSource{Name: "other", Schedule: watcherDraft("https://example.com/other")})
	if _, _, err := e.CreateAssignment(in, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	first, err := e.ApplyAssignment(in.AssignmentID, assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	oldPage, oldOther := first.Applied["page"].ScheduleID, first.Applied["other"].ScheduleID
	plan := *first.Plan
	plan.Revision++
	plan.Sources = []commodel.AssignmentSource{{Name: "page", Schedule: watcherDraft("https://example.com/changed")}}
	updated, err := e.UpdateAssignmentPlan(in.AssignmentID, plan, first.Grants, assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.ApplyAssignment(updated.AssignmentID, assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	page := second.Applied["page"]
	if page.ScheduleID == oldPage || page.Action != "created" {
		t.Fatalf("changed source kept its old schedule: %+v", page)
	}
	actual, err := e.LoadSchedule(page.ScheduleID)
	if err != nil || actual.Probe == nil || actual.Probe.URL != "https://example.com/changed" {
		t.Fatalf("new plan not materialized: %+v %v", actual, err)
	}
	if _, err := e.LoadSchedule(oldPage); err == nil {
		t.Fatal("old source generation remains active")
	}
	if _, err := e.LoadSchedule(oldOther); err == nil || second.Applied["other"].Action != "removed" {
		t.Fatalf("removed source remains active: %+v %v", second.Applied["other"], err)
	}
}

func TestAssignmentDefaultGrantCannotWidenNarrowGrant(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-grants-default")
	in.Grants.MaxWakesPerDay = 1
	if _, _, err := e.CreateAssignment(in, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	plan := *in.Plan
	plan.Revision++
	if _, err := e.UpdateAssignmentPlan(in.AssignmentID, plan, commodel.AssignmentGrants{}, assignmentCaller()); err == nil {
		t.Fatal("zero means the default 20 wakes, which widens the grant of one")
	}
}

// TestAssignmentIntentOwnerOnly (SPEC-0080 A1.4): a foreign principal gets
// the same not-found refusal on every verb; the owner record is untouched.
func TestAssignmentIntentOwnerOnly(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-test002")
	if _, _, err := e.CreateAssignment(in, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	foreign := assignmentForeignCaller()
	if _, err := e.LoadAssignment("asg-test002", foreign); !errors.Is(err, ErrAssignmentNotFound) {
		t.Fatalf("foreign read must be refused: %v", err)
	}
	if err := e.PauseAssignment("asg-test002", foreign); !errors.Is(err, ErrAssignmentNotFound) {
		t.Fatalf("foreign pause must be refused: %v", err)
	}
	got, err := e.LoadAssignment("asg-test002", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	if got.Envelope.PrincipalID != "alice" || got.State != "draft" {
		t.Fatalf("owner record drifted: %+v", got.Envelope)
	}
}

// TestAssignmentPlanIdempotent (SPEC-0080 A1.2): applying the same plan
// twice produces the same schedule set and the same durable receipts.
func TestAssignmentPlanIdempotent(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-test010")
	if _, _, err := e.CreateAssignment(in, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	got, err := e.ApplyAssignment("asg-test010", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "active" || got.Applied["page"].ScheduleID == "" || got.Applied["page"].Action != "created" {
		t.Fatalf("apply must activate with a created receipt: %+v", got.Applied)
	}
	sched, err := e.LoadSchedule(got.Applied["page"].ScheduleID)
	if err != nil {
		t.Fatalf("artifact must exist: %v", err)
	}
	if sched.AssignmentID != "asg-test010" || sched.ScheduleID != commodel.AssignmentScheduleID("asg-test010", "page") {
		t.Fatalf("artifact identity wrong: %+v", sched.AssignmentID)
	}
	again, err := e.ApplyAssignment("asg-test010", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	if again.Applied["page"].Action != "kept" {
		t.Fatalf("re-apply must keep the artifact: %+v", again.Applied)
	}
	entries, _ := os.ReadDir(filepath.Join(e.root, "schedules"))
	if len(entries) != 1 {
		t.Fatalf("expected exactly one artifact, got %d", len(entries))
	}
}

// TestAssignmentLifecyclePropagates (SPEC-0080 R1.5): pause silences the
// owned artifacts, resume re-arms them, cancel retires them durably.
func TestAssignmentLifecyclePropagates(t *testing.T) {
	e := testEngine(t)
	if _, _, err := e.CreateAssignment(newTestAssignment("asg-test020"), assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyAssignment("asg-test020", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	sid := commodel.AssignmentScheduleID("asg-test020", "page")
	if err := e.PauseAssignment("asg-test020", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	sched, err := e.LoadSchedule(sid)
	if err != nil || !sched.Paused {
		t.Fatalf("pause must silence the artifact: %+v %v", sched, err)
	}
	if err := e.ResumeAssignment("asg-test020", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	sched, err = e.LoadSchedule(sid)
	if err != nil || sched.Paused || sched.NextDue.IsZero() {
		t.Fatalf("resume must re-arm the artifact: %+v %v", sched, err)
	}
	if err := e.CancelAssignment("asg-test020", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LoadSchedule(sid); err == nil {
		t.Fatal("cancel must retire the artifact")
	}
	got, err := e.LoadAssignment("asg-test020", assignmentCaller())
	if err != nil || got.State != "cancelled" || got.CancelledAt == nil {
		t.Fatalf("cancelled record expected: %+v %v", got, err)
	}
}

// A paused assignment must leave pause only through Resume: apply would mark
// it active while its kept schedules stay paused, and a plan update would move
// it to waiting the same way.
func TestApplyRefusesPausedAssignment(t *testing.T) {
	e := testEngine(t)
	if _, _, err := e.CreateAssignment(newTestAssignment("asg-paused"), assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyAssignment("asg-paused", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if err := e.PauseAssignment("asg-paused", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyAssignment("asg-paused", assignmentCaller()); err == nil {
		t.Fatal("apply accepted a paused assignment")
	}
	plan := *newTestAssignment("asg-paused").Plan
	if _, err := e.UpdateAssignmentPlan("asg-paused", plan, commodel.AssignmentGrants{MaxWakesPerDay: 10}, assignmentCaller()); err == nil {
		t.Fatal("plan update accepted a paused assignment")
	}
	got, err := e.LoadAssignment("asg-paused", assignmentCaller())
	if err != nil || got.State != "paused" {
		t.Fatalf("paused assignment left pause without resume: %+v %v", got, err)
	}
	sched, err := e.LoadSchedule(commodel.AssignmentScheduleID("asg-paused", "page"))
	if err != nil || !sched.Paused {
		t.Fatalf("paused assignment source must stay paused: %+v %v", sched, err)
	}
}

func TestAssignmentDraftCannotBecomeActiveWithoutApply(t *testing.T) {
	e := testEngine(t)
	draft := newTestAssignment("asg-draft-guard")
	draft.Plan = nil
	created, _, err := e.CreateAssignment(draft, assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.PauseAssignment(created.AssignmentID, assignmentCaller(), created.Revision); err != nil {
		t.Fatal(err)
	}
	paused, err := e.LoadAssignment(created.AssignmentID, assignmentCaller())
	if err != nil || paused.State != "paused" {
		t.Fatalf("draft pause failed: %+v %v", paused, err)
	}
	if err := e.ResumeAssignment(created.AssignmentID, assignmentCaller(), paused.Revision); err != nil {
		t.Fatal(err)
	}
	resumed, err := e.LoadAssignment(created.AssignmentID, assignmentCaller())
	if err != nil || resumed.State != "draft" || resumed.AppliedRev != 0 {
		t.Fatalf("draft resume must stay a draft: %+v %v", resumed, err)
	}
	plan := commodel.AssignmentPlan{Sources: []commodel.AssignmentSource{{Name: "page", Schedule: watcherDraft("https://example.com/")}}}
	updated, err := e.UpdateAssignmentPlan(created.AssignmentID, plan, created.Grants, assignmentCaller(), resumed.Revision)
	if err != nil || updated.Plan == nil || updated.Plan.Revision != 1 || updated.State != "waiting" {
		t.Fatalf("zero-revision dry-run plan must save at revision 1: %+v %v", updated, err)
	}
}

func TestAssignmentMockSourceBindsDestinationChat(t *testing.T) {
	e := testEngine(t)
	draft := newTestAssignment("asg-mock-binding")
	draft.Plan.Sources[0].Schedule.Channel = "mock"
	draft.Plan.Sources[0].Schedule.ChatID = 42
	if _, _, err := e.CreateAssignment(draft, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyAssignment(draft.AssignmentID, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	schedule, err := e.LoadSchedule(commodel.AssignmentScheduleID(draft.AssignmentID, "page"))
	if err != nil {
		t.Fatal(err)
	}
	want := identity.MockEnvelope(draft.Envelope.PrincipalID, 42, draft.Envelope.RuntimeID, draft.Envelope.PolicyVersion)
	if !schedule.Envelope.Equal(want) {
		t.Fatal("mock assignment source must use its target chat envelope")
	}
}

// TestAssignmentDeleteTerminal (ADR-0094 assignment_remove): removal retires
// the applied artifacts like cancel, then deletes the record itself — a later
// read reports not-found, a stale revision conflicts, and a foreign caller
// cannot even probe existence.
func TestAssignmentDeleteTerminal(t *testing.T) {
	e := testEngine(t)
	if _, _, err := e.CreateAssignment(newTestAssignment("asg-test025"), assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyAssignment("asg-test025", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	item, err := e.LoadAssignment("asg-test025", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteAssignment("asg-test025", assignmentCaller(), item.Revision+9); !errors.Is(err, ErrRoutineRevision) {
		t.Fatalf("stale revision must conflict: %v", err)
	}
	sid := commodel.AssignmentScheduleID("asg-test025", "page")
	if err := e.DeleteAssignment("asg-test025", assignmentCaller(), item.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LoadAssignment("asg-test025", assignmentCaller()); !errors.Is(err, ErrAssignmentNotFound) {
		t.Fatalf("removed record must read as not-found: %v", err)
	}
	if _, err := e.LoadSchedule(sid); err == nil {
		t.Fatal("remove must retire the applied artifact")
	}
	if err := e.DeleteAssignment("asg-test025", identity.Envelope{PrincipalID: "mallory", ContextID: "mallory"}, 1); !errors.Is(err, ErrAssignmentNotFound) {
		t.Fatalf("foreign remove must not probe existence: %v", err)
	}
}

// TestAssignmentDriftWaiting (SPEC-0080 R1.8): an artifact deleted outside
// the assignment control path flips the record to waiting with a blocked
// reason on the next reconcile sweep; re-applying recreates it.
func TestAssignmentDriftWaiting(t *testing.T) {
	e := testEngine(t)
	if _, _, err := e.CreateAssignment(newTestAssignment("asg-test030"), assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyAssignment("asg-test030", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	sid := commodel.AssignmentScheduleID("asg-test030", "page")
	if err := e.DeleteSchedule(sid, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileAssignments(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, err := e.LoadAssignment("asg-test030", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "waiting" || got.Blocked == "" {
		t.Fatalf("drift must surface waiting+blocked: %+v", got)
	}
	fixed, err := e.ApplyAssignment("asg-test030", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	if fixed.State != "active" || fixed.Applied["page"].Action != "created" {
		t.Fatalf("re-apply must recreate the artifact: %+v", fixed.Applied)
	}
}

// TestAssignmentWakeFenced (SPEC-0080 R2.2/R2.3): a change on an owned
// artifact admits exactly one specialist wake carrying untrusted evidence;
// pause fences further wakes and the per-day budget caps them.
func TestAssignmentWakeFenced(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-test040")
	in.Grants.MaxWakesPerDay = 1
	if _, _, err := e.CreateAssignment(in, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	applied, err := e.ApplyAssignment("asg-test040", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	sid := applied.Applied["page"].ScheduleID
	sched, err := e.LoadSchedule(sid)
	if err != nil {
		t.Fatal(err)
	}
	var wakes []commodel.Job
	e.deps.Dispatch.EnqueueJob = func(j commodel.Job) (bool, error) { wakes = append(wakes, j); return true, nil }
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := e.SeedWatchBaseline(sid, "v1", now); err != nil {
		t.Fatal(err)
	}
	settle := func(sched commodel.Schedule, value string, minute int) commodel.RoutineOccurrence {
		o := watchOccurrence(sched.ScheduleID, "watcher", sched.Revision, sched.Envelope, now.Add(time.Duration(minute)*time.Minute))
		putOccurrenceState(t, e, e.root, o, "probing")
		e.SettleProbe(o, sched, value, WatchHash(value), now.Add(time.Duration(minute)*time.Minute), nil, nil)
		return o
	}
	settle(sched, "v2", 0)
	if len(wakes) != 1 || !strings.Contains(wakes[0].Text, "Summarize the change") || !strings.Contains(wakes[0].Text, "untrusted") {
		t.Fatalf("expected one specialist wake with untrusted evidence: %+v", wakes)
	}
	got, _ := e.LoadAssignment("asg-test040", assignmentCaller())
	if got.Checkpoint.WakesToday != 1 || got.Checkpoint.LLMCalls != 1 {
		t.Fatalf("checkpoint must count the wake: %+v", got.Checkpoint)
	}
	settle(sched, "v3", 1)
	if len(wakes) != 1 {
		t.Fatalf("budget must cap the second wake, got %d", len(wakes))
	}

	in2 := newTestAssignment("asg-test041")
	in2.Grants.MaxWakesPerDay = 10
	if _, _, err := e.CreateAssignment(in2, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	applied2, err := e.ApplyAssignment("asg-test041", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	sid2 := applied2.Applied["page"].ScheduleID
	sched2, _ := e.LoadSchedule(sid2)
	if err := e.SeedWatchBaseline(sid2, "v1", now); err != nil {
		t.Fatal(err)
	}
	if err := e.PauseAssignment("asg-test041", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	settle(sched2, "v2", 2)
	if len(wakes) != 1 {
		t.Fatalf("paused assignment must fence wakes, got %d", len(wakes))
	}

	in3 := newTestAssignment("asg-test042")
	in3.Grants.MaxWakesPerDay = 0
	if _, _, err := e.CreateAssignment(in3, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	applied3, err := e.ApplyAssignment("asg-test042", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	sid3 := applied3.Applied["page"].ScheduleID
	sched3, _ := e.LoadSchedule(sid3)
	if err := e.SeedWatchBaseline(sid3, "v1", now); err != nil {
		t.Fatal(err)
	}
	settle(sched3, "v2", 3)
	if len(wakes) != 2 {
		t.Fatalf("unset grant must fall back to the default budget, got %d", len(wakes))
	}
}

// TestAssignmentListOwnerScoped (SPEC-0080 R1.6/R3.1): list returns only the
// caller's records — a foreign principal sees an empty set, never an error
// that would reveal existence.
func TestAssignmentListOwnerScoped(t *testing.T) {
	e := testEngine(t)
	for _, id := range []string{"asg-test050", "asg-test051"} {
		if _, _, err := e.CreateAssignment(newTestAssignment(id), assignmentCaller()); err != nil {
			t.Fatal(err)
		}
	}
	other := newTestAssignment("asg-test052")
	other.Envelope = assignmentForeignCaller()
	if _, _, err := e.CreateAssignment(other, assignmentForeignCaller()); err != nil {
		t.Fatal(err)
	}
	mine, err := e.ListAssignments(assignmentCaller())
	if err != nil || len(mine) != 2 {
		t.Fatalf("owner must see exactly own assignments: %d %v", len(mine), err)
	}
	theirs, err := e.ListAssignments(assignmentForeignCaller())
	if err != nil || len(theirs) != 1 || theirs[0].AssignmentID != "asg-test052" {
		t.Fatalf("foreign list must contain only own records: %+v", theirs)
	}
}

// TestAssignmentGrantsNarrowOnly (SPEC-0080 R2.4): a plan update may narrow
// grants freely, but widening requires an authorization stamped at the
// plan's own revision — the agent can never widen on its own.
func TestAssignmentGrantsNarrowOnly(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-test060")
	in.Grants.MaxWakesPerDay = 10
	if _, _, err := e.CreateAssignment(in, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	narrower := *in.Plan
	narrower.Revision = 2
	got, err := e.UpdateAssignmentPlan("asg-test060", narrower, commodel.AssignmentGrants{MaxWakesPerDay: 5}, assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	if got.Grants.MaxWakesPerDay != 5 || got.Plan.Revision != 2 {
		t.Fatalf("narrowing must apply: %+v", got.Grants)
	}
	wider := narrower
	wider.Revision = 3
	if _, err := e.UpdateAssignmentPlan("asg-test060", wider, commodel.AssignmentGrants{MaxWakesPerDay: 50}, assignmentCaller()); err == nil {
		t.Fatal("widening without authorize must be refused")
	}
	if err := e.AuthorizeAssignmentGrants("asg-test060", 3, commodel.AssignmentGrants{MaxWakesPerDay: 50, MaxSources: 8}, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	got, err = e.UpdateAssignmentPlan("asg-test060", wider, commodel.AssignmentGrants{MaxWakesPerDay: 50}, assignmentCaller())
	if err != nil || got.Grants.MaxWakesPerDay != 50 {
		t.Fatalf("authorized widening must apply: %+v %v", got.Grants, err)
	}
	widest := narrower
	widest.Revision = 4
	if err := e.AuthorizeAssignmentGrants("asg-test060", 4, commodel.AssignmentGrants{MaxWakesPerDay: 500, MaxSources: 16}, assignmentCaller()); err != nil {
		t.Fatalf("owner-authorized grants above legacy bound must apply: %v", err)
	}
	got, err = e.UpdateAssignmentPlan("asg-test060", widest, commodel.AssignmentGrants{MaxWakesPerDay: 500, MaxSources: 16}, assignmentCaller())
	if err != nil || got.Grants.MaxWakesPerDay != 500 {
		t.Fatalf("high-volume grant widening must apply: %+v %v", got.Grants, err)
	}
}

// TestAssignmentAppliedScheduleTicks (SPEC-0080 A3.1): an applied source
// schedule must produce occurrences on the live tick — the draft has to
// carry the ownership fields ValidateOccurrence enforces (actor, scope).
func TestAssignmentAppliedScheduleTicks(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-test070")
	in.OrganizationID = "org-1"
	if _, _, err := e.CreateAssignment(in, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyAssignment("asg-test070", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	items, err := e.ListSchedules(assignmentCaller())
	if err != nil || len(items) != 1 {
		t.Fatalf("applied schedule missing: %v %v", err, items)
	}
	authorized := false
	authorize := func(_ string, j commodel.Job) error {
		if j.OrganizationID != "org-1" || j.ActorID != "alice" || j.ScopeID != "user:alice" {
			return errors.New("occurrence job missing owner stamps")
		}
		authorized = true
		return nil
	}
	now := items[0].NextDue.Add(time.Second)
	if err := e.TickSchedules(now, "", authorize); err != nil {
		t.Fatal(err)
	}
	if !authorized {
		t.Fatal("authorize saw no occurrence job")
	}
	reloaded, err := e.ListSchedules(assignmentCaller())
	if err != nil || len(reloaded) != 1 {
		t.Fatal(err)
	}
	if reloaded[0].LastOccurrenceID == "" {
		t.Fatal("applied assignment schedule produced no occurrence on tick")
	}
}

// TestAssignmentApplyPartialFailureKeepsReceipts (SPEC-0080 R1.8, live M23
// evidence): a plan whose second source fails validation must still persist
// the receipts of the sources already reconciled — otherwise the artifact is
// an orphan: it fires but lifecycle verbs can never reach it (observed live:
// apply error left a firing watcher with zero receipts and pause was a no-op).
func TestAssignmentApplyPartialFailureKeepsReceipts(t *testing.T) {
	e := testEngine(t)
	in := newTestAssignment("asg-test080")
	in.Plan.Sources = append(in.Plan.Sources, commodel.AssignmentSource{
		Name: "inbox", Schedule: commodel.RoutineDraft{
			Timezone: "UTC", Expression: "every:5m", JobKind: "watcher",
			Probe: &commodel.WatchProbe{Kind: "hook"}, Input: "inbox", Channel: "telegram_bot", ChatID: 1,
		},
	})
	if _, _, err := e.CreateAssignment(in, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyAssignment("asg-test080", assignmentCaller()); err == nil {
		t.Fatal("broken second source must fail the apply")
	}
	got, err := e.LoadAssignment("asg-test080", assignmentCaller())
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := got.Applied["page"]
	if !ok || rc.ScheduleID == "" {
		t.Fatalf("receipts of reconciled sources must survive the failure: %+v", got.Applied)
	}
	if got.State != "waiting" {
		t.Fatalf("partial apply must leave the assignment waiting, got %q", got.State)
	}
	if err := e.PauseAssignment("asg-test080", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	sched, err := e.LoadSchedule(rc.ScheduleID)
	if err != nil || !sched.Paused {
		t.Fatalf("pause must reach the partial artifact: %v %+v", err, sched)
	}
}

func TestTelegramAssignmentNormalizesOnlyPublicChatReferences(t *testing.T) {
	plan := &commodel.AssignmentPlan{Revision: 1, Sources: []commodel.AssignmentSource{{Name: "tg", Schedule: commodel.RoutineDraft{Probe: &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-telegram-mcp-watch-chat-messages-1234", Sources: []commodel.WatchProbeSource{{Name: "one", Args: map[string]any{"chat_id": "https://t.me/example_chat/123"}}, {Name: "two", Args: map[string]any{"chat_id": "https://t.me/s/other_chat"}}}}}}}}
	if err := ValidateAssignmentPlan(plan, commodel.AssignmentGrants{}); err != nil {
		t.Fatal(err)
	}
	if got := plan.Sources[0].Schedule.Probe.Sources[0].Args["chat_id"]; got != "example_chat" {
		t.Fatalf("first chat_id = %v", got)
	}
	if got := plan.Sources[0].Schedule.Probe.Sources[1].Args["chat_id"]; got != "other_chat" {
		t.Fatalf("second chat_id = %v", got)
	}
	plan.Sources[0].Schedule.Probe.Sources[0].Args["chat_id"] = "https://t.me/+privateInvite"
	if err := ValidateAssignmentPlan(plan, commodel.AssignmentGrants{}); err == nil {
		t.Fatal("invite link was treated as a public chat")
	}
}

func TestWakeReservationIsIdempotentAcrossDayAndHonorsPause(t *testing.T) {
	e := testEngine(t)
	now := time.Now().UTC()
	a := commodel.Assignment{AssignmentID: "wake-reservation", Revision: 1, Envelope: assignmentCaller(), State: "active", Plan: &commodel.AssignmentPlan{Revision: 1}, AppliedRev: 1, Grants: commodel.AssignmentGrants{MaxWakesPerDay: 1}, CreatedAt: now, UpdatedAt: now}
	if err := os.MkdirAll(filepath.Join(e.root, "assignments"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := e.atomicJSON(e.AssignmentPath(a.AssignmentID), a); err != nil {
		t.Fatal(err)
	}
	first, firstErr := e.ReserveAssignmentWake(a.AssignmentID, "operation-one", now, 1)
	second, secondErr := e.ReserveAssignmentWake(a.AssignmentID, "operation-one", now.Add(24*time.Hour), 1)
	if !first || !second || firstErr != nil || secondErr != nil {
		t.Fatal("same operation charged or rejected across day rollover")
	}
	stored, err := e.loadAssignmentLocked(a.AssignmentID)
	if err != nil || stored.Checkpoint.WakesToday != 1 || stored.Checkpoint.LLMCalls != 1 {
		t.Fatal("reservation charged twice", err)
	}
	stored.State = "paused"
	if err := e.atomicJSON(e.AssignmentPath(a.AssignmentID), stored); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := e.ReserveAssignmentWake(a.AssignmentID, "operation-one", now, 1); allowed {
		t.Fatal("idempotency bypassed pause")
	}
	if err := e.SettleAssignmentWake(a.AssignmentID, "operation-one", 2); err == nil {
		t.Fatal("wrong generation settled reservation")
	}
	if err := e.SettleAssignmentWake(a.AssignmentID, "operation-one", 1); err != nil {
		t.Fatal(err)
	}
	if err := e.SettleAssignmentWake(a.AssignmentID, "operation-one", 1); err != nil {
		t.Fatal("idempotent settle", err)
	}
}
