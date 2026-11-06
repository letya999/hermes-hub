package communication

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/routines"
)

// The lock split gives the engine its own mutex while queue admission keeps
// taking the spool mutex inside the dispatch port (engine -> spool order).
// The retired reverse edge (a spool-held settle calling back into the
// engine) is gone with the old owner; this regression pins what remains:
// a probe settle and a concurrent due dispatch, both holding e.mu before
// reaching the spool-backed ports, must complete without deadlock.
func TestEventSettleDoesNotDeadlockAgainstEngineDispatch(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "ev-lock", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "mail events", JobKind: "event", Sink: "agent", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sched.AssignmentID = "asg-lock"
	sched.AssignmentPlanRevision = 1
	if err := atomicJSON(s.schedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	asg := commodel.Assignment{AssignmentID: "asg-lock", Revision: 1, Envelope: caller, Intent: "watch mail", State: "active",
		Plan: &commodel.AssignmentPlan{Revision: 1}, AppliedRev: 1,
		Grants:    commodel.AssignmentGrants{MaxWakesPerDay: 8},
		CreatedAt: now, UpdatedAt: now}
	if err := os.MkdirAll(filepath.Join(s.root, "assignments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.rt.AssignmentPath(asg.AssignmentID), asg); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = sched.ScheduleID
	o.Revision = sched.Revision
	o.JobKind = "event"
	o.Job.Trigger = "cron"
	o.Job.Envelope = sched.Envelope
	o.State = "probing"
	o.Job.ID = routines.OccurrenceID(o)
	o.Job.IdempotencyKey = o.Job.ID
	if err := atomicJSON(filepath.Join(s.root, "occurrences", routines.OccurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}

	// A second schedule's pending occurrence forces DispatchDueOccurrences to
	// reach the enqueue port while it holds the engine lock.
	agent, err := s.CreateSchedule(Schedule{ScheduleID: "occ-lock", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "every:1m", Input: "wake", JobKind: "agent", Enabled: true}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	pend := RoutineOccurrence{ScheduleID: agent.ScheduleID, Revision: agent.Revision, DueAt: now, Job: o.Job}
	if err := s.PutOccurrence(pend, caller); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"events": []map[string]string{{"id": "lk1", "text": "mail arrived"}}})
	if err != nil {
		t.Fatal(err)
	}

	settleDone := make(chan error, 1)
	go func() {
		settleDone <- s.SettleProbe(o, sched, string(body), routines.WatchHash(string(body)), now, nil, s.authorizeOccurrence)
	}()
	// Let the settle reach the engine critical section before the dispatch
	// starts — the interleaving being pinned is e.mu held while the
	// spool-backed enqueue port runs.
	time.Sleep(20 * time.Millisecond)
	dispatchDone := make(chan error, 1)
	go func() {
		dispatchDone <- s.rt.DispatchDueOccurrences(now.Add(time.Second), s.authorizeOccurrence)
	}()

	timeout := time.After(20 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case err := <-settleDone:
			if err != nil {
				t.Fatalf("settle: %v", err)
			}
		case err := <-dispatchDone:
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
		case <-timeout:
			t.Fatal("event settle and engine dispatch deadlocked across the split locks")
		}
	}
}
