package automation

// Event-plane assertions ported from the retired communication-owned
// stream/event machinery (internal/communication/streams_test.go pre-M32).
// The new owner is tick-driven: streams materialize occurrences under the
// host clock, acquisition fans out through eventStore.acquire, settlement
// checkpoints a durable batch in eventStore.settle, and delivery intents
// ride the host outbox instead of a gateway outbox. Every test below keeps
// its original invariant, expressed against the real owner seams.
//
// Retired with the loop architecture (no 1:1 seam — invariants now covered
// by the owner's own suite or by design change):
//   - per-schedule poll goroutines/backoff/supervisor respawn
//     (TestStreamPollLifecycle, TestStreamPollBackoff,
//     TestStreamSupervisorReconcile/LateSubscription/Respawns,
//     TestStartStreamLoops): the host tick replaces long-running loops;
//     pacing is MinIntervalSeconds against durable LastPollAt — covered by
//     TestStreamPollPacesByMinInterval below and TestStreamPauseAndCursorGap.
//   - free pacing helpers (streamMinInterval/streamBackoff/
//     streamRoundDelay/streamCallBudget/streamProbe* tests): wait bounds are
//     connector-declared args now; SampleWatch still clamps them, covered by
//     TestWatchSampleClampsWaitBoundsToBudget below.
//   - TestStreamToolCallInjectsBound and
//     TestStreamCursorReadCoalescesWithinOwnerOnly: the gateway HTTP forwarder
//     is gone; the host calls cfg.ToolCall in-process per owner tick.
// Administrative epochs now bind in the ToolHub owner before occurrence
// persistence; admission_epoch_test.go covers resumed and pending work.

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
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

// eventFixture exposes the owner's event plane at the seam the retired
// tests drove: a real host, its per-principal engine, and the durable
// eventStore — all production code.
type eventFixture struct {
	h    *harness
	host *Host
	eng  *routines.Engine
	env  identity.Envelope
}

func newEventFixture(t *testing.T) *eventFixture {
	t.Helper()
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")
	host := h.host(t)
	eng, err := host.engineFor("alice")
	if err != nil {
		t.Fatal(err)
	}
	return &eventFixture{h: h, host: host, eng: eng, env: alice}
}

func (f *eventFixture) create(t *testing.T, sc commodel.Schedule) commodel.Schedule {
	t.Helper()
	sc.Envelope = f.env
	sc.UserID, sc.ActorID = f.env.PrincipalID, f.env.PrincipalID
	sc.ScopeID = "user:" + f.env.PrincipalID
	sc.Channel, sc.ChatID, sc.Timezone = "telegram_bot", 11, "UTC"
	item, err := f.host.CreateSchedule(context.Background(), f.env, sc, "")
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// probing persists an occurrence in the state the probe pipeline owns —
// the dispatcher admitted it and probing is the in-flight phase — and
// returns it with the same state so callers can settle against it.
func (f *eventFixture) probing(t *testing.T, o commodel.RoutineOccurrence) commodel.RoutineOccurrence {
	t.Helper()
	o.State = "probing"
	// PutOccurrence's own binding: the durable record is keyed by the
	// occurrence id the result/recovery paths look up by job id.
	o.Job.ID = routines.OccurrenceID(o)
	o.Job.IdempotencyKey = o.Job.ID
	o.Job.CreatedAt = o.DueAt
	path := filepath.Join(f.h.root, "users", "alice", "occurrences", routines.OccurrenceID(o)+".json")
	if err := writeJSONAtomic(path, o); err != nil {
		t.Fatal(err)
	}
	return o
}

// settle is the old s.settleEvents: settle a probe result through the real
// event plane (checkpoint, filter, durable batch, fanout).
func (f *eventFixture) settle(o commodel.RoutineOccurrence, sc commodel.Schedule, body []byte, now time.Time, probeErr error, auth func(commodel.Job) error) {
	_, _ = f.host.events.settle(f.eng, o, sc, body, now, probeErr, auth)
}

// pollOnce is the old g.streamPollOnce: materialize the stream occurrence
// under the host clock, then run the probe pass with the injected tool call.
func (f *eventFixture) pollOnce(sc commodel.Schedule, toolCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error)) error {
	now := f.h.now
	f.host.cfg.ToolCall = toolCall
	if err := f.host.tickStreams(context.Background(), f.eng, "alice", now); err != nil {
		return err
	}
	if err := f.eng.DispatchDueOccurrences(now.Add(time.Second), allowJob); err != nil {
		return err
	}
	return f.eng.ProbeDueOccurrencesContext(context.Background(), now.Add(time.Second), allowJob, nil, toolCall, nil)
}

func (f *eventFixture) state(t *testing.T, scheduleID string) eventState {
	t.Helper()
	o := commodel.RoutineOccurrence{ScheduleID: scheduleID, Job: commodel.Job{Envelope: f.env}}
	st, _, err := f.host.events.load(o)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (f *eventFixture) pendingOutbox(t *testing.T) []outboxRecord {
	t.Helper()
	recs, err := f.host.outbox.pending()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// streamSchedule is the old Schedule literal shape the tests authored.
func (f *eventFixture) streamSchedule(id, input, sink string, probe *commodel.WatchProbe) commodel.Schedule {
	return commodel.Schedule{ScheduleID: id, Input: input, Expression: "* * * * *", JobKind: "event", Delivery: "stream", Sink: sink, Probe: probe, Enabled: true}
}

func allowJob(commodel.Job) error { return nil }

// S6.1 — delivery=stream is an event-only mcp_tool mode; poll is the
// default and other kinds reject it outright.
func TestStreamDeliveryValidation(t *testing.T) {
	f := newEventFixture(t)
	base := commodel.Schedule{ScheduleID: "sd", Envelope: f.env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 * * * *", Input: "x"}

	bad := base
	bad.ScheduleID = "sd1"
	bad.JobKind = "event"
	bad.Delivery = "carrier"
	bad.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-abc"}
	if _, err := f.host.CreateSchedule(context.Background(), f.env, bad, ""); err == nil {
		t.Fatal("unknown delivery value accepted")
	}

	nonEvent := base
	nonEvent.ScheduleID = "sd2"
	nonEvent.JobKind = "watcher"
	nonEvent.Delivery = "stream"
	nonEvent.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-abc"}
	if _, err := f.host.CreateSchedule(context.Background(), f.env, nonEvent, ""); err == nil {
		t.Fatal("stream accepted on a non-event schedule")
	}

	httpProbe := base
	httpProbe.ScheduleID = "sd3"
	httpProbe.JobKind = "event"
	httpProbe.Delivery = "stream"
	httpProbe.Probe = &commodel.WatchProbe{Kind: "http", URL: "https://example.com/feed"}
	if _, err := f.host.CreateSchedule(context.Background(), f.env, httpProbe, ""); err == nil {
		t.Fatal("stream accepted an http probe — it needs a wait-capable tool")
	}

	good := base
	good.ScheduleID = "sd4"
	good.JobKind = "event"
	good.Delivery = "stream"
	good.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait_for_settled-abc", Args: map[string]any{"chat_id": "@dan"}}
	if _, err := f.host.CreateSchedule(context.Background(), f.env, good, ""); err != nil {
		t.Fatalf("stream event schedule rejected: %v", err)
	}
}

// Stream schedules are materialized by the stream tick (wait-loop
// subscription), poll event schedules by the cron materializer — both as
// durable occurrences under the owner root.
func TestStreamAndPollSchedulesMaterialize(t *testing.T) {
	f := newEventFixture(t)
	f.create(t, commodel.Schedule{ScheduleID: "st1", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}})
	f.create(t, commodel.Schedule{ScheduleID: "pl1", Expression: "* * * * *", Input: "poll dms", JobKind: "event", Probe: &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}})
	f.h.now = time.Now().UTC().Add(2 * time.Minute)
	if err := f.host.Tick(context.Background(), f.h.now); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(f.h.root, "users", "alice", "occurrences"))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, entry := range entries {
		var o commodel.RoutineOccurrence
		raw, _ := os.ReadFile(filepath.Join(f.h.root, "users", "alice", "occurrences", entry.Name()))
		if json.Unmarshal(raw, &o) == nil {
			ids[o.ScheduleID] = true
		}
	}
	if !ids["st1"] || !ids["pl1"] {
		t.Fatalf("both stream and poll schedules must materialize occurrences: %v", ids)
	}
}

