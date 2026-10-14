package communication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// S1.1 — a check occurrence whose probe errors re-dues for a bounded retry
// instead of settling failed; exhaustion still fails visibly (SPEC-0054 R1.1).
func TestCheckProbeErrorRetriesThenFails(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "c-retry", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "is it up", JobKind: "check", Expect: "changed", Probe: &WatchProbe{URL: "https://example.com/x"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID, o.JobKind, o.Job.Text = "c-retry", "check", "is it up"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, string) ([]byte, error) { return nil, errors.New("source down") }
	now := o.DueAt
	if err := s.ProbeDueOccurrences(now, fail); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var live RoutineOccurrence
	if json.Unmarshal(b, &live) != nil || live.State != "probing" || live.RetryCount != 1 || !live.RetryAfter.After(now) {
		t.Fatalf("first probe error must back off: %+v", live)
	}
	// Probing again before the backoff deadline is a no-op.
	if err := s.ProbeDueOccurrences(now, fail); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if json.Unmarshal(b, &live) != nil || live.State != "probing" || live.RetryCount != 1 {
		t.Fatalf("pre-deadline pass must not retry: %+v", live)
	}
	// Past the deadline the occurrence re-probes; exhaustion settles failed.
	now = live.RetryAfter
	for live.State == "probing" {
		prev := live.RetryCount
		if err := s.ProbeDueOccurrences(now, fail); err != nil {
			t.Fatal(err)
		}
		b, _ = os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
		if json.Unmarshal(b, &live) != nil {
			t.Fatal("occurrence unreadable")
		}
		if live.State == "probing" {
			if live.RetryCount != prev+1 {
				t.Fatalf("due retry made no progress: %+v", live)
			}
			now = live.RetryAfter
		}
	}
	b, _ = os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if json.Unmarshal(b, &live) != nil || live.State != "failed" {
		t.Fatalf("exhausted retries must settle failed: %+v", live)
	}
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("failed probe notified the user: %+v", d)
	}
}

// S1.1 — retry is a check-only contract: a watcher probe error still settles
// failed immediately.
func TestCheckRetryNotForeignKind(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w-noretry", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/x"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID, o.JobKind = "w-noretry", "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(o.DueAt, func(context.Context, string) ([]byte, error) { return nil, errors.New("down") }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	var live RoutineOccurrence
	if json.Unmarshal(b, &live) != nil || live.State != "failed" || live.RetryCount != 0 {
		t.Fatalf("watcher must not retry: %+v", live)
	}
}

// S1.1 — validation: max_retries belongs to check and stays inside 0..8.
func TestMaxRetriesValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := Schedule{Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "x", JobKind: "check", Expect: "changed", Probe: &WatchProbe{URL: "https://example.com/x"}}
	bad := base
	bad.ScheduleID, bad.MaxRetries = "bad-nine", 9
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("max_retries=9 accepted")
	}
	bad = base
	bad.ScheduleID, bad.MaxRetries, bad.JobKind = "bad-kind", 2, "watcher"
	bad.Expect = ""
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("max_retries on watcher accepted")
	}
	good := base
	good.ScheduleID, good.MaxRetries = "good-eight", 8
	if _, err := s.CreateSchedule(good, caller, ""); err != nil {
		t.Fatalf("max_retries=8 rejected: %v", err)
	}
}

