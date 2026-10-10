package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func occurrenceFixture(t *testing.T) (*Spool, RoutineOccurrence, *Gateway) {
	t.Helper()
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Channel: "telegram_bot", ChatID: 11, Text: "scheduled work"}
	o := RoutineOccurrence{ScheduleID: "schedule-one", Revision: 1, DueAt: time.Unix(100, 0), Job: job}
	g := &Gateway{spool: s, config: Config{OrganizationID: "personal"}, users: map[int64]User{11: {ID: "alice", Enabled: true}}, now: func() time.Time { return time.Unix(90, 0) }}
	return s, o, g
}

// A reminder occurrence delivers its text straight to the outbox at fire
// time: no ordinary job, no runtime, no model call.
func TestReminderOccurrenceDeliversWithoutAgent(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	o.JobKind = "reminder"
	o.Job.Text = "Позвонить в банк"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatalf("reminder woke the agent: %+v %v", job, err)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.Text != "Позвонить в банк" || delivery.ChatID != 11 {
		t.Fatalf("reminder delivery: %+v %v", delivery, err)
	}
	if err := s.CompleteDelivery(delivery.ID); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.ClaimDelivery(); err != nil || extra != nil {
		t.Fatal("reminder delivered twice")
	}
}

// A reminder schedule materializes reminder occurrences end to end: the kind
// travels from the durable schedule through the occurrence into the outbox.
func TestReminderScheduleFiresThroughOutbox(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	_ = o
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	due := time.Now().UTC().Add(time.Minute)
	created, err := s.CreateSchedule(Schedule{ScheduleID: "remind-one", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "once:" + due.Format(time.RFC3339), Input: "Забрать посылку", JobKind: "reminder"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(due.Add(time.Second), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(due.Add(time.Second), g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatalf("reminder woke the agent: %+v %v", job, err)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.Text != "Забрать посылку" {
		t.Fatalf("delivery: %+v %v", delivery, err)
	}
	if created.JobKind != "reminder" {
		t.Fatalf("kind lost: %+v", created)
	}
}

// A script-kind occurrence executes inside the bounded Starlark runner and
// delivers its emit() output through the outbox — never an agent job, never
// a runtime, never a model call.
func TestScriptOccurrenceRunsInSandboxAndDelivers(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	o.JobKind = "script"
	o.Job.Text = `emit("disk at 91%")`
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatalf("script occurrence woke the agent: %+v %v", job, err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || d.Text != "disk at 91%" {
		t.Fatalf("emitted text not delivered: %+v %v", d, err)
	}
	b, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved RoutineOccurrence
	if err := json.Unmarshal(b, &saved); err != nil || saved.State != "dispatched" {
		t.Fatalf("script occurrence not dispatched: %+v %v", saved, err)
	}
	// A failing script leaves a failed occurrence — not an agent job.
	o2 := o
	o2.DueAt = o.DueAt.Add(time.Minute)
	o2.Job.Text = `raise`
	if err := s.PutOccurrence(o2, o2.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o2.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatalf("failed script woke the agent: %+v %v", job, err)
	}
	b, _ = os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o2)+".json"))
	var saved2 RoutineOccurrence
	if err := json.Unmarshal(b, &saved2); err != nil || saved2.State != "failed" {
		t.Fatalf("failed script not recorded: %+v %v", saved2, err)
	}
}

// The hub is the second syntax gate: a schedule created past the tool layer
// (control API, import) still cannot install a script that never compiles.
func TestCreateScheduleRejectsBrokenScript(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	_ = o
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	due := time.Now().UTC().Add(time.Minute)
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "broken", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "once:" + due.Format(time.RFC3339), Input: `emit(`, JobKind: "script"}, caller, ""); err == nil {
		t.Fatal("syntax-broken script schedule accepted")
	}
}

