package routines

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	var mu sync.Mutex
	return NewEngine(root, &mu, Deps{
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
		NormalizeDelivery: func(d commodel.Delivery) (commodel.Delivery, error) { return d, nil },
		EnqueueJob:        func(commodel.Job) (bool, error) { return true, nil },
		EnqueueDelivery:   func(commodel.Delivery) error { return nil },
		Signal:            func() {},
		ChannelFor:        func(string) OccurrenceChannel { return stubChannel{} },
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
	sched, err := e.LoadScheduleLocked(got.Applied["page"].ScheduleID)
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
	sched, err := e.LoadScheduleLocked(sid)
	if err != nil || !sched.Paused {
		t.Fatalf("pause must silence the artifact: %+v %v", sched, err)
	}
	if err := e.ResumeAssignment("asg-test020", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	sched, err = e.LoadScheduleLocked(sid)
	if err != nil || sched.Paused || sched.NextDue.IsZero() {
		t.Fatalf("resume must re-arm the artifact: %+v %v", sched, err)
	}
	if err := e.CancelAssignment("asg-test020", assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LoadScheduleLocked(sid); err == nil {
		t.Fatal("cancel must retire the artifact")
	}
	got, err := e.LoadAssignment("asg-test020", assignmentCaller())
	if err != nil || got.State != "cancelled" || got.CancelledAt == nil {
		t.Fatalf("cancelled record expected: %+v %v", got, err)
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
	sched, err := e.LoadScheduleLocked(sid)
	if err != nil {
		t.Fatal(err)
	}
	var wakes []commodel.Job
	e.deps.EnqueueJob = func(j commodel.Job) (bool, error) { wakes = append(wakes, j); return true, nil }
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
	sched2, _ := e.LoadScheduleLocked(sid2)
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
	sched3, _ := e.LoadScheduleLocked(sid3)
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
	if err := e.AuthorizeAssignmentGrants("asg-test060", 3, commodel.AssignmentGrants{MaxWakesPerDay: 50}, assignmentCaller()); err != nil {
		t.Fatal(err)
	}
	got, err = e.UpdateAssignmentPlan("asg-test060", wider, commodel.AssignmentGrants{MaxWakesPerDay: 50}, assignmentCaller())
	if err != nil || got.Grants.MaxWakesPerDay != 50 {
		t.Fatalf("authorized widening must apply: %+v %v", got.Grants, err)
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
	authorize := func(j commodel.Job) error {
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
	sched, err := e.LoadScheduleLocked(rc.ScheduleID)
	if err != nil || !sched.Paused {
		t.Fatalf("pause must reach the partial artifact: %v %+v", err, sched)
	}
}
