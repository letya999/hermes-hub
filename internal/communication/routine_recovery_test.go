package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

func TestRoutineTickLogsCause(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	_, host, root := automationGatewayFixture(t, c)
	env := c.Users[0].Envelope(11)
	in := Schedule{ScheduleID: "tick-log", Envelope: env, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "reminder"}
	if _, err := host.CreateSchedule(context.Background(), env, in, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "users", "alice", "occurrences")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(previous)
	_ = host.Tick(context.Background(), in.NextDue)
	// "stage=N: open " asserts the cause text survives diagnostics.Redact —
	// on Linux the temp path itself is 32+ token chars and gets redacted,
	// so the occurrences/ path substring is not a stable assertion.
	if !bytes.Contains(out.Bytes(), []byte(": open ")) || !bytes.Contains(out.Bytes(), []byte("remains pending")) {
		t.Fatalf("cause missing from tick log: %s", out.String())
	}
}

func TestTerminalMappingDriftDoesNotBlockOtherOccurrences(t *testing.T) {
	s, old, _ := occurrenceFixture(t)
	if err := s.PutOccurrence(old, old.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(old.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	mapping, err := s.loadMappingLocked(routines.OccurrenceID(old))
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
	if err := s.DispatchDueOccurrences(next.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.root, "occurrences", routines.OccurrenceID(next)+".json"))
	if err != nil || len(b) == 0 {
		t.Fatalf("next occurrence unavailable: %v", err)
	}
}

func TestInvalidOccurrenceIsIsolatedWithoutDeletingEvidence(t *testing.T) {
	s, valid, _ := occurrenceFixture(t)
	bad := filepath.Join(s.root, "occurrences", "routine-bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOccurrence(valid, valid.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(valid.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "occurrences", "quarantine", "routine-bad.json")); err != nil {
		t.Fatalf("bad record not preserved: %v", err)
	}
	if job, err := s.ClaimJob(); err != nil || job == nil || job.ID != routines.OccurrenceID(valid) {
		t.Fatalf("healthy occurrence blocked: %+v %v", job, err)
	}
}

func TestScheduleDoesNotAccumulateWhileProbing(t *testing.T) {
	s, _, _ := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	created, err := s.CreateSchedule(Schedule{ScheduleID: "bounded", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "work", JobKind: "agent"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue, "", s.authorizeOccurrence); err != nil {
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
	if err := s.TickSchedules(created.NextDue.Add(2*time.Minute), "", s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("in-flight occurrences=%d, %v", len(entries), err)
	}
}

func TestScheduleDoesNotAccumulateWhileAgentJobActive(t *testing.T) {
	s, _, _ := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	created, err := s.CreateSchedule(Schedule{ScheduleID: "bounded", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "work", JobKind: "agent"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue, "", s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(created.NextDue, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue.Add(2*time.Minute), "", s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("active job occurrences=%d, %v", len(entries), err)
	}
}

// Occurrence-detail retention (aging terminal details out to receipts while
// keeping live occurrences) was communication-spool machinery and has no
// owner-side entry point yet: Spool.Compact only touches the communication
// outbox, and neither Engine nor Host exposes occurrence compaction. Tracked
// as a #523 follow-up for the ToolHub retention slice — the history scanner
// already reads occurrences/{archive,receipts} for when it lands.
func TestCompactQuietOccurrencesKeepsReceiptAndLiveWork(t *testing.T) {
	t.Skip("occurrence retention unimplemented under the ToolHub owner")
}

func TestActiveMappingDriftIsolatesSchedule(t *testing.T) {
	s, old, _ := occurrenceFixture(t)
	if err := s.PutOccurrence(old, old.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(old.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	mapping, err := s.loadMappingLocked(routines.OccurrenceID(old))
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
	if err := s.DispatchDueOccurrences(next.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(s.root, "occurrences", routines.OccurrenceID(next)+".json"))
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
	s, o, _ := occurrenceFixture(t)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.root, "occurrences", routines.OccurrenceID(o)+".json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, s.authorizeOccurrence); err != nil {
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
	s, _, _ := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	created, err := s.CreateSchedule(Schedule{ScheduleID: "bounded", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "work", JobKind: "agent"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue, "", s.authorizeOccurrence); err != nil {
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
	if err := s.TickSchedules(created.NextDue.Add(time.Minute), "", s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "occurrences", "quarantine", id+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleResumesAfterOldMappingIsCompacted(t *testing.T) {
	s, _, _ := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	created, err := s.CreateSchedule(Schedule{ScheduleID: "bounded", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "work", JobKind: "agent"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue, "", s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(created.NextDue, s.authorizeOccurrence); err != nil {
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
	if err := s.TickSchedules(created.NextDue.Add(time.Minute), "", s.authorizeOccurrence); err != nil {
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

// Same retired contract as TestCompactQuietOccurrencesKeepsReceiptAndLiveWork:
// occurrence archive/receipt aging was communication-spool machinery; the
// owner-side retention entry point is a tracked #523 follow-up.
func TestCompactArchivesRecentQuietOccurrence(t *testing.T) {
	t.Skip("occurrence retention unimplemented under the ToolHub owner")
}

// Same retired contract as TestCompactQuietOccurrencesKeepsReceiptAndLiveWork:
// delivery-gated occurrence aging was spool compaction; the owner-side
// retention entry point is a tracked #523 follow-up.
func TestCompactDispatchedWaitsForDelivery(t *testing.T) {
	t.Skip("occurrence retention unimplemented under the ToolHub owner")
}

func TestPendingMappingDriftDoesNotRetryOrBlockOtherSchedule(t *testing.T) {
	s, old, _ := occurrenceFixture(t)
	if err := s.PutOccurrence(old, old.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(old.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	id := routines.OccurrenceID(old)
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
	if err := s.DispatchDueOccurrences(next.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(filepath.Join(s.root, "occurrences", routines.OccurrenceID(next)+".json"))
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
