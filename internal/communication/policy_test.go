package communication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/routines"
)

// S2.1 — a change inside the owner's quiet window still lands in the outbox
// but stays unclaimable until the window ends (SPEC-0054 R2.1).
func TestQuietHoursDeferDelivery(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	// The window wraps the real clock so the defer end is always ~1h ahead.
	now := time.Now().UTC()
	from := now.Add(-time.Hour).Format("15:04")
	to := now.Add(time.Hour).Format("15:04")
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wq", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch quietly", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/q"}, QuietFrom: from, QuietTo: to}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "wq"
	o.JobKind = "watcher"
	o.Job.Text = "watch quietly"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(now, func(context.Context, string) ([]byte, error) { return []byte("v1"), nil }); err != nil {
		t.Fatal(err)
	}
	o2 := o
	o2.DueAt = o.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(o2, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o2.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(now, func(context.Context, string) ([]byte, error) { return []byte("v2"), nil }); err != nil {
		t.Fatal(err)
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatalf("quiet-window delivery claimed early: %+v %v", d, err)
	}
	name, err := firstJSON(filepath.Join(s.root, "outbox", "pending"))
	if err != nil || name == "" {
		t.Fatalf("quiet delivery not in outbox: %q %v", name, err)
	}
	b, err := os.ReadFile(filepath.Join(s.root, "outbox", "pending", name))
	if err != nil {
		t.Fatal(err)
	}
	var d Delivery
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	end := time.Date(now.Add(time.Hour).Year(), now.Add(time.Hour).Month(), now.Add(time.Hour).Day(), now.Add(time.Hour).Hour(), now.Add(time.Hour).Minute(), 0, 0, time.UTC)
	if !d.NotBefore.Equal(end.UTC()) {
		t.Fatalf("not_before = %v, want window end %v", d.NotBefore, end.UTC())
	}
	// Past the window end the same durable delivery claims normally.
	d.NotBefore = now.Add(-time.Minute)
	if err := atomicJSON(filepath.Join(s.root, "outbox", "pending", name), d); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimDelivery()
	if err != nil || got == nil || got.ID != d.ID {
		t.Fatalf("deferred delivery never claimed: %+v %v", got, err)
	}
}

// S2.1 — the overnight quiet window (22:00-07:00) wraps midnight correctly.
func TestQuietOvernightWraps(t *testing.T) {
	sched := Schedule{Timezone: "UTC", QuietFrom: "22:00", QuietTo: "07:00"}
	day := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	if got := routines.QuietDefer(sched, at(23, 30)); !got.Equal(at(31, 0)) {
		t.Fatalf("23:30 should defer to next 07:00, got %v", got)
	}
	if got := routines.QuietDefer(sched, at(3, 0)); !got.Equal(at(7, 0)) {
		t.Fatalf("03:00 should defer to 07:00 same day, got %v", got)
	}
	if got := routines.QuietDefer(sched, at(12, 0)); !got.IsZero() {
		t.Fatalf("12:00 is outside the window, got %v", got)
	}
	if got := routines.QuietDefer(sched, at(7, 0)); !got.IsZero() {
		t.Fatalf("07:00 is the window end, got %v", got)
	}
	sched2 := Schedule{Timezone: "UTC", QuietFrom: "13:00", QuietTo: "15:00"}
	if got := routines.QuietDefer(sched2, at(14, 0)); !got.Equal(at(15, 0)) {
		t.Fatalf("14:00 should defer to 15:00, got %v", got)
	}
	if got := routines.QuietDefer(sched2, at(16, 0)); !got.IsZero() {
		t.Fatalf("16:00 is outside, got %v", got)
	}
}

// S2.1 — quiet windows are both-or-neither strict HH:MM.
func TestQuietValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := Schedule{ScheduleID: "qv", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/q"}}
	oneSided := base
	oneSided.QuietFrom = "22:00"
	if _, err := s.CreateSchedule(oneSided, caller, ""); err == nil {
		t.Fatal("one-sided quiet window accepted")
	}
	bad := base
	bad.ScheduleID = "qv2"
	bad.QuietFrom, bad.QuietTo = "25:00", "07:00"
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("invalid HH:MM accepted")
	}
	same := base
	same.ScheduleID = "qv3"
	same.QuietFrom, same.QuietTo = "07:00", "07:00"
	if _, err := s.CreateSchedule(same, caller, ""); err == nil {
		t.Fatal("empty quiet window accepted")
	}
	good := base
	good.ScheduleID = "qv4"
	good.QuietFrom, good.QuietTo = "22:00", "07:00"
	if _, err := s.CreateSchedule(good, caller, ""); err != nil {
		t.Fatalf("valid quiet window rejected: %v", err)
	}
}

// S2.1 — the digest's own delivery is owner-facing too: inside a quiet
// window it waits for the window end instead of landing at night.
func TestDigestRespectsQuietWindow(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	now := time.Now().UTC()
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "dq", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 8 * * *", Input: "morning digest", JobKind: "digest", QuietFrom: now.Add(-time.Hour).Format("15:04"), QuietTo: now.Add(time.Hour).Format("15:04")}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "dq"
	o.JobKind = "digest"
	o.Job.Text = "morning digest"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatalf("quiet digest claimed early: %+v %v", d, err)
	}
	name, err := firstJSON(filepath.Join(s.root, "outbox", "pending"))
	if err != nil || name == "" {
		t.Fatalf("digest delivery missing from outbox: %v", err)
	}
}

