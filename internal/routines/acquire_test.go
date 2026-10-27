package routines

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAcquirePoolSharesWithinBoundary (SPEC-0081 R2.1): two schedules of the
// same principal polling one URL share a single fetch inside the poll
// window; a foreign principal never reuses the cached body.
func TestAcquirePoolSharesWithinBoundary(t *testing.T) {
	e := testEngine(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	url := "https://example.com/feed"
	e.AcquireSubscribeLocked("alice", url, "sched-a", 5*time.Minute, now)
	e.AcquireSubscribeLocked("alice", url, "sched-b", 10*time.Minute, now)
	if err := e.AcquireRecordLocked("alice", url, []byte("body-1"), now); err != nil {
		t.Fatal(err)
	}
	body, ok := e.AcquirePeekLocked("alice", url, now.Add(time.Minute))
	if !ok || string(body) != "body-1" {
		t.Fatalf("fresh pool body must be shared: %q ok=%v", body, ok)
	}
	if _, ok := e.AcquirePeekLocked("alice", url, now.Add(6*time.Minute)); ok {
		t.Fatal("stale pool body must not be shared past the shortest interval")
	}
	if _, ok := e.AcquirePeekLocked("mallory", url, now.Add(time.Minute)); ok {
		t.Fatal("pool must never cross the principal boundary")
	}
	e.AcquireBackoffLocked("alice", url, now.Add(15*time.Minute), now)
	if _, ok := e.AcquirePeekLocked("alice", url, now.Add(2*time.Minute)); ok {
		t.Fatal("backoff must suppress reuse until retry-after elapses")
	}
}

// TestAcquirePoolSkipsSecondFetch (SPEC-0081 A2.1): two due occurrences for
// the same URL under one principal perform exactly one fetch.
func TestAcquirePoolSkipsSecondFetch(t *testing.T) {
	e := testEngine(t)
	env := assignmentCaller()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"w-acq-a", "w-acq-b"} {
		sched := watcherDraft("https://example.com/").Schedule()
		sched.ScheduleID = id
		sched.Envelope = env
		sched.UserID = "alice"
		sched.Enabled = true
		sched.NextDue = now.Add(-time.Minute)
		sched.NotifyMode = ""
		if err := e.atomicJSON(e.SchedulePath(id), sched); err != nil {
			t.Fatal(err)
		}
		o := watchOccurrence(id, "watcher", sched.Revision, env, now.Add(-time.Minute))
		putOccurrenceState(t, e, e.root, o, "probing")
	}
	fetches := 0
	fetch := func(context.Context, string) ([]byte, error) { fetches++; return []byte("body"), nil }
	if err := e.ProbeDueOccurrencesContext(context.Background(), now, nil, fetch, nil, nil); err != nil {
		t.Fatal(err)
	}
	if fetches != 1 {
		t.Fatalf("pool must collapse duplicate fetches, got %d", fetches)
	}
}

// TestAcquireBackoffSuppressesRefetch (SPEC-0081 R2.2): a source-declared
// retry-after fences further fetches until the deadline passes — a hammered
// source gets relief, not duplicate polls.
func TestAcquireBackoffSuppressesRefetch(t *testing.T) {
	e := testEngine(t)
	env := assignmentCaller()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	sched := watcherDraft("https://example.com/").Schedule()
	sched.ScheduleID = "w-acq-rl"
	sched.Envelope = env
	sched.UserID = "alice"
	sched.Enabled = true
	sched.NextDue = now.Add(-time.Minute)
	if err := e.atomicJSON(e.SchedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	fetches := 0
	fetch := func(context.Context, string) ([]byte, error) {
		fetches++
		return nil, &RetryAfterError{Cause: errors.New("probe status 429"), Delay: 15 * time.Minute}
	}
	o := watchOccurrence(sched.ScheduleID, "watcher", sched.Revision, env, now.Add(-time.Minute))
	putOccurrenceState(t, e, e.root, o, "probing")
	if err := e.ProbeDueOccurrencesContext(context.Background(), now, nil, fetch, nil, nil); err != nil {
		t.Fatal(err)
	}
	o2 := watchOccurrence(sched.ScheduleID, "watcher", sched.Revision, env, now.Add(time.Minute))
	putOccurrenceState(t, e, e.root, o2, "probing")
	if err := e.ProbeDueOccurrencesContext(context.Background(), now.Add(time.Minute), nil, fetch, nil, nil); err != nil {
		t.Fatal(err)
	}
	if fetches != 1 {
		t.Fatalf("retry-after must fence the second fetch, got %d", fetches)
	}
}
