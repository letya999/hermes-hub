package communication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestM10CronDaysAndMinutes(t *testing.T) {
	from := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	next, err := nextCron("0 12 12 * 2", time.UTC, from)
	if err != nil || next != from.Add(24*time.Hour) {
		t.Fatalf("Monday 12th must match day of month: %v %v", next, err)
	}
	next, err = nextCron("* * * * *", time.UTC, from)
	if err != nil || next != from.Add(time.Minute) {
		t.Fatalf("minute skipped: %v %v", next, err)
	}
}

func TestM10UsageTornTailAndUnknown(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := testJob("known", "user:alice")
	report := hubruntime.SessionUsage{SessionFound: true, SessionID: "one", Model: "cheap", FetchedAt: time.Now(), InputTokens: int64ptr(10), OutputTokens: int64ptr(4)}
	if err = s.RecordUsage(job, "run1", report); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(usagePath(s.root), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"job_id":"interrupted`); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	job.ID = "unknown"
	if err = s.RecordUsage(job, "run2", hubruntime.SessionUsage{}); err != nil {
		t.Fatal(err)
	}
	totals, err := s.UsageTotals("user:alice")
	if err != nil || totals.Jobs != 2 || totals.UnknownJobs != 1 || totals.InputTokens != 10 {
		t.Fatalf("totals=%+v %v", totals, err)
	}
	tails, err := filepath.Glob(filepath.Join(s.root, "usage-tail-*.partial"))
	if err != nil || len(tails) != 1 {
		t.Fatalf("tail was discarded: %v %v", tails, err)
	}
	totals, err = s.UsageTotalsSince("user:alice", time.Now().Add(-time.Hour), "cheap")
	if err != nil || totals.Jobs != 1 {
		t.Fatalf("model filter=%+v %v", totals, err)
	}
	// Corruption of a complete record must never be silently erased.
	f, err = os.OpenFile(usagePath(s.root), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("broken\n")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	job.ID = "third"
	if err = s.RecordUsage(job, "run3", report); err == nil {
		t.Fatal("complete corruption accepted")
	}
}

func m10Watch(t *testing.T, s *Spool, o RoutineOccurrence) Schedule {
	t.Helper()
	in := Schedule{ScheduleID: o.ScheduleID, Envelope: o.Job.Envelope, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "watch", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com"}}
	schedule, err := s.CreateSchedule(in, o.Job.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SeedWatchBaseline(in.ScheduleID, "old", time.Now()); err != nil {
		t.Fatal(err)
	}
	return schedule
}

func TestM10ProbeReceiptRecoversFailedOutboxWithoutRefetch(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	m10Watch(t, s, o)
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(s.root, "outbox", "pending")
	saved := pending + ".saved"
	if err := os.Rename(pending, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetch := func(context.Context, string) ([]byte, error) { calls++; return []byte("new"), nil }
	if err := s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, g.authorizeOccurrence, fetch, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, pending); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, g.authorizeOccurrence, fetch, nil, nil); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "now: new") || calls != 1 {
		t.Fatalf("delivery=%+v calls=%d err=%v", d, calls, err)
	}
	st, err := s.loadWatchStateLocked(o.ScheduleID)
	if err != nil || st.Generation != 1 || st.Value != "new" {
		t.Fatalf("state=%+v %v", st, err)
	}
}

func TestM10DeleteDuringProbeDiscardsResult(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	m10Watch(t, s, o)
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, g.authorizeOccurrence, func(context.Context, string) ([]byte, error) { close(started); <-release; return []byte("new"), nil }, nil, nil)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not start")
	}
	if err := s.DeleteSchedule(o.ScheduleID, o.Job.Envelope); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d != nil {
		t.Fatalf("deleted probe delivered: %+v %v", d, err)
	}
	st, err := s.loadWatchStateLocked(o.ScheduleID)
	if err != nil || st.Value != "old" {
		t.Fatalf("cancelled result changed baseline: %+v %v", st, err)
	}
}

func TestM10ProactiveCapacityAndCancellation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		o.ScheduleID = id
		o.State = "probing"
		if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
			t.Fatal(err)
		}
		o.State = "probing"
		if err := atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
			t.Fatal(err)
		}
	}
	var active, peak atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- s.processProactive(ctx, "probing", func(RoutineOccurrence) {
			n := active.Add(1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
		})
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("more than two workers")
	default:
	}
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if peak.Load() > 2 || active.Load() != 0 || len(s.proactiveFlight) != 0 {
		t.Fatalf("worker leak peak=%d active=%d flights=%v", peak.Load(), active.Load(), s.proactiveFlight)
	}
}