// S2.2 — daily_limit caps owner noise per owner-timezone day: over the cap
// the occurrence still advances the baseline but settles `capped` with no
// delivery; a new day resets (SPEC-0054 R2.2).
func TestDailyLimitCaps(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	now := time.Now().UTC()
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wd", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "noisy watch", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/d"}, DailyLimit: 1}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "wd"
	o.JobKind = "watcher"
	o.Job.Text = "noisy watch"
	fire := func(occ RoutineOccurrence, value string) {
		t.Helper()
		if err := s.PutOccurrence(occ, caller); err != nil {
			t.Fatal(err)
		}
		if err := s.DispatchDueOccurrences(occ.DueAt, s.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
		if err := s.ProbeDueOccurrences(now, func(context.Context, string) ([]byte, error) { return []byte(value), nil }); err != nil {
			t.Fatal(err)
		}
	}
	fire(o, "v1") // seeds the baseline silently
	o2 := o
	o2.DueAt = o.DueAt.Add(time.Hour)
	fire(o2, "v2")
	if d, err := s.ClaimDelivery(); err != nil || d == nil {
		t.Fatalf("first change within cap did not deliver: %+v %v", d, err)
	}
	o3 := o2
	o3.DueAt = o2.DueAt.Add(time.Hour)
	fire(o3, "v3")
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatalf("over-cap change delivered: %+v %v", d, err)
	}
	var live RoutineOccurrence
	if b, err := os.ReadFile(filepath.Join(s.root, "occurrences", routines.OccurrenceID(o3)+".json")); err != nil || json.Unmarshal(b, &live) != nil || live.State != "capped" {
		t.Fatalf("over-cap occurrence state: %+v", live)
	}
	// Baseline still advanced while capped — a same-day repeat of v3 is no change.
	s.mu.Lock()
	st, err := s.loadWatchStateLocked("wd")
	s.mu.Unlock()
	if err != nil || st.Value != "v3" {
		t.Fatalf("capped tick lost the baseline advance: %+v %v", st, err)
	}
	// Next owner-tz day the cap resets.
	o4 := o3
	o4.DueAt = o3.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(o4, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o4.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	nextDay := now.Add(26 * time.Hour)
	if err := s.ProbeDueOccurrences(nextDay, func(context.Context, string) ([]byte, error) { return []byte("v4"), nil }); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("new-day change did not deliver: %+v %v", d, err)
	}
}

// S2.2 — daily_limit is bounded 0..32.
func TestDailyLimitValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := Schedule{ScheduleID: "dv", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/q"}}
	for i, limit := range []int{33, -1} {
		bad := base
		bad.ScheduleID = fmt.Sprintf("dv%d", i)
		bad.DailyLimit = limit
		if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
			t.Fatalf("daily_limit=%d accepted", limit)
		}
	}
	ok := base
	ok.ScheduleID = "dv-ok"
	ok.DailyLimit = 32
	if _, err := s.CreateSchedule(ok, caller, ""); err != nil {
		t.Fatalf("daily_limit=32 rejected: %v", err)
	}
}

// S2.3 — notify_mode=agent escalates a real change to the model with the
// evidence flagged untrusted, and the idempotent job cannot double-admit
// (SPEC-0054 R2.3).
func TestAgentEscalationOnChange(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	now := time.Now().UTC()
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wa", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "escalate changes", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/a"}, NotifyMode: "agent"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "wa"
	o.JobKind = "watcher"
	o.Job.Text = "escalate changes"
	o.Job.Trigger = "cron"
	fire := func(occ RoutineOccurrence, value string) {
		t.Helper()
		if err := s.PutOccurrence(occ, caller); err != nil {
			t.Fatal(err)
		}
		if err := s.DispatchDueOccurrences(occ.DueAt, s.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
		if err := s.ProbeDueOccurrences(now, func(context.Context, string) ([]byte, error) { return []byte(value), nil }); err != nil {
			t.Fatal(err)
		}
	}
	fire(o, "v1")
	if job, _ := s.ClaimJob(); job != nil {
		t.Fatalf("baseline seed woke the agent: %+v", job)
	}
	o2 := o
	o2.DueAt = o.DueAt.Add(time.Hour)
	fire(o2, "v2")
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("agent-mode change produced a plain delivery: %+v", d)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("agent escalation did not admit a job: %+v %v", job, err)
	}
	if !strings.Contains(job.Text, "untrusted") || !strings.Contains(job.Text, "v1") || !strings.Contains(job.Text, "v2") {
		t.Fatalf("escalated job lacks untrusted evidence: %+v", job)
	}
	if extra, _ := s.ClaimJob(); extra != nil {
		t.Fatalf("one change admitted two jobs: %+v", extra)
	}
}

// S2.3 — agent escalation is only meaningful on probing kinds; a reminder
// must stay LLM-free forever (red-team).
func TestAgentModeRejectedOnReminder(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	bad := Schedule{ScheduleID: "ra", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "ping", JobKind: "reminder", NotifyMode: "agent"}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("notify_mode=agent accepted on a reminder")
	}
}

// S4.1 — an empty brief slot (no changed watches, no open tasks) produces
// one deterministic delivery and never admits an agent job (SPEC-0054 R4).
func TestBriefEmptySlotNoModel(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	now := time.Now().UTC()
	o.ScheduleID = "brief1"
	o.JobKind = "brief"
	o.Job.Text = "Утренний бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "Новых изменений нет") {
		t.Fatalf("empty brief should deliver deterministic text: %+v %v", d, err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("empty brief admitted an agent job")
	}
}

