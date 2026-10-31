package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The SQLite backend gives the spool's record classes table durability
// while binary payloads keep landing on the filesystem. State moves are
// UPDATEs, so a rename can never leave a half-moved record.
func TestSpoolDBFileSystem(t *testing.T) {
	root := t.TempDir()
	fs, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	record := filepath.Join(root, "pending", "job-1.json")
	if err := fs.WriteFile(record, []byte(`{"id":"job-1"}`)); err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"id":"job-1"}` {
		t.Fatalf("record body diverged: %s", body)
	}
	// A job state transition is one UPDATE — pending to running.
	claimed := filepath.Join(root, "running", "job-1.json")
	if err := fs.Rename(record, claimed); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("moved record still reads: %v", err)
	}
	entries, err := fs.ReadDir(filepath.Join(root, "running"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != "job-1.json" {
		t.Fatalf("running class lists %v", entries)
	}
	if err := fs.Remove(claimed); err != nil {
		t.Fatal(err)
	}

	// Payloads and scratch stay real files beside the database.
	blob := filepath.Join(root, "inbound", "m-1.bin")
	if err := fs.WriteFile(blob, []byte{0x1, 0x2}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(blob); err != nil {
		t.Fatalf("binary payload must stay a file: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "inbound", "m-1.bin")); err != nil {
		t.Fatal(err)
	}
}

// The spool picks its record backend from the activation marker: with
// records.activated present, reads come from spool.db rows; without it the
// same files stay authoritative even when a stray spool.db exists.
func TestSpoolSelectsActivatedBackend(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(root, "pending", "job-1.json")
	if err := dbBack.WriteFile(record, []byte(`{"from":"db"}`)); err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pending"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, []byte(`{"from":"file"}`), 0600); err != nil {
		t.Fatal(err)
	}

	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.fsys == nil {
		t.Fatal("spool has no record backend")
	}
	body, err := s.fsys.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"from":"file"}` {
		t.Fatalf("unactivated spool must read files: %s", body)
	}
	if err := s.fsys.Close(); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(root, "records.activated")
	if err := os.WriteFile(marker, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	body, err = s2.fsys.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"from":"db"}` {
		t.Fatalf("activated spool must read spool.db: %s", body)
	}
	if err := s2.fsys.Close(); err != nil {
		t.Fatal(err)
	}
}

// Writes on an activated spool land in spool.db rows, not sibling files.
func TestSpoolDBWritesGoToRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()

	record := filepath.Join(root, "callbacks", "cb-1.json")
	if err := s.writeJSON(record, map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		t.Fatal("record write created a shadow file on an activated spool")
	}
	check, err := openSpoolDB(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	body, err := check.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"k":"v"}` {
		t.Fatalf("row body diverged: %s", body)
	}
}

// The full job state machine must run on rows alone: enqueue lands in
// pending, claim moves it to running, completion to done — all inside
// spool.db, with the filesystem showing no shadow files.
func TestSpoolDBJobLifecycle(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-db-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-db-1", Text: "hello", CreatedAt: time.Now().UTC()}
	queued, err := s.Enqueue(job)
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("fresh job not queued")
	}
	pending := filepath.Join(root, "pending", "job-db-1.json")
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		t.Fatal("enqueue shadowed a file on an activated spool")
	}
	claimed, err := s.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != "job-db-1" {
		t.Fatalf("claimed %+v", claimed)
	}
	if _, err := os.Lstat(filepath.Join(root, "running", "job-db-1.json")); !os.IsNotExist(err) {
		t.Fatal("claim shadowed a file on an activated spool")
	}
	if err := s.CompleteJob("job-db-1"); err != nil {
		t.Fatal(err)
	}
	s.fsys.Close()

	check, err := openSpoolDB(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	if _, err := check.ReadFile(filepath.Join(root, "done", "job-db-1.json")); err != nil {
		t.Fatalf("done row missing: %v", err)
	}
	if names, err := check.ReadDir(filepath.Join(root, "mappings")); err != nil || len(names) == 0 {
		t.Fatalf("mapping rows missing: %v %v", names, err)
	}
}

