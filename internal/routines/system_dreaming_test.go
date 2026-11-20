package routines

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// A night whose operation keeps failing must settle failed: the proactive
// pass always picks the oldest occurrence of a schedule, so a night left in
// state=dreaming would block every later night forever.
func TestFailedDreamingNightDoesNotBlockNextNight(t *testing.T) {
	e := testEngine(t)
	night := func(due time.Time) commodel.RoutineOccurrence {
		o := commodel.RoutineOccurrence{ScheduleID: SystemDreamingID, Revision: 1, DueAt: due, JobKind: "dreaming", State: "dreaming",
			Job: commodel.Job{Envelope: assignmentCaller(), Channel: "system", Trigger: "cron"}}
		if err := e.atomicJSON(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	first := night(time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC))
	second := night(first.DueAt.Add(24 * time.Hour))
	ran := map[time.Time]int{}
	run := func(_ context.Context, o commodel.RoutineOccurrence) error {
		ran[o.DueAt]++
		if o.DueAt.Equal(first.DueAt) {
			return errors.New("memory processor unavailable")
		}
		return nil
	}
	allow := func(commodel.Job) error { return nil }
	now := second.DueAt
	for i := 0; i < 10 && ran[second.DueAt] == 0; i++ {
		_ = e.RunDueDreaming(context.Background(), now, allow, run)
		now = now.Add(10 * time.Minute)
	}
	if ran[first.DueAt] != dreamingAttempts {
		t.Fatalf("failed night attempts = %d, want %d", ran[first.DueAt], dreamingAttempts)
	}
	if ran[second.DueAt] != 1 {
		t.Fatalf("failed night blocked the next night: attempts=%v", ran)
	}
	state := func(o commodel.RoutineOccurrence) string {
		var live commodel.RoutineOccurrence
		body, err := os.ReadFile(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"))
		if err != nil || json.Unmarshal(body, &live) != nil {
			t.Fatalf("read occurrence: %v", err)
		}
		return live.State
	}
	if got := state(first); got != "failed" {
		t.Fatalf("failed night state = %q, want failed", got)
	}
	if got := state(second); got != "dispatched" {
		t.Fatalf("next night state = %q, want dispatched", got)
	}
}

// A run that never returns — even one ignoring ctx entirely — must not hold
// the schedule's oldest slot: each call hits the bounded run, the timeout
// counts as an attempt, and the night settles failed instead of wedging
// every later night behind it.
func TestDreamingRunTimeoutCountsAsAttempt(t *testing.T) {
	e := testEngine(t)
	old := dreamingRunTimeout
	dreamingRunTimeout = 20 * time.Millisecond
	defer func() { dreamingRunTimeout = old }()
	due := time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC)
	o := commodel.RoutineOccurrence{ScheduleID: SystemDreamingID, Revision: 1, DueAt: due, JobKind: "dreaming", State: "dreaming",
		Job: commodel.Job{Envelope: assignmentCaller(), Channel: "system", Trigger: "cron"}}
	if err := e.atomicJSON(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	runs := 0
	wedged := func(context.Context, commodel.RoutineOccurrence) error {
		runs++
		<-release // ignores ctx entirely — the bounded run still returns
		return nil
	}
	allow := func(commodel.Job) error { return nil }
	now := due
	for i := 0; i < 10; i++ {
		_ = e.RunDueDreaming(context.Background(), now, allow, wedged)
		var live commodel.RoutineOccurrence
		body, err := os.ReadFile(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"))
		if err != nil || json.Unmarshal(body, &live) != nil {
			t.Fatalf("read occurrence: %v", err)
		}
		if live.State == "failed" {
			break
		}
		now = now.Add(10 * time.Minute)
	}
	if runs != dreamingAttempts {
		t.Fatalf("wedged run attempts = %d, want %d", runs, dreamingAttempts)
	}
	var live commodel.RoutineOccurrence
	body, err := os.ReadFile(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"))
	if err != nil || json.Unmarshal(body, &live) != nil {
		t.Fatalf("read occurrence: %v", err)
	}
	if live.State != "failed" || live.RetryCount != dreamingAttempts {
		t.Fatalf("wedged night settled %+v, want failed after %d attempts", live, dreamingAttempts)
	}
}

// A parent-context shutdown must leave the night pending: the run may still
// be in flight, and burning an attempt on shutdown would shorten the retry
// budget for no operational reason.
func TestDreamingRunShutdownLeavesNightPending(t *testing.T) {
	e := testEngine(t)
	due := time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC)
	o := commodel.RoutineOccurrence{ScheduleID: SystemDreamingID, Revision: 1, DueAt: due, JobKind: "dreaming", State: "dreaming",
		Job: commodel.Job{Envelope: assignmentCaller(), Channel: "system", Trigger: "cron"}}
	if err := e.atomicJSON(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.RunDueDreaming(ctx, due, func(commodel.Job) error { return nil },
		func(ctx context.Context, _ commodel.RoutineOccurrence) error {
			t.Error("run called under canceled ctx")
			return nil
		})
	var live commodel.RoutineOccurrence
	body, readErr := os.ReadFile(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"))
	if readErr != nil || json.Unmarshal(body, &live) != nil {
		t.Fatalf("read occurrence: %v", readErr)
	}
	if live.State != "dreaming" || live.RetryCount != 0 {
		t.Fatalf("shutdown burned an attempt: %+v", live)
	}
	_ = err
}