// A watcher schedule must carry a bounded probe spec: http(s) only, no
// embedded credentials, compilable regex. Anything else is rejected at
// create so no dead watch ever enters the queue.
func TestWatcherScheduleRequiresValidProbe(t *testing.T) {
	s, _, _ := occurrenceFixture(t)
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	due := time.Now().UTC().Add(time.Minute)
	base := Schedule{Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "once:" + due.Format(time.RFC3339), Input: "watch the page", JobKind: "watcher"}
	base.ScheduleID = "w-no-probe"
	if _, err := s.CreateSchedule(base, caller, ""); err == nil {
		t.Fatal("watcher without probe accepted")
	}
	for i, bad := range []*WatchProbe{
		{URL: "file:///etc/passwd"},
		{URL: "https://user:pass@example.com/x"},
		{URL: "https://example.com/x", Regex: "([invalid"},
	} {
		d := base
		d.ScheduleID = "w-bad-" + strconv.Itoa(i)
		d.Probe = bad
		if _, err := s.CreateSchedule(d, caller, ""); err == nil {
			t.Fatalf("watcher probe %v accepted", bad)
		}
	}
	ok := base
	ok.ScheduleID = "w-ok"
	ok.Probe = &WatchProbe{URL: "https://example.com/page", Regex: "price: (\\d+)"}
	if _, err := s.CreateSchedule(ok, caller, ""); err != nil {
		t.Fatalf("valid watcher rejected: %v", err)
	}
}