// S4.1 — a changed watch turns the brief into one admitted agent job whose
// text carries the authorized snapshot flagged untrusted; a replayed dispatch
// must not admit a second job (SPEC-0054 R4).
func TestBriefChangeAdmitsOneJob(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wb", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "price watch", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/p"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath("wb"), WatchState{ScheduleID: "wb", Baseline: "h1", Value: "250", ChangedAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "brief2"
	o.JobKind = "brief"
	o.Job.Text = "Утренний бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("brief admitted no agent job: %v", err)
	}
	if !strings.Contains(job.Text, "untrusted") || !strings.Contains(job.Text, "price watch") || !strings.Contains(job.Text, "250") {
		t.Fatalf("snapshot missing or not flagged untrusted: %q", job.Text)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if again, err := s.ClaimJob(); err != nil || again != nil {
		t.Fatal("brief dispatch replay admitted a second job")
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatal("non-empty brief produced a plain delivery")
	}
}

// S4.1 — a failing watch source is named unavailable in the snapshot —
// never reported as "no changes" (SPEC-0054 R4).
func TestBriefUnavailableNamed(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wf", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "fragile feed", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/f"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath("wf"), WatchState{ScheduleID: "wf", LastError: "connection refused", LastSuccess: time.Now().UTC().Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "brief3"
	o.JobKind = "brief"
	o.Job.Text = "Утренний бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("unavailable source produced no brief job")
	}
	if !strings.Contains(job.Text, "fragile feed") || !strings.Contains(job.Text, "недоступен") || strings.Contains(job.Text, "Новых изменений нет") {
		t.Fatalf("unavailable source not named: %q", job.Text)
	}
}

// S4.1 — another principal's changed watch never enters the owner's brief
// snapshot (SPEC-0054 R4 red-team).
func TestBriefForeignWatchesExcluded(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}}
	bobEnv := bob.Envelope(22)
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w-foreign", Envelope: bobEnv, OrganizationID: o.Job.OrganizationID, UserID: "bob", ActorID: "bob", ScopeID: "user:bob", Channel: "telegram_bot", ChatID: 22, Timezone: "UTC", Expression: "0 * * * *", Input: "bob watch", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/b"}}, bobEnv, ""); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath("w-foreign"), WatchState{ScheduleID: "w-foreign", Baseline: "h1", Value: "secret-999", ChangedAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "brief4"
	o.JobKind = "brief"
	o.Job.Text = "Утренний бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatalf("foreign watch leaked into the brief: %+v", job)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || strings.Contains(d.Text, "bob watch") || strings.Contains(d.Text, "secret-999") {
		t.Fatalf("foreign data leaked or missing empty-brief delivery: %+v %v", d, err)
	}
}

// S4.1 — the brief snapshot includes the owner's open tasks; archived and
// foreign tasks stay out (SPEC-0054 R4).
func TestBriefListsOpenTasks(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	user := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	if _, _, err := s.CreateTask(user, 11, 0, "finish report", false); err != nil {
		t.Fatal(err)
	}
	done, _, err := s.CreateTask(user, 11, 0, "old task", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveTask(user, 11, done.TaskID); err != nil {
		t.Fatal(err)
	}
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}}
	if _, _, err := s.CreateTask(bob, 22, 0, "bob task", false); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "brief5"
	o.JobKind = "brief"
	o.Job.Text = "Утренний бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("open task produced no brief job")
	}
	if !strings.Contains(job.Text, "finish report") || strings.Contains(job.Text, "old task") || strings.Contains(job.Text, "bob task") {
		t.Fatalf("task section wrong: %q", job.Text)
	}
}

// S4.1 — the brief cursor advances with dispatch: a second brief does not
// re-report the same change (SPEC-0054 R4).
func TestBriefCursorAdvances(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wc", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "price watch", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/c"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath("wc"), WatchState{ScheduleID: "wc", Baseline: "h1", Value: "250", ChangedAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "brief6", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 8 * * *", Input: "Утренний бриф", JobKind: "brief"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "brief6"
	o.JobKind = "brief"
	o.Job.Text = "Утренний бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil || !strings.Contains(job.Text, "price watch") {
		t.Fatalf("first brief lost the change: %+v %v", job, err)
	}
	if err := s.CompleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	// Next slot, same unchanged watch state: nothing new to report.
	o2 := o
	o2.DueAt = now.Add(24 * time.Hour)
	if err := s.PutOccurrence(o2, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now.Add(25*time.Hour), s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatalf("brief re-reported the same change: %+v", job)
	}
	if d, err := s.ClaimDelivery(); err != nil || d == nil || !strings.Contains(d.Text, "Новых изменений нет") {
		t.Fatalf("second brief should be a deterministic empty slot: %+v %v", d, err)
	}
}