func TestM10ProbeKindsAndFailure(t *testing.T) {
	probe := &WatchProbe{Kind: "github_issue", URL: "https://api.github.com/repos/owner/repo/issues/1"}
	for _, state := range []string{"open", "closed"} {
		v, err := probeValue(probe, []byte(`{"state":"`+state+`"}`))
		if err != nil || v != state {
			t.Fatalf("value=%q %v", v, err)
		}
	}
	for _, body := range []string{`{}`, `bad`, `{"state":"deleted"}`} {
		if _, err := probeValue(probe, []byte(body)); err == nil {
			t.Fatal("invalid issue state accepted")
		}
	}
	if _, err := probeValue(&WatchProbe{Regex: `v=(\d+)`}, []byte("missing")); err == nil {
		t.Fatal("missing capture treated as value")
	}
	g := &Gateway{}
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("private network reached") }))
	defer private.Close()
	if _, err := g.fetchWatch(context.Background(), private.URL); err == nil {
		t.Fatal("private probe allowed")
	}
}

func TestM10ScriptResultReceiptAndFailure(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	o.JobKind = "script"
	o.Job.Text = `emit("hello")`
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	calls := 0
	run := func(context.Context, RoutineOccurrence) ([]string, error) { calls++; return []string{"hello"}, nil }
	if err := s.RunDueScripts(context.Background(), o.DueAt, g.authorizeOccurrence, run); err != nil {
		t.Fatal(err)
	}
	// Simulate restart after output handoff, before occurrence settlement.
	file := filepath.Join(s.root, "occurrences", occurrenceID(o)+".json")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var saved RoutineOccurrence
	if err = json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	saved.State = "scripting"
	if err = atomicJSON(file, saved); err != nil {
		t.Fatal(err)
	}
	if err = s.RunDueScripts(context.Background(), o.DueAt, g.authorizeOccurrence, run); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || d.Text != "hello" || calls != 1 {
		t.Fatalf("script replay=%+v calls=%d %v", d, calls, err)
	}
	if err = s.CompleteDelivery(d.ID); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.ClaimDelivery(); err != nil || extra != nil {
		t.Fatalf("duplicate script delivery=%+v %v", extra, err)
	}
	o.ScheduleID = "failing"
	if err = s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err = s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err = s.RunDueScripts(context.Background(), o.DueAt, g.authorizeOccurrence, func(context.Context, RoutineOccurrence) ([]string, error) { return nil, errors.New("OOM") }); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if err != nil || json.Unmarshal(body, &saved) != nil || saved.State != "failed" {
		t.Fatalf("script failure not durable: %s %v", body, err)
	}
	if _, err = g.runIsolatedScript(context.Background(), o); err == nil {
		t.Fatal("unsupervised script executed")
	}
}