// A crash with a claimed job must recover on rows alone: the interrupted run
// lands in failed/uncertain, the ledger survives the restart, and replaying
// the same idempotent admission stays a no-op.
func TestSpoolDBRestartRecovery(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-db-2", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-db-2", Text: "hello", CreatedAt: time.Now().UTC()}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil {
		t.Fatal("job not claimed")
	}
	// Process death: no graceful close, the writer lock is still released by
	// flock semantics on file close, so emulate by closing the DB handle only.
	s.fsys.Close()

	s2, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	mapping, found, err := s2.Mapping("job-db-2")
	if err != nil {
		t.Fatal(err)
	}
	if !found || mapping.Status != "uncertain" {
		t.Fatalf("crashed job mapping = %+v, found %v", mapping, found)
	}
	if names, err := s2.fsys.ReadDir(filepath.Join(root, "failed")); err != nil || len(names) != 1 || names[0] != "job-db-2.json" {
		t.Fatalf("crashed job not in failed/: %v %v", names, err)
	}
	if state := s2.reservationState("job-db-2"); state == "open" {
		t.Fatal("crashed reservation still open after recovery")
	}
	queued, err := s2.Enqueue(job)
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("idempotent replay enqueued a duplicate")
	}
	if names, err := s2.fsys.ReadDir(filepath.Join(root, "pending")); err != nil || len(names) != 0 {
		t.Fatalf("replay shadowed a pending row: %v %v", names, err)
	}
	s2.fsys.Close()
}

// The budget ledger is a durable record: an open reservation must survive a
// restart, or demand accounting silently resets. On an activated spool the
// ledger lives in spool.db, not in a budget.json file.
func TestSpoolDBLedgerSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-db-3", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-db-3", Text: "hello", CreatedAt: time.Now().UTC()}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "budget.json")); !os.IsNotExist(err) {
		t.Fatal("ledger shadowed a budget.json file on an activated spool")
	}
	s.fsys.Close()

	s2, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.fsys.Close()
	if state := s2.reservationState("job-db-3"); state != "open" {
		t.Fatalf("reservation lost across restart: %q", state)
	}
	if bucket := s2.budgetScope("user:alice"); bucket.PendingJobs != 1 {
		t.Fatalf("scope demand lost across restart: %+v", bucket)
	}
}

// The usage journal is a durable record: metering appends land in spool.db,
// never in a usage.jsonl shadow file, and still answer UsageTotals after a
// restart.
func TestSpoolDBUsageJournalOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job-db-4", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", ContextID: "alice"}
	var in, out int64 = 40, 12
	report := hubruntime.SessionUsage{SessionID: "sess-1", SessionFound: true, Model: "m", FetchedAt: time.Now().UTC(), InputTokens: &in, OutputTokens: &out}
	if err := s.RecordUsage(job, "run-1", report); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "usage.jsonl")); !os.IsNotExist(err) {
		t.Fatal("metering shadowed a usage.jsonl file on an activated spool")
	}
	s.fsys.Close()

	s2, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.fsys.Close()
	totals, err := s2.UsageTotals("user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if totals.Jobs != 1 || totals.InputTokens != 40 || totals.OutputTokens != 12 {
		t.Fatalf("journal totals lost: %+v", totals)
	}
	// Idempotent across the restart: the same (job, run) pair never appends twice.
	if err := s2.RecordUsage(job, "run-1", report); err != nil {
		t.Fatal(err)
	}
	again, err := s2.UsageTotals("user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if again.Jobs != 1 {
		t.Fatalf("duplicate metering recorded: %+v", again)
	}
}