// S4.1 — source_ids scope the brief snapshot, and a brief carries no probe
// of its own (SPEC-0054 R4).
func TestBriefSourceIDsScope(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	mk := func(id, input string) {
		t.Helper()
		if _, err := s.CreateSchedule(Schedule{ScheduleID: id, Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: input, JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/" + id}}, caller, ""); err != nil {
			t.Fatal(err)
		}
	}
	mk("s-in", "in scope")
	mk("s-out", "out of scope")
	for _, id := range []string{"s-in", "s-out"} {
		if err := atomicJSON(s.watchPath(id), WatchState{ScheduleID: id, Baseline: "h", Value: "v-" + id, ChangedAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "brief7", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 8 * * *", Input: "бриф", JobKind: "brief", SourceIDs: []string{"s-in"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "brief8", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 8 * * *", Input: "бриф", JobKind: "brief", Probe: &WatchProbe{URL: "https://example.com/x"}}, caller, ""); err == nil {
		t.Fatal("brief with a probe accepted")
	}
	now := time.Now().UTC()
	o.ScheduleID = "brief7"
	o.JobKind = "brief"
	o.Job.Text = "бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("scoped brief produced no job")
	}
	if !strings.Contains(job.Text, "in scope") || strings.Contains(job.Text, "out of scope") {
		t.Fatalf("source_ids scope not honored: %q", job.Text)
	}
	if err := s.authorizeOccurrence(*job); err != nil {
		t.Fatalf("brief bundle fails send-time re-authorization: %v", err)
	}
}

// S4.2 — a revoked brief cancels its due occurrence through the shared
// machinery; a replayed pending occurrence after a crash dedups the admitted
// job by its durable file (SPEC-0054 R4).
func TestBriefRevokedStaysQuietAndRestartDedups(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wrv", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "feed", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/r"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath("wrv"), WatchState{ScheduleID: "wrv", Baseline: "h", Value: "v", ChangedAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "brief9", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 8 * * *", Input: "бриф", JobKind: "brief"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "brief9"
	o.JobKind = "brief"
	o.Job.Text = "бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule("brief9", caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("revoked brief admitted a job")
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatal("revoked brief produced a delivery")
	}
	var occ RoutineOccurrence
	b, err := os.ReadFile(filepath.Join(s.root, "occurrences", routines.OccurrenceID(o)+".json"))
	if err != nil || json.Unmarshal(b, &occ) != nil || occ.State != "cancelled" {
		t.Fatalf("revoked brief occurrence not cancelled: %s %v", occ.State, err)
	}
	// Crash replay: the occurrence stayed pending while the admitted job is
	// already durable — re-dispatch must not admit a second one.
	if err := os.MkdirAll(filepath.Join(s.root, "schedules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "brief10", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 8 * * *", Input: "бриф", JobKind: "brief"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o2 := o
	o2.ScheduleID = "brief10"
	if err := s.PutOccurrence(o2, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	var occ2 RoutineOccurrence
	b2, err := os.ReadFile(filepath.Join(s.root, "occurrences", routines.OccurrenceID(o2)+".json"))
	if err != nil || json.Unmarshal(b2, &occ2) != nil {
		t.Fatal(err)
	}
	occ2.State = "pending"
	if err := atomicJSON(filepath.Join(s.root, "occurrences", routines.OccurrenceID(o2)+".json"), occ2); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("crash-replayed brief lost its job")
	}
	if err := s.CompleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if again, err := s.ClaimJob(); err != nil || again != nil {
		t.Fatal("crash replay admitted a second brief job")
	}
}

// S4.3 — inside the owner's quiet window a brief slot stays pending: no
// model call, no delivery. After the window the same occurrence dispatches
// and its unread changes still compose the brief (SPEC-0054 R4, A4).
func TestBriefQuietDefersSlot(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wq-b", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "feed", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/qb"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath("wq-b"), WatchState{ScheduleID: "wq-b", Baseline: "h", Value: "v", ChangedAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "briefq", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 8 * * *", Input: "бриф", JobKind: "brief", QuietFrom: now.Add(-30 * time.Minute).Format("15:04"), QuietTo: now.Add(30 * time.Minute).Format("15:04")}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "briefq"
	o.JobKind = "brief"
	o.Job.Text = "бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("quiet window admitted a brief job")
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatal("quiet window produced a brief delivery")
	}
	var occ RoutineOccurrence
	b, err := os.ReadFile(filepath.Join(s.root, "occurrences", routines.OccurrenceID(o)+".json"))
	if err != nil || json.Unmarshal(b, &occ) != nil || occ.State != "pending" {
		t.Fatalf("quiet brief occurrence state: %s %v", occ.State, err)
	}
	// The window is over and the slot is still inside catch-up: the deferred
	// brief dispatches and the queued change reaches the model.
	if err := s.DispatchDueOccurrences(now.Add(45*time.Minute), s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("deferred brief never admitted its job")
	}
	if !strings.Contains(job.Text, "feed") {
		t.Fatalf("deferred brief lost the queued change: %q", job.Text)
	}
}

// S4.3 — a persistently failing source is named unavailable once per
// distinct error; the next slot reports the ongoing count in the
// deterministic footer instead of repeating the same line (SPEC-0054 R4).
func TestBriefUnavailableErrorDeduped(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "werr", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "fragile", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/e"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath("werr"), WatchState{ScheduleID: "werr", LastError: "connection refused", LastSuccess: time.Now().UTC().Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "briefe", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 8 * * *", Input: "бриф", JobKind: "brief"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "briefe"
	o.JobKind = "brief"
	o.Job.Text = "бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil || !strings.Contains(job.Text, "недоступен") {
		t.Fatalf("first slot did not name the unavailable source: %+v %v", job, err)
	}
	if err := s.CompleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	o2 := o
	o2.DueAt = now.Add(25 * time.Hour)
	if err := s.PutOccurrence(o2, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now.Add(26*time.Hour), s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("same unavailable error woke the model a second time")
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "Источников недоступно: 1") {
		t.Fatalf("second slot did not report the ongoing count: %+v %v", d, err)
	}
}

// S4.3 — two dispatchers racing one due brief admit a single agent job:
// the engine mutex serializes them and the second pass sees the settled
// occurrence (SPEC-0054 R4, A6).
func TestBriefTwoWorkersSingleJob(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wrace", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "feed", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/race"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath("wrace"), WatchState{ScheduleID: "wrace", Baseline: "h", Value: "v", ChangedAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "briefraced"
	o.JobKind = "brief"
	o.Job.Text = "бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.DispatchDueOccurrences(now, s.authorizeOccurrence)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("raced brief admitted no job")
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("two workers admitted two brief jobs")
	}
}

// S5.1 — a tool result carrying an events array (or a bare array) yields one
// discrete event per item with its durable identity (SPEC-0054 R5.2).
func TestExtractEventsArray(t *testing.T) {
	body := []byte(`{"events":[{"id":"m1","text":"new mail"},{"event_id":"m2","text":"newsletter"}]}`)
	events, err := routines.ExtractEvents(&WatchProbe{Kind: "mcp_tool"}, body)
	if err != nil || len(events) != 2 || events[0].ID != "m1" || events[1].ID != "m2" {
		t.Fatalf("events not extracted: %+v %v", events, err)
	}
	flat, err := routines.ExtractEvents(&WatchProbe{Kind: "mcp_tool"}, []byte(`[{"message_id":"x1","text":"ping"}]`))
	if err != nil || len(flat) != 1 || flat[0].ID != "x1" {
		t.Fatalf("bare array not extracted: %+v %v", flat, err)
	}
}

// S5.1 — an event without a declared id gets a stable content hash so the
// same item dedupes across polls (SPEC-0054 R5.2).
func TestExtractEventsFallbackID(t *testing.T) {
	body := []byte(`{"events":[{"text":"no id here"}]}`)
	first, err := routines.ExtractEvents(&WatchProbe{Kind: "mcp_tool"}, body)
	if err != nil || len(first) != 1 || first[0].ID == "" {
		t.Fatalf("no fallback id: %+v %v", first, err)
	}
	second, err := routines.ExtractEvents(&WatchProbe{Kind: "mcp_tool"}, body)
	if err != nil || second[0].ID != first[0].ID {
		t.Fatal("fallback id unstable across polls")
	}
}

// S5.1 — a changed http value is exactly one event keyed by the new value
// hash, carrying its bounded text (SPEC-0054 R5.2).
func TestExtractEventsHTTPChange(t *testing.T) {
	events, err := routines.ExtractEvents(&WatchProbe{Kind: "http"}, []byte("new value"))
	if err != nil || len(events) != 1 || events[0].ID != routines.WatchHash("new value") || events[0].Text != "new value" {
		t.Fatalf("http change not one event: %+v %v", events, err)
	}
}

// S5.2 — event fanout to per-event outbox deliveries with a durable
// seen-set dedupe rides the owner's real event store now; asserted in
// internal/toolhub/automation/streams_ported_test.go
// (TestSettleStreamEventsNotify) plus the re-poll dedupe in
// TestStreamSourceEditDedupesIndependently.
//
// S5.1 — a bare string item carries itself as the event text, and an object
// without `text` falls back to a human-facing key (SPEC-0054 R5.1).
func TestExtractEventsTextFallbacks(t *testing.T) {
	probe := &WatchProbe{Kind: "mcp_tool"}
	events, err := routines.ExtractEvents(probe, []byte(`[{"id":"a1","title":"headline"},"just a string",{"id":"b2","message":"hello"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events: %v", events)
	}
	if events[0].Text != "headline" || events[1].Text != "just a string" || events[2].Text != "hello" {
		t.Fatalf("text fallbacks: %+v", events)
	}
}

// S5.2 — sink=agent bundles fresh events into one untrusted-evidence
// job on the owner; asserted in
// internal/toolhub/automation/streams_ported_test.go
// (TestSettleStreamEventsAgentSink).
//
// S5.2 — sink is event-only; reminders keep refusing agent escalation
// (SPEC-0054 R5.2 red-team).
func TestSinkValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := Schedule{ScheduleID: "sv", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "x"}
	rem := base
	rem.ScheduleID = "sv1"
	rem.JobKind = "reminder"
	rem.Sink = "agent"
	if _, err := s.CreateSchedule(rem, caller, ""); err == nil {
		t.Fatal("sink=agent accepted on a reminder")
	}
	ev := base
	ev.ScheduleID = "sv2"
	ev.JobKind = "event"
	ev.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-abc"}
	ev.Sink = "pager"
	if _, err := s.CreateSchedule(ev, caller, ""); err == nil {
		t.Fatal("unknown sink value accepted")
	}
	ev.Sink = "notify"
	if _, err := s.CreateSchedule(ev, caller, ""); err != nil {
		t.Fatalf("sink=notify rejected: %v", err)
	}
}

// S5.2 — the durable seen-set stays bounded at 512 entries with the gap
// surfaced durably; the assertion moved to the owner's real event store in
// internal/toolhub/automation/streams_ported_test.go
// (TestEventGapSurfacedOnSeenEviction).

// S5.2 — the owner-timezone daily cap keeps over-cap events unseen instead
// of spamming the outbox; asserted against the owner's real event store in
// internal/toolhub/automation/host_test.go
// (TestStreamDailyCapLeavesExcessEventUnseen).
//
// S5.2 — the agent bundle stays bounded for fat event batches; ported to
// internal/toolhub/automation/streams_ported_test.go
// (TestEventBundleBounded).
//
// S5.2 — a sink admission failure no longer requeues to the connector: the
// owner retains the durable pending batch and drains it exactly once on
// recovery, which is strictly stronger than the retired best-effort
// requeue. Asserted in internal/toolhub/automation/streams_ported_test.go
// (TestStreamPollRetainsBatchOnSinkFailure); the cap's durable overflow
// counter is covered by TestStreamCursorFeedsBackAndWaitsForOverflow.
//
// S5.2 — a 401/403-shaped tool error fails the occurrence instead of
// silently retrying; the subscription's degraded card state derives from
// source errors and gaps. Asserted in streams_ported_test.go
// (TestSettleStreamEventsAuthDegraded) and host_test.go
// (TestStreamPauseAndCursorGap).

// S5.3 — /watches lists the caller's event subscriptions with source, sink,
// last success/error and degraded state; foreign owners stay invisible.
// The listing rides the real ToolHub host: schedule.list and the routine
// card resolve over the control RPC, and a source failure degrades the
// rendered line through the durable event state.
func TestWatchesCommandLists(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.Users = append(c.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: t.TempDir(), WorkspaceDir: t.TempDir()})
	failDead := false
	toolCall := func(_ context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		if failDead && probe.Tool == "hub-mail-dead" {
			return nil, errors.New("tool error: 403 forbidden")
		}
		return []byte(`{"events":[{"id":"a1","text":"first mail"}]}`), nil
	}
	g, host, _ := automationGatewayFixtureProbe(t, c, nil, nil, toolCall)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", `{"schedule_id":"ev8","timezone":"UTC","expression":"* * * * *","input":"mail events","job_kind":"event","delivery":"stream","probe":{"kind":"mcp_tool","tool":"hub-mail-events-abc","min_interval_seconds":60,"sources":[{"name":"live","tool":"hub-mail-live"},{"name":"dead","tool":"hub-mail-dead"}]}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("watch create: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/routines", strings.NewReader(`{"schedule_id":"ev-bob","timezone":"UTC","expression":"* * * * *","input":"bob events","job_kind":"event","delivery":"stream","probe":{"kind":"mcp_tool","tool":"hub-bob-events","min_interval_seconds":60}}`))
	r.Header.Set("Authorization", "Bearer control-token")
	r.Header.Set("X-Hub-Principal", "bob")
	w = httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("bob create: %d %s", w.Code, w.Body.String())
	}
	if err := host.Tick(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	out := g.watchesCommand(user, 11)
	if !strings.Contains(out, "ev8") || !strings.Contains(out, "hub-mail-events-abc") || !strings.Contains(out, "notify") || !strings.Contains(out, ", ok ") {
		t.Fatalf("subscription not rendered: %q", out)
	}
	if strings.Contains(out, "ev-bob") {
		t.Fatalf("foreign owner's subscription listed: %q", out)
	}
	// A later poll where one source fails degrades the line durably.
	failDead = true
	if err := host.Tick(context.Background(), time.Now().UTC().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	out = g.watchesCommand(user, 11)
	if !strings.Contains(out, "degraded") || !strings.Contains(out, ", err ") {
		t.Fatalf("degraded state not rendered: %q", out)
	}
}

// S5.3 — /watches is a real command: it routes to the subscription list
// instead of falling through to unknown-command help.
func TestWatchesCommandRoutes(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, _, _ := automationGatewayFixture(t, c)
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/watches", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "Подписки на события") {
		t.Fatalf("/watches did not route: %v", fake.sent)
	}
}

// S3.2 — a watcher may poll an owner's read-only ToolHub tool instead of a
// URL: the projected tool text is the compared value, and the URL fetch is
// never touched (SPEC-0054 R3.2).
func TestMCPToolProbeFeedsWatcher(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	now := time.Now().UTC()
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wm", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch connector", JobKind: "watcher", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-google-work-search-abc"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "wm"
	o.JobKind = "watcher"
	o.Job.Text = "watch connector"
	var toolArgs map[string]any
	value := "v1"
	toolCall := func(_ context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		if principal != "alice" {
			t.Fatalf("probe ran under %q", principal)
		}
		toolArgs = probe.Args
		return []byte(value), nil
	}
	noFetch := func(context.Context, string) ([]byte, error) {
		t.Fatal("mcp_tool probe fetched a URL")
		return nil, nil
	}
	fire := func(occ RoutineOccurrence) {
		t.Helper()
		if err := s.PutOccurrence(occ, caller); err != nil {
			t.Fatal(err)
		}
		if err := s.DispatchDueOccurrences(occ.DueAt, s.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
		if err := s.ProbeDueOccurrencesContext(context.Background(), now, s.authorizeOccurrence, noFetch, toolCall, nil); err != nil {
			t.Fatal(err)
		}
	}
	fire(o)
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("baseline seed notified: %+v", d)
	}
	value = "v2"
	o2 := o
	o2.DueAt = o.DueAt.Add(time.Hour)
	fire(o2)
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "v2") {
		t.Fatalf("mcp_tool change did not deliver: %+v %v", d, err)
	}
	_ = toolArgs
}

// S3.2 — the gateway's tool-call probe posts {tool, arguments} to ToolHub's
// /v1/tool-call with the owner-pinned credential; is_error is a probe error,
// never a compared value.
func TestToolCallProbeHTTP(t *testing.T) {
	var auth, principal, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tool-call" {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		auth = r.Header.Get("Authorization")
		principal = r.Header.Get("X-Hub-Principal")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(`{"text":"price 42","is_error":false}`))
	}))
	defer server.Close()
	g := &Gateway{config: Config{ToolHubURL: server.URL, Supervised: true, RuntimeAuth: "control-token"}}
	got, err := g.toolCallProbe(context.Background(), "alice", &WatchProbe{Kind: "mcp_tool", Tool: "hub-x", Args: map[string]any{"q": "price"}})
	if err != nil || string(got) != "price 42" {
		t.Fatalf("toolCallProbe: %q %v", got, err)
	}
	if auth != "Bearer control-token" || principal != "alice" {
		t.Fatalf("credential headers: %q %q", auth, principal)
	}
	if !strings.Contains(body, `"tool":"hub-x"`) || !strings.Contains(body, `"q":"price"`) {
		t.Fatalf("tool call body: %s", body)
	}
}