// S6.2 — connector wait tools return an explicit no-event sentinel on
// timeout; it must not be mistaken for an event payload.
func TestExtractEventsNoEventSentinel(t *testing.T) {
	events, err := routines.ExtractEvents(&commodel.WatchProbe{Kind: "mcp_tool"}, []byte(`{"event": false, "reason": "timeout", "waiting_for": 12345}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("timeout sentinel produced events: %+v", events)
	}
}

// S6.2 — a settled-burst object is one event whose text keeps the routing
// fields (chat_id for the agent to answer into), and whose requeue args use
// the connector's chat_id contract rather than a hub event id.
func TestExtractEventsBurstRequeueAndText(t *testing.T) {
	body := `{"event": true, "chat_id": 777, "name": "Danil", "username": "dan", "message_count": 3, "first_message_id": 10, "last_message_id": 12, "kind": "dm", "sender_id": 42}`
	events, err := routines.ExtractEvents(&commodel.WatchProbe{Kind: "mcp_tool"}, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("burst did not extract as one event: %+v", events)
	}
	ev := events[0]
	if ev.Requeue["chat_id"] != float64(777) {
		t.Fatalf("requeue args missing chat_id: %+v", ev.Requeue)
	}
	if !strings.Contains(ev.Text, "777") || !strings.Contains(ev.Text, "Danil") {
		t.Fatalf("burst text dropped routing fields: %q", ev.Text)
	}
}

// A text-carrying event still exposes its connector fields to the filter:
// Raw keeps the compacted item JSON (chat_id here) even though Text holds
// only the message body — otherwise source-scope filters are unwritable.
func TestExtractEventsRawKeepsConnectorFields(t *testing.T) {
	body := `{"id": "m1", "text": "hello github.com/x", "chat_id": -1001234, "sender_id": 42}`
	events, err := routines.ExtractEvents(&commodel.WatchProbe{Kind: "mcp_tool"}, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want one event, got %+v", events)
	}
	ev := events[0]
	if ev.Text != "hello github.com/x" {
		t.Fatalf("text: %q", ev.Text)
	}
	if !strings.Contains(ev.Raw, `"chat_id":-1001234`) || !strings.Contains(ev.Raw, `"sender_id":42`) {
		t.Fatalf("raw dropped connector fields: %q", ev.Raw)
	}
}

// S6.3 — a stream batch fans out to the owner outbox as a durable delivery
// intent, and an identical re-poll is fully deduped; flushing reaches the
// communication intake exactly once.
func TestSettleStreamEventsNotify(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str1", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	now := time.Now().UTC()
	body := []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 2, "first_message_id": 10, "last_message_id": 11, "kind": "dm"}`)
	o := streamOccurrence(sc, now)
	o = f.probing(t, o)
	f.settle(o, sc, body, now, nil, allowJob)
	recs := f.pendingOutbox(t)
	if len(recs) != 1 || !strings.Contains(recs[0].Delivery.Text, "Danil") || !strings.HasPrefix(recs[0].Delivery.Text, "watch dms") {
		t.Fatalf("stream delivery intent wrong: %+v", recs)
	}
	if err := f.host.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.h.comm.count() != 1 {
		t.Fatalf("stream batch never reached communication: %d", f.h.comm.count())
	}
	// Same batch replayed: deduped by the durable seen-set.
	o2 := streamOccurrence(sc, now.Add(time.Second))
	o2 = f.probing(t, o2)
	f.settle(o2, sc, body, now.Add(time.Second), nil, allowJob)
	if err := f.host.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.h.comm.count() != 1 {
		t.Fatal("replayed stream batch delivered again")
	}
}

// S6.3 — sink=agent on a stream wakes the agent once per batch with the
// untrusted-evidence flag and the burst's routing fields intact.
func TestSettleStreamEventsAgentSink(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str2", "answer Danil for me", "agent", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	now := time.Now().UTC()
	body := []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 2, "first_message_id": 10, "last_message_id": 11, "kind": "dm"}`)
	o := streamOccurrence(sc, now)
	o = f.probing(t, o)
	f.settle(o, sc, body, now, nil, allowJob)
	if err := f.host.reconcile(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	job := f.h.sup.onlyJob()
	if job.ID == "" {
		t.Fatal("agent stream batch admitted no job")
	}
	if !strings.Contains(job.Text, "untrusted") || !strings.Contains(job.Text, "777") || !strings.Contains(job.Text, "answer Danil for me") {
		t.Fatalf("stream agent bundle: %q", job.Text)
	}
	intents, err := f.host.queue.completed()
	if err != nil || len(intents) != 1 || !intents[0].Job.Probe {
		t.Fatal("probe-settled job lost its probe stamp — memory capture would mislabel it routine")
	}
	if len(f.pendingOutbox(t)) != 0 {
		t.Fatal("agent stream produced a plain delivery")
	}
	// Replay dedupes the whole batch (same deterministic job id).
	o2 := streamOccurrence(sc, now.Add(time.Second))
	o2 = f.probing(t, o2)
	f.settle(o2, sc, body, now.Add(time.Second), nil, allowJob)
	if err := f.host.reconcile(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, logical := f.h.sup.counts(); logical != 1 {
		t.Fatal("replayed stream batch woke the agent again")
	}
}

// The stream path feeds the same raw JSON to the filter as the poll path:
// input() carries connector fields, not only the extracted text.
func TestStreamFilterInputPrefersRawJSON(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-raw2", "watch groups", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("drop" if "-1001" not in input() else "keep")`}))
	var gotInput string
	f.host.events.filter = func(ctx context.Context, o commodel.RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
		gotInput = input
		return "keep", nil
	}
	body := []byte(`{"event": true, "id": "e9", "text": "ping", "chat_id": -1001234, "sender_id": 7}`)
	o := streamOccurrence(sc, time.Now().UTC())
	o = f.probing(t, o)
	f.settle(o, sc, body, time.Now().UTC(), nil, allowJob)
	if !strings.Contains(gotInput, `"chat_id":-1001234`) {
		t.Fatalf("stream filter input lacks connector fields: %q", gotInput)
	}
}