func TestM10DigestQuietSelectionAndRecovery(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	schedule := m10Watch(t, s, o)
	schedule.NotifyMode = "digest"
	if err := atomicJSON(s.schedulePath(schedule.ScheduleID), schedule); err != nil {
		t.Fatal(err)
	}
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, g.authorizeOccurrence, func(context.Context, string) ([]byte, error) { return []byte("new"), nil }, nil, nil); err != nil {
		t.Fatal(err)
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatalf("quiet watch sent=%+v %v", d, err)
	}
	digest := schedule
	digest.ScheduleID = "digest"
	digest.JobKind = "digest"
	digest.Probe = nil
	digest.NotifyMode = ""
	digest.SourceIDs = []string{schedule.ScheduleID}
	// The unselected source must never appear.
	other := schedule
	other.ScheduleID = "unselected"
	other.Input = "do not include"
	if err := atomicJSON(s.schedulePath(other.ScheduleID), other); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.watchPath(other.ScheduleID), WatchState{ScheduleID: other.ScheduleID, Generation: 1, Value: "secret", ChangedAt: o.DueAt}); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.schedulePath(digest.ScheduleID), digest); err != nil {
		t.Fatal(err)
	}
	d := o
	d.ScheduleID = digest.ScheduleID
	d.JobKind = "digest"
	d.DueAt = o.DueAt.Add(time.Minute)
	if err := s.PutOccurrence(d, d.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(d.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil || !strings.Contains(delivery.Text, "new") || strings.Contains(delivery.Text, "secret") {
		t.Fatalf("digest=%+v %v", delivery, err)
	}
	if err = s.CompleteDelivery(delivery.ID); err != nil {
		t.Fatal(err)
	}
	// A stale occurrence state replays its canonical receipt without another send.
	d.Job.ID = occurrenceID(d)
	d.Job.IdempotencyKey = d.Job.ID
	d.State = "pending"
	if err = atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(d)+".json"), d); err != nil {
		t.Fatal(err)
	}
	if err = s.DispatchDueOccurrences(d.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.ClaimDelivery(); err != nil || extra != nil {
		t.Fatalf("receipt replay sent twice: %+v %v", extra, err)
	}
	d.DueAt = d.DueAt.Add(time.Minute)
	if err = s.PutOccurrence(d, d.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err = s.DispatchDueOccurrences(d.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	delivery, err = s.ClaimDelivery()
	if err != nil || delivery == nil || !strings.Contains(delivery.Text, "Новых изменений нет") {
		t.Fatalf("cursor lost: %+v %v", delivery, err)
	}
}

func TestM10RoutineStateSnoozeAndValidation(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	schedule := m10Watch(t, s, o)
	g.spool = s
	g.config.ControlAuth = "token"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	call := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer token")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/v1/routines/"+schedule.ScheduleID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"watch_state"`) {
		t.Fatalf("state=%d %s", w.Code, w.Body.String())
	}
	until := time.Now().Add(2 * time.Hour).UTC()
	if w := call("POST", "/v1/routines/"+schedule.ScheduleID+"/snooze", `{"until":"`+until.Format(time.RFC3339)+`"}`); w.Code != 200 {
		t.Fatalf("snooze=%d %s", w.Code, w.Body.String())
	}
	saved, err := s.loadScheduleLocked(schedule.ScheduleID)
	if err != nil || saved.Revision != 2 || saved.SnoozedUntil.IsZero() {
		t.Fatalf("snooze state=%+v %v", saved, err)
	}
	if err = s.TickSchedules(time.Now().Add(time.Minute), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("snoozed work enqueued: %v %v", entries, err)
	}
	for _, edit := range []func(*Schedule){func(s *Schedule) { s.SourceIDs = []string{"bad/id"} }, func(s *Schedule) { s.NotifyMode = "spam" }, func(s *Schedule) { s.Probe.Regex = "no capture" }, func(s *Schedule) { s.Probe.Kind = "guessed" }, func(s *Schedule) { s.Probe.URL = "https://user:secret@example.com" }} {
		draft := schedule
		probe := *schedule.Probe
		draft.Probe = &probe
		edit(&draft)
		if err := ValidateSchedule(draft, draft.Envelope, time.Now()); err == nil {
			t.Fatal("bad draft accepted")
		}
	}
}

func TestM10WriterLockAndLocalUsage(t *testing.T) {
	root := t.TempDir()
	unlock, err := LockSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSpool(root)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	if _, err = NewSpool(root); err == nil {
		unlock()
		t.Fatal("second writer recovered a live spool")
	}
	unlock()
	unlock, err = LockSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	g := &Gateway{spool: s, now: func() time.Time { return time.Now() }}
	job := testJob("usage", "user:alice")
	if err = s.RecordUsage(job, "run", hubruntime.SessionUsage{SessionFound: true, SessionID: "one", Model: "tiny", InputTokens: int64ptr(17), OutputTokens: int64ptr(2)}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"/usage", "/usage day", "/usage week model=tiny", "/usage model=tiny", "/usage month tiny"} {
		got := g.journalUsage(User{ID: "alice"}, command)
		if !strings.Contains(got, "вход 17") {
			t.Fatalf("%s: %s", command, got)
		}
	}
	for _, command := range []string{"/usage wrong", "/usage day week", "/usage model=", "/usage day tiny extra"} {
		if got := g.journalUsage(User{ID: "alice"}, command); !strings.Contains(got, "Используйте") {
			t.Fatalf("invalid %s: %s", command, got)
		}
	}
}

func TestM10SupervisorScriptClientHTTPBoundaries(t *testing.T) {
	_, o, g := occurrenceFixture(t)
	o.JobKind = "script"
	o.Job.ID = "owned"
	o.Job.Trigger = "cron"
	o.Job.Text = `emit("ok")`
	response, status := `["ok"]`, 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request hubruntime.ScriptRequest
		if r.URL.Path != "/v1/script" || r.Header.Get("Authorization") != "Bearer token" || json.NewDecoder(r.Body).Decode(&request) != nil || request.PrincipalID != "alice" || request.Text != o.Job.Text || !request.DueAt.Equal(o.DueAt) {
			t.Error("script ownership lost in transport")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	defer srv.Close()
	g.config.Supervised = true
	g.config.RuntimeURL = srv.URL
	g.config.RuntimeAuth = "token"
	texts, err := g.runIsolatedScript(context.Background(), o)
	if err != nil || len(texts) != 1 || texts[0] != "ok" {
		t.Fatalf("script client=%v %v", texts, err)
	}
	for _, bad := range []string{`invalid`, `["a","b","c","d","e"]`, `["` + strings.Repeat("x", 4097) + `"]`, strings.Repeat("x", 128*1024+1)} {
		response = bad
		if _, err = g.runIsolatedScript(context.Background(), o); err == nil {
			t.Fatal("invalid script response accepted")
		}
	}
	status = 502
	if _, err = g.runIsolatedScript(context.Background(), o); err == nil {
		t.Fatal("failed helper accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = g.runIsolatedScript(ctx, o); err == nil {
		t.Fatal("cancelled script request started")
	}
	g.config.RuntimeURL = ":invalid"
	if _, err = g.runIsolatedScript(context.Background(), o); err == nil {
		t.Fatal("invalid supervisor URL accepted")
	}
}

func TestM10RevokedProbeCannotUpdateOrDeliver(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	m10Watch(t, s, o)
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	revoked := false
	authorize := func(Job) error {
		if revoked {
			return errors.New("revoked")
		}
		return nil
	}
	if err := s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, authorize, func(context.Context, string) ([]byte, error) { revoked = true; return []byte("private new value"), nil }, nil, nil); err != nil {
		t.Fatal(err)
	}
	st, err := s.loadWatchStateLocked(o.ScheduleID)
	if err != nil || st.Value != "old" {
		t.Fatalf("revoked probe changed state=%+v %v", st, err)
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatalf("revoked probe sent=%+v %v", d, err)
	}
}

func TestM10DeleteAfterDispatchFencesDeliveryAndAgent(t *testing.T) {
	for _, kind := range []string{"reminder", "agent"} {
		t.Run(kind, func(t *testing.T) {
			s, o, g := occurrenceFixture(t)
			due := time.Now().Add(time.Minute).UTC()
			schedule := Schedule{ScheduleID: "delete-late", Envelope: o.Job.Envelope, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "once:" + due.Format(time.RFC3339), Input: "late", JobKind: kind}
			if _, err := s.CreateSchedule(schedule, o.Job.Envelope, ""); err != nil {
				t.Fatal(err)
			}
			if err := s.TickSchedules(due.Add(time.Second), "", g.authorizeOccurrence); err != nil {
				t.Fatal(err)
			}
			if err := s.DispatchDueOccurrences(due.Add(time.Second), g.authorizeOccurrence); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteSchedule(schedule.ScheduleID, o.Job.Envelope); err != nil {
				t.Fatal(err)
			}
			if kind == "agent" {
				if job, err := s.ClaimJob(); err != nil || job != nil {
					t.Fatalf("deleted agent admitted=%+v %v", job, err)
				}
			} else {
				api := &fakeAPI{}
				g.api = api
				g.deliverOne(context.Background())
				if len(api.sent) != 0 {
					t.Fatal("late cancelled reminder sent")
				}
			}
			if _, err := s.CreateSchedule(schedule, o.Job.Envelope, ""); err == nil {
				t.Fatal("retired ID resurrected old work")
			}
		})
	}
}

func TestM10CapturedAttachmentsRemainReadableOnlyByOwner(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	g.api = api
	if err = g.mockLinkClaim(42, "alice"); err != nil {
		t.Fatal(err)
	}
	blob, err := g.spool.WriteDeliveryBlob("capture", "note.txt", []byte("attachment bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.spool.EnqueueDelivery(Delivery{ID: "capture", Channel: "mock", ChatID: 42, Text: "file", Artifacts: []hubruntime.ArtifactRef{{Name: "note.txt", Blob: blob}}}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	r := httptest.NewRequest("GET", "/v1/mock/outbox", nil)
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	var out []struct {
		ArtifactData map[string][]byte `json:"artifact_data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out) != 1 || string(out[0].ArtifactData["note.txt"]) != "attachment bytes" {
		t.Fatalf("attachment lost after spool cleanup: %d %s", w.Code, w.Body.String())
	}
	if len(api.sent)+len(api.docs)+len(api.photos) != 0 {
		t.Fatal("mock sent externally")
	}
	r = httptest.NewRequest("POST", "/v1/mock/outbox", nil)
	w = httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatalf("read-only mock surface=%d", w.Code)
	}
}

