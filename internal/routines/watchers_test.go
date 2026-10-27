package routines

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

type watcherTestDeps struct {
	mu         *sync.Mutex
	jobs       []commodel.Job
	deliveries []commodel.Delivery
	mappings   []string
}

func newWatcherTest(t *testing.T) (*Engine, *watcherTestDeps, string) {
	root := t.TempDir()
	for _, dir := range []string{"occurrences", "schedules", "watchers", "cancelled"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	d := &watcherTestDeps{mu: &sync.Mutex{}}
	e := NewEngine(root, d.mu, Deps{
		WriteJSONAtomic: func(path string, value any) error {
			b, err := json.Marshal(value)
			if err != nil {
				return err
			}
			return os.WriteFile(path, b, 0600)
		},
		ChannelFor: func(string) OccurrenceChannel { return stubChannel{} },
		LoadMapping: func(id string) (commodel.JobMapping, error) {
			d.mappings = append(d.mappings, id)
			return commodel.JobMapping{}, os.ErrNotExist
		},
		EnqueueJob: func(j commodel.Job) (bool, error) {
			d.jobs = append(d.jobs, j)
			return true, nil
		},
		EnqueueDelivery: func(dv commodel.Delivery) error {
			d.deliveries = append(d.deliveries, dv)
			return nil
		},
		NormalizeDelivery: func(dv commodel.Delivery) (commodel.Delivery, error) { return dv, nil },
		Signal:            func() {},
	})
	return e, d, root
}

func watchOccurrence(scheduleID, kind string, rev uint64, env identity.Envelope, due time.Time) commodel.RoutineOccurrence {
	o := commodel.RoutineOccurrence{
		ScheduleID: scheduleID,
		Revision:   rev,
		DueAt:      due,
		JobKind:    kind,
		Job: commodel.Job{
			Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice",
			OrganizationID: "personal", Channel: "telegram_bot", ChatID: 11,
			Text: "watch the source",
		},
	}
	o.Job.ID = OccurrenceID(o)
	o.Job.IdempotencyKey = o.Job.ID
	return o
}

func putOccurrenceState(t *testing.T, e *Engine, root string, o commodel.RoutineOccurrence, state string) {
	t.Helper()
	o.State = state
	if err := e.atomicJSON(filepath.Join(root, "occurrences", OccurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}
}

func readOccurrence(t *testing.T, root string, o commodel.RoutineOccurrence) commodel.RoutineOccurrence {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "occurrences", OccurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var live commodel.RoutineOccurrence
	if err := json.Unmarshal(body, &live); err != nil {
		t.Fatal(err)
	}
	return live
}

// A dead probe must surface to the owner: the first distinct failure
// delivers one alert, repeats of the same normalized error stay silent and a
// later success re-arms the alert for the next failure episode.
func TestProbeFailureNotifiesOncePerError(t *testing.T) {
	e, d, root := newWatcherTest(t)
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	item := commodel.Schedule{ScheduleID: "w-fail", Revision: 1, Envelope: env, Channel: "telegram_bot", Input: "watch dzen", Timezone: "UTC"}
	if err := e.atomicJSON(e.SchedulePath("w-fail"), item); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 21, 30, 0, 0, time.UTC)
	settle := func(value string, probeErr error, minute int) commodel.RoutineOccurrence {
		o := watchOccurrence("w-fail", "watcher", 1, env, now.Add(time.Duration(minute)*time.Minute))
		putOccurrenceState(t, e, root, o, "probing")
		e.SettleProbe(o, item, value, WatchHash(value), now.Add(time.Duration(minute)*time.Minute), probeErr, nil)
		return o
	}
	live := settle("", errors.New("probe status 302"), 0)
	if got := readOccurrence(t, root, live).State; got != "failed" {
		t.Fatalf("occurrence state %q", got)
	}
	if len(d.deliveries) != 1 || !strings.Contains(d.deliveries[0].Text, "probe status 302") {
		t.Fatalf("expected one failure alert, got %+v", d.deliveries)
	}
	settle("", errors.New("probe status 302"), 1)
	if len(d.deliveries) != 1 {
		t.Fatalf("repeated same error must stay silent, got %d deliveries", len(d.deliveries))
	}
	settle("ok", nil, 2)
	st, err := e.LoadWatchStateLocked("w-fail")
	if err != nil || st.LastError != "" {
		t.Fatalf("success must clear last_error, got %q err=%v", st.LastError, err)
	}
	settle("", errors.New("probe status 302"), 3)
	if len(d.deliveries) != 2 {
		t.Fatalf("failure after recovery must notify again, got %d deliveries", len(d.deliveries))
	}
}

// An empty extracted value is a broken probe, not a baseline: reddit-style
// empty bodies or vacuous regexes must never seed sha256("").
func TestProbeEmptyValueFails(t *testing.T) {
	e, _, root := newWatcherTest(t)
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	item := commodel.Schedule{ScheduleID: "w-empty", Revision: 1, Envelope: env, Channel: "telegram_bot", Input: "watch reddit", Timezone: "UTC"}
	now := time.Date(2026, 10, 18, 21, 30, 0, 0, time.UTC)
	o := watchOccurrence("w-empty", "watcher", 1, env, now)
	putOccurrenceState(t, e, root, o, "probing")
	e.SettleProbe(o, item, "", WatchHash(""), now, nil, nil)
	if got := readOccurrence(t, root, o).State; got != "failed" {
		t.Fatalf("empty value must fail the occurrence, got %q", got)
	}
	st, err := e.LoadWatchStateLocked("w-empty")
	if err != nil {
		t.Fatal(err)
	}
	if st.Baseline != "" || !strings.Contains(st.LastError, "empty") {
		t.Fatalf("empty value seeded state %+v", st)
	}
}

// notify_mode=agent cannot be deferred like a delivery, so a quiet window
// suppresses the wake entirely and marks the occurrence instead of spawning
// a night-time model run.
func TestAgentEscalationRespectsQuiet(t *testing.T) {
	e, d, root := newWatcherTest(t)
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	quiet := commodel.Schedule{ScheduleID: "w-quiet", Revision: 1, Envelope: env, Channel: "telegram_bot", Input: "zen", Timezone: "UTC", NotifyMode: "agent", QuietFrom: "00:00", QuietTo: "23:59", Probe: &commodel.WatchProbe{URL: "https://example.com"}}
	loud := commodel.Schedule{ScheduleID: "w-loud", Revision: 1, Envelope: env, Channel: "telegram_bot", Input: "zen", Timezone: "UTC", NotifyMode: "agent", Probe: &commodel.WatchProbe{URL: "https://example.com"}}
	now := time.Date(2026, 10, 18, 21, 30, 0, 0, time.UTC)
	for _, item := range []commodel.Schedule{quiet, loud} {
		if err := e.atomicJSON(e.WatchPath(item.ScheduleID), WatchState{ScheduleID: item.ScheduleID, Baseline: "old", Value: "old"}); err != nil {
			t.Fatal(err)
		}
		o := watchOccurrence(item.ScheduleID, "watcher", 1, env, now)
		putOccurrenceState(t, e, root, o, "probing")
		e.SettleProbe(o, item, "new value", WatchHash("new value"), now, nil, nil)
	}
	quietLive := readOccurrence(t, root, watchOccurrence("w-quiet", "watcher", 1, env, now))
	if quietLive.State != "capped" {
		t.Fatalf("quiet escalation must be capped, got %q", quietLive.State)
	}
	if len(d.jobs) != 1 || !strings.HasPrefix(d.jobs[0].Text, "[untrusted watcher evidence]") {
		t.Fatalf("loud schedule must enqueue exactly one evidence job, got %+v", d.jobs)
	}
	if len(d.deliveries) != 0 {
		t.Fatalf("agent mode must not fall back to delivery, got %+v", d.deliveries)
	}
}

// The job-binding drift guard exists for occurrences that admit jobs through
// DispatchDueOccurrences. Probe kinds never do — their only mapping is the
// escalation product, a deliberately rewritten job that can never match.
func TestDispatchSkipsProbeKindBinding(t *testing.T) {
	e, d, root := newWatcherTest(t)
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	now := time.Date(2026, 10, 18, 21, 30, 0, 0, time.UTC)
	putOccurrenceState(t, e, root, watchOccurrence("w-settled", "watcher", 1, env, now.Add(-time.Minute)), "dispatched")
	agent := watchOccurrence("agent-one", "agent", 1, env, now.Add(-time.Minute))
	putOccurrenceState(t, e, root, agent, "pending")
	if err := e.DispatchDueOccurrences(now, func(commodel.Job) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(d.mappings) != 1 || d.mappings[0] != agent.Job.ID {
		t.Fatalf("binding check must reach only job-admitting kinds, got %v", d.mappings)
	}
	if len(d.jobs) != 1 {
		t.Fatalf("agent occurrence not admitted: %v", d.jobs)
	}
}

// A rejected probe script or regex must carry the compiler's reason — the
// agent only sees the HTTP error body and cannot fix what it cannot read.
func TestValidateProbeErrorDetail(t *testing.T) {
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	hooks := DraftHooks{
		Channel: func(commodel.Schedule) error { return nil },
		Script:  ScriptSyntaxError,
	}
	in := commodel.Schedule{
		ScheduleID: "w-script", Revision: 1, Envelope: caller, UserID: "alice",
		ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal",
		Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "every:5m",
		Input: "watch it", JobKind: "watcher",
		Probe: &commodel.WatchProbe{Kind: "script", URL: "https://example.com", Script: "emit(1"},
	}
	err := ValidateScheduleDraft(in, caller, hooks)
	if err == nil || !strings.Contains(err.Error(), "does not compile:") {
		t.Fatalf("script rejection must carry the compiler reason, got %v", err)
	}
	in.Probe = &commodel.WatchProbe{Kind: "http", URL: "https://example.com", Regex: "(unclosed"}
	err = ValidateScheduleDraft(in, caller, hooks)
	if err == nil || !strings.Contains(err.Error(), "does not compile:") {
		t.Fatalf("regex rejection must carry the compiler reason, got %v", err)
	}
}

// TestHookProbeValidates (SPEC-0081 R5.1): a hook probe names its intake
// label as hook:<name> — never a fetchable URL — and requires event kind.
func TestHookProbeValidates(t *testing.T) {
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	hooks := DraftHooks{
		Channel: func(commodel.Schedule) error { return nil },
		Script:  ScriptSyntaxError,
	}
	in := commodel.Schedule{
		ScheduleID: "w-hook", Revision: 1, Envelope: caller, UserID: "alice",
		ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal",
		Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "every:5m",
		Input: "watch hooks", JobKind: "event",
		Probe: &commodel.WatchProbe{Kind: "hook", URL: "hook:github"},
	}
	if err := ValidateScheduleDraft(in, caller, hooks); err != nil {
		t.Fatalf("hook probe must validate: %v", err)
	}
	in.Probe.URL = "https://example.com"
	if err := ValidateScheduleDraft(in, caller, hooks); err == nil {
		t.Fatal("hook probe must reject a fetchable URL")
	}
}
