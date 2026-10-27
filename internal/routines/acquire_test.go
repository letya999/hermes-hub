package routines

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// TestAcquirePoolSharesWithinBoundary (SPEC-0081 R2.1): schedules of one
// principal polling one URL share a single fetch inside the same sweep; a
// foreign principal never reuses the body and a new sweep never sees it.
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
		t.Fatalf("pool body must be shared inside the sweep: %q ok=%v", body, ok)
	}
	e.resetAcquireBurstLocked()
	if _, ok := e.AcquirePeekLocked("alice", url, now.Add(time.Minute)); ok {
		t.Fatal("a new sweep must never see the previous body")
	}
	if _, ok := e.AcquirePeekLocked("mallory", url, now.Add(time.Minute)); ok {
		t.Fatal("pool must never cross the principal boundary")
	}
	if err := e.AcquireRecordLocked("alice", url, []byte("body-2"), now); err != nil {
		t.Fatal(err)
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

// TestAcquireSweepRefetchesAcrossSweeps (SPEC-0081 R2.1): coalescing shares
// one fetch inside a single probe sweep; a body is never served to the next
// sweep — a re-due occurrence must see the live page, not a stale copy.
func TestAcquireSweepRefetchesAcrossSweeps(t *testing.T) {
	e := testEngine(t)
	env := assignmentCaller()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	sched := watcherDraft("https://example.com/").Schedule()
	sched.ScheduleID = "w-acq-sweep"
	sched.Envelope = env
	sched.UserID = "alice"
	sched.Enabled = true
	sched.NextDue = now.Add(-time.Minute)
	sched.NotifyMode = ""
	if err := e.atomicJSON(e.SchedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	fetches := 0
	fetch := func(context.Context, string) ([]byte, error) { fetches++; return []byte("v"), nil }
	o := watchOccurrence(sched.ScheduleID, "watcher", sched.Revision, env, now.Add(-time.Minute))
	putOccurrenceState(t, e, e.root, o, "probing")
	if err := e.ProbeDueOccurrencesContext(context.Background(), now, nil, fetch, nil, nil); err != nil {
		t.Fatal(err)
	}
	o2 := watchOccurrence(sched.ScheduleID, "watcher", sched.Revision, env, now.Add(time.Minute))
	putOccurrenceState(t, e, e.root, o2, "probing")
	if err := e.ProbeDueOccurrencesContext(context.Background(), now, nil, fetch, nil, nil); err != nil {
		t.Fatal(err)
	}
	if fetches != 2 {
		t.Fatalf("a new sweep must refetch, got %d", fetches)
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

// TestHookIntakeDrainsInbox (SPEC-0081 R5.1): a posted hook payload lands in
// the durable inbox and the next probing occurrence drains exactly one
// payload — restart-safe because the inbox is on disk, not in memory.
func TestHookIntakeDrainsInbox(t *testing.T) {
	e := testEngine(t)
	env := assignmentCaller()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	sched := watcherDraft("hook:github").Schedule()
	sched.ScheduleID = "w-hook-1"
	sched.Envelope = env
	sched.UserID = "alice"
	sched.Enabled = true
	sched.JobKind = "event"
	sched.Probe.Kind = "hook"
	sched.NotifyMode = ""
	if err := e.atomicJSON(e.SchedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	if err := e.IntakeHook("w-hook-1", []byte(`{"events":[{"id":"h1","text":"push to main"}]}`), now); err != nil {
		t.Fatal(err)
	}
	var deliveries []commodel.Delivery
	e.deps.EnqueueDelivery = func(d commodel.Delivery) error { deliveries = append(deliveries, d); return nil }
	var settled [][]byte
	e.deps.SettleEvents = func(o commodel.RoutineOccurrence, sched commodel.Schedule, body []byte, at time.Time, probeErr error, authorize func(commodel.Job) error) []commodel.Event {
		settled = append(settled, body)
		return nil
	}
	o := watchOccurrence("w-hook-1", "event", sched.Revision, env, now.Add(-time.Minute))
	putOccurrenceState(t, e, e.root, o, "probing")
	fetches := 0
	if err := e.ProbeDueOccurrencesContext(context.Background(), now, nil, func(context.Context, string) ([]byte, error) { fetches++; return nil, errors.New("must not fetch") }, nil, nil); err != nil {
		t.Fatal(err)
	}
	if fetches != 0 {
		t.Fatal("hook probes never fetch")
	}
	if e.HookPendingCount("w-hook-1") != 0 {
		t.Fatal("inbox must be drained after probing")
	}
	if len(settled) != 1 || !strings.Contains(string(settled[0]), "push to main") {
		t.Fatalf("hook payload must reach event settlement: %v", settled)
	}
}