// The outbox is a record class too: enqueue→claim→complete runs on rows, a
// crash in sending/ recovers into failed/, and replayed enqueues stay
// idempotent — no shadow files anywhere under outbox/.
func TestSpoolDBDeliveryLifecycle(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDelivery(Delivery{ID: "d-1", ChatID: 11, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "outbox", "pending", "d-1.json")); !os.IsNotExist(err) {
		t.Fatal("delivery shadowed a file on an activated spool")
	}
	if err := s.EnqueueDelivery(Delivery{ID: "d-1", ChatID: 11, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil {
		t.Fatal(err)
	}
	if d == nil || d.ID != "d-1" {
		t.Fatalf("claimed %+v", d)
	}
	if names, err := s.fsys.ReadDir(filepath.Join(root, "outbox", "pending")); err != nil || len(names) != 0 {
		t.Fatalf("claimed delivery still pending: %v %v", names, err)
	}
	s.fsys.Close()

	// Crash while sending: recovery lands the delivery in failed/.
	s2, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.fsys.Close()
	if names, err := s2.fsys.ReadDir(filepath.Join(root, "outbox", "failed")); err != nil || len(names) != 1 || names[0] != "d-1.json" {
		t.Fatalf("crashed delivery not recovered to failed/: %v %v", names, err)
	}
}

// Routine schedules are record rows: creation lands in spool.db and the
// rebuilt engine lists them after a restart — no shadow file under
// schedules/.
func TestSpoolDBScheduleOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	in := Schedule{ScheduleID: "sched-1", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 * * * *", Input: "ping", JobKind: "reminder"}
	if _, err := s.CreateSchedule(in, caller, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "schedules", "sched-1.json")); !os.IsNotExist(err) {
		t.Fatal("schedule shadowed a file on an activated spool")
	}
	s.fsys.Close()

	s2, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.fsys.Close()
	items, err := s2.ListSchedules(caller)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ScheduleID != "sched-1" {
		t.Fatalf("schedule lost across restart: %+v", items)
	}
}

// Occurrences are records too: a due reminder dispatched through the engine
// produces its delivery without a single shadow file under occurrences/ or
// outbox/.
func TestSpoolDBOccurrenceDispatchOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Channel: "telegram_bot", ChatID: 11, Text: "call the bank"}
	o := RoutineOccurrence{ScheduleID: "sched-rem", Revision: 1, DueAt: time.Unix(100, 0), Job: job, JobKind: "reminder"}
	if err := s.PutOccurrence(o, job.Envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "occurrences", routines.OccurrenceID(o)+".json")); !os.IsNotExist(err) {
		if names, _ := s.fsys.ReadDir(filepath.Join(root, "occurrences")); len(names) == 0 {
			t.Fatal("occurrence landed nowhere")
		}
		t.Fatal("occurrence shadowed a file on an activated spool")
	}
	g := &Gateway{spool: s, config: Config{OrganizationID: "personal", Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, users: map[int64]User{11: {ID: "alice", Enabled: true}}, now: func() time.Time { return time.Unix(90, 0) }}
	s.resolveChannel = func(id string) routines.OccurrenceChannel { return g.channel(id) }
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil {
		t.Fatal(err)
	}
	if d == nil || d.Text != "call the bank" {
		t.Fatalf("reminder delivery: %+v", d)
	}
}

// The operator surfaces around the queue must read rows too: update offset
// cursors, rewrite a claimed delivery, complete it with its payload blob
// staying a file, repair a torn admission, and answer session/active-mapping
// queries — all without shadow files on an activated spool.
func TestSpoolDBSurfaces(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()

	// Telegram update cursor: a durable record, not a file.
	if err := s.SaveOffset(42); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "offset.json")); !os.IsNotExist(err) {
		t.Fatal("offset cursor shadowed a file")
	}
	if got, err := s.LoadOffset(); err != nil || got != 42 {
		t.Fatalf("offset: %d %v", got, err)
	}

	// Delivery blob payloads stay real files next to the row record.
	blob, err := s.WriteDeliveryBlob("d-2", "part-1.bin", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if body, err := s.ReadDeliveryBlob("d-2", blob); err != nil || string(body) != "payload" {
		t.Fatalf("blob readback: %q %v", body, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "outbox", "blobs", "d-2", blob)); err != nil {
		t.Fatalf("blob must stay a file: %v", err)
	}
	if err := s.EnqueueDelivery(Delivery{ID: "d-2", ChatID: 11, Text: "with blob", Artifacts: []hubruntime.ArtifactRef{{Blob: blob}}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("claim: %+v %v", d, err)
	}
	d.Text = "part one of two"
	if err := s.UpdateSending(*d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "outbox", "sending", "d-2.json")); !os.IsNotExist(err) {
		t.Fatal("sending update shadowed a file")
	}
	if err := s.CompleteDelivery("d-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "outbox", "blobs", "d-2", blob)); !os.IsNotExist(err) {
		t.Fatal("completed delivery kept its blob")
	}

	// Torn admission: mapping gone, job still pending — a replay must repair
	// it from the durable pending record instead of duplicating the job.
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-db-5", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-db-5", Text: "repair me", CreatedAt: time.Now().UTC()}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if err := s.fsys.Remove(s.mappingPath(job.ID)); err != nil {
		t.Fatal(err)
	}
	queued, err := s.Enqueue(job)
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("torn admission duplicated the job")
	}
	if _, found, err := s.Mapping(job.ID); err != nil || !found {
		t.Fatalf("admission not repaired: %v %v", found, err)
	}

	// Session + active-mapping surfaces answer from rows.
	if err := s.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "running", SessionID: "sess-9", RunID: "run-9", RuntimeGeneration: "g1"}); err != nil {
		t.Fatal(err)
	}
	conv, found, err := s.SessionFor("alice", "alice", "telegram-11")
	if err != nil {
		t.Fatal(err)
	}
	if !found || conv.SessionID != "sess-9" {
		t.Fatalf("session lookup: %+v %v", conv, found)
	}
	actives, err := s.ActiveJobsForTask("alice", "")
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, m := range actives {
		if m.JobID == job.ID {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("active mapping missing from rows: %+v", actives)
	}
}