// S3.2 — a tool error or a missing ToolHub config is a probe error
// (unknown), never a compared value.
func TestToolCallProbeErrors(t *testing.T) {
	g := &Gateway{config: Config{}}
	if _, err := g.toolCallProbe(context.Background(), "alice", &WatchProbe{Kind: "mcp_tool", Tool: "hub-x"}); err == nil {
		t.Fatal("unconfigured tool hub probed successfully")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"internal failure","is_error":true}`))
	}))
	defer server.Close()
	g = &Gateway{config: Config{ToolHubURL: server.URL, Supervised: true, RuntimeAuth: "tok"}}
	if _, err := g.toolCallProbe(context.Background(), "alice", &WatchProbe{Kind: "mcp_tool", Tool: "hub-x"}); err == nil {
		t.Fatal("is_error tool result accepted as a value")
	}
	g = &Gateway{config: Config{ToolHubURL: server.URL, Supervised: true}}
	if _, err := g.toolCallProbe(context.Background(), "alice", &WatchProbe{Kind: "mcp_tool", Tool: "hub-x"}); err == nil {
		t.Fatal("credential-less tool hub probed successfully")
	}
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"text":"should not parse"}`))
	}))
	defer denied.Close()
	g = &Gateway{config: Config{ToolHubURL: denied.URL, Supervised: true, RuntimeAuth: "tok"}}
	if _, err := g.toolCallProbe(context.Background(), "alice", &WatchProbe{Kind: "mcp_tool", Tool: "hub-x"}); err == nil {
		t.Fatal("non-2xx tool hub response accepted as a value")
	}
}