// S1.2 — a fired check tells the owner which predicate evaluated and what
// the values were, not just the stored prompt (SPEC-0054 R1.2).
func TestCheckDeliveryNamesVerdict(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if err := s.SeedWatchBaseline("c-verdict", "state: open", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "c-verdict", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "tell me when it merges", JobKind: "check", Expect: "equals:state: closed", Probe: &WatchProbe{URL: "https://example.com/pr", Regex: "(state: \\w+)"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID, o.JobKind, o.Job.Text = "c-verdict", "check", "tell me when it merges"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(o.DueAt, func(context.Context, string) ([]byte, error) { return []byte("state: closed"), nil }); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("met predicate must deliver: %v", err)
	}
	for _, want := range []string{"tell me when it merges", "equals:state: closed", "state: open", "state: closed"} {
		if !strings.Contains(d.Text, want) {
			t.Fatalf("verdict text missing %q: %s", want, d.Text)
		}
	}
}

// S1.3 — a task: probe reads the owner's task state without any fetch: an
// open task keeps the check skipped, archiving it fires once (SPEC-0054 R1.3).
func TestCheckTaskProbeFiresOnClose(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	user := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	task, _, err := s.CreateTask(user, 11, 0, "watch me", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "c-task", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "task still open?", JobKind: "check", Expect: "equals:closed", Probe: &WatchProbe{URL: "task:" + task.TaskID}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	fire := func(due time.Time) string {
		t.Helper()
		oc := RoutineOccurrence{ScheduleID: "c-task", Revision: 1, DueAt: due, JobKind: "check", Job: o.Job}
		oc.Job.Text = "task still open?"
		if err := s.PutOccurrence(oc, caller); err != nil {
			t.Fatal(err)
		}
		if err := s.DispatchDueOccurrences(due, g.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
		if err := s.ProbeDueOccurrences(due, func(context.Context, string) ([]byte, error) {
			t.Error("task: probe must never fetch")
			return nil, errors.New("must not be called")
		}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(oc)+".json"))
		var live RoutineOccurrence
		if json.Unmarshal(b, &live) != nil {
			t.Fatal("occurrence unreadable")
		}
		return live.State
	}
	if state := fire(o.DueAt); state != "skipped" {
		t.Fatalf("open task must skip an equals:closed check: %s", state)
	}
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("skipped check delivered: %+v", d)
	}
	task.Archived = true
	s.mu.Lock()
	if err := s.writeTaskLocked(task); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	if state := fire(o.DueAt.Add(time.Hour)); state != "dispatched" {
		t.Fatalf("closed task must fire: %s", state)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "open") || !strings.Contains(d.Text, "closed") {
		t.Fatalf("task verdict: %+v %v", d, err)
	}
}

// S1.3 — a foreign task id resolves under the schedule owner's principal and
// fails as a probe error (retry then failed) — never as a synthetic state.
func TestCheckTaskProbeForeignTaskFails(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}}
	foreign, _, err := s.CreateTask(bob, 22, 0, "not yours", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "c-foreign", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "peek", JobKind: "check", Expect: "equals:closed", MaxRetries: 1, Probe: &WatchProbe{URL: "task:" + foreign.TaskID}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID, o.JobKind = "c-foreign", "check"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(o.DueAt, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	var live RoutineOccurrence
	if json.Unmarshal(b, &live) != nil || live.State != "probing" || live.RetryCount != 1 {
		t.Fatalf("foreign task must be a probe error that retries: %+v", live)
	}
	if err := s.ProbeDueOccurrences(live.RetryAfter, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if json.Unmarshal(b, &live) != nil || live.State != "failed" {
		t.Fatalf("foreign task must fail closed: %+v", live)
	}
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("foreign task notified the user: %+v", d)
	}
}

// S1.3 — task: ids are validated at creation.
func TestTaskProbeValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	bad := Schedule{ScheduleID: "task-bad", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "x", JobKind: "check", Expect: "equals:closed", Probe: &WatchProbe{URL: "task:"}}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("empty task: id accepted")
	}
	bad.ScheduleID, bad.Probe.URL = "task-bad2", "task:../escape"
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("task: escape accepted")
	}
}

// S1.4 — a reminder due during downtime settles `missed` past the catch-up
// window instead of delivering late: the documented overdue policy (#247).
func TestReminderOverdueSettlesMissed(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	o.JobKind, o.ScheduleID = "reminder", "remind-late"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	// Two hours past due exceeds the default one-hour catch-up window.
	if err := s.DispatchDueOccurrences(o.DueAt.Add(2*time.Hour), g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	var live RoutineOccurrence
	if json.Unmarshal(b, &live) != nil || live.State != "missed" {
		t.Fatalf("out-of-window occurrence must settle missed: %+v", live)
	}
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("missed reminder delivered: %+v", d)
	}
}

// S1.3 — creating a check on task: through the control API seeds the
// baseline from the task state, not from a URL fetch (SPEC-0054 R1.3).
func TestCheckTaskProbeSeedsBaselineOverHTTP(t *testing.T) {
	s, _, g := occurrenceFixture(t)
	g.spool = s
	g.config.ControlAuth = "tok"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	task, _, err := s.CreateTask(User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}, 11, 0, "seed me", false)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"schedule_id":"c-http-task","timezone":"UTC","expression":"0 * * * *","input":"ping me when closed","job_kind":"check","expect":"equals:closed","probe":{"url":"task:%s"}}`, task.TaskID)
	req := httptest.NewRequest("POST", "/v1/routines", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("task: check rejected: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	st, err := s.loadWatchStateLocked("c-http-task")
	s.mu.Unlock()
	if err != nil || st.Value != "open" {
		t.Fatalf("baseline not seeded from task state: %+v %v", st, err)
	}
}