// The routines plane keeps its durable records under the same substrate:
// assignments, watcher baselines and digest cursors must survive a restart
// as rows, not shadow files.
func TestSpoolDBRoutineRecordsOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	if _, _, err := s.rt.CreateAssignment(commodel.Assignment{AssignmentID: "asg-1", Envelope: caller, Intent: "watch the vendor", Title: "Vendor"}, caller); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "assignments", "asg-1.json")); !os.IsNotExist(err) {
		t.Fatal("assignment shadowed a file")
	}
	if err := s.SeedWatchBaseline("w-1", "v0", time.Unix(50, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "watchers", "w-1.json")); !os.IsNotExist(err) {
		t.Fatal("watch baseline shadowed a file")
	}
	if err := s.writeJSON(s.digestPath("sched-9"), DigestState{ScheduleID: "sched-9", LastDigestAt: time.Unix(60, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "digests", "sched-9.json")); !os.IsNotExist(err) {
		t.Fatal("digest cursor shadowed a file")
	}
	s.fsys.Close()

	s2, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.fsys.Close()
	item, err := s2.rt.LoadAssignment("asg-1", caller)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Vendor" {
		t.Fatalf("assignment lost: %+v", item)
	}
	s2.mu.Lock()
	watch, err := s2.rt.LoadWatchStateLocked("w-1")
	s2.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if watch.Value != "v0" {
		t.Fatalf("watch baseline lost: %+v", watch)
	}
	s2.mu.Lock()
	digest, err := s2.rt.LoadDigestCursorLocked("sched-9")
	s2.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if digest.LastDigestAt != time.Unix(60, 0) {
		t.Fatalf("digest cursor lost: %+v", digest)
	}
}

// Tasks are durable records: creation writes rows, and a job bound by
// TaskID must still resolve its conversation binding after a restart.
func TestSpoolDBTasksOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	task, _, err := s.CreateTask(user, 11, 0, "watch vendor", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "tasks", task.TaskID+".json")); !os.IsNotExist(err) {
		t.Fatal("task shadowed a file")
	}
	s.fsys.Close()

	s2, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.fsys.Close()
	got, err := s2.TaskByID(user, 11, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != task.TaskID {
		t.Fatalf("task lost across restart: %+v", got)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-task-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-task-1", ChatID: 11, TaskID: task.TaskID, Text: "bound", CreatedAt: time.Now().UTC()}
	if _, err := s2.Enqueue(job); err != nil {
		t.Fatalf("task binding unavailable on rows: %v", err)
	}
}

// Run receipts are write-ahead records: a terminal stream event must land in
// run-events rows, settle the mapping, and hand off the answer delivery —
// without a single shadow file.
func TestSpoolDBStreamReceiptOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-db-6", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-db-6", ChatID: 11, Text: "work", CreatedAt: time.Now().UTC()}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "running", RunID: "run-1", SessionID: "sess-1", RuntimeGeneration: "g1"}); err != nil {
		t.Fatal(err)
	}
	event := hubruntime.ExecuteResponse{JobID: job.ID, EventID: "e-1", Status: "completed", LastEvent: "run.completed", RunID: "run-1", SessionID: "sess-1", RuntimeGeneration: "g1", Text: "the answer"}
	if err := s.RecordStreamEvent(*claimed, event); err != nil {
		t.Fatal(err)
	}
	if names, err := s.fsys.ReadDir(filepath.Join(root, "run-events")); err != nil || len(names) == 0 {
		t.Fatalf("receipt missing from rows: %v %v", names, err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, "run-events")); err == nil && len(entries) > 0 {
		t.Fatalf("receipt shadowed a file: %v", entries)
	}
	mapping, found, err := s.Mapping(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || mapping.Status != "completed" {
		t.Fatalf("terminal event did not settle: %+v", mapping)
	}
	d, err := s.ClaimDelivery()
	if err != nil {
		t.Fatal(err)
	}
	if d == nil || d.Text != "the answer" {
		t.Fatalf("handoff delivery: %+v", d)
	}
}