// S3.2 — mcp_tool probes validate their tool name, reject a URL, and bound
// args; a check may also use one.
func TestMCPToolProbeValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := Schedule{ScheduleID: "mv", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch", JobKind: "watcher"}
	noTool := base
	noTool.Probe = &WatchProbe{Kind: "mcp_tool"}
	if _, err := s.CreateSchedule(noTool, caller, ""); err == nil {
		t.Fatal("mcp_tool without a tool accepted")
	}
	withURL := base
	withURL.ScheduleID = "mv2"
	withURL.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-x", URL: "https://example.com"}
	if _, err := s.CreateSchedule(withURL, caller, ""); err == nil {
		t.Fatal("mcp_tool with a url accepted")
	}
	badName := base
	badName.ScheduleID = "mv3"
	badName.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "bad name!"}
	if _, err := s.CreateSchedule(badName, caller, ""); err == nil {
		t.Fatal("unsafe tool name accepted")
	}
	bigArgs := base
	bigArgs.ScheduleID = "mv4"
	bigArgs.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-x", Args: map[string]any{"blob": strings.Repeat("x", 9*1024)}}
	if _, err := s.CreateSchedule(bigArgs, caller, ""); err == nil {
		t.Fatal("oversized args accepted")
	}
	badRequeue := base
	badRequeue.ScheduleID = "mv5"
	badRequeue.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-x", RequeueTool: "bad name!"}
	if _, err := s.CreateSchedule(badRequeue, caller, ""); err == nil {
		t.Fatal("unsafe requeue tool name accepted")
	}
	check := base
	check.ScheduleID = "mv6"
	check.JobKind = "check"
	check.Expect = "changed"
	check.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-x"}
	if _, err := s.CreateSchedule(check, caller, ""); err != nil {
		t.Fatalf("check mcp_tool rejected: %v", err)
	}
}