// probe.sources fans one subscription out over several acquisition inputs:
// every source is probed inside its own bounded call, the batches merge
// through one dedup/filter/sink, and the whole window costs one agent
// wake — never one per source.
func TestStreamSourcesFanoutMergesOneWake(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-multi", "watch groups", "agent",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Args: map[string]any{"timeout": 20},
			Sources: []commodel.WatchProbeSource{
				{Name: "alpha", Args: map[string]any{"chat_id": float64(1)}},
				{Name: "beta", Args: map[string]any{"chat_id": float64(2)}},
			}}))
	var mu sync.Mutex
	called := map[int]bool{}
	toolCall := func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch probe.Args["chat_id"] {
		case float64(1):
			called[1] = true
			return []byte(`{"event": true, "id": "m1", "text": "alpha ping", "chat_id": 1, "next_cursor": "ca1"}`), nil
		case float64(2):
			called[2] = true
			return []byte(`{"event": true, "id": "m1", "text": "beta ping", "chat_id": 2, "next_cursor": "cb1"}`), nil
		}
		return []byte(`{"event": false}`), nil
	}
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatalf("healthy multi-source round reported blocked: %v", err)
	}
	if !called[1] || !called[2] {
		t.Fatalf("fan-out missed sources: %v", called)
	}
	if err := f.host.reconcile(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	job := f.h.sup.onlyJob()
	if job.ID == "" {
		t.Fatal("merged multi-source batch admitted no job")
	}
	// Both sources surface in one bundle: identical connector ids in
	// different chats must not dedup against each other, and the agent
	// sees which input each event came from.
	if !strings.Contains(job.Text, "alpha ping") || !strings.Contains(job.Text, "beta ping") {
		t.Fatalf("bundle dropped a source's event: %q", job.Text)
	}
	if !strings.Contains(job.Text, "[alpha]") || !strings.Contains(job.Text, "[beta]") {
		t.Fatalf("bundle lost source attribution: %q", job.Text)
	}
	if _, logical := f.h.sup.counts(); logical != 1 {
		t.Fatal("one batch produced more than one agent wake")
	}
	st := f.state(t, "str-multi")
	if st.SourceCursors["alpha"] != "ca1" || st.SourceCursors["beta"] != "cb1" {
		t.Fatalf("per-source cursors not persisted: %+v", st.SourceCursors)
	}
	// A replayed round dedupes per source — no second wake.
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatal(err)
	}
	if err := f.host.reconcile(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, logical := f.h.sup.counts(); logical != 1 {
		t.Fatal("replayed multi-source batch woke the agent again")
	}
}

func TestStreamCursorFeedsBackAndWaitsForOverflow(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("cursor-overflow", "watch messages", "notify",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-watch-chat-messages", CursorArg: "cursor", MinIntervalSeconds: 60}))
	calls := []string{}
	toolCall := func(_ context.Context, _ string, probe *commodel.WatchProbe) ([]byte, error) {
		cursor, _ := probe.Args["cursor"].(string)
		calls = append(calls, cursor)
		if cursor == "" {
			return []byte(`{"events":[],"next_cursor":"3"}`), nil
		}
		items := []map[string]any{}
		for i := 4; i <= 13; i++ {
			items = append(items, map[string]any{"id": i, "text": fmt.Sprintf("message %d", i)})
		}
		return json.Marshal(map[string]any{"events": items, "next_cursor": "13"})
	}
	// First poll seeds the high-water cursor without marking anything seen.
	o := streamOccurrence(sc, f.h.now)
	o = f.probing(t, o)
	body, err := f.host.events.acquire(context.Background(), o, sc, toolCall)
	if err != nil {
		t.Fatal(err)
	}
	f.settle(o, sc, body, f.h.now, nil, allowJob)
	st := f.state(t, sc.ScheduleID)
	if st.Cursor != "3" || len(st.Seen) != 0 {
		t.Fatalf("baseline must only store high watermark: %+v", st)
	}
	// Second poll overflows the eight-delivery batch cap: the cursor holds
	// until the backlog drains.
	o = streamOccurrence(sc, f.h.now.Add(time.Minute))
	o = f.probing(t, o)
	body, err = f.host.events.acquire(context.Background(), o, sc, toolCall)
	if err != nil {
		t.Fatal(err)
	}
	f.settle(o, sc, body, f.h.now.Add(time.Minute), nil, allowJob)
	st = f.state(t, sc.ScheduleID)
	if st.Cursor != "3" || len(st.Seen) != 8 {
		t.Fatalf("overflow advanced cursor or lost admitted events: %+v", st)
	}
	// Resume drains the rest under the same still-probing occurrence, then
	// the cursor advances.
	if _, err := f.host.events.resume(f.eng, o, sc, f.h.now.Add(2*time.Minute), allowJob); err != nil {
		t.Fatal(err)
	}
	st = f.state(t, sc.ScheduleID)
	if st.Cursor != "13" || len(st.Seen) != 10 {
		t.Fatalf("replay did not drain overflow: %+v", st)
	}
	if strings.Join(calls, ",") != ",3" {
		t.Fatalf("cursor feedback mismatch: %v", calls)
	}
}

// A source may narrow to a sibling read tool, and an unnamed source still
// gets a stable positional label so its cursor survives restarts.
func TestStreamSourceToolOverrideAndPositionalLabel(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-mixed", "watch mixed", "notify",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", MinIntervalSeconds: 60,
			Sources: []commodel.WatchProbeSource{
				{Args: map[string]any{"chat_id": float64(1)}},
				{Name: "rss", Tool: "hub-rss-read", Args: map[string]any{"feed": "f"}},
			}}))
	var mu sync.Mutex
	tools := map[string]bool{}
	toolCall := func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		tools[probe.Tool] = true
		if probe.Tool == "hub-rss-read" {
			return []byte(`{"event": true, "id": "r1", "text": "rss item", "next_cursor": "cr1"}`), nil
		}
		return []byte(`{"event": true, "id": "t1", "text": "tg item", "next_cursor": "ct1"}`), nil
	}
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatalf("round blocked: %v", err)
	}
	if !tools["hub-tg-wait-abc"] || !tools["hub-rss-read"] {
		t.Fatalf("source tool override not honored: %v", tools)
	}
	st := f.state(t, "str-mixed")
	if st.SourceCursors["rss"] != "cr1" {
		t.Fatalf("named source cursor missing: %+v", st.SourceCursors)
	}
	if st.SourceCursors["s0"] != "ct1" {
		t.Fatalf("unnamed source needs a stable label: %+v", st.SourceCursors)
	}
}