// Payload sidecars share a record dir: a directory listing must show the
// union of row names and real files, or compaction and cleanup lose sight of
// one substrate or the other.
func TestSpoolDBReadDirUnionsRowsAndPayloads(t *testing.T) {
	root := t.TempDir()
	fs, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	dir := filepath.Join(root, "occurrences")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(filepath.Join(dir, "o-1.json"), []byte(`{"id":"o-1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "o-1.probe-result"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	names, err := fs.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
	}
	if !seen["o-1.json"] || !seen["o-1.probe-result"] {
		t.Fatalf("listing must union rows and files: %v", names)
	}
}

// Retention aging works on record mtimes: a fresh done record survives
// Compact, an aged one archives out of the row table — never shadowing a
// file, and never sweeping live state because mtime is unknown.
func TestSpoolDBCompactAgesRowRecords(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-db-c1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-db-c1", Text: "hello", CreatedAt: time.Now().UTC()}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob("job-db-c1"); err != nil {
		t.Fatal(err)
	}
	done := filepath.Join(root, "done", "job-db-c1.json")
	limits := CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20}
	if err := s.Compact(time.Now(), 72*time.Hour, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fsys.ReadFile(done); err != nil {
		t.Fatalf("fresh record must survive compaction: %v", err)
	}
	if err := s.Compact(time.Now().Add(2*72*time.Hour), 72*time.Hour, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fsys.ReadFile(done); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aged record must archive out: %v", err)
	}
}

// A settled occurrence archives inside the row substrate: the hot record
// leaves occurrences/, its history lands in occurrences/receipts/ and the
// terminal record in occurrences/archive/ — all as rows, no shadow files.
func TestSpoolDBCompactArchivesOccurrenceOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-occ-1", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Channel: "telegram_bot", ChatID: 11, Text: "scheduled work"}
	o := RoutineOccurrence{ScheduleID: "sched-occ-1", Revision: 1, DueAt: time.Now().Add(-time.Hour), Job: job, State: "no_change"}
	name := occurrenceID(o) + ".json"
	if err := s.writeJSON(filepath.Join(root, "occurrences", name), o); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(time.Now(), 72*time.Hour, CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fsys.ReadFile(filepath.Join(root, "occurrences", name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settled occurrence must leave the hot class: %v", err)
	}
	if _, err := s.fsys.ReadFile(filepath.Join(root, "occurrences", "archive", name)); err != nil {
		t.Fatalf("occurrence archive row missing: %v", err)
	}
	if _, err := s.fsys.ReadFile(filepath.Join(root, "occurrences", "receipts", name)); err != nil {
		t.Fatalf("occurrence receipt row missing: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, "occurrences")); err == nil {
		for _, e := range entries {
			if e.Name() == name {
				t.Fatal("occurrence compaction shadowed a file")
			}
		}
	}
}

// Terminal receipts and their mappings age out on rows exactly like files:
// a completed stream event's receipt archives once its delivery settles and
// the retention cutoff passes; the terminal mapping leaves with it.
func TestSpoolDBCompactArchivesReceiptAndMappingOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-db-r1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-db-r1", ChatID: 11, Text: "work", CreatedAt: time.Now().UTC()}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "running", RunID: "run-1", SessionID: "sess-1", RuntimeGeneration: "g1"}); err != nil {
		t.Fatal(err)
	}
	event := hubruntime.ExecuteResponse{JobID: job.ID, EventID: "e-1", Status: "completed", LastEvent: "run.completed", RunID: "run-1", SessionID: "sess-1", RuntimeGeneration: "g1", Text: "done"}
	if err := s.RecordStreamEvent(*claimed, event); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("handoff delivery: %+v %v", d, err)
	}
	receipts, err := s.fsys.ReadDir(filepath.Join(root, "run-events"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipt rows: %v %v", receipts, err)
	}
	receipt := filepath.Join(root, "run-events", receipts[0])
	future := time.Now().Add(2 * 72 * time.Hour)
	limits := CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20}
	if err := s.Compact(future, 72*time.Hour, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fsys.ReadFile(receipt); err != nil {
		t.Fatalf("receipt with a live delivery must stay hot: %v", err)
	}
	if err := s.CompleteDelivery(d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(future, 72*time.Hour, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fsys.ReadFile(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aged receipt must archive out: %v", err)
	}
	if _, found, err := s.Mapping(job.ID); err != nil || found {
		t.Fatalf("aged terminal mapping must archive: %v %v", found, err)
	}
}

// Journal compaction must rewrite the usage.jsonl ROW, not shadow a file:
// bounded growth lives inside spool.db on an activated spool.
func TestSpoolDBUsageCompactKeepsRow(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	for i, jid := range []string{"u-1", "u-2", "u-3"} {
		job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: jid, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message"}
		report := hubruntime.SessionUsage{SessionFound: true, SessionID: "sess", InputTokens: int64ptr(int64(i + 1)), OutputTokens: int64ptr(1)}
		if err := s.RecordUsage(job, "run-"+jid, report); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Compact(time.Now(), 72*time.Hour, CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20, MaxUsageEntries: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(usagePath(root)); !os.IsNotExist(err) {
		t.Fatal("usage compaction shadowed a journal file on an activated spool")
	}
	entries, err := s.usageEntriesLocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 2 {
		t.Fatalf("journal not bounded in rows: %d", len(entries))
	}
}

// Inline-keyboard approvals resolve through the callbacks record class:
// a persisted callback tuple must resolve after the move to rows.
func TestSpoolDBApprovalCallbackOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	id := "0123456789abcdef01234567"
	if err := s.writeJSON(filepath.Join(root, "callbacks", id+".json"), approvalCallback{JobID: "job-cb", RequestID: "req-1", Choice: "yes"}); err != nil {
		t.Fatal(err)
	}
	jobID, reqID, choice, err := s.ApprovalCallback("a:" + id)
	if err != nil {
		t.Fatal(err)
	}
	if jobID != "job-cb" || reqID != "req-1" || choice != "yes" {
		t.Fatalf("callback tuple: %q %q %q", jobID, reqID, choice)
	}
}

// Mapping rebuild runs at open against the record classes: an orphaned job
// row with an idempotency key must regain its mapping and its dedup index.
func TestSpoolDBRebuildMappingsOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	rawOrphan, _ := json.Marshal(Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-orphan", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-orphan", ChatID: 11, Text: "t", CreatedAt: time.Now().UTC()})
	if err := dbBack.WriteFile(filepath.Join(root, "done", "job-orphan.json"), rawOrphan); err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	mapping, found, err := s.Mapping("job-orphan")
	if err != nil {
		t.Fatal(err)
	}
	if !found || mapping.Status != "completed" {
		t.Fatalf("orphan job never rebuilt a mapping: %+v %v", mapping, found)
	}
}

// An interrupted journal append leaves an unterminated tail inside the row:
// open-time repair quarantines it and truncates the row back to the last
// complete record.
func TestSpoolDBUsageTailRepairOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	good := []byte(`{"version":2,"job_id":"j-ok","run_id":"r-ok","measured_at":"2026-01-01T00:00:00Z"}` + "\n")
	if err := dbBack.WriteFile(usagePath(root), append(good, []byte(`{"version":2,"job_id":"trunc`)...)); err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "j-new", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message"}
	report := hubruntime.SessionUsage{SessionFound: true, SessionID: "sess", InputTokens: int64ptr(5), OutputTokens: int64ptr(2)}
	if err := s.RecordUsage(job, "r-new", report); err != nil {
		t.Fatal(err)
	}
	entries, err := s.usageEntriesLocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].JobID != "j-ok" || entries[1].JobID != "j-new" {
		t.Fatalf("tail repair lost entries: %+v", entries)
	}
}

// A part-delivered message persists progress on the sending row; the record
// rewrites through the substrate, not a shadow file.
func TestSpoolDBUpdateSendingOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	if err := s.EnqueueDelivery(Delivery{ID: "d-upd", ChatID: 11, Text: "part-1", Channel: "telegram_bot", ConversationID: "telegram-11", DeliveryTargetID: "telegram-11"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("claim: %+v %v", d, err)
	}
	d.Text = "part-1 continued"
	if err := s.UpdateSending(*d); err != nil {
		t.Fatal(err)
	}
	body, err := s.fsys.ReadFile(filepath.Join(root, "outbox", "sending", "d-upd.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "continued") {
		t.Fatal("sending row did not persist progress")
	}
}

// A failed job with a terminal mapping archives on rows through the same
// guard as files: compaction must read the failed class from the substrate.
func TestSpoolDBCompactArchivesFailedOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-db-f1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-db-f1", ChatID: 11, Text: "work", CreatedAt: time.Now().UTC()}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if err := s.FailJob("job-db-f1"); err != nil {
		t.Fatal(err)
	}
	failed := filepath.Join(root, "failed", "job-db-f1.json")
	if err := s.Compact(time.Now().Add(2*72*time.Hour), 72*time.Hour, CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fsys.ReadFile(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal failed record must archive out: %v", err)
	}
}

// A routine job's claim consults its occurrence row: with a current
// occurrence the job dispatches; if the row is invisible the claim path
// wrongly cancels live work.
func TestSpoolDBClaimRoutineJobReadsOccurrenceRow(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "routine-ext-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron", IdempotencyKey: "idem-rt-1", ChatID: 11, Text: "tick", CreatedAt: time.Now().UTC()}
	o := RoutineOccurrence{ScheduleID: "ext-sched", Revision: 1, DueAt: time.Now().Add(-time.Minute), Job: job, State: "dispatched"}
	if err := s.writeJSON(filepath.Join(root, "occurrences", "routine-ext-1.json"), o); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != "routine-ext-1" {
		t.Fatalf("routine job must claim, not cancel: %+v", claimed)
	}
	if _, err := s.fsys.ReadFile(filepath.Join(root, "failed", "routine-ext-1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("routine job was wrongly cancelled")
	}
}

// The admission cursor is a durable record: after restart the fairness
// rotation must resume from the row, or one owner's lane starves.
func TestSpoolDBAdmissionCursorOnRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.WriteFile(filepath.Join(root, "admission-cursor.json"), []byte(`{"scope":"alice\u0000alice"}`)); err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	mk := func(id, principal string) Job {
		return Job{Envelope: identity.TelegramEnvelope(principal, 11, principal, "policy-1"), ID: id, OrganizationID: "personal", UserID: principal, ActorID: principal, ScopeID: "user:" + principal, Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-" + id, ChatID: 11, Text: "w", CreatedAt: time.Now().UTC()}
	}
	if _, err := s.Enqueue(mk("job-cur-a", "alice")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(mk("job-cur-b", "bob")); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.PrincipalID != "bob" {
		t.Fatalf("admission cursor must rotate past the recorded scope: %+v", claimed)
	}
}

// The outbox bound counts pending rows: on an activated spool a full queue
// must still refuse new ordinary deliveries instead of silently overflowing.
func TestSpoolDBDeliveryBoundCountsRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	s.limits.MaxDeliveries = 1
	if err := s.EnqueueDelivery(Delivery{ID: "d-b1", ChatID: 11, Text: "one", Channel: "telegram_bot", ConversationID: "telegram-11", DeliveryTargetID: "telegram-11"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDelivery(Delivery{ID: "d-b2", ChatID: 11, Text: "two", Channel: "telegram_bot", ConversationID: "telegram-11", DeliveryTargetID: "telegram-11"}); err == nil {
		t.Fatal("pending bound ignored a full row queue")
	}
}

// A completed delivery lives in done/ rows: re-enqueueing the same id must
// stay idempotent instead of pushing a duplicate back onto pending.
func TestSpoolDBDeliveryDedupSeesTerminalRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	if err := s.EnqueueDelivery(Delivery{ID: "d-dup", ChatID: 11, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("claim: %v %v", d, err)
	}
	if err := s.CompleteDelivery(d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDelivery(Delivery{ID: "d-dup", ChatID: 11, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	if names, err := s.fsys.ReadDir(filepath.Join(root, "outbox", "pending")); err != nil || len(names) != 0 {
		t.Fatalf("completed delivery re-enqueued onto pending: %v %v", names, err)
	}
}

// The gateway resolves a routine job's occurrence for conversation rebinding;
// on an activated spool that record is a row, not a file.
func TestSpoolDBOccurrenceForReadsRow(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.WriteFile(filepath.Join(root, "occurrences", "routine-x.json"), []byte(`{"schedule_id":"sched-1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	g := &Gateway{spool: s}
	if _, ok := g.occurrenceFor("routine-x"); !ok {
		t.Fatal("occurrence row invisible to the gateway")
	}
}

// The stream supervisor diffs stream-eligible schedules against the running
// set; with schedules in rows the diff must still see them.
func TestSpoolDBStreamIDsReadsRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	in := Schedule{ScheduleID: "stream-1", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "probe", JobKind: "event", Delivery: "stream", Enabled: true, Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-probe", MinIntervalSeconds: 60}}
	if _, err := s.CreateSchedule(in, caller, ""); err != nil {
		t.Fatal(err)
	}
	if ids := s.streamIDs(time.Now().UTC()); !ids["stream-1"] {
		t.Fatalf("stream schedule row not enumerated: %v", ids)
	}
}

// Human-confirmation callbacks live under callbacks/: a row-backed record
// must still prove the confirmation against ToolHub.
func TestSpoolDBHumanCallbackReadsRow(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	g := &Gateway{spool: s, config: Config{ToolHubURL: server.URL}, now: time.Now}
	actor := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	proof := api.HumanConfirmation{OnboardingID: "onboard-human", Actor: actor, Nonce: "opaque", Revision: 1, ActionDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
	button, err := s.humanConfirmationButton(proof, "telegram_bot", 11)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.confirmHumanCallback(context.Background(), actor, "telegram_bot", 11, button.Data); err != nil {
		t.Fatalf("row-backed callback rejected: %v", err)
	}
}

// Usage export reads the journal directly for operators; on an activated
// spool the journal is a row inside spool.db.
func TestSpoolDBUsageExportReadsRows(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	job := Job{ID: "job-u1", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", ContextID: "alice"}
	if err := s.RecordUsage(job, "run-1", hubruntime.SessionUsage{SessionID: "sess-u1", SessionFound: true, Model: "m", FetchedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	records, err := ReadUsageJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].JobID != "job-u1" {
		t.Fatalf("usage export missed the row journal: %+v", records)
	}
}

// The event-layout manifest is itself a record row once activated: a second
// writer open must read it from spool.db instead of replaying the migration
// and tripping over the events fence file.
func TestSpoolDBReopenWriterKeepsEventLayout(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.fsys.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := NewSpool(root)
	if err != nil {
		t.Fatalf("reopened writer replayed the layout migration: %v", err)
	}
	defer s2.fsys.Close()
}

// Per-cursor .initialized markers are written through the record backend;
// a corrupt row marker must fail closed instead of being silently replaced.
func TestSpoolDBEventStateMarkerReadsRow(t *testing.T) {
	root := t.TempDir()
	dbBack, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(root, "event-state", "bad.json.initialized")
	if err := dbBack.WriteFile(markerPath, []byte(`{"version":999}`)); err != nil {
		t.Fatal(err)
	}
	if err := dbBack.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.fsys.Close()
	s.mu.Lock()
	err = s.writeEventStateLocked("bad", EventState{ScheduleID: "bad", Cursor: "c"})
	s.mu.Unlock()
	if err == nil {
		t.Fatal("corrupt .initialized row silently replaced")
	}
}