// S3.2 — end to end: the proactive tick resolves an mcp_tool watcher through
// ToolHub's /v1/tool-call and notifies on a changed result.
func TestTickProbesMCPToolWatcher(t *testing.T) {
	value := "seed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tool-call" {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"text": value, "is_error": false})
	}))
	defer server.Close()
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w-tick-mcp", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch connector", JobKind: "watcher", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-x"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	g.spool = s.Spool
	g.config.ToolHubURL = server.URL
	g.config.Supervised = true
	g.config.RuntimeAuth = "control-token"
	due := time.Now().UTC().Add(-time.Minute)
	sched, _ := s.loadScheduleLocked("w-tick-mcp")
	sched.NextDue = due
	_ = atomicJSON(s.schedulePath("w-tick-mcp"), sched)
	s.tickProactiveTool(due, nil, g.toolCallProbe)
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("seed notified: %+v", d)
	}
	value = "changed-value"
	sched, _ = s.loadScheduleLocked("w-tick-mcp")
	sched.NextDue = due.Add(time.Minute)
	_ = atomicJSON(s.schedulePath("w-tick-mcp"), sched)
	s.tickProactiveTool(due.Add(2*time.Minute), nil, g.toolCallProbe)
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "changed-value") {
		t.Fatalf("mcp tick did not deliver the change: %+v %v", d, err)
	}
}