// One dead source must not starve healthy siblings: the round still
// settles the good batch, the failure is attributed to its own label in
// the durable state, and the loop stays unblocked.
func TestStreamSourcesPartialFailureIsolated(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-partial", "watch groups", "agent",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc",
			Sources: []commodel.WatchProbeSource{
				{Name: "alive", Args: map[string]any{"chat_id": float64(1)}},
				{Name: "dead", Args: map[string]any{"chat_id": float64(9)}},
			}}))
	toolCall := func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		if probe.Args["chat_id"] == float64(9) {
			return nil, errors.New("connector exploded")
		}
		return []byte(`{"event": true, "id": "m1", "text": "still here"}`), nil
	}
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatalf("partial failure must not block the round: %v", err)
	}
	if err := f.host.reconcile(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	job := f.h.sup.onlyJob()
	if job.ID == "" || !strings.Contains(job.Text, "still here") {
		t.Fatalf("healthy source's event lost to a sibling failure: %+v", job)
	}
	st := f.state(t, "str-partial")
	if !strings.Contains(st.SourceErrors["dead"], "failed") {
		t.Fatalf("source failure not attributed: %+v", st.SourceErrors)
	}
	// A fully failed round settles its occurrence failed — the durable
	// LastPollAt still paces the next acquisition round.
	dead := func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		return nil, errors.New("all down")
	}
	f.h.now = f.h.now.Add(2 * time.Minute)
	_ = f.pollOnce(sc, dead)
	entries, err := os.ReadDir(filepath.Join(f.h.root, "users", "alice", "occurrences"))
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, entry := range entries {
		var live commodel.RoutineOccurrence
		raw, _ := os.ReadFile(filepath.Join(f.h.root, "users", "alice", "occurrences", entry.Name()))
		if json.Unmarshal(raw, &live) == nil && live.State == "failed" {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("all-failed round must settle the occurrence failed")
	}
}

// watch_sample runs one bounded probe round and returns the raw event
// shape per source with candidate-filter verdicts — the agent inspects
// what a connector emits before committing keep|drop|urgent logic, and
// the sample never fans out or marks anything seen.
func TestWatchSampleReturnsRawEventsAndVerdicts(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-sample", "watch groups", "agent",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("keep")`,
			Sources: []commodel.WatchProbeSource{
				{Name: "alpha", Args: map[string]any{"chat_id": float64(1)}},
				{Name: "beta", Args: map[string]any{"chat_id": float64(2)}},
			}}))
	f.host.cfg.ToolCall = func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		if probe.Args["chat_id"] == float64(1) {
			return []byte(`{"event": true, "id": "m1", "text": "ping", "chat_id": -1001, "sender_id": 7}`), nil
		}
		return []byte(`{"event": false, "reason": "timeout"}`), nil
	}
	var mu sync.Mutex
	var filterInputSeen string
	filterSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/holds" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var req struct {
			Text  string `json:"text"`
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		filterInputSeen = req.Input
		mu.Unlock()
		_ = json.NewEncoder(w).Encode([]string{"keep"})
	}))
	defer filterSrv.Close()
	f.host.cfg.Supervisor = &HTTPSupervisorClient{URL: filterSrv.URL, Token: "control"}
	sample, err := f.host.SampleWatch(context.Background(), f.env, sc.ScheduleID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.Sources) != 2 {
		t.Fatalf("sample must report per source: %+v", sample.Sources)
	}
	bySource := map[string]sampleSource{}
	for _, src := range sample.Sources {
		bySource[src.Source] = src
	}
	alpha := bySource["alpha"]
	if len(alpha.Events) != 1 {
		t.Fatalf("alpha sample wrong: %+v", alpha)
	}
	if !strings.Contains(alpha.Events[0].Raw, `"chat_id":-1001`) {
		t.Fatalf("sample lost the raw event fields the filter needs: %+v", alpha.Events[0])
	}
	if alpha.Verdicts["m1"] != "keep" {
		t.Fatalf("candidate filter verdict missing: %+v", alpha.Verdicts)
	}
	mu.Lock()
	if !strings.Contains(filterInputSeen, `"chat_id"`) {
		t.Fatalf("filter ran on text, not raw JSON: %q", filterInputSeen)
	}
	mu.Unlock()
	if beta := bySource["beta"]; len(beta.Events) != 0 || beta.Error != "" {
		t.Fatalf("timeout source should report an empty event list, not an error: %+v", beta)
	}
	// Sampling never wakes the agent or marks events seen.
	if _, logical := f.h.sup.counts(); logical != 0 {
		t.Fatal("sample fanned out to the sink")
	}
	if len(f.pendingOutbox(t)) != 0 {
		t.Fatal("sample produced a delivery")
	}
	st := f.state(t, "str-sample")
	if len(st.Seen) != 0 {
		t.Fatal("sample marked events seen")
	}
	// A foreign caller must not probe someone else's connector — sampling
	// is owner-only like every other routine control.
	mallory := f.h.envelope(t, "mallory")
	if _, err := f.host.SampleWatch(context.Background(), mallory, sc.ScheduleID, ""); err == nil {
		t.Fatal("foreign caller sampled an owned watch")
	}
}

// The sample path shares its per-source call budget; owner wait bounds
// larger than the budget must be clamped before the toolCall is issued.
func TestWatchSampleClampsWaitBoundsToBudget(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-sample-clamp", "watch", "notify",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait_for_new_message-a", Args: map[string]any{"timeout": 50}}))
	var seen float64
	f.host.cfg.ToolCall = func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		switch v := probe.Args["timeout"].(type) {
		case float64:
			seen = v
		case int:
			seen = float64(v)
		}
		return []byte(`{"event": false, "reason": "timeout"}`), nil
	}
	if _, err := f.host.SampleWatch(context.Background(), f.env, sc.ScheduleID, ""); err != nil {
		t.Fatal(err)
	}
	if seen <= 0 || seen >= 15 {
		t.Fatalf("sample must clamp wait bounds under its budget, got %v", seen)
	}
}

// watch_sample's filter argument dry-runs a CANDIDATE gate over the same
// sample — the agent evaluates a new filter against real event shapes
// before committing it to the watch.
func TestWatchSampleRunsCandidateFilter(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-cand", "watch groups", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	f.host.cfg.ToolCall = func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"event": true, "id": "m1", "text": "ping", "chat_id": -1001}`), nil
	}
	var gotSrc string
	filterSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/holds" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotSrc = req.Text
		_ = json.NewEncoder(w).Encode([]string{"drop"})
	}))
	defer filterSrv.Close()
	f.host.cfg.Supervisor = &HTTPSupervisorClient{URL: filterSrv.URL, Token: "control"}
	sample, err := f.host.SampleWatch(context.Background(), f.env, sc.ScheduleID, `emit("drop" if "-9999" in input() else "keep")`)
	if err != nil {
		t.Fatal(err)
	}
	if gotSrc == "" {
		t.Fatal("candidate filter never ran")
	}
	if sample.Sources[0].Verdicts["m1"] != "drop" {
		t.Fatalf("candidate verdict lost: %+v", sample.Sources[0])
	}
}

