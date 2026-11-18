package automation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

func settledOccurrence(t *testing.T, host *Host, env identity.Envelope, schedule commodel.Schedule, state string) commodel.RoutineOccurrence {
	t.Helper()
	o := commodel.RoutineOccurrence{ScheduleID: schedule.ScheduleID, Revision: schedule.Revision, DueAt: schedule.NextDue,
		JobKind: schedule.JobKind, Job: commodel.Job{Envelope: env, UserID: env.PrincipalID, ActorID: env.PrincipalID,
			ScopeID: "user:" + env.PrincipalID, Channel: schedule.Channel, ChatID: schedule.ChatID, Text: schedule.Input}}
	eng, err := host.engineFor(env.PrincipalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.PutOccurrence(o, env); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(host.ownerRoot(env.PrincipalID), "occurrences", routines.OccurrenceID(o)+".json")
	if err := loadJSON(path, &o); err != nil {
		t.Fatal(err)
	}
	o.State = state
	if err := writeJSONAtomic(path, o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestOccurrenceCompactionRetainsLiveDeliveryAndHistory(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	bob := h.envelope(t, "bob")
	for _, op := range []string{"read", "write"} {
		h.grant(t, "alice", op)
		h.grant(t, "bob", op)
	}
	host := h.host(t)
	legacy := t.TempDir()
	if err := os.Mkdir(filepath.Join(legacy, "outbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	host.cfg.LegacySpoolDir = legacy
	for _, state := range []string{"no_change", "dispatched"} {
		s := draft("rt-retention-"+state, alice)
		s.JobKind = "reminder"
		item, err := host.CreateSchedule(context.Background(), alice, s, "")
		if err != nil {
			t.Fatal(err)
		}
		settledOccurrence(t, host, alice, item, state)
	}
	base := filepath.Join(host.ownerRoot("alice"), "occurrences")
	// The IDs come from the owner history projection.
	quiet, err := host.RoutineHistory(context.Background(), alice, "rt-retention-no_change")
	if err != nil || len(quiet) != 1 {
		t.Fatalf("quiet history: %v %+v", err, quiet)
	}
	quietID := quiet[0].Run.ID
	bobSchedule := draft("rt-retention-no_change", bob)
	bobSchedule.JobKind = "reminder"
	bobItem, err := host.CreateSchedule(context.Background(), bob, bobSchedule, "")
	if err != nil {
		t.Fatal(err)
	}
	bobOccurrence := settledOccurrence(t, host, bob, bobItem, "pending")
	sidecar := filepath.Join(base, quietID+".probe-result")
	if err := os.WriteFile(sidecar, []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(sidecar, h.now.Add(-73*time.Hour), h.now.Add(-73*time.Hour)); err != nil {
		t.Fatal(err)
	}
	live, err := host.RoutineHistory(context.Background(), alice, "rt-retention-dispatched")
	if err != nil || len(live) != 1 {
		t.Fatalf("live history: %v %+v", err, live)
	}
	liveID := live[0].Run.ID
	now := h.now.Add(2 * time.Hour)
	compact := func() {
		t.Helper()
		if err := host.CompactOccurrences(now, 72*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	compact()
	if _, err := os.Stat(filepath.Join(base, "archive", quietID+".json")); err != nil {
		t.Fatalf("settled quiet detail not archived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(host.ownerRoot("bob"), "occurrences", bobOccurrence.Job.ID+".json")); err != nil {
		t.Fatalf("alice compaction touched bob's pending occurrence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(host.ownerRoot("bob"), "occurrences", "receipts", bobOccurrence.Job.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("alice's receipt crossed into bob's history: %v", err)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("orphaned replay sidecar retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, liveID+".json")); err != nil {
		t.Fatalf("unadmitted delivery lost live occurrence: %v", err)
	}
	d := commodel.Delivery{ID: "reminder-" + liveID, OccurrenceID: liveID, OwnerPrincipalID: "alice", OwnerContextID: "alice", Channel: "telegram_bot", ChatID: 11, Text: "hello"}
	rec, err := host.outbox.enqueue("alice", d, now)
	if err != nil {
		t.Fatal(err)
	}
	compact() // owner outbox has not handed the delivery to communication
	if _, err := os.Stat(filepath.Join(base, liveID+".json")); err != nil {
		t.Fatalf("pending owner delivery lost live occurrence: %v", err)
	}
	if err := host.outbox.markSent(rec); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(legacy, "outbox", "pending", d.ID+".json")
	if err := writeJSONAtomic(pending, d); err != nil {
		t.Fatal(err)
	}
	compact() // the read-only communication spool still has the delivery
	if _, err := os.Stat(filepath.Join(base, liveID+".json")); err != nil {
		t.Fatalf("pending channel delivery lost live occurrence: %v", err)
	}
	done := filepath.Join(legacy, "outbox", "done", d.ID+".json")
	if err := os.MkdirAll(filepath.Dir(done), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pending, done); err != nil {
		t.Fatal(err)
	}
	compact()
	if _, err := os.Stat(filepath.Join(base, "archive", liveID+".json")); err != nil {
		t.Fatalf("settled delivered detail not archived: %v", err)
	}
	rows, err := host.RoutineHistory(context.Background(), alice, "rt-retention-dispatched")
	if err != nil || len(rows) != 1 || rows[0].Run.DeliveryState != "delivered" {
		t.Fatalf("compacted history lost outcome: %v %+v", err, rows)
	}
	archive := filepath.Join(base, "archive", liveID+".json")
	if err := os.Chtimes(archive, now.Add(-49*time.Hour), now.Add(-49*time.Hour)); err != nil {
		t.Fatal(err)
	}
	compact()
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("aged archive retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "receipts", liveID+".json")); err != nil {
		t.Fatalf("receipt lost before retention: %v", err)
	}
	receipt := filepath.Join(base, "receipts", liveID+".json")
	if err := os.Chtimes(receipt, now.Add(-73*time.Hour), now.Add(-73*time.Hour)); err != nil {
		t.Fatal(err)
	}
	compact()
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatalf("aged receipt retained: %v", err)
	}
}

func TestOccurrenceCompactionKeepsAgentResultContract(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	for _, op := range []string{"read", "write"} {
		h.grant(t, "alice", op)
	}
	host := h.host(t)
	legacy := t.TempDir()
	if err := os.Mkdir(filepath.Join(legacy, "outbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	host.cfg.LegacySpoolDir = legacy
	schedule, err := host.CreateSchedule(context.Background(), alice, draft("rt-agent-retention", alice), "")
	if err != nil {
		t.Fatal(err)
	}
	o := settledOccurrence(t, host, alice, schedule, "dispatched")
	admitted := o.Job
	admitted.ConversationID = routines.RoutineConversation(o.ScheduleID)
	if _, err := host.queue.admit(admitted, h.now); err != nil {
		t.Fatal(err)
	}
	if _, err := host.queue.recordResult("alice", o.Job.ID, "completed", "done", h.now); err != nil {
		t.Fatal(err)
	}
	d := commodel.Delivery{ID: "result-" + o.Job.ID, JobID: o.Job.ID, OwnerPrincipalID: "alice", OwnerContextID: "alice", Channel: "telegram_bot", ChatID: 11, Text: "done"}
	if d.OccurrenceID != "" { // completed results keep their existing send contract
		t.Fatal("result unexpectedly requires occurrence reauthorization")
	}
	rec, err := host.outbox.enqueue("alice", d, h.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.outbox.markSent(rec); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(legacy, "outbox", "done", d.ID+".json"), d); err != nil {
		t.Fatal(err)
	}
	if err := host.CompactOccurrences(h.now.Add(time.Hour), 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(host.ownerRoot("alice"), "occurrences", "archive", o.Job.ID+".json")); err != nil {
		t.Fatalf("terminal agent detail not archived: %v", err)
	}
	rows, err := host.RoutineHistory(context.Background(), alice, schedule.ScheduleID)
	if err != nil || len(rows) != 1 || rows[0].Run.ExecutionState != "completed" || rows[0].Run.DeliveryState != "delivered" {
		t.Fatalf("agent receipt lost execution/delivery status: %v %+v", err, rows)
	}
}

func TestOccurrenceCompactionWaitsForEventChildResult(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	legacy := t.TempDir()
	if err := os.Mkdir(filepath.Join(legacy, "outbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	host.cfg.LegacySpoolDir = legacy
	s := draft("rt-event-retention", alice)
	s.JobKind = "event"
	s.Probe = &commodel.WatchProbe{Kind: "http", URL: "https://example.test/feed"}
	item, err := host.CreateSchedule(context.Background(), alice, s, "")
	if err != nil {
		t.Fatal(err)
	}
	o := settledOccurrence(t, host, alice, item, "dispatched")
	child := o.Job
	child.ID, child.IdempotencyKey = "event-child-retention", "event-child-retention"
	if _, err := host.queue.admit(child, h.now, o.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := host.queue.recordResult("alice", child.ID, "completed", "done", h.now); err != nil {
		t.Fatal(err)
	}
	now := h.now.Add(time.Hour)
	if err := host.CompactOccurrences(now, 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(host.ownerRoot("alice"), "occurrences", o.Job.ID+".json")
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("source archived before child result delivery: %v", err)
	}
	d := commodel.Delivery{ID: "result-" + child.ID, JobID: child.ID, OwnerPrincipalID: "alice", OwnerContextID: "alice", Channel: "telegram_bot", ChatID: 11, Text: "done"}
	rec, err := host.outbox.enqueue("alice", d, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.outbox.markSent(rec); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(legacy, "outbox", "done", d.ID+".json"), d); err != nil {
		t.Fatal(err)
	}
	if err := host.CompactOccurrences(now, 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(host.ownerRoot("alice"), "occurrences", "archive", o.Job.ID+".json")); err != nil {
		t.Fatalf("settled event source not archived: %v", err)
	}
}