func TestM10EnrolledKnowledgeContextAndRevocation(t *testing.T) {
	// The /v1/knowledge endpoint and its enrolled-token context resolution
	// moved out of the gateway with CHG-0121: knowledge is read in-process
	// from the runtime state dir. Context subdir coverage lives in
	// internal/agenttools tests.
	c := testConfig(t)
	c.ControlAuth = "operator"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v1/knowledge", nil)
	req.Header.Set("Authorization", "Bearer operator")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("knowledge endpoint still served: %d", rec.Code)
	}
}

type m10BlockingUsage struct {
	fakeRunner
	started chan struct{}
}

func (r *m10BlockingUsage) SessionUsage(ctx context.Context, _ hubruntime.ExecuteRequest) (hubruntime.SessionUsage, error) {
	r.started <- struct{}{}
	<-ctx.Done()
	return hubruntime.SessionUsage{}, ctx.Err()
}

func TestM10UsageQueriesBoundAndStopWithGateway(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.commandCtx = ctx
	runner := &m10BlockingUsage{started: make(chan struct{}, 2)}
	g.runner = runner
	g.api = &fakeAPI{}
	for i := 1; i <= 3; i++ {
		if err = g.handleUpdate(context.Background(), taskUpdate(i, i, "/usage session", 11, 0)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case <-runner.started:
		case <-time.After(5 * time.Second):
			t.Fatal("usage query not started")
		}
	}
	if g.usageQueries.Load() != 2 {
		t.Fatalf("unbounded usage queries: %d", g.usageQueries.Load())
	}
	cancel()
	done := make(chan struct{})
	go func() { g.usageWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown left usage query running")
	}
	if g.usageQueries.Load() != 0 {
		t.Fatal("usage capacity leaked")
	}
	if job, err := g.spool.ClaimJob(); err != nil || job != nil {
		t.Fatalf("usage entered LLM queue: %+v %v", job, err)
	}
}