// S6.3 — a connector failure on the stream poll settles the occurrence
// failed instead of emitting a fake "nothing new".
func TestSettleStreamEventsAuthDegraded(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str3", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	now := time.Now().UTC()
	o := streamOccurrence(sc, now)
	o = f.probing(t, o)
	f.settle(o, sc, nil, now, errors.New("tool error: 403 forbidden"), allowJob)
	raw, err := os.ReadFile(filepath.Join(f.h.root, "users", "alice", "occurrences", routines.OccurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var live commodel.RoutineOccurrence
	if json.Unmarshal(raw, &live) != nil || live.State != "failed" {
		t.Fatalf("auth failure not marked failed: %+v %v", live, err)
	}
}

// S6.3 — an owner whose authorization no longer holds fails closed on the
// stream path exactly like occurrences do: no delivery, occurrence dropped.
func TestSettleStreamEventsUnauthorized(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str4", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	deny := func(commodel.Job) error { return errors.New("routine owner unavailable") }
	body := []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 1}`)
	o := streamOccurrence(sc, time.Now().UTC())
	o = f.probing(t, o)
	f.settle(o, sc, body, time.Now().UTC(), nil, deny)
	if len(f.pendingOutbox(t)) != 0 {
		t.Fatal("unauthorized stream batch delivered")
	}
	raw, err := os.ReadFile(filepath.Join(f.h.root, "users", "alice", "occurrences", routines.OccurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var live commodel.RoutineOccurrence
	if json.Unmarshal(raw, &live) != nil || live.State != "cancelled" {
		t.Fatalf("authorization failure must cancel the occurrence: %+v %v", live, err)
	}
}

// S6.3 — one stream poll pass: the injected tool result lands as a durable
// delivery intent through the same settle path the tick uses.
func TestStreamPollOnceDelivers(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str5", "watch dms", "notify",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait_for_settled_message-abc", Args: map[string]any{"chat_id": "@dan", "max_wait_ms": 8000}}))
	toolCall := func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 2}`), nil
	}
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatal(err)
	}
	recs := f.pendingOutbox(t)
	if len(recs) != 1 || !strings.Contains(recs[0].Delivery.Text, "Danil") {
		t.Fatalf("stream poll admitted no delivery: %+v", recs)
	}
	// A timeout sentinel result: nothing lands, the poll still succeeds.
	f.h.now = f.h.now.Add(2 * time.Minute)
	toolCall = func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"event": false, "reason": "timeout"}`), nil
	}
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatal(err)
	}
	if len(f.pendingOutbox(t)) != 1 {
		t.Fatal("timeout sentinel delivered")
	}
}

// A poll tool on a stream honors its declared min_interval between rounds:
// a second tick inside the window must not re-acquire.
func TestStreamPollPacesByMinInterval(t *testing.T) {
	f := newEventFixture(t)
	f.create(t, f.streamSchedule("str-pace", "watch feed", "notify",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-feed-poll-abc", MinIntervalSeconds: 3600}))
	calls := 0
	f.host.cfg.ToolCall = func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		calls++
		return []byte(`{"events":[]}`), nil
	}
	if err := f.host.Tick(context.Background(), f.h.now); err != nil {
		t.Fatal(err)
	}
	if err := f.host.Tick(context.Background(), f.h.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("stream poll ignored the declared cadence: %d calls", calls)
	}
}

// A multi-event batch admits exactly one agent job carrying every event's
// text as raw UTF-8 bytes, and a replayed batch admits nothing more.
func TestStreamBatchOneJobUTF8(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-utf", "watch groups", "agent", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	body := []byte(`{"events":[{"event_id":"a","text":"Смотри: github.com/x — топ!"},{"event_id":"b","text":"Проверка байтов Юникода"},{"event_id":"c","text":"plain"}]}`)
	now := time.Now().UTC()
	o := streamOccurrence(sc, now)
	o = f.probing(t, o)
	f.settle(o, sc, body, now, nil, allowJob)
	if err := f.host.reconcile(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	job := f.h.sup.onlyJob()
	if job.ID == "" {
		t.Fatal("stream batch admitted no job")
	}
	for _, want := range []string{"Смотри: github.com/x — топ!", "Проверка байтов Юникода", "plain"} {
		if !strings.Contains(job.Text, want) {
			t.Fatalf("batch job dropped event text %q: %q", want, job.Text)
		}
	}
	if _, logical := f.h.sup.counts(); logical != 1 {
		t.Fatal("one batch admitted more than one job")
	}
	o2 := streamOccurrence(sc, now.Add(time.Second))
	o2 = f.probing(t, o2)
	f.settle(o2, sc, body, now.Add(time.Second), nil, allowJob)
	if err := f.host.reconcile(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, logical := f.h.sup.counts(); logical != 1 {
		t.Fatal("replayed batch admitted a job")
	}
}

// A stream batch persists the source cursor like the occurrence path does,
// so a restart resumes acquisition rather than re-reading from scratch.
func TestStreamBatchPersistsCursor(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-cur", "watch feed", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	body := []byte(`{"events":[{"event_id":"e1","text":"hi"}],"next_cursor":"cur-42"}`)
	o := streamOccurrence(sc, time.Now().UTC())
	o = f.probing(t, o)
	f.settle(o, sc, body, time.Now().UTC(), nil, allowJob)
	st := f.state(t, "str-cur")
	if st.Cursor != "cur-42" {
		t.Fatalf("stream batch dropped the cursor: %+v", st)
	}
	if len(f.pendingOutbox(t)) == 0 {
		t.Fatal("cursor batch dropped its event")
	}
}

// SPEC-0081 R3.2 — when the durable seen-set rolls past its cap, the evicted
// range surfaces as a counted gap instead of silently losing dedup proof.
func TestEventGapSurfacedOnSeenEviction(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-gap", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	now := time.Now().UTC()
	for batch := 0; batch < 3; batch++ {
		var items []string
		for i := 0; i < 8; i++ {
			items = append(items, fmt.Sprintf(`{"id":"b%d-e%d","text":"m%d-%d"}`, batch, i, batch, i))
		}
		body := []byte(`{"events":[` + strings.Join(items, ",") + `]}`)
		o := streamOccurrence(sc, now.Add(time.Duration(batch)*time.Minute))
		o = f.probing(t, o)
		f.settle(o, sc, body, now.Add(time.Duration(batch)*time.Minute), nil, allowJob)
	}
	// Force the seen-set past its cap in one state write to prove the
	// eviction path counts the dropped range durably.
	f.host.events.mu.Lock()
	lo := commodel.RoutineOccurrence{ScheduleID: "str-gap", Job: commodel.Job{Envelope: f.env}}
	st, path, err := f.host.events.load(lo)
	if err != nil {
		f.host.events.mu.Unlock()
		t.Fatal(err)
	}
	for i := 0; i < 522; i++ {
		st.Seen = append(st.Seen, fmt.Sprintf("fill-%d", i))
	}
	_ = writeJSONAtomic(path, st)
	f.host.events.mu.Unlock()
	// Eviction happens at the next settle write.
	o := streamOccurrence(sc, now.Add(time.Hour))
	o = f.probing(t, o)
	f.settle(o, sc, []byte(`{"events":[{"id":"last","text":"tail"}]}`), now.Add(time.Hour), nil, allowJob)
	got := f.state(t, "str-gap")
	if got.GapCount == 0 || got.LastGapAt.IsZero() {
		t.Fatalf("evicted seen range must surface as a gap: %+v", got)
	}
}

// SPEC-0081 R3.1 — a source-declared resume cursor lands on the durable
// event state so the next poll can continue exactly where the source left
// off.
func TestEventCursorCaptured(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-cur", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	now := time.Now().UTC()
	body := []byte(`{"events":[{"id":"e1","text":"m1"}],"next_cursor":"cursor-42"}`)
	o := streamOccurrence(sc, now)
	o = f.probing(t, o)
	f.settle(o, sc, body, now, nil, allowJob)
	st := f.state(t, "str-cur")
	if st.Cursor != "cursor-42" {
		t.Fatalf("cursor not captured: %+v", st)
	}
}

// SPEC-0081 R4.1 — a deterministic filter script gates each event before the
// sink sees it: dropped events are marked seen without delivery, urgent
// ones bypass the quiet window.
func TestEventFilterDropsBeforeFanout(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-filter", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("drop" if "noise" in input() else "keep")`}))
	f.host.events.filter = func(ctx context.Context, o commodel.RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
		if strings.Contains(input, "noise") {
			return "drop", nil
		}
		return "keep", nil
	}
	now := time.Now().UTC()
	body := []byte(`{"events":[{"id":"n1","text":"noise spam"},{"id":"k1","text":"boss pinged you"}]}`)
	o := streamOccurrence(sc, now)
	o = f.probing(t, o)
	f.settle(o, sc, body, now, nil, allowJob)
	recs := f.pendingOutbox(t)
	if len(recs) != 1 {
		t.Fatalf("kept event must deliver: %+v", recs)
	}
	if strings.Contains(recs[0].Delivery.Text, "noise") {
		t.Fatalf("dropped event must not reach the sink: %q", recs[0].Delivery.Text)
	}
	st := f.state(t, "str-filter")
	seen := false
	for _, id := range st.Seen {
		seen = seen || id == "n1"
	}
	if !seen {
		t.Fatal("dropped event must be marked seen so it never refires")
	}
}