// S3.2 — creating an mcp_tool check over HTTP seeds the baseline through the
// tool call, never through a URL fetch.
func TestCheckMCPProbeSeedsBaselineOverHTTP(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "tok"
	c.Supervised = true
	c.RuntimeURL = "http://runtime.test"
	c.RuntimeAuth = "control-token"
	g, host, _ := automationGatewayFixture(t, c)
	// One ToolHub URL serves both surfaces this path touches: the control
	// RPC multiplexes to the real host while /v1/tool-call answers the
	// baseline probe.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/tool-call", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "open"})
	})
	mux.Handle("/", host.ControlHandler("automation-test"))
	toolHub := httptest.NewServer(mux)
	defer toolHub.Close()
	g.config.ToolHubURL = toolHub.URL
	body := `{"schedule_id":"c-http-mcp","timezone":"UTC","expression":"0 * * * *","input":"connector state","job_kind":"check","expect":"equals:closed","probe":{"kind":"mcp_tool","tool":"hub-x"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/routines", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	st, err := host.LoadWatchState(context.Background(), g.user("alice").Envelope(11), "c-http-mcp")
	if err != nil || st.Value != "open" {
		t.Fatalf("baseline not seeded from tool call: %+v %v", st, err)
	}
}

// S5.1 — connector wait responses carry their items under `results` or
// `messages`, and native ids are often numeric (telegram message ids); both
// shapes must yield discrete dedup-able events (SPEC-0054 R5.1/R5.2).
func TestExtractEventsResultsAndNumericIDs(t *testing.T) {
	probe := &WatchProbe{Kind: "mcp_tool"}
	events, err := routines.ExtractEvents(probe, []byte(`{"results":[{"id":325267,"text":"first"},{"id":325268,"text":"second"}]}`))
	if err != nil || len(events) != 2 || events[0].ID != "325267" || events[1].ID != "325268" {
		t.Fatalf("results envelope not extracted: %+v %v", events, err)
	}
	msgs, err := routines.ExtractEvents(probe, []byte(`{"messages":[{"message_id":991,"text":"ping"}]}`))
	if err != nil || len(msgs) != 1 || msgs[0].ID != "991" {
		t.Fatalf("messages envelope not extracted: %+v %v", msgs, err)
	}
}

// S5.2/SPEC-0080 R2.2 — the agent-sink admission fences live in the owner's
// event plane now: assignment wake budget, paused-assignment fence, quiet
// window and daily cap all hold the durable batch back instead of spending
// a wake. Ported to internal/toolhub/automation/streams_ported_test.go:
// TestEventAgentSinkRespectsAssignmentBudget,
// TestEventAgentSinkPausedAssignmentNeverWakes,
// TestEventAgentSinkRespectsQuietWindow,
// TestEventAgentSinkRespectsDailyLimit.

// A watch-fail notice that never reaches the owner must not suppress the
// next identical probe failure: the dedupe digest lands on delivery, not
// on enqueue (same contract as routine failure notices).
func TestWatchFailNoticeDedupeWaitsForDelivery(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "wf1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch quietly", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/q"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	fire := func(seq int) {
		oc := o
		oc.ScheduleID = "wf1"
		oc.JobKind = "watcher"
		oc.DueAt = now.Add(time.Duration(seq) * time.Hour)
		oc.Job.Trigger = "cron"
		oc.Job.ID = routines.OccurrenceID(oc)
		oc.State = "probing"
		if err := atomicJSON(filepath.Join(s.root, "occurrences", routines.OccurrenceID(oc)+".json"), oc); err != nil {
			t.Fatal(err)
		}
		s.rt.SettleProbe(oc, sched, "", "", now, errors.New("source exploded"), s.authorizeOccurrence)
	}
	fire(0)
	item, err := s.loadScheduleLocked("wf1")
	if err != nil {
		t.Fatal(err)
	}
	if item.LastFailureSHA256 != "" {
		t.Fatalf("dedupe digest recorded before delivery: %q", item.LastFailureSHA256)
	}
	// The send never confirms: the channel errors and the notice dies in
	// outbox/failed through the real delivery path.
	g.api = &fakeAPI{err: errors.New("send down")}
	g.deliverOne(context.Background())
	fire(1)
	name, err := firstJSON(filepath.Join(s.root, "outbox", "pending"))
	if err != nil || name == "" {
		t.Fatal("undelivered notice suppressed the retry")
	}
	if !strings.Contains(name, "watch-fail-") {
		t.Fatalf("retry produced a non-watch-fail delivery: %q", name)
	}
	// Once the notice actually reaches the owner the digest is recorded;
	// the next identical failure dedupes again.
	g.api = &fakeAPI{}
	g.deliverOne(context.Background())
	item, err = s.loadScheduleLocked("wf1")
	if err != nil || item.LastFailureSHA256 == "" {
		t.Fatalf("delivered notice must record the dedupe digest: %q %v", item.LastFailureSHA256, err)
	}
	fire(2)
	if extra, err := s.ClaimDelivery(); err != nil || extra != nil {
		t.Fatalf("delivered notice must suppress the identical failure: %+v %v", extra, err)
	}
}
