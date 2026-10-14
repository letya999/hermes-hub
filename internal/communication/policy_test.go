package communication

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// S2.1 — a change inside the owner's quiet window still lands in the outbox
// but stays unclaimable until the window ends (SPEC-0054 R2.1).
func TestQuietHoursDeferDelivery(t *testing.T) {
	s, o, g := occurrenceFixture(t)
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
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
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
	if err := s.DispatchDueOccurrences(o2.DueAt, g.authorizeOccurrence); err != nil {
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
	end := time.Date(now.Year(), now.Month(), now.Day(), now.Add(time.Hour).Hour(), now.Add(time.Hour).Minute(), 0, 0, time.UTC)
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
	if got := quietDefer(sched, at(23, 30)); !got.Equal(at(31, 0)) {
		t.Fatalf("23:30 should defer to next 07:00, got %v", got)
	}
	if got := quietDefer(sched, at(3, 0)); !got.Equal(at(7, 0)) {
		t.Fatalf("03:00 should defer to 07:00 same day, got %v", got)
	}
	if got := quietDefer(sched, at(12, 0)); !got.IsZero() {
		t.Fatalf("12:00 is outside the window, got %v", got)
	}
	if got := quietDefer(sched, at(7, 0)); !got.IsZero() {
		t.Fatalf("07:00 is the window end, got %v", got)
	}
	sched2 := Schedule{Timezone: "UTC", QuietFrom: "13:00", QuietTo: "15:00"}
	if got := quietDefer(sched2, at(14, 0)); !got.Equal(at(15, 0)) {
		t.Fatalf("14:00 should defer to 15:00, got %v", got)
	}
	if got := quietDefer(sched2, at(16, 0)); !got.IsZero() {
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
	s, o, g := occurrenceFixture(t)
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
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
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
	s, o, g := occurrenceFixture(t)
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
		if err := s.DispatchDueOccurrences(occ.DueAt, g.authorizeOccurrence); err != nil {
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
	if b, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o3)+".json")); err != nil || json.Unmarshal(b, &live) != nil || live.State != "capped" {
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
	if err := s.DispatchDueOccurrences(o4.DueAt, g.authorizeOccurrence); err != nil {
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
	s, o, g := occurrenceFixture(t)
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
		if err := s.DispatchDueOccurrences(occ.DueAt, g.authorizeOccurrence); err != nil {
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
	s, o, g := occurrenceFixture(t)
	now := time.Now().UTC()
	o.ScheduleID = "brief1"
	o.JobKind = "brief"
	o.Job.Text = "Утренний бриф"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
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
	s, o, g := occurrenceFixture(t)
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
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("brief admitted no agent job: %v", err)
	}
	if !strings.Contains(job.Text, "untrusted") || !strings.Contains(job.Text, "price watch") || !strings.Contains(job.Text, "250") {
		t.Fatalf("snapshot missing or not flagged untrusted: %q", job.Text)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
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
	s, o, g := occurrenceFixture(t)
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
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
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
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}}
	bobEnv := bob.envelope(22)
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
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
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
	s, o, g := occurrenceFixture(t)
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
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
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
	s, o, g := occurrenceFixture(t)
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
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
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
	if err := s.DispatchDueOccurrences(now.Add(25*time.Hour), g.authorizeOccurrence); err != nil {
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
	s, o, g := occurrenceFixture(t)
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
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("scoped brief produced no job")
	}
	if !strings.Contains(job.Text, "in scope") || strings.Contains(job.Text, "out of scope") {
		t.Fatalf("source_ids scope not honored: %q", job.Text)
	}
}

// S4.2 — a revoked brief cancels its due occurrence through the shared
// machinery; a replayed pending occurrence after a crash dedups the admitted
// job by its durable file (SPEC-0054 R4).
func TestBriefRevokedStaysQuietAndRestartDedups(t *testing.T) {
	s, o, g := occurrenceFixture(t)
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
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("revoked brief admitted a job")
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatal("revoked brief produced a delivery")
	}
	var occ RoutineOccurrence
	b, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
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
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	var occ2 RoutineOccurrence
	b2, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o2)+".json"))
	if err != nil || json.Unmarshal(b2, &occ2) != nil {
		t.Fatal(err)
	}
	occ2.State = "pending"
	if err := atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o2)+".json"), occ2); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
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

// S3.2 — a watcher may poll an owner's read-only ToolHub tool instead of a
// URL: the projected tool text is the compared value, and the URL fetch is
// never touched (SPEC-0054 R3.2).
func TestMCPToolProbeFeedsWatcher(t *testing.T) {
	s, o, g := occurrenceFixture(t)
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
		if err := s.DispatchDueOccurrences(occ.DueAt, g.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
		if err := s.ProbeDueOccurrencesContext(context.Background(), now, g.authorizeOccurrence, noFetch, toolCall); err != nil {
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
	g.spool = s
	g.config.ToolHubURL = server.URL
	g.config.Supervised = true
	g.config.RuntimeAuth = "control-token"
	due := time.Now().UTC().Add(-time.Minute)
	sched, _ := s.loadScheduleLocked("w-tick-mcp")
	sched.NextDue = due
	_ = atomicJSON(s.schedulePath("w-tick-mcp"), sched)
	g.tickProactive(due)
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("seed notified: %+v", d)
	}
	value = "changed-value"
	sched, _ = s.loadScheduleLocked("w-tick-mcp")
	sched.NextDue = due.Add(time.Minute)
	_ = atomicJSON(s.schedulePath("w-tick-mcp"), sched)
	g.tickProactive(due.Add(2 * time.Minute))
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "changed-value") {
		t.Fatalf("mcp tick did not deliver the change: %+v %v", d, err)
	}
}

// S3.2 — creating an mcp_tool check over HTTP seeds the baseline through the
// tool call, never through a URL fetch.
func TestCheckMCPProbeSeedsBaselineOverHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tool-call" {
			_ = json.NewEncoder(w).Encode(map[string]any{"text": "open"})
			return
		}
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()
	s, _, g := occurrenceFixture(t)
	g.spool = s
	g.config.ControlAuth = "tok"
	g.config.ToolHubURL = server.URL
	g.config.Supervised = true
	g.config.RuntimeAuth = "control-token"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	body := `{"schedule_id":"c-http-mcp","timezone":"UTC","expression":"0 * * * *","input":"connector state","job_kind":"check","expect":"equals:closed","probe":{"kind":"mcp_tool","tool":"hub-x"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/routines", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	s.mu.Lock()
	st, err := s.loadWatchStateLocked("c-http-mcp")
	s.mu.Unlock()
	if err != nil || st.Value != "open" {
		t.Fatalf("baseline not seeded from tool call: %+v %v", st, err)
	}
}