// SPEC-0081 R4.1 — an urgent verdict bypasses the quiet window and reaches
// the owner immediately.
func TestEventFilterUrgentBypassesQuiet(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-urgent", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("urgent")`}))
	sc.QuietFrom, sc.QuietTo = "00:00", "23:59"
	f.host.events.filter = func(ctx context.Context, o commodel.RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
		return "urgent", nil
	}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"events":[{"id":"u1","text":"production is down"}]}`)
	o := streamOccurrence(sc, now)
	o = f.probing(t, o)
	f.settle(o, sc, body, now, nil, allowJob)
	recs := f.pendingOutbox(t)
	if len(recs) != 1 || !recs[0].Delivery.NotBefore.IsZero() {
		t.Fatalf("urgent event must bypass the quiet window: %+v", recs)
	}
}

// S6.3 — when the sink cannot admit an event, the durable pending batch
// keeps it: the occurrence stays probing and the next resume admits the
// same event exactly once — strictly stronger than the retired best-effort
// connector requeue, which only signaled the loss.
func TestStreamPollRetainsBatchOnSinkFailure(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str6", "watch dms", "notify",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", RequeueTool: "hub-tg-requeue"}))
	// Break the outbox so the burst cannot be admitted durably.
	if err := os.RemoveAll(f.host.outbox.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.host.outbox.dir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 2}`)
	o := streamOccurrence(sc, f.h.now)
	o = f.probing(t, o)
	if _, err := f.host.events.settle(f.eng, o, sc, body, f.h.now, nil, allowJob); err == nil {
		t.Fatal("broken outbox must surface as a settle error")
	}
	st := f.state(t, "str6")
	if st.Pending == nil {
		t.Fatal("sink failure must keep the durable batch for retry")
	}
	// Restore the outbox: the retained batch delivers exactly once.
	if err := os.Remove(f.host.outbox.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := f.host.events.resume(f.eng, o, sc, f.h.now.Add(time.Minute), allowJob); err != nil {
		t.Fatal(err)
	}
	recs := f.pendingOutbox(t)
	if len(recs) != 1 || !strings.Contains(recs[0].Delivery.Text, "Danil") {
		t.Fatalf("retained batch did not deliver after recovery: %+v", recs)
	}
}

// S6.4 — the stream source is the live schedule record: pause, disable or
// delete stops the tick from re-acquiring instead of polling a stale copy.
func TestStreamSourceStopsOnInactive(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("ss", "watch", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}))
	calls := 0
	toolCall := func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		calls++
		return []byte(`{"events":[]}`), nil
	}
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatalf("active stream source rejected: %v", err)
	}
	if calls != 1 {
		t.Fatalf("active stream source never polled: %d", calls)
	}
	if err := f.host.PauseSchedule(context.Background(), f.env, "ss"); err != nil {
		t.Fatal(err)
	}
	f.h.now = f.h.now.Add(2 * time.Minute)
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("paused stream source still polled")
	}
	if err := f.host.DeleteSchedule(context.Background(), f.env, sc.ScheduleID); err != nil {
		t.Fatal(err)
	}
	f.h.now = f.h.now.Add(2 * time.Minute)
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("deleted stream source still polled")
	}
}

// S6.4 — the stream tick finds exactly the live stream subscriptions:
// poll events, non-event jobs and paused streams stay out of the loop set.
func TestStreamSchedulesListing(t *testing.T) {
	f := newEventFixture(t)
	base := commodel.Schedule{Expression: "* * * * *", Input: "x"}
	stream := base
	stream.ScheduleID, stream.JobKind, stream.Delivery = "s-live", "event", "stream"
	stream.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}
	f.create(t, stream)
	paused := stream
	paused.ScheduleID = "s-paused"
	f.create(t, paused)
	if err := f.host.PauseSchedule(context.Background(), f.env, "s-paused"); err != nil {
		t.Fatal(err)
	}
	poll := base
	poll.ScheduleID, poll.JobKind = "p-live", "event"
	poll.Probe = &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-a"}
	f.create(t, poll)
	other := base
	other.ScheduleID, other.JobKind = "w-live", "watcher"
	other.Probe = &commodel.WatchProbe{Kind: "http", URL: "https://example.com/feed", Regex: "(x)"}
	f.create(t, other)
	if err := f.host.tickStreams(context.Background(), f.eng, "alice", f.h.now); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.h.root, "users", "alice", "occurrences"))
	ids := map[string]bool{}
	for _, entry := range entries {
		var o commodel.RoutineOccurrence
		raw, _ := os.ReadFile(filepath.Join(f.h.root, "users", "alice", "occurrences", entry.Name()))
		if json.Unmarshal(raw, &o) == nil {
			ids[o.ScheduleID] = true
		}
	}
	if !ids["s-live"] || ids["s-paused"] || ids["p-live"] || ids["w-live"] {
		t.Fatalf("stream scan wrong: %v", ids)
	}
}

