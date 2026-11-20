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
