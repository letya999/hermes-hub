package communication

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestScheduleDoesNotAccumulateOccurrencesWhilePreviousPending(t *testing.T) {
	s, _, g := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	created, err := s.CreateSchedule(Schedule{ScheduleID: "bounded", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "work", JobKind: "agent"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue.Add(2*time.Minute), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending occurrences = %d, %v", len(entries), err)
	}
}

// Schedule CRUD round-trip at the spool layer: create/list/update/pause/
// snooze/delete plus routine-failure dedup, replay and the native-cron guard.
func TestScheduleCRUDRoundTrip(t *testing.T) {
	s, _, g := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	foreign := identity.TelegramEnvelope("bob", 22, "bob", "policy-1")

	cron := Schedule{ScheduleID: "cron-one", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "*/15 * * * *", Input: "tick", JobKind: "agent"}
	created, err := s.CreateSchedule(cron, caller, "")
	if err != nil || !created.Enabled || created.NextDue.IsZero() {
		t.Fatalf("create: %+v %v", created, err)
	}
	// Identical re-create replays the stored schedule, never duplicates.
	replay, err := s.CreateSchedule(cron, caller, "")
	if err != nil || replay.ScheduleID != "cron-one" {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	list, err := s.ListSchedules(caller)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	if foreignList, err := s.ListSchedules(foreign); err != nil || len(foreignList) != 0 {
		t.Fatal("foreign principal saw the schedule")
	}

	updated, err := s.UpdateSchedule("cron-one", caller, "0 * * * *", "tock", nil)
	if err != nil || updated.Expression != "0 * * * *" || updated.Input != "tock" || updated.Revision != created.Revision+1 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	disabled := false
	paused, err := s.UpdateSchedule("cron-one", caller, "", "", &disabled)
	if err != nil || paused.Enabled || !paused.Paused {
		t.Fatalf("disable: %+v %v", paused, err)
	}
	if _, err := s.UpdateSchedule("cron-one", foreign, "", "", nil); err == nil {
		t.Fatal("foreign update accepted")
	}
	if _, err := s.UpdateSchedule("cron-one", caller, "not-a-cron", "", nil); err == nil {
		t.Fatal("broken expression accepted")
	}

	if err := s.SnoozeSchedule("cron-one", caller, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SnoozeSchedule("cron-one", caller, time.Now().UTC().Add(-time.Minute)); err == nil {
		t.Fatal("past snooze accepted")
	}
	if err := s.PauseSchedule("cron-one", caller); err != nil {
		t.Fatal(err)
	}

	// Routine failure digest: first failure notifies, the repeat dedupes,
	// a success clears the digest so the next failure notifies again.
	if notify, err := s.RecordRoutineFailure("cron-one", "digest-1"); err != nil || !notify {
		t.Fatal("first failure must notify")
	}
	if notify, err := s.RecordRoutineFailure("cron-one", "digest-1"); err != nil || notify {
		t.Fatal("repeated failure notified again")
	}
	if notify, err := s.RecordRoutineFailure("absent", "d"); err != nil || !notify {
		t.Fatal("missing schedule must still notify")
	}
	if err := s.ClearRoutineFailure("cron-one"); err != nil {
		t.Fatal(err)
	}
	if notify, err := s.RecordRoutineFailure("cron-one", "digest-1"); err != nil || !notify {
		t.Fatal("cleared digest must notify again")
	}
	if err := s.ClearRoutineFailure("absent"); err != nil {
		t.Fatal(err)
	}

	// Delete writes the tombstone; the retired id can never be recreated.
	if err := s.DeleteSchedule("cron-one", caller); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(cron, caller, ""); err == nil {
		t.Fatal("retired schedule id recreated")
	}
	if err := s.DeleteSchedule("cron-one", foreign); err == nil {
		t.Fatal("foreign delete accepted")
	}

	// TickSchedules materializes a due cron occurrence for an enabled schedule.
	live := Schedule{ScheduleID: "cron-live", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "go", JobKind: "agent"}
	if _, err := s.CreateSchedule(live, caller, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(2 * time.Minute)
	if err := s.TickSchedules(now, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	// A fresh schedule on an unmigrated native-cron context is refused.
	fresh := live
	fresh.ScheduleID = "cron-native"
	if _, err := s.CreateSchedule(fresh, caller, "unmigrated"); err == nil {
		t.Fatal("unmigrated native cron accepted")
	}

	// ValidateSchedule rejects malformed drafts without touching the spool.
	bad := cron
	bad.ScheduleID = "bad-id"
	bad.JobKind = "nonsense"
	if err := ValidateSchedule(bad, caller, time.Now().UTC()); err == nil {
		t.Fatal("bad job kind accepted")
	}
	ok := cron
	ok.ScheduleID = "ok-id"
	if err := ValidateSchedule(ok, caller, time.Now().UTC()); err != nil {
		t.Fatalf("valid draft rejected: %v", err)
	}
	badTZ := ok
	badTZ.Timezone = "Mars/Olympus"
	if err := ValidateSchedule(badTZ, caller, time.Now().UTC()); err == nil {
		t.Fatal("bad timezone accepted")
	}
}