// SPEC-0081 R4.1 — a filter that fails to evaluate must not silently admit
// the event: the occurrence settles failed, the event stays unseen for a
// later retry, and nothing reaches the sink.
func TestEventFilterErrorFailsClosed(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-failclosed", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("keep")`}))
	f.host.events.filter = func(_ context.Context, _ commodel.RoutineOccurrence, _, _ string, _ bool, _ []string) (string, error) {
		return "", errors.New("script runtime exploded")
	}
	now := time.Now().UTC()
	body := []byte(`{"events":[{"id":"x1","text":"boss pinged you"}]}`)
	o := streamOccurrence(sc, now)
	o = f.probing(t, o)
	f.settle(o, sc, body, now, nil, allowJob)
	if len(f.pendingOutbox(t)) != 0 {
		t.Fatal("filter error admitted the event")
	}
	st := f.state(t, "str-failclosed")
	for _, id := range st.Seen {
		if id == "x1" {
			t.Fatal("filter error must not mark the event seen — it would never retry")
		}
	}
	raw, err := os.ReadFile(filepath.Join(f.h.root, "users", "alice", "occurrences", routines.OccurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var live commodel.RoutineOccurrence
	if err := json.Unmarshal(raw, &live); err != nil || live.State != "failed" {
		t.Fatalf("filter error must settle the occurrence failed: %+v %v", live, err)
	}
}

// SPEC-0081 R4.1 — the per-event filter performs I/O (the script runs in a
// disposable runtime over HTTP); holding the event-store lock across it
// starves every other reader for the whole script duration.
func TestEventFilterRunsOutsideStoreLock(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-lockfree", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("keep")`}))
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.host.events.filter = func(_ context.Context, _ commodel.RoutineOccurrence, _, _ string, _ bool, _ []string) (string, error) {
		once.Do(func() { close(started) })
		<-release
		return "keep", nil
	}
	now := time.Now().UTC()
	body := []byte(`{"events":[{"id":"y1","text":"deploy finished"}]}`)
	o := streamOccurrence(sc, now)
	o = f.probing(t, o)
	settled := make(chan struct{})
	go func() {
		_, _ = f.host.events.settle(f.eng, o, sc, body, now, nil, allowJob)
		close(settled)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("filter script never ran")
	}
	read := make(chan struct{})
	go func() {
		_, _, _ = f.host.events.load(o)
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(2 * time.Second):
		t.Fatal("event-store lock held across the filter script")
	}
	close(release)
	select {
	case <-settled:
	case <-time.After(3 * time.Second):
		t.Fatal("settle never finished")
	}
	if len(f.pendingOutbox(t)) == 0 {
		t.Fatal("kept event must deliver after the filter releases")
	}
}

// SPEC-0081 R4.1 — an uncompilable filter is rejected at create time so the
// schedule never persists a gate that can only fail-open at poll time.
func TestEventFilterValidationAtCreate(t *testing.T) {
	f := newEventFixture(t)
	mk := func(id, filter string) commodel.Schedule {
		return commodel.Schedule{ScheduleID: id, Envelope: f.env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Probe: &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: filter}}
	}
	if _, err := f.host.CreateSchedule(context.Background(), f.env, mk("f-good", `emit("keep")`), ""); err != nil {
		t.Fatalf("valid filter rejected: %v", err)
	}
	if _, err := f.host.CreateSchedule(context.Background(), f.env, mk("f-bad", `emit("keep"`), ""); err == nil {
		t.Fatal("uncompilable filter accepted at create")
	}
	if _, err := f.host.CreateSchedule(context.Background(), f.env, mk("f-empty", ""), ""); err != nil {
		t.Fatalf("absent filter must stay valid: %v", err)
	}
}

// SPEC-0081 R4.1 — stream delivery applies the same per-event gate as poll
// delivery: a dropped event is marked seen without reaching the sink.
func TestStreamPollOnceAppliesFilter(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("strf", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("drop" if "noise" in input() else "keep")`}))
	f.host.events.filter = func(_ context.Context, _ commodel.RoutineOccurrence, _, input string, _ bool, _ []string) (string, error) {
		if strings.Contains(input, "noise") {
			return "drop", nil
		}
		return "keep", nil
	}
	toolCall := func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"n1","text":"noise spam"},{"id":"k1","text":"boss pinged you"}]}`), nil
	}
	if err := f.pollOnce(sc, toolCall); err != nil {
		t.Fatalf("healthy filtered poll reported blocked: %v", err)
	}
	recs := f.pendingOutbox(t)
	if len(recs) != 1 {
		t.Fatalf("kept stream event must deliver: %+v", recs)
	}
	if strings.Contains(recs[0].Delivery.Text, "noise") {
		t.Fatalf("dropped event must not reach the sink: %q", recs[0].Delivery.Text)
	}
	st := f.state(t, "strf")
	seen := false
	for _, id := range st.Seen {
		seen = seen || id == "n1"
	}
	if !seen {
		t.Fatal("dropped stream event must be marked seen")
	}
}

// SPEC-0081 R4.1 — a stream filter that cannot evaluate fails closed:
// events stay unseen for a retry and the occurrence settles failed.
func TestStreamPollOnceFilterErrorFailsClosed(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("strf2", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("keep")`}))
	f.host.events.filter = func(_ context.Context, _ commodel.RoutineOccurrence, _, _ string, _ bool, _ []string) (string, error) {
		return "", errors.New("script runtime exploded")
	}
	toolCall := func(context.Context, string, *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"x1","text":"boss pinged you"}]}`), nil
	}
	_ = f.pollOnce(sc, toolCall)
	if len(f.pendingOutbox(t)) != 0 {
		t.Fatal("filter error admitted an event")
	}
	st := f.state(t, "strf2")
	for _, id := range st.Seen {
		if id == "x1" {
			t.Fatal("filter error marked the event seen")
		}
	}
}

// A connector fan-out over a dozen chats merges into one candidate batch:
// same-message ids in different chats must never dedup across sources.
func TestFourteenTelegramSourcesMergeOneCandidateBatch(t *testing.T) {
	f := newEventFixture(t)
	var sources []commodel.WatchProbeSource
	for i := 0; i < 14; i++ {
		sources = append(sources, commodel.WatchProbeSource{Name: fmt.Sprintf("chat-%d", i), Args: map[string]any{"chat_id": float64(i)}})
	}
	sc := f.create(t, f.streamSchedule("str14", "watch all chats", "agent",
		&commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Sources: sources}))
	f.host.cfg.ToolCall = func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
		return []byte(`{"event": true, "id": "same-msg", "text": "morning report"}`), nil
	}
	if err := f.pollOnce(sc, f.host.cfg.ToolCall); err != nil {
		t.Fatal(err)
	}
	if err := f.host.reconcile(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	job := f.h.sup.onlyJob()
	if job.ID == "" {
		t.Fatal("merged batch admitted no job")
	}
	// One wake for the whole merged batch, never fourteen.
	if _, logical := f.h.sup.counts(); logical != 1 {
		t.Fatalf("14 sources produced %d agent wakes", logical)
	}
	st := f.state(t, "str14")
	if len(st.Seen) != 14 {
		t.Fatalf("same-id events across sources must all persist: %d", len(st.Seen))
	}
}