func TestM10StatusRoutinesConnectorsStayDeterministic(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	g.runner = &fakeRunner{}
	for i, command := range []string{"/status", "/routines", "/connectors", "/usage"} {
		if err = g.handleUpdate(context.Background(), taskUpdate(i+1, i+1, command, 11, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if job, err := g.spool.ClaimJob(); err != nil || job != nil {
		t.Fatalf("local command entered LLM queue: %+v %v", job, err)
	}
	// /usage resolves asynchronously; settle before counting so the reply
	// write cannot race the TempDir cleanup either.
	g.usageWG.Wait()
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "outbox", "pending"))
	if err != nil || len(entries) != 5 {
		t.Fatalf("local replies=%d %v", len(entries), err)
	}
}

func TestM10DigestLimitsAndReportsSourceErrorsOnce(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	template := m10Watch(t, s, o)
	for i := 0; i < 18; i++ {
		source := template
		source.ScheduleID = "source-" + string(rune('a'+i))
		source.Input = source.ScheduleID
		if err := atomicJSON(s.schedulePath(source.ScheduleID), source); err != nil {
			t.Fatal(err)
		}
		state := WatchState{ScheduleID: source.ScheduleID, Generation: 1, Value: "value", ChangedAt: o.DueAt, LastError: "probe failed"}
		if err := atomicJSON(s.watchPath(source.ScheduleID), state); err != nil {
			t.Fatal(err)
		}
	}
	digest := template
	digest.ScheduleID = "digest-errors"
	digest.JobKind = "digest"
	digest.Probe = nil
	if err := atomicJSON(s.schedulePath(digest.ScheduleID), digest); err != nil {
		t.Fatal(err)
	}
	d := o
	d.ScheduleID = digest.ScheduleID
	d.JobKind = "digest"
	for pass, want := range []int{16, 2, 0} {
		d.DueAt = o.DueAt.Add(time.Duration(pass) * time.Minute)
		if err := s.PutOccurrence(d, d.Job.Envelope); err != nil {
			t.Fatal(err)
		}
		if err := s.DispatchDueOccurrences(d.DueAt, g.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
		delivery, err := s.ClaimDelivery()
		if err != nil || delivery == nil {
			t.Fatalf("digest missing: %v", err)
		}
		if got := strings.Count(delivery.Text, "• "); got != want {
			t.Fatalf("same errors repeated or entries lost: pass=%d got=%d want=%d text=%s", pass, got, want, delivery.Text)
		}
		if pass == 2 && !strings.Contains(delivery.Text, "Источников недоступно: 18") {
			t.Fatal("persistent errors presented as healthy")
		}
		if err = s.CompleteDelivery(delivery.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestM10DigestCorruptionCannotAdvanceCursor(t *testing.T) {
	for _, kind := range []string{"receipt", "cursor", "watch"} {
		t.Run(kind, func(t *testing.T) {
			s, o, g := occurrenceFixture(t)
			digest := m10Watch(t, s, o)
			digest.JobKind = "digest"
			digest.Probe = nil
			if err := atomicJSON(s.schedulePath(digest.ScheduleID), digest); err != nil {
				t.Fatal(err)
			}
			o.JobKind = "digest"
			if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(s.root, "occurrences", occurrenceID(o)+".digest-result")
			if kind == "cursor" {
				name = s.digestPath(o.ScheduleID)
			}
			if kind == "watch" {
				name = s.watchPath(o.ScheduleID)
			}
			if err := os.WriteFile(name, []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err == nil {
				t.Fatal("corruption treated as successful digest")
			}
			if delivery, err := s.ClaimDelivery(); err != nil || delivery != nil {
				t.Fatalf("corrupt digest delivered=%+v %v", delivery, err)
			}
		})
	}
}

func TestM10MappedDeliveryRechecksOwnerPolicyAndTarget(t *testing.T) {
	for _, kind := range []string{"disabled", "policy", "runtime", "target", "allowed"} {
		t.Run(kind, func(t *testing.T) {
			s, job := streamJob(t)
			c := testConfig(t)
			g, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			g.spool = s
			api := &fakeAPI{}
			g.api = api
			delivery := Delivery{ID: "mapped", JobID: job.ID, Channel: "telegram_bot", ChatID: 11, DeliveryTargetID: job.DeliveryTargetID, Text: "private reply"}
			switch kind {
			case "disabled":
				g.config.Users[0].Enabled = false
			case "policy":
				g.config.Users[0].PolicyVersion = "policy-2"
			case "runtime":
				g.config.Users[0].RuntimeID = "different"
			case "target":
				delivery.DeliveryTargetID = "telegram-22"
				delivery.ChatID = 22
			}
			if err = s.EnqueueDelivery(delivery); err != nil {
				t.Fatal(err)
			}
			g.deliverOne(context.Background())
			want := 0
			if kind == "allowed" {
				want = 1
			}
			if len(api.sent) != want {
				t.Fatalf("%s: sends=%v", kind, api.sent)
			}
		})
	}
}

func TestM10UsageLegacyAndCompleteUnterminatedRecord(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := usageEntry{ScopeID: "user:alice", JobID: "legacy", RunID: "old", InputTokens: int64ptr(999), OutputTokens: int64ptr(99)}
	body, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(usagePath(s.root), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordUsage(testJob("new", "user:alice"), "new", hubruntime.SessionUsage{SessionFound: true, SessionID: "one", InputTokens: int64ptr(3), OutputTokens: int64ptr(1)}); err != nil {
		t.Fatal(err)
	}
	totals, err := s.UsageTotals("user:alice")
	if err != nil || totals.LegacyJobs != 1 || totals.InputTokens != 3 || totals.Jobs != 2 {
		t.Fatalf("legacy double counted: %+v %v", totals, err)
	}
	if err = os.WriteFile(usagePath(s.root), []byte(strings.Repeat("x", 64*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordUsage(testJob("third", "user:alice"), "third", hubruntime.SessionUsage{}); err == nil {
		t.Fatal("oversized corrupt record accepted")
	}
}

func TestM10ProactiveCorruptionAndRevokedScript(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	o.JobKind = "script"
	o.Job.Text = `emit("ok")`
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	revoked := false
	authorize := func(Job) error {
		if revoked {
			return errors.New("revoked")
		}
		return nil
	}
	calls := 0
	if err := s.RunDueScripts(context.Background(), o.DueAt, authorize, func(context.Context, RoutineOccurrence) ([]string, error) {
		calls++
		revoked = true
		return []string{"private"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil || calls != 1 {
		t.Fatalf("revoked script delivered=%+v %v calls=%d", d, err, calls)
	}
	o.ScheduleID = "never-run"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.RunDueScripts(context.Background(), o.DueAt, authorize, func(context.Context, RoutineOccurrence) ([]string, error) {
		t.Error("revoked script ran")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "occurrences", "corrupt.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.processProactive(context.Background(), "probing", func(RoutineOccurrence) { t.Error("corrupt work executed") }); err == nil {
		t.Fatal("corrupt occurrence skipped silently")
	}
	if err := os.RemoveAll(filepath.Join(s.root, "occurrences")); err != nil {
		t.Fatal(err)
	}
	if err := s.processProactive(context.Background(), "probing", func(RoutineOccurrence) {}); err == nil {
		t.Fatal("missing queue treated as empty success")
	}
}

func TestM10ScriptReceiptSurvivesFailedHandoff(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	o.JobKind = "script"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(s.root, "outbox", "pending")
	saved := pending + ".saved"
	if err := os.Rename(pending, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	run := func(context.Context, RoutineOccurrence) ([]string, error) {
		calls++
		return []string{"saved output"}, nil
	}
	if err := s.RunDueScripts(context.Background(), o.DueAt, g.authorizeOccurrence, run); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, pending); err != nil {
		t.Fatal(err)
	}
	if err := s.RunDueScripts(context.Background(), o.DueAt, g.authorizeOccurrence, run); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || d.Text != "saved output" || calls != 1 {
		t.Fatalf("delivery=%+v calls=%d err=%v", d, calls, err)
	}
}

func TestM10ScriptCorruptReceiptNeverReexecutes(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	o.JobKind = "script"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".script-output"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := s.RunDueScripts(context.Background(), o.DueAt, g.authorizeOccurrence, func(context.Context, RoutineOccurrence) ([]string, error) { calls++; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d != nil || calls != 0 {
		t.Fatalf("corrupt receipt reexecuted or delivered: %+v %d %v", d, calls, err)
	}
}

func TestM10ProactiveCorruptionAndAdmissionBound(t *testing.T) {
	s, _, _ := occurrenceFixture(t)
	root := filepath.Join(s.root, "occurrences")
	if err := os.WriteFile(filepath.Join(root, "broken.json"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.processProactive(context.Background(), "probing", func(RoutineOccurrence) { t.Error("corrupt spool executed") }); err == nil {
		t.Fatal("corruption ignored")
	}
	if err := os.Remove(filepath.Join(root, "broken.json")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		o := RoutineOccurrence{ScheduleID: fmt.Sprintf("watch-%02d", i), State: "probing", DueAt: time.Unix(int64(i), 0)}
		if err := atomicJSON(filepath.Join(root, fmt.Sprintf("%02d.json", i)), o); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	if err := s.processProactive(context.Background(), "probing", func(o RoutineOccurrence) {
		calls.Add(1)
		if o.DueAt.Unix() >= 16 {
			t.Error("newer work displaced oldest work")
		}
	}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 16 || len(s.proactiveFlight) != 0 {
		t.Fatalf("admission=%d flights=%v", calls.Load(), s.proactiveFlight)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := s.processProactive(context.Background(), "probing", func(RoutineOccurrence) { t.Error("missing spool executed") }); err == nil {
		t.Fatal("missing spool ignored")
	}
}

func TestM10ScriptSyntaxAdmissionRejectsEmptyAndOversized(t *testing.T) {
	for _, src := range []string{"", strings.Repeat("x", scriptMaxSource+1), "emit("} {
		if ScriptSyntaxError(src) == nil {
			t.Fatal("invalid script accepted")
		}
	}
	if err := ScriptSyntaxError(`emit("ok")`); err != nil {
		t.Fatal(err)
	}
}

func TestM10RoutineHTTPRejectsForeignSnoozeAndCorruptState(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	schedule := m10Watch(t, s, o)
	g.spool = s
	g.config.ControlAuth = "token"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	call := func(method, target, body, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+auth)
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}
	for _, bad := range []struct {
		method, target, body, auth string
		status                     int
	}{
		{"GET", "/v1/routines/absent", "", "wrong", 401},
		{"POST", "/v1/routines/" + schedule.ScheduleID + "/snooze", "{", "token", 400},
		{"POST", "/v1/routines/" + schedule.ScheduleID + "/snooze", `{"until":"` + g.now().Add(-time.Hour).UTC().Format(time.RFC3339) + `"}`, "token", 400},
		{"POST", "/v1/routines/absent/snooze", `{"until":"2099-01-01T00:00:00Z"}`, "token", 403},
		{"DELETE", "/v1/routines/absent", "", "token", 403},
		{"PATCH", "/v1/routines/" + schedule.ScheduleID, "", "token", 405},
	} {
		if got := call(bad.method, bad.target, bad.body, bad.auth); got.Code != bad.status {
			t.Fatalf("%s %s =%d %s want=%d", bad.method, bad.target, got.Code, got.Body.String(), bad.status)
		}
	}
	if err := os.WriteFile(s.watchPath(schedule.ScheduleID), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := call("GET", "/v1/routines/"+schedule.ScheduleID, "", "token"); got.Code != 500 {
		t.Fatalf("corrupt baseline exposed=%d", got.Code)
	}
	if err := os.WriteFile(s.schedulePath(schedule.ScheduleID), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := call("GET", "/v1/routines/"+schedule.ScheduleID, "", "token"); got.Code != 500 {
		t.Fatalf("corrupt schedule exposed=%d", got.Code)
	}
	if got := call("GET", "/v1/routines", "", "token"); got.Code != 500 {
		t.Fatalf("corrupt schedule list hidden=%d", got.Code)
	}
}

func TestM10ProbeMissingCapturePreservesBaseline(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	schedule := m10Watch(t, s, o)
	schedule.Probe.Regex = `state=(open|closed)`
	if err := atomicJSON(s.schedulePath(schedule.ScheduleID), schedule); err != nil {
		t.Fatal(err)
	}
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, g.authorizeOccurrence, func(context.Context, string) ([]byte, error) { return []byte("login required"), nil }, nil, nil); err != nil {
		t.Fatal(err)
	}
	st, err := s.loadWatchStateLocked(o.ScheduleID)
	if err != nil || st.Value != "old" || st.LastError == "" {
		t.Fatalf("failed capture replaced baseline: %+v %v", st, err)
	}
	// The failed capture alerts the owner once without touching the baseline.
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "probe failed") {
		t.Fatalf("failed capture must alert the owner once: %+v %v", d, err)
	}
	if _, err := probeValue(nil, nil); err == nil {
		t.Fatal("nil probe accepted")
	}
	if len([]rune(boundedWatchText(strings.Repeat("я", 1500)))) != 1025 {
		t.Fatal("long notification was not bounded")
	}
}

func TestM10ProactiveNeverRunsTwoOccurrencesOfOneScheduleInOnePass(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	o.State = "probing"
	for i := 0; i < 3; i++ {
		o.DueAt = time.Unix(int64(i), 0)
		if err := atomicJSON(filepath.Join(s.root, "occurrences", fmt.Sprintf("%d.json", i)), o); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	if err := s.processProactive(context.Background(), "probing", func(RoutineOccurrence) { calls++ }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("same schedule admitted %d times", calls)
	}
}

func TestM10ControlTokenRevocationAndOwnerDeliveryIdentity(t *testing.T) {
	_, _, g := occurrenceFixture(t)
	g.config.ControlAuth = "operator"
	g.config.ControlTokensFile = filepath.Join(t.TempDir(), "enrollment.json")
	body, err := json.Marshal(map[string]identity.Envelope{"enrolled": identity.TelegramEnvelope("bob", 22, "bob", "policy-1")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.config.ControlTokensFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	g.config.ControlAuth = "enrolled"
	g.config.Users = []User{{ID: "alice", Enabled: true}}
	req := httptest.NewRequest("GET", "/v1/tasks", nil)
	req.Header.Set("Authorization", "Bearer enrolled")
	req.Header.Set("X-Hub-Principal", "alice")
	if _, ok := g.authorizeControl(req); ok {
		t.Fatal("owner without delivery identity was accepted")
	}
}

func TestM10CorruptStorageStopsUsageAndProbeHandoff(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	if err := os.Mkdir(usagePath(s.root), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UsageTotals("user:alice"); err == nil {
		t.Fatal("unreadable journal reported as zero")
	}
	if err := s.RecordUsage(o.Job, "run", hubruntime.SessionUsage{}); err == nil {
		t.Fatal("unreadable journal replaced")
	}
	m10Watch(t, s, o)
	if err := os.WriteFile(s.watchPath(o.ScheduleID), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	o.State = "probing"
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	result := probeResult{Occurrence: o, Watch: WatchState{ScheduleID: o.ScheduleID, Generation: 1}}
	s.mu.Lock()
	err := s.applyProbeResultLocked(result)
	s.mu.Unlock()
	if err == nil {
		t.Fatal("corrupt baseline overwritten by handoff")
	}
	if err := s.DeleteSchedule(o.ScheduleID, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err = s.applyProbeResultLocked(result)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var cancelled RoutineOccurrence
	if err := json.Unmarshal(b, &cancelled); err != nil || cancelled.State != "cancelled" {
		t.Fatalf("stale result=%s %v", b, err)
	}
}