// A watcher occurrence is probed without touching the model: first success
// seeds a durable baseline silently, an unchanged tick records no_change at
// zero cost, and a changed hash delivers one notification with prev/now.
func TestWatcherProbeLifecycle(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch the price", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/p", Regex: "price: (\\d+)"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "w1"
	o.JobKind = "watcher"
	o.Job.Text = "watch the price"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	body := []byte("the price: 100 today")
	fetch := func(context.Context, string) ([]byte, error) { return body, nil }
	// First tick seeds the baseline — nothing reaches the user.
	if err := s.ProbeDueOccurrences(o.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("baseline seed notified the user: %+v", d)
	}
	// Unchanged value: no delivery, occurrence recorded no_change.
	o2 := o
	o2.DueAt = o.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(o2, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o2.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(o2.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("unchanged probe notified the user: %+v", d)
	}
	// Changed value: one delivery carrying old and new.
	body = []byte("the price: 250 today")
	o3 := o
	o3.DueAt = o2.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(o3, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o3.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(o3.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "100") || !strings.Contains(d.Text, "250") || !strings.Contains(d.Text, "watch the price") {
		t.Fatalf("change notification: %+v %v", d, err)
	}
	// The durable state records when the value changed — digests read it.
	b, err := os.ReadFile(s.watchPath("w1"))
	if err != nil {
		t.Fatal(err)
	}
	var st WatchState
	if json.Unmarshal(b, &st) != nil || !st.ChangedAt.Equal(o3.DueAt.UTC()) {
		t.Fatalf("watch state: %+v", st)
	}
	// A probe error records failure without notifying or losing the baseline.
	o4 := o
	o4.DueAt = o3.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(o4, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o4.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(o4.DueAt, func(context.Context, string) ([]byte, error) { return nil, errors.New("timeout") }); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("failing probe notified the user: %+v", d)
	}
	// A probe without a regex compares the whole bounded body.
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w2", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch page", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/whole"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o5 := o
	o5.ScheduleID = "w2"
	o5.JobKind = "watcher"
	o5.DueAt = o4.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(o5, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o5.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(o5.DueAt, func(context.Context, string) ([]byte, error) { return []byte("page v1"), nil }); err != nil {
		t.Fatal(err)
	}
	o6 := o5
	o6.DueAt = o5.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(o6, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o6.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(o6.DueAt, func(context.Context, string) ([]byte, error) { return []byte("page v2"), nil }); err != nil {
		t.Fatal(err)
	}
	d2, err := s.ClaimDelivery()
	if err != nil || d2 == nil || !strings.Contains(d2.Text, "v1") || !strings.Contains(d2.Text, "v2") {
		t.Fatalf("whole-body watch notification: %+v %v", d2, err)
	}
}

// The live fetch is bounded: one GET, no caches, 2xx only, capped body.
func TestFetchWatchBounded(t *testing.T) {
	var sawNoCache bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawNoCache = r.Header.Get("Cache-Control") == "no-cache"
		_, _ = w.Write(bytes.Repeat([]byte("x"), 300*1024))
	}))
	defer server.Close()
	g := &Gateway{}
	body, err := g.fetchWatch(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !sawNoCache {
		t.Fatal("watch fetch must bypass caches")
	}
	if len(body) > 256*1024 {
		t.Fatalf("fetch body unbounded: %d", len(body))
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	if _, err := g.fetchWatch(context.Background(), bad.URL); err == nil {
		t.Fatal("non-2xx probe treated as a value")
	}
}

// The gateway tick wires schedules -> dispatch -> probing: a watcher created
// through the schedule path is probed by the loop itself, not only by tests
// calling ProbeDueOccurrences by hand.
func TestGatewayTickProbesWatcher(t *testing.T) {
	body := []byte("the price: 100 today")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w-tick", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch the price", JobKind: "watcher", Probe: &WatchProbe{URL: server.URL, Regex: "price: (\\d+)"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	g.spool = s
	due := time.Now().UTC().Add(-time.Minute)
	sched, _ := s.loadScheduleLocked("w-tick")
	sched.NextDue = due
	_ = atomicJSON(s.schedulePath("w-tick"), sched)
	g.tickProactive(due)
	// Seed tick ran silently.
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("seed notified: %+v", d)
	}
	body = []byte("the price: 777 today")
	sched, _ = s.loadScheduleLocked("w-tick")
	sched.NextDue = due.Add(time.Minute)
	_ = atomicJSON(s.schedulePath("w-tick"), sched)
	g.tickProactive(due.Add(2 * time.Minute))
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "777") {
		t.Fatalf("tick probe did not deliver the change: %+v %v", d, err)
	}
}

// A check follow-up compares the probed value against the baseline seeded
// when the follow-up was created: expect=unchanged fires only if the world
// did not move ("PR still open Friday"), expect=changed fires if it did.
// No baseline or a probe error means the predicate cannot be evaluated —
// the occurrence fails, it never delivers blindly.
func TestCheckOccurrenceEvaluatesAgainstSeededBaseline(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	due := time.Now().UTC().Add(time.Minute)
	for i, expect := range []string{"", "bogus"} {
		bad := Schedule{ScheduleID: "c-bad-" + strconv.Itoa(i), Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "once:" + due.Format(time.RFC3339), Input: "remind about the PR", JobKind: "check", Probe: &WatchProbe{URL: "https://example.com/pr", Regex: "state: (\\w+)"}, Expect: expect}
		if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
			t.Fatalf("check with expect %q accepted", expect)
		}
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "c1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "remind about the PR", JobKind: "check", Probe: &WatchProbe{URL: "https://example.com/pr", Regex: "state: (\\w+)"}, Expect: "unchanged"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	// Creation-time seed: the world read "open" when the user asked.
	if err := s.SeedWatchBaseline("c1", "open", due.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	fire := func(scheduleID, text, body string) RoutineOccurrence {
		oc := o
		oc.ScheduleID = scheduleID
		oc.JobKind = "check"
		oc.Job.Text = text
		if err := s.PutOccurrence(oc, caller); err != nil {
			t.Fatal(err)
		}
		if err := s.DispatchDueOccurrences(oc.DueAt, g.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
		b := []byte(body)
		if err := s.ProbeDueOccurrences(oc.DueAt, func(context.Context, string) ([]byte, error) { return b, nil }); err != nil {
			t.Fatal(err)
		}
		return oc
	}
	// Still "open" at fire time — unchanged -> the reminder is delivered.
	fire("c1", "remind about the PR", "state: open")
	if d, _ := s.ClaimDelivery(); d == nil || !strings.Contains(d.Text, "remind about the PR") {
		t.Fatalf("met check did not deliver: %+v", d)
	}
	// Now expect=changed on the same probe: "open" persists -> skipped.
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "c2", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "ping me when merged", JobKind: "check", Probe: &WatchProbe{URL: "https://example.com/pr", Regex: "state: (\\w+)"}, Expect: "changed"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWatchBaseline("c2", "open", due.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	oc := fire("c2", "ping me when merged", "state: open")
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("unmet check delivered: %+v", d)
	}
	b, _ := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(oc)+".json"))
	var saved RoutineOccurrence
	if err := json.Unmarshal(b, &saved); err != nil || saved.State != "skipped" {
		t.Fatalf("unmet check not skipped: %+v %v", saved, err)
	}
	// Merged -> changed -> delivered.
	oc2 := oc
	oc2.DueAt = oc.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(oc2, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(oc2.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(oc2.DueAt, func(context.Context, string) ([]byte, error) { return []byte("state: merged"), nil }); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.ClaimDelivery(); d == nil || !strings.Contains(d.Text, "ping me when merged") {
		t.Fatalf("met check did not deliver: %+v", d)
	}
}

// Check creation anchors the baseline at the moment the user asked: the
// handler probes once, seeds it, then persists the schedule. An unreachable
// source yields 502 and no schedule — never a silently-skipping follow-up.
func TestRoutineCreateSeedsCheckBaseline(t *testing.T) {
	var hits int
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("state: open"))
	}))
	defer source.Close()
	s, o, g := occurrenceFixture(t)
	g.spool = s
	g.config.ControlAuth = "tok"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	post := func(id, probeURL string) int {
		body := strings.NewReader(fmt.Sprintf(`{"schedule_id":%q,"timezone":"UTC","expression":"0 * * * *","input":"remind about the PR","job_kind":"check","expect":"unchanged","probe":{"url":%q,"regex":"state: (\\w+)"}}`, id, probeURL))
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/routines", body)
		req.Header.Set("Authorization", "Bearer "+g.config.ControlAuth)
		req.Header.Set("X-Hub-Principal", "alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("c-http", source.URL); code != http.StatusOK {
		t.Fatalf("check create: %d", code)
	}
	if hits != 1 {
		t.Fatalf("creation probed %d times", hits)
	}
	st, err := s.loadWatchStateLocked("c-http")
	if err != nil || st.Baseline != watchHash("open") {
		t.Fatalf("baseline not seeded: %+v %v", st, err)
	}
	if code := post("c-dead", "http://127.0.0.1:1/dead"); code != http.StatusBadGateway {
		t.Fatalf("dead source create: %d", code)
	}
	if _, err := s.loadScheduleLocked("c-dead"); err == nil {
		t.Fatal("dead source created a schedule")
	}
	// No regex: the baseline is the whole bounded body.
	postNoRe := func(id string) int {
		body := strings.NewReader(fmt.Sprintf(`{"schedule_id":%q,"timezone":"UTC","expression":"0 * * * *","input":"watch page","job_kind":"check","expect":"changed","probe":{"url":%q}}`, id, source.URL))
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/routines", body)
		req.Header.Set("Authorization", "Bearer "+g.config.ControlAuth)
		req.Header.Set("X-Hub-Principal", "alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := postNoRe("c-whole"); code != http.StatusOK {
		t.Fatalf("no-regex check create: %d", code)
	}
	st2, err := s.loadWatchStateLocked("c-whole")
	if err != nil || st2.Baseline != watchHash("state: open") {
		t.Fatalf("whole-body baseline wrong: %+v %v", st2, err)
	}
	_ = o
}

// A digest occurrence aggregates the owner's watchers that changed since the
// previous digest into ONE deterministic outbox message — no model, no
// runtime. The marker advances so repeats only report new changes.
func TestDigestOccurrenceAggregatesChangedWatches(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	mk := func(id, input string) {
		t.Helper()
		if _, err := s.CreateSchedule(Schedule{ScheduleID: id, Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: input, JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/" + id, Regex: "(x)"}}, caller, ""); err != nil {
			t.Fatal(err)
		}
	}
	mk("wa", "price")
	mk("wb", "stale watch")
	old := time.Now().UTC().Add(-48 * time.Hour)
	// wa changed recently, wb changed before the last digest marker.
	if err := atomicJSON(s.watchPath("wa"), WatchState{ScheduleID: "wa", Baseline: "h1", Value: "250", ChangedAt: time.Now().UTC().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath("wb"), WatchState{ScheduleID: "wb", Baseline: "h2", Value: "1", ChangedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.digestPath("dg"), DigestState{ScheduleID: "dg", LastDigestAt: old.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "dg"
	o.JobKind = "digest"
	o.Job.Text = "Утренний дайджест"
	o.DueAt = now.Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "Утренний дайджест") || !strings.Contains(d.Text, "price") || !strings.Contains(d.Text, "250") || strings.Contains(d.Text, "stale watch") {
		t.Fatalf("digest: %+v %v", d, err)
	}
	// A check without a probe must be rejected — it would have nothing to
	// evaluate and would panic the creation path.
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "noprobe", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Input: "check", JobKind: "check", Expect: "changed"}, caller, ""); err == nil {
		t.Fatal("check without a probe accepted")
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "badurl", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Input: "check", JobKind: "check", Expect: "changed", Probe: &WatchProbe{URL: "file:///etc/passwd"}}, caller, ""); err == nil {
		t.Fatal("check with a non-http probe accepted")
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "badre", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Input: "check", JobKind: "check", Expect: "changed", Probe: &WatchProbe{URL: "https://example.com/", Regex: "(["}}, caller, ""); err == nil {
		t.Fatal("check with a broken regex accepted")
	}
	// The digest kind itself must be a valid schedule kind.
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "dg2", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 8 * * *", Input: "morning digest", JobKind: "digest"}, caller, ""); err != nil {
		t.Fatalf("digest schedule rejected: %v", err)
	}
	// Marker advanced: a second digest right away has nothing new.
	var dg DigestState
	b, _ := os.ReadFile(s.digestPath("dg"))
	if err := json.Unmarshal(b, &dg); err != nil || dg.LastDigestAt.Before(now) {
		t.Fatalf("digest marker not advanced: %+v %v", dg, err)
	}
}

func TestDueOccurrenceSurvivesRestartAndEnqueuesExactlyOneOrdinaryJob(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt.Add(-time.Second), g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("future occurrence executed")
	}
	restarted, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	s = restarted
	g.spool = s
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	job, err := s.ClaimJob()
	if err != nil || job == nil || job.Trigger != "cron" || job.ID != occurrenceID(o) || job.Envelope != o.Job.Envelope {
		t.Fatalf("ordinary job: %+v %v", job, err)
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("duplicate occurrence job")
	}
	// Simulate a crash after queue admission but before the occurrence settled.
	path := filepath.Join(s.root, "occurrences", occurrenceID(o)+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved RoutineOccurrence
	_ = json.Unmarshal(b, &saved)
	saved.State = "pending"
	if err := atomicJSON(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("handoff replay created duplicate")
	}
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	changed := o
	changed.Job.Text = "different"
	if err := s.PutOccurrence(changed, changed.Job.Envelope); err == nil {
		t.Fatal("occurrence identity conflict accepted")
	}
}
func TestOccurrenceRejectsWrongOwnerSecretsRevocationAndExpiredCatchup(t *testing.T) {
	for _, kind := range []string{"owner", "secret", "revoked", "policy", "expired"} {
		t.Run(kind, func(t *testing.T) {
			s, o, g := occurrenceFixture(t)
			caller := o.Job.Envelope
			switch kind {
			case "owner":
				caller.ExternalIdentityID = "other"
			case "secret":
				o.Job.Text = "GITHUB_TOKEN=secret"
			case "revoked":
				delete(g.users, 11)
			case "policy":
				t.Setenv("HUB_POLICY_VERSION", "policy-new")
			}
			err := s.PutOccurrence(o, caller)
			if kind == "owner" || kind == "secret" {
				if err == nil {
					t.Fatal("unsafe occurrence accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			now := o.DueAt
			if kind == "expired" {
				now = now.Add(2 * time.Hour)
			}
			if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
				t.Fatal(err)
			}
			if job, err := s.ClaimJob(); err != nil || job != nil {
				t.Fatal("unauthorized or expired occurrence executed")
			}
		})
	}
	s, o, _ := occurrenceFixture(t)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, nil); err == nil {
		t.Fatal("missing authorizer accepted")
	}
	if err := s.DispatchDueOccurrences(o.DueAt, func(Job) error { return errors.New("revoked") }); err != nil {
		t.Fatal(err)
	}
}

func TestRunAtCommandPersistsVerifiedOccurrenceWithoutImmediateExecution(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	o.DueAt = time.Unix(100, 0)
	o.Job.Trigger = ""
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("occurrences=%d err=%v", len(entries), err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("occurrence executed early")
	}
	if err := s.DispatchDueOccurrences(time.Unix(100, 0), g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil || job.Text != "scheduled work" || job.Trigger != "cron" {
		t.Fatalf("scheduled job: %+v %v", job, err)
	}
}

func TestOccurrencesDoNotOverlapAndUnknownRunContinuesToBlockSchedule(t *testing.T) {
	s, first, g := occurrenceFixture(t)
	second := first
	second.DueAt = first.DueAt.Add(time.Second)
	for _, o := range []RoutineOccurrence{second, first} {
		if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
			t.Fatal(err)
		}
	}
	now := second.DueAt
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil || job.ID != occurrenceID(first) {
		t.Fatalf("earliest occurrence not selected: %+v %v", job, err)
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("overlapping occurrence admitted")
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{Status: "uncertain"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("unknown original run allowed overlap")
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{Status: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimJob()
	if err != nil || next == nil || next.ID != occurrenceID(second) {
		t.Fatalf("terminal run did not release schedule: %+v %v", next, err)
	}
}

func TestOccurrenceSerializedBoundRejectsEscapedOversize(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	o.Job.Text = strings.Repeat("\x01", 64*1024)
	if err := s.PutOccurrence(o, o.Job.Envelope); err == nil {
		t.Fatal("oversized durable occurrence accepted")
	}
}