// S6.3 — a settled batch is durable: a host restart recovers the same
// event-state record and cursor instead of re-polling from scratch.
func TestStreamBatchSurvivesRestart(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("str-restart", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	body := []byte(`{"events":[{"id":"e1","text":"hi"}],"next_cursor":"c1"}`)
	o := streamOccurrence(sc, f.h.now)
	o = f.probing(t, o)
	f.settle(o, sc, body, f.h.now, nil, allowJob)
	if len(f.pendingOutbox(t)) != 1 {
		t.Fatal("batch never landed in the owner outbox")
	}
	restarted := f.h.host(t)
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(context.Background(), f.h.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	st := f.state(t, "str-restart")
	if st.Cursor != "c1" {
		t.Fatalf("restart lost the batch cursor: %+v", st)
	}
	// The restart tick flushes the one durable delivery to communication —
	// exactly once, never replayed.
	if f.h.comm.count() != 1 {
		t.Fatalf("restart must deliver the retained batch once, got %d", f.h.comm.count())
	}
}

// Ported from TestRoutineStreamPauseFencesInFlightBatch /
// TestRoutineStreamEditFencesInFlightBatch (communication): a durable pending
// batch is fenced once the schedule paused or its revision moved — the old
// loop suppressed delivery; the owner refuses the batch outright and keeps
// it durable for recovery.
func stagePendingBatch(t *testing.T, f *eventFixture, sc commodel.Schedule, o commodel.RoutineOccurrence, events []commodel.Event) {
	t.Helper()
	st := eventState{ScheduleID: sc.ScheduleID, ProbeRevision: sc.ProbeRevision, StreamAt: f.h.now, StreamRevision: sc.Revision, Pending: &eventBatch{JobID: o.Job.ID, Revision: sc.Revision, Events: events}}
	path := filepath.Join(f.host.events.dir, safeName("alice"), safeName(sc.ScheduleID)+".json")
	if err := writeJSONAtomic(path, st); err != nil {
		t.Fatal(err)
	}
}

func TestStreamEditFencesPendingBatch(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("fence-edit", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	o := f.probing(t, streamOccurrence(sc, f.h.now))
	stagePendingBatch(t, f, sc, o, []commodel.Event{{ID: "one", Text: "stale batch"}})
	if _, err := f.host.EditRoutine(context.Background(), f.env, sc.ScheduleID, map[string]json.RawMessage{"input": json.RawMessage(`"new text"`)}, false, nil); err != nil {
		t.Fatal(err)
	}
	cur, err := f.host.LoadSchedule(context.Background(), f.env, sc.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	drained, resumeErr := f.host.events.resume(f.eng, o, cur, f.h.now, allowJob)
	if resumeErr == nil || !drained {
		t.Fatalf("edited schedule resumed a stale batch: drained=%v err=%v", drained, resumeErr)
	}
	if err := f.host.Tick(context.Background(), f.h.now.Add(2*time.Minute)); err != nil {
		t.Fatalf("fenced batch wedged the tick: %v", err)
	}
	if st := f.state(t, sc.ScheduleID); st.Pending != nil || st.GapCount == 0 {
		t.Fatalf("fenced batch was not dropped as a recorded gap: %+v", st)
	}
	if f.h.comm.count() != 0 {
		t.Fatal("stale batch delivered after edit")
	}
}

// An edit that moves the schedule revision under a pending batch must not
// wedge the principal: the stale batch is dropped as a recorded gap and the
// tick still materializes every other stream of the same principal.
func TestScheduleEditDropsPendingBatchAndKeepsOtherStreams(t *testing.T) {
	f := newEventFixture(t)
	edited := f.create(t, f.streamSchedule("a-edit", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	other := f.create(t, f.streamSchedule("b-other", "watch mail", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-def"}))
	o := f.probing(t, streamOccurrence(edited, f.h.now))
	stagePendingBatch(t, f, edited, o, []commodel.Event{{ID: "one", Text: "stale batch"}})
	if _, err := f.host.EditRoutine(context.Background(), f.env, edited.ScheduleID, map[string]json.RawMessage{"input": json.RawMessage(`"new text"`)}, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.host.tickStreams(context.Background(), f.eng, "alice", f.h.now.Add(time.Minute)); err != nil {
		t.Fatalf("schedule edit wedged the principal's streams: %v", err)
	}
	st := f.state(t, edited.ScheduleID)
	if st.Pending != nil || st.GapCount == 0 || st.LastGapAt.IsZero() {
		t.Fatalf("stale batch not dropped as a recorded gap: %+v", st)
	}
	if got := f.state(t, other.ScheduleID); got.StreamAt.IsZero() {
		t.Fatalf("other stream of the principal was not materialized: %+v", got)
	}
	if f.h.comm.count() != 0 {
		t.Fatal("stale batch delivered after edit")
	}
}

// A schedule whose durable stream state cannot be read skips only itself:
// the principal's other streams still materialize instead of wedging the
// whole tick.
func TestCorruptStreamStateKeepsOtherStreams(t *testing.T) {
	f := newEventFixture(t)
	corrupt := f.create(t, f.streamSchedule("a-corrupt", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	other := f.create(t, f.streamSchedule("b-other", "watch mail", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-def"}))
	path := filepath.Join(f.host.events.dir, safeName("alice"), safeName(corrupt.ScheduleID)+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.host.tickStreams(context.Background(), f.eng, "alice", f.h.now.Add(time.Minute)); err != nil {
		t.Fatalf("one corrupt stream state wedged the principal's streams: %v", err)
	}
	if got := f.state(t, other.ScheduleID); got.StreamAt.IsZero() {
		t.Fatalf("other stream of the principal was not materialized: %+v", got)
	}
}

func TestStreamPauseFencesPendingBatch(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("fence-pause", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	o := f.probing(t, streamOccurrence(sc, f.h.now))
	stagePendingBatch(t, f, sc, o, []commodel.Event{{ID: "one", Text: "queued batch"}})
	if err := f.host.PauseSchedule(context.Background(), f.env, sc.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if err := f.host.Tick(context.Background(), f.h.now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if f.h.comm.count() != 0 {
		t.Fatal("paused stream drained its in-flight batch")
	}
	if got := f.state(t, sc.ScheduleID); got.Pending == nil {
		t.Fatal("paused stream dropped its durable batch")
	}
}

// Ported from TestRoutineEventSourceEditHasIndependentDeduplication: moving a
// stream to a different tool rebinds the cursor — the same native event id
// from the new source is fresh evidence, not a replay.
func TestStreamSourceEditDedupesIndependently(t *testing.T) {
	f := newEventFixture(t)
	sc := f.create(t, f.streamSchedule("fence-source", "watch dms", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}))
	body := []byte(`{"events":[{"id":"one","text":"source event"}]}`)
	o := f.probing(t, streamOccurrence(sc, f.h.now))
	f.settle(o, sc, body, f.h.now, nil, allowJob)
	if err := f.host.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.h.comm.count() != 1 {
		t.Fatalf("first source batch lost: %d", f.h.comm.count())
	}
	cur, err := f.host.EditRoutine(context.Background(), f.env, sc.ScheduleID, map[string]json.RawMessage{"probe": json.RawMessage(`{"kind":"mcp_tool","tool":"hub-tg-wait-xyz"}`)}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	o2 := f.probing(t, streamOccurrence(cur, f.h.now.Add(time.Second)))
	if _, err := f.host.events.settle(f.eng, o2, cur, body, f.h.now.Add(time.Second), nil, allowJob); err != nil {
		t.Fatalf("second settle: %v", err)
	}
	if err := f.host.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.h.comm.count() != 2 {
		t.Fatalf("new source lost its batch to the old dedup set: %d", f.h.comm.count())
	}
}
