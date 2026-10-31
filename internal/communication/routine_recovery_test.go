package communication

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestRoutineTickLogsCause(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	if err := os.RemoveAll(filepath.Join(s.root, "occurrences")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(previous)
	g.tickProactive(o.DueAt)
	// "stage=1: open " asserts the cause text survives diagnostics.Redact —
	// on Linux the temp path itself is 32+ token chars and gets redacted,
	// so the occurrences/ path substring is not a stable assertion.
	if !bytes.Contains(out.Bytes(), []byte("stage=1: open ")) || !bytes.Contains(out.Bytes(), []byte("remains pending")) {
		t.Fatalf("cause missing from tick log: %s", out.String())
	}
}

func TestTerminalMappingDriftDoesNotBlockOtherOccurrences(t *testing.T) {
	s, old, g := occurrenceFixture(t)
	if err := s.PutOccurrence(old, old.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(old.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	mapping, err := s.loadMappingLocked(occurrenceID(old))
	if err != nil {
		t.Fatal(err)
	}
	mapping.Status = "completed"
	mapping.Fingerprint = "historical-drift"
	if err := atomicJSON(s.mappingPath(mapping.JobID), mapping); err != nil {
		t.Fatal(err)
	}
	next := old
	next.DueAt = old.DueAt.Add(time.Minute)
	if err := s.PutOccurrence(next, next.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(next.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(next)+".json"))
	if err != nil || len(b) == 0 {
		t.Fatalf("next occurrence unavailable: %v", err)
	}
}

func TestInvalidOccurrenceIsIsolatedWithoutDeletingEvidence(t *testing.T) {
	s, valid, g := occurrenceFixture(t)
	bad := filepath.Join(s.root, "occurrences", "routine-bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOccurrence(valid, valid.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(valid.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "occurrences", "quarantine", "routine-bad.json")); err != nil {
		t.Fatalf("bad record not preserved: %v", err)
	}
	if job, err := s.ClaimJob(); err != nil || job == nil || job.ID != occurrenceID(valid) {
		t.Fatalf("healthy occurrence blocked: %+v %v", job, err)
	}
}

func TestScheduleDoesNotAccumulateWhileProbing(t *testing.T) {
	s, _, g := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	created, err := s.CreateSchedule(Schedule{ScheduleID: "bounded", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "work", JobKind: "agent"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	schedules, err := s.ListSchedules(caller)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.root, "occurrences", schedules[0].LastOccurrenceID+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var o RoutineOccurrence
	if err := json.Unmarshal(body, &o); err != nil {
		t.Fatal(err)
	}
	o.State = "probing"
	if err := atomicJSON(path, o); err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue.Add(2*time.Minute), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("in-flight occurrences=%d, %v", len(entries), err)
	}
}

func TestScheduleDoesNotAccumulateWhileAgentJobActive(t *testing.T) {
	s, _, g := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	created, err := s.CreateSchedule(Schedule{ScheduleID: "bounded", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "work", JobKind: "agent"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(created.NextDue, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue.Add(2*time.Minute), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("active job occurrences=%d, %v", len(entries), err)
	}
}

func TestCompactQuietOccurrencesKeepsReceiptAndLiveWork(t *testing.T) {
	s, quiet, g := occurrenceFixture(t)
	quiet.DueAt = quiet.DueAt.Add(-72 * time.Hour)
	if err := s.PutOccurrence(quiet, quiet.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	id := occurrenceID(quiet)
	path := filepath.Join(s.root, "occurrences", id+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved RoutineOccurrence
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	saved.State = "no_change"
	if err := atomicJSON(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, quiet.DueAt, quiet.DueAt); err != nil {
		t.Fatal(err)
	}
	active := quiet
	active.DueAt = quiet.DueAt.Add(time.Minute)
	if err := s.PutOccurrence(active, active.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(quiet.DueAt.Add(72*time.Hour), 14*24*time.Hour, CompactLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("terminal detail retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "occurrences", occurrenceID(active)+".json")); err != nil {
		t.Fatalf("active removed: %v", err)
	}
	if err := s.PutOccurrence(quiet, quiet.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("recreated compacted occurrence: %v", err)
	}
	_ = g
}

func TestActiveMappingDriftIsolatesSchedule(t *testing.T) {
	s, old, g := occurrenceFixture(t)
	if err := s.PutOccurrence(old, old.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(old.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	mapping, err := s.loadMappingLocked(occurrenceID(old))
	if err != nil {
		t.Fatal(err)
	}
	mapping.Fingerprint = "drift"
	if err := atomicJSON(s.mappingPath(mapping.JobID), mapping); err != nil {
		t.Fatal(err)
	}
	next := old
	next.ScheduleID = "another"
	next.DueAt = old.DueAt.Add(time.Minute)
	if err := s.PutOccurrence(next, next.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(next.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(next)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var progressed RoutineOccurrence
	if err := json.Unmarshal(body, &progressed); err != nil {
		t.Fatal(err)
	}
	if progressed.State != "dispatched" {
		t.Fatalf("healthy schedule state: %s", progressed.State)
	}
}

func TestQuarantinedOccurrenceCannotReplay(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.root, "occurrences", occurrenceID(o)+".json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOccurrence(o, o.Job.Envelope); err == nil {
		t.Fatal("quarantined occurrence recreated")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("replay created: %v", err)
	}
}

func TestCorruptLastOccurrenceDoesNotBlockScheduleTick(t *testing.T) {
	s, _, g := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	created, err := s.CreateSchedule(Schedule{ScheduleID: "bounded", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "work", JobKind: "agent"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListSchedules(caller)
	if err != nil {
		t.Fatal(err)
	}
	id := items[0].LastOccurrenceID
	path := filepath.Join(s.root, "occurrences", id+".json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue.Add(time.Minute), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "occurrences", "quarantine", id+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleResumesAfterOldMappingIsCompacted(t *testing.T) {
	s, _, g := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	created, err := s.CreateSchedule(Schedule{ScheduleID: "bounded", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "work", JobKind: "agent"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(created.NextDue, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListSchedules(caller)
	if err != nil {
		t.Fatal(err)
	}
	id := items[0].LastOccurrenceID
	if err := os.Remove(s.mappingPath(id)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.root, "pending", spoolFileID(id)+".json")); err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue.Add(time.Minute), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListSchedules(caller)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].LastOccurrenceID == id {
		t.Fatal("old compacted mapping held schedule forever")
	}
}

func TestCompactArchivesRecentQuietOccurrence(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	id := occurrenceID(o)
	path := filepath.Join(s.root, "occurrences", id+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved RoutineOccurrence
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	saved.State = "no_change"
	if err := atomicJSON(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(time.Now(), 14*24*time.Hour, CompactLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("quiet occurrence still in hot path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "occurrences", "archive", id+".json")); err != nil {
		t.Fatalf("detail missing: %v", err)
	}
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("archived occurrence replayed: %v", err)
	}
}

func TestCompactDispatchedWaitsForDelivery(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	o.JobKind = "reminder"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	id := occurrenceID(o)
	path := filepath.Join(s.root, "occurrences", id+".json")
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(time.Now(), 14*24*time.Hour, CompactLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pending delivery lost occurrence: %v", err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("delivery: %+v %v", d, err)
	}
	if err := s.CompleteDelivery(d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(time.Now(), 14*24*time.Hour, CompactLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("completed delivery retained hot occurrence: %v", err)
	}
}

func TestPendingMappingDriftDoesNotRetryOrBlockOtherSchedule(t *testing.T) {
	s, old, g := occurrenceFixture(t)
	if err := s.PutOccurrence(old, old.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(old.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	id := occurrenceID(old)
	path := filepath.Join(s.root, "occurrences", id+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pending RoutineOccurrence
	if err := json.Unmarshal(body, &pending); err != nil {
		t.Fatal(err)
	}
	pending.State = "pending"
	if err := atomicJSON(path, pending); err != nil {
		t.Fatal(err)
	}
	mapping, err := s.loadMappingLocked(id)
	if err != nil {
		t.Fatal(err)
	}
	mapping.Fingerprint = "drift"
	if err := atomicJSON(s.mappingPath(id), mapping); err != nil {
		t.Fatal(err)
	}
	next := old
	next.ScheduleID = "another"
	next.DueAt = old.DueAt.Add(time.Minute)
	if err := s.PutOccurrence(next, next.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(next.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(next)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var progressed RoutineOccurrence
	if err := json.Unmarshal(body, &progressed); err != nil {
		t.Fatal(err)
	}
	if progressed.State != "dispatched" {
		t.Fatalf("healthy schedule state %s", progressed.State)
	}
}
