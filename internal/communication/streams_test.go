package communication

import (
	"bytes"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// S6.1 — delivery=stream is an event-only mcp_tool mode; poll is the
// default and other kinds reject it outright.
func TestStreamDeliveryValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := Schedule{ScheduleID: "sd", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "x"}

	bad := base
	bad.ScheduleID = "sd1"
	bad.JobKind = "event"
	bad.Delivery = "carrier"
	bad.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-abc"}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("unknown delivery value accepted")
	}

	nonEvent := base
	nonEvent.ScheduleID = "sd2"
	nonEvent.JobKind = "watcher"
	nonEvent.Delivery = "stream"
	nonEvent.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-abc"}
	if _, err := s.CreateSchedule(nonEvent, caller, ""); err == nil {
		t.Fatal("stream accepted on a non-event schedule")
	}

	httpProbe := base
	httpProbe.ScheduleID = "sd3"
	httpProbe.JobKind = "event"
	httpProbe.Delivery = "stream"
	httpProbe.Probe = &WatchProbe{Kind: "http", URL: "https://example.com/feed"}
	if _, err := s.CreateSchedule(httpProbe, caller, ""); err == nil {
		t.Fatal("stream accepted an http probe — it needs a wait-capable tool")
	}

	good := base
	good.ScheduleID = "sd4"
	good.JobKind = "event"
	good.Delivery = "stream"
	good.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait_for_settled-abc", Args: map[string]any{"chat_id": "@dan"}}
	if _, err := s.CreateSchedule(good, caller, ""); err != nil {
		t.Fatalf("stream event schedule rejected: %v", err)
	}
}

// S6.1 — stream schedules are owned by the poll loop, not the occurrence
// machinery: a due stream schedule produces no occurrence file.
func TestTickSchedulesSkipsStream(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "st1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "pl1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "poll dms", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	// Tick past both schedules' first due: only the poll one may fire.
	now := time.Now().UTC().Add(2 * time.Minute)
	if err := s.TickSchedules(now, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil {
		t.Fatal(err)
	}
	jsonCount := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			jsonCount++
		}
	}
	if jsonCount != 1 {
		t.Fatalf("expected exactly one (poll) occurrence, got %d", jsonCount)
	}
}

// S6.2 — connector wait tools return an explicit no-event sentinel on
// timeout; it must not be mistaken for an event payload.
func TestExtractEventsNoEventSentinel(t *testing.T) {
	events, err := extractEvents(&WatchProbe{Kind: "mcp_tool"}, []byte(`{"event": false, "reason": "timeout", "waiting_for": 12345}`))
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
	events, err := extractEvents(&WatchProbe{Kind: "mcp_tool"}, []byte(body))
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
	events, err := extractEvents(&WatchProbe{Kind: "mcp_tool"}, []byte(body))
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

// S6.3 — a stream batch fans out to the owner outbox with no occurrence in
// play, and an identical re-poll is fully deduped.
func TestSettleStreamEventsNotify(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	body := []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 2, "first_message_id": 10, "last_message_id": 11, "kind": "dm"}`)
	s.settleStreamEvents(sched, body, now, nil, g.authorizeOccurrence)
	// The outbox worker re-resolves OccurrenceID at send time: a stream batch
	// ref must map back to its owning schedule and re-authorize, or the
	// delivery dies as uncertain before the channel sees it.
	fake := &fakeAPI{}
	g.api = fake
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "Danil") || !strings.HasPrefix(fake.sent[0], "watch dms") {
		t.Fatalf("stream delivery dropped at send-time verification: %v", fake.sent)
	}
	// Same batch replayed: deduped by the durable seen-set.
	s.settleStreamEvents(sched, body, now.Add(time.Second), nil, g.authorizeOccurrence)
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 {
		t.Fatal("replayed stream batch delivered again")
	}
}

// S6.3 — sink=agent on a stream wakes the agent once per batch with the
// untrusted-evidence flag and the burst's routing fields intact.
func TestSettleStreamEventsAgentSink(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str2", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "answer Danil for me", JobKind: "event", Delivery: "stream", Sink: "agent", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	body := []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 2, "first_message_id": 10, "last_message_id": 11, "kind": "dm"}`)
	s.settleStreamEvents(sched, body, now, nil, g.authorizeOccurrence)
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("agent stream batch admitted no job")
	}
	if !strings.Contains(job.Text, "untrusted") || !strings.Contains(job.Text, "777") || !strings.Contains(job.Text, "answer Danil for me") {
		t.Fatalf("stream agent bundle: %q", job.Text)
	}
	if err := g.authorizeOccurrence(*job); err != nil {
		t.Fatalf("stream agent bundle fails send-time re-authorization: %v", err)
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatal("agent stream produced a plain delivery")
	}
	// Replay dedupes the whole batch (same deterministic job id).
	s.settleStreamEvents(sched, body, now.Add(time.Second), nil, g.authorizeOccurrence)
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("replayed stream batch woke the agent again")
	}
}

// The stream path feeds the same raw JSON to the filter as the poll path:
// input() carries connector fields, not only the extracted text.
func TestStreamFilterInputPrefersRawJSON(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-raw2", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch groups", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("drop" if "-1001" not in input() else "keep")`}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	var gotInput string
	s.probeScript = func(ctx context.Context, o RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
		gotInput = input
		return "keep", nil
	}
	body := []byte(`{"event": true, "id": "e9", "text": "ping", "chat_id": -1001234, "sender_id": 7}`)
	s.settleStreamEvents(sched, body, time.Now().UTC(), nil, g.authorizeOccurrence)
	if !strings.Contains(gotInput, `"chat_id":-1001234`) {
		t.Fatalf("stream filter input lacks connector fields: %q", gotInput)
	}
}

// probe.sources fans one subscription out over several acquisition inputs:
// every source is probed inside its own bounded call, the batches merge
// through one dedup/filter/sink, and the whole window costs one agent
// wake — never one per source.
func TestStreamSourcesFanoutMergesOneWake(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-multi", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch groups", JobKind: "event", Delivery: "stream", Sink: "agent",
		Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Args: map[string]any{"timeout": 20},
			Sources: []WatchProbeSource{
				{Name: "alpha", Args: map[string]any{"chat_id": float64(1)}},
				{Name: "beta", Args: map[string]any{"chat_id": float64(2)}},
			}}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	called := map[int]bool{}
	toolCall := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
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
	_, blocked := g.streamPollOnce(context.Background(), sched, toolCall)
	if blocked {
		t.Fatal("healthy multi-source round reported blocked")
	}
	if !called[1] || !called[2] {
		t.Fatalf("fan-out missed sources: %v", called)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
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
	if job2, err := s.ClaimJob(); err != nil || job2 != nil {
		t.Fatal("one batch produced more than one agent wake")
	}
	st, _ := s.EventStateOf("str-multi")
	if st.SourceCursors["alpha"] != "ca1" || st.SourceCursors["beta"] != "cb1" {
		t.Fatalf("per-source cursors not persisted: %+v", st.SourceCursors)
	}
	// A replayed round dedupes per source — no second wake.
	g.streamPollOnce(context.Background(), sched, toolCall)
	if job2, err := s.ClaimJob(); err != nil || job2 != nil {
		t.Fatal("replayed multi-source batch woke the agent again")
	}
}

// A source may narrow to a sibling read tool, and an unnamed source still
// gets a stable positional label so its cursor survives restarts.
func TestStreamSourceToolOverrideAndPositionalLabel(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-mixed", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch mixed", JobKind: "event", Delivery: "stream",
		Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", MinIntervalSeconds: 60,
			Sources: []WatchProbeSource{
				{Args: map[string]any{"chat_id": float64(1)}},
				{Name: "rss", Tool: "hub-rss-read", Args: map[string]any{"feed": "f"}},
			}}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	tools := map[string]bool{}
	toolCall := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		tools[probe.Tool] = true
		if probe.Tool == "hub-rss-read" {
			return []byte(`{"event": true, "id": "r1", "text": "rss item", "next_cursor": "cr1"}`), nil
		}
		return []byte(`{"event": true, "id": "t1", "text": "tg item", "next_cursor": "ct1"}`), nil
	}
	_, blocked := g.streamPollOnce(context.Background(), sched, toolCall)
	if blocked {
		t.Fatal("round blocked")
	}
	if !tools["hub-tg-wait-abc"] || !tools["hub-rss-read"] {
		t.Fatalf("source tool override not honored: %v", tools)
	}
	st, _ := s.EventStateOf("str-mixed")
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
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-partial", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch groups", JobKind: "event", Delivery: "stream", Sink: "agent",
		Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc",
			Sources: []WatchProbeSource{
				{Name: "alive", Args: map[string]any{"chat_id": float64(1)}},
				{Name: "dead", Args: map[string]any{"chat_id": float64(9)}},
			}}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	toolCall := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		if probe.Args["chat_id"] == float64(9) {
			return nil, errors.New("connector exploded")
		}
		return []byte(`{"event": true, "id": "m1", "text": "still here"}`), nil
	}
	_, blocked := g.streamPollOnce(context.Background(), sched, toolCall)
	if blocked {
		t.Fatal("partial failure must not block the round")
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil || !strings.Contains(job.Text, "still here") {
		t.Fatalf("healthy source's event lost to a sibling failure: %+v %v", job, err)
	}
	st, _ := s.EventStateOf("str-partial")
	if !strings.Contains(st.SourceErrors["dead"], "exploded") {
		t.Fatalf("source failure not attributed: %+v", st.SourceErrors)
	}
	// A fully failed round is the one that degrades and blocks.
	dead := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		return nil, errors.New("all down")
	}
	if _, blocked := g.streamPollOnce(context.Background(), sched, dead); !blocked {
		t.Fatal("all-failed round must block so the loop paces")
	}
}

// watch_sample runs one bounded probe round and returns the raw event
// shape per source with candidate-filter verdicts — the agent inspects
// what a connector emits before committing keep|drop|urgent logic, and
// the sample never fans out or marks anything seen.
func TestWatchSampleReturnsRawEventsAndVerdicts(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-sample", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch groups", JobKind: "event", Delivery: "stream", Sink: "agent",
		Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("keep")`,
			Sources: []WatchProbeSource{
				{Name: "alpha", Args: map[string]any{"chat_id": float64(1)}},
				{Name: "beta", Args: map[string]any{"chat_id": float64(2)}},
			}}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	toolCall := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		if probe.Args["chat_id"] == float64(1) {
			return []byte(`{"event": true, "id": "m1", "text": "ping", "chat_id": -1001, "sender_id": 7}`), nil
		}
		return []byte(`{"event": false, "reason": "timeout"}`), nil
	}
	var filterInputSeen string
	s.probeScript = func(ctx context.Context, o RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
		filterInputSeen = input
		return "keep", nil
	}
	sample, err := g.sampleRoutine(context.Background(), sched.ScheduleID, caller, "", toolCall)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.Sources) != 2 {
		t.Fatalf("sample must report per source: %+v", sample.Sources)
	}
	alpha := sample.Sources[0]
	if alpha.Source != "alpha" || len(alpha.Events) != 1 {
		t.Fatalf("alpha sample wrong: %+v", alpha)
	}
	if !strings.Contains(alpha.Events[0].Raw, `"chat_id":-1001`) {
		t.Fatalf("sample lost the raw event fields the filter needs: %+v", alpha.Events[0])
	}
	if alpha.Verdicts["m1"] != "keep" {
		t.Fatalf("candidate filter verdict missing: %+v", alpha.Verdicts)
	}
	if !strings.Contains(filterInputSeen, `"chat_id"`) {
		t.Fatalf("filter ran on text, not raw JSON: %q", filterInputSeen)
	}
	if beta := sample.Sources[1]; len(beta.Events) != 0 || beta.Error != "" {
		t.Fatalf("timeout source should report an empty event list, not an error: %+v", beta)
	}
	// Sampling never wakes the agent or marks events seen.
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("sample fanned out to the sink")
	}
	st, _ := s.EventStateOf("str-sample")
	if len(st.Seen) != 0 {
		t.Fatal("sample marked events seen")
	}
	// A foreign caller must not probe someone else's connector — sampling
	// is owner-only like every other routine control.
	mallory := identity.TelegramEnvelope("mallory", 99, "mallory", "policy-1")
	if _, err := g.sampleRoutine(context.Background(), sched.ScheduleID, mallory, "", toolCall); err == nil {
		t.Fatal("foreign caller sampled an owned watch")
	}
}

// watch_sample's filter argument dry-runs a CANDIDATE gate over the same
// sample — the agent evaluates a new filter against real event shapes
// before committing it to the watch.
func TestWatchSampleRunsCandidateFilter(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-cand", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch groups", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	toolCall := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		return []byte(`{"event": true, "id": "m1", "text": "ping", "chat_id": -1001}`), nil
	}
	var gotSrc string
	s.probeScript = func(ctx context.Context, o RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
		gotSrc = src
		return "drop", nil
	}
	sample, err := g.sampleRoutine(context.Background(), sched.ScheduleID, caller, `emit("drop" if "-9999" in input() else "keep")`, toolCall)
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

// POST /v1/routines/{id}/sample is the owner-facing wire for watch_sample:
// a failed source surfaces its own error entry instead of pretending the
// source is quiet.
func TestWatchSampleHTTPRouteReportsSourceErrors(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	handler := g.Handler()
	post := func(path, body, principal string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer control-token")
		req.Header.Set("X-Hub-Principal", principal)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	create := `{"schedule_id":"samp-http","timezone":"UTC","expression":"* * * * *","input":"watch groups","job_kind":"event","delivery":"stream","probe":{"kind":"mcp_tool","tool":"hub-tg-wait-abc","sources":[{"name":"alpha","args":{"chat_id":1}}]}}`
	if rec := post("/v1/routines", create, "alice"); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec := post("/v1/routines/samp-http/sample", `{}`, "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("sample: %d %s", rec.Code, rec.Body.String())
	}
	var sample struct {
		ScheduleID string `json:"schedule_id"`
		Sources    []struct {
			Source string `json:"source"`
			Error  string `json:"error"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sample); err != nil {
		t.Fatal(err)
	}
	if sample.ScheduleID != "samp-http" || len(sample.Sources) != 1 || sample.Sources[0].Source != "alpha" {
		t.Fatalf("sample shape lost: %s", rec.Body.String())
	}
	// No live connector in tests: the source's failure must surface in its
	// own error field, not as a silent empty batch.
	if sample.Sources[0].Error == "" {
		t.Fatalf("dead source reported as quiet: %s", rec.Body.String())
	}
	// Foreign principal is denied — control auth rejects the unknown
	// caller before the routine-ownership check ever runs.
	if rec := post("/v1/routines/samp-http/sample", `{}`, "mallory"); rec.Code == http.StatusOK {
		t.Fatalf("foreign caller sampled: %s", rec.Body.String())
	}
}

// S6.3 — a connector auth failure on the stream poll marks the durable
// event state degraded instead of emitting a fake "nothing new".
func TestSettleStreamEventsAuthDegraded(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str3", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	s.settleStreamEvents(sched, nil, time.Now().UTC(), errors.New("tool error: 403 forbidden"), g.authorizeOccurrence)
	st, err := s.EventStateOf("str3")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Degraded || st.LastError == "" {
		t.Fatalf("auth failure not marked degraded: %+v", st)
	}
}

// S6.3 — an owner whose authorization no longer holds fails closed on the
// stream path exactly like occurrences do: no delivery, error recorded.
func TestSettleStreamEventsUnauthorized(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str4", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	deny := func(Job) error { return errors.New("routine owner unavailable") }
	body := []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 1}`)
	s.settleStreamEvents(sched, body, time.Now().UTC(), nil, deny)
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatal("unauthorized stream batch delivered")
	}
	st, err := s.EventStateOf("str4")
	if err != nil || st.LastError == "" {
		t.Fatalf("authorization failure not recorded: %+v %v", st, err)
	}
}

// S6.3 — one stream poll pass: the injected tool result lands in the
// outbox through the same settle path the loop uses.
func TestStreamPollOnceDelivers(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str5", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait_for_settled_message-abc", Args: map[string]any{"chat_id": "@dan", "max_wait_ms": 8000}}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	toolCall := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		return []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 2}`), nil
	}
	g.streamPollOnce(context.Background(), sched, toolCall)
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "Danil") {
		t.Fatalf("stream poll admitted no delivery: %+v %v", d, err)
	}
	// A timeout sentinel result: nothing lands, the poll still counts as success.
	toolCall = func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		return []byte(`{"event": false, "reason": "timeout"}`), nil
	}
	g.streamPollOnce(context.Background(), sched, toolCall)
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatal("timeout sentinel delivered")
	}
	st, err := s.EventStateOf("str5")
	if err != nil || st.LastSuccess.IsZero() {
		t.Fatalf("healthy empty poll not recorded: %+v %v", st, err)
	}
}

// A plain poll tool on a stream honors its declared min_interval between
// rounds: an immediate-returning poll would otherwise hot-spin.
func TestStreamPollPacesByMinInterval(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	_, err := s.CreateSchedule(Schedule{ScheduleID: "str-pace", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch feed", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-feed-poll-abc", MinIntervalSeconds: 3600}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	toolCall := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		calls++
		return []byte(`{"events":[]}`), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	g.streamPoll(ctx, "str-pace", toolCall)
	if calls != 1 {
		t.Fatalf("stream poll ignored the declared cadence: %d calls", calls)
	}
}

// A multi-event batch admits exactly one agent job carrying every event's
// text as raw UTF-8 bytes, and a replayed batch admits nothing more.
func TestStreamBatchOneJobUTF8(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-utf", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch groups", JobKind: "event", Delivery: "stream", Sink: "agent", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"events":[{"event_id":"a","text":"Смотри: github.com/x — топ!"},{"event_id":"b","text":"Проверка байтов Юникода"},{"event_id":"c","text":"plain"}]}`)
	now := time.Now().UTC()
	s.settleStreamEvents(sched, body, now, nil, g.authorizeOccurrence)
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("stream batch admitted no job")
	}
	for _, want := range []string{"Смотри: github.com/x — топ!", "Проверка байтов Юникода", "plain"} {
		if !bytes.Contains([]byte(job.Text), []byte(want)) {
			t.Fatalf("batch job dropped event text %q: %q", want, job.Text)
		}
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("one batch admitted more than one job")
	}
	s.settleStreamEvents(sched, body, now.Add(time.Second), nil, g.authorizeOccurrence)
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("replayed batch admitted a job")
	}
}

// A stream batch persists the source cursor like the occurrence path does,
// so a restart resumes acquisition rather than re-reading from scratch.
func TestStreamBatchPersistsCursor(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-cur", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch feed", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"events":[{"event_id":"e1","text":"hi"}],"next_cursor":"cur-42"}`)
	s.settleStreamEvents(sched, body, time.Now().UTC(), nil, g.authorizeOccurrence)
	st, err := s.EventStateOf("str-cur")
	if err != nil || st.Cursor != "cur-42" {
		t.Fatalf("stream batch dropped the cursor: %+v %v", st, err)
	}
	if d, err := s.ClaimDelivery(); err != nil || d == nil {
		t.Fatal("cursor batch dropped its event")
	}
}

// SPEC-0081 R3.2 — when the durable seen-set rolls past its cap, the evicted
// range surfaces as a counted gap instead of silently losing dedup proof.
func TestEventGapSurfacedOnSeenEviction(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-gap", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for batch := 0; batch < 3; batch++ {
		var items []string
		for i := 0; i < 8; i++ {
			items = append(items, fmt.Sprintf(`{"id":"b%d-e%d","text":"m%d-%d"}`, batch, i, batch, i))
		}
		body := []byte(`{"events":[` + strings.Join(items, ",") + `]}`)
		o2 := o
		o2.Job.ID = fmt.Sprintf("occ-gap-%d", batch)
		putOccurrence := filepath.Join(s.root, "occurrences", occurrenceID(o2)+".json")
		o2.State = "probing"
		if err := atomicJSON(putOccurrence, o2); err != nil {
			t.Fatal(err)
		}
		s.settleEvents(o2, sched, body, now.Add(time.Duration(batch)*time.Minute), nil, g.authorizeOccurrence)
	}
	// Force the seen-set past its cap in one state write to prove the
	// eviction path counts the dropped range durably.
	s.mu.Lock()
	st, _ := s.loadEventStateLocked("str-gap")
	for i := 0; i < eventSeenCap+10; i++ {
		st.markSeen(fmt.Sprintf("fill-%d", i), now)
	}
	_ = atomicJSON(s.eventPath("str-gap"), st)
	s.mu.Unlock()
	got, err := s.EventStateOf("str-gap")
	if err != nil {
		t.Fatal(err)
	}
	if got.GapCount == 0 || got.LastGapAt.IsZero() {
		t.Fatalf("evicted seen range must surface as a gap: %+v", got)
	}
}

// SPEC-0081 R3.1 — a source-declared resume cursor lands on the durable
// event state so the next poll can continue exactly where the source left
// off.
func TestEventCursorCaptured(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-cur", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	body := []byte(`{"events":[{"id":"e1","text":"m1"}],"next_cursor":"cursor-42"}`)
	o.ScheduleID = "str-cur"
	o.Job.Trigger = "cron"
	o.State = "probing"
	if err := atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}
	s.settleEvents(o, sched, body, now, nil, g.authorizeOccurrence)
	st, err := s.EventStateOf("str-cur")
	if err != nil || st.Cursor != "cursor-42" {
		t.Fatalf("cursor not captured: %+v %v", st, err)
	}
}

// SPEC-0081 R4.1 — a deterministic filter script gates each event before the
// sink sees it: dropped events are marked seen without delivery, urgent
// ones bypass the quiet window.
func TestEventFilterDropsBeforeFanout(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-filter", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("drop" if "noise" in input() else "keep")`}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	s.probeScript = func(ctx context.Context, o RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
		if strings.Contains(input, "noise") {
			return "drop", nil
		}
		return "keep", nil
	}
	now := time.Now().UTC()
	body := []byte(`{"events":[{"id":"n1","text":"noise spam"},{"id":"k1","text":"boss pinged you"}]}`)
	o.ScheduleID = "str-filter"
	o.Job.Trigger = "cron"
	o.State = "probing"
	if err := atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}
	s.settleEvents(o, sched, body, now, nil, g.authorizeOccurrence)
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("kept event must deliver: %+v %v", d, err)
	}
	if strings.Contains(d.Text, "noise") {
		t.Fatalf("dropped event must not reach the sink: %q", d.Text)
	}
	st, _ := s.EventStateOf("str-filter")
	if !st.seen("n1") {
		t.Fatal("dropped event must be marked seen so it never refires")
	}
}

// SPEC-0081 R4.1 — an urgent verdict bypasses the quiet window and reaches
// the owner immediately.
func TestEventFilterUrgentBypassesQuiet(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-urgent", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", QuietFrom: "00:00", QuietTo: "23:59", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("urgent")`}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	s.probeScript = func(ctx context.Context, o RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
		return "urgent", nil
	}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"events":[{"id":"u1","text":"production is down"}]}`)
	o.ScheduleID = "str-urgent"
	o.Job.Trigger = "cron"
	o.State = "probing"
	if err := atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}
	s.settleEvents(o, sched, body, now, nil, g.authorizeOccurrence)
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !d.NotBefore.IsZero() {
		t.Fatalf("urgent event must bypass the quiet window: %+v %v", d, err)
	}
}

// The filter script reads input() — it must receive the event's compacted
// raw JSON (connector fields like chat_id), not just the text, or source
// scope filters are unwritable.
func TestEventFilterInputPrefersRawJSON(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-raw", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("keep")`}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	var gotInput string
	s.probeScript = func(ctx context.Context, o RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
		gotInput = input
		return "keep", nil
	}
	body := []byte(`{"id":"e1","text":"ping","chat_id":-1001234,"sender_id":7}`)
	o.ScheduleID = "str-raw"
	o.Job.Trigger = "cron"
	o.State = "probing"
	if err := atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}
	s.settleEvents(o, sched, body, time.Now().UTC(), nil, g.authorizeOccurrence)
	if !strings.Contains(gotInput, `"chat_id":-1001234`) {
		t.Fatalf("filter input lacks connector fields: %q", gotInput)
	}
}

// S6.3 — when the sink cannot admit an event, the stream poll best-effort
// requeues it using the connector-native chat_id contract, never content.
func TestStreamPollOnceRequeuesByChatID(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str6", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", RequeueTool: "hub-tg-requeue"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	// Break the outbox so the burst cannot be admitted.
	pending := filepath.Join(s.root, "outbox", "pending")
	if err := os.RemoveAll(pending); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requeueArgs map[string]any
	toolCall := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		if probe.Tool == "hub-tg-requeue" {
			requeueArgs = probe.Args
			return []byte(`{"requeued": true}`), nil
		}
		return []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 2}`), nil
	}
	g.streamPollOnce(context.Background(), sched, toolCall)
	if len(requeueArgs) != 1 || requeueArgs["chat_id"] != float64(777) {
		t.Fatalf("requeue must carry only the connector chat_id: %v", requeueArgs)
	}
}

// S6.3 — wait tools get a bounded wait arg inside the call budget when the
// subscriber gave none; explicit owner bounds are honored as-is.
func TestStreamProbeWaitBounds(t *testing.T) {
	orig := &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait_for_settled_message-a", Args: map[string]any{"chat_id": "@dan"}}
	settled := streamProbe(orig)
	if settled.Args["max_wait_ms"] != 20000 {
		t.Fatalf("settled wait tool got no default bound: %v", settled.Args)
	}
	if _, mutated := orig.Args["max_wait_ms"]; mutated {
		t.Fatal("stream probe mutated the stored schedule args")
	}
	newMsg := streamProbe(&WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait_for_new_message-a"})
	if newMsg.Args["timeout"] != 20 {
		t.Fatalf("new-message wait tool got no default bound: %v", newMsg.Args)
	}
	kept := streamProbe(&WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait_for_settled_message-a", Args: map[string]any{"max_wait_ms": 5000}})
	if kept.Args["max_wait_ms"] != 5000 {
		t.Fatalf("owner wait bound overridden: %v", kept.Args)
	}
	plain := streamProbe(&WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-abc"})
	if len(plain.Args) != 0 {
		t.Fatalf("non-wait tool args mutated: %v", plain.Args)
	}
}

// S6.3 — the stream call path forwards the injected wait bound to ToolHub
// under the owner-pinned credential.
func TestStreamToolCallInjectsBound(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(`{"text":"{\"event\":false}","is_error":false}`))
	}))
	defer server.Close()
	g := &Gateway{config: Config{ToolHubURL: server.URL, Supervised: true, RuntimeAuth: "tok"}}
	_, err := g.streamToolCall(context.Background(), "alice", &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait_for_settled_message-a", Args: map[string]any{"chat_id": "@dan"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"max_wait_ms":20000`) {
		t.Fatalf("wait bound missing from stream tool call: %s", body)
	}
}

// S6.4 — the stream source is the live schedule record: pause, disable,
// delete or demotion all stop the loop instead of polling a stale copy.
func TestStreamSourceStopsOnInactive(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "ss", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.streamSource("ss"); err != nil {
		t.Fatalf("active stream source rejected: %v", err)
	}
	if err := s.PauseSchedule("ss", caller); err != nil {
		t.Fatal(err)
	}
	if _, err := g.streamSource("ss"); err == nil {
		t.Fatal("paused stream source still active")
	}
	if err := s.DeleteSchedule(sched.ScheduleID, caller); err != nil {
		t.Fatal(err)
	}
	if _, err := g.streamSource("ss"); err == nil {
		t.Fatal("deleted stream source still active")
	}
}

// S6.4 — the supervisor's scan finds exactly the live stream subscriptions:
// poll events, non-event jobs and paused streams stay out of the loop set.
func TestStreamSchedulesListing(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := Schedule{Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "x"}
	stream := base
	stream.ScheduleID, stream.JobKind, stream.Delivery = "s-live", "event", "stream"
	stream.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}
	if _, err := s.CreateSchedule(stream, caller, ""); err != nil {
		t.Fatal(err)
	}
	paused := stream
	paused.ScheduleID = "s-paused"
	if _, err := s.CreateSchedule(paused, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PauseSchedule("s-paused", caller); err != nil {
		t.Fatal(err)
	}
	poll := base
	poll.ScheduleID, poll.JobKind = "p-live", "event"
	poll.Probe = &WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-a"}
	if _, err := s.CreateSchedule(poll, caller, ""); err != nil {
		t.Fatal(err)
	}
	other := base
	other.ScheduleID, other.JobKind = "w-live", "watcher"
	other.Probe = &WatchProbe{Kind: "http", URL: "https://example.com/feed", Regex: "(x)"}
	if _, err := s.CreateSchedule(other, caller, ""); err != nil {
		t.Fatal(err)
	}
	ids := g.streamSchedules()
	if !ids["s-live"] || ids["s-paused"] || ids["p-live"] || ids["w-live"] {
		t.Fatalf("stream scan wrong: %v", ids)
	}
}

// S6.4 — a failing connector backs off exponentially to a bounded ceiling:
// the loop stays alive for the subscription's hours without hot-spinning.
func TestStreamBackoff(t *testing.T) {
	steps := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, want := range steps {
		if got := streamBackoff(i + 1); got != want {
			t.Fatalf("backoff %d = %v, want %v", i+1, got, want)
		}
	}
}

// S6.4 — the stream loop keeps a bounded wait-call outstanding: it settles
// every batch into the durable pipeline, exits on cancellation, and exits
// when the durable record stops being a live stream source.
func TestStreamPollLifecycle(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "lp", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	toolCall := func(c context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"event": true, "chat_id": 777, "name": "Danil", "message_count": 1}`), nil
		}
		cancel() // the second empty poll is enough: stop the loop from inside
		return []byte(`{"event": false, "reason": "timeout"}`), nil
	}
	g.streamPoll(ctx, sched.ScheduleID, toolCall)
	if calls < 2 {
		t.Fatalf("loop exited after %d polls", calls)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatal("stream loop batch never reached the outbox")
	}
	// A second loop on the same source exits as soon as its durable record
	// stops being a live stream: pause it and confirm prompt return.
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.streamPoll(context.Background(), sched.ScheduleID, toolCall)
	}()
	if err := s.PauseSchedule(sched.ScheduleID, caller); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop ignored the source going inactive")
	}
}

// S6.4 — a failing probe paces the loop with streamBackoff instead of
// hot-spinning: two failures cost 1s + 2s before the third attempt.
func TestStreamPollBackoff(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "bp", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	toolCall := func(c context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		calls++
		if calls == 3 {
			cancel()
		}
		return nil, errors.New("connector dead")
	}
	start := time.Now()
	g.streamPoll(ctx, sched.ScheduleID, toolCall)
	if calls != 3 || time.Since(start) < 3*time.Second {
		t.Fatalf("unpaced failure loop: %d calls in %v", calls, time.Since(start))
	}
}

// S6.4 — the supervisor spawns a loop per live stream schedule, lets a
// dead loop's slot close, and unwinds every running poll on cancel.
func TestStreamSupervisorReconcile(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	_, err := s.CreateSchedule(Schedule{ScheduleID: "sup", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	toolCall := func(c context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"event": false, "reason": "timeout"}`), nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.streamSupervisor(ctx, toolCall)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("supervisor never started the stream loop")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor ignored cancellation")
	}
}

// S6.4 — reconcile is periodic: a subscription created after the
// supervisor started is picked up without a hub restart.
func TestStreamSupervisorLateSubscription(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	toolCall := func(c context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"event": false, "reason": "timeout"}`), nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.streamSupervisor(ctx, toolCall)
	}()
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "late", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if calls.Load() == 0 {
		t.Fatal("late subscription never started streaming")
	}
}

// S6.4 — an exited loop's slot frees: a stream paused then re-enabled under
// the same id is streamed again, not blocked by the dead loop's entry.
func TestStreamSupervisorRespawns(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "rev", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	toolCall := func(c context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"event": false, "reason": "timeout"}`), nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.streamSupervisor(ctx, toolCall)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("loop never started")
	}
	if err := s.PauseSchedule("rev", caller); err != nil {
		t.Fatal(err)
	}
	// Let the old loop exit on its next source load, then re-enable.
	time.Sleep(100 * time.Millisecond)
	enabled := true
	if _, err := s.UpdateSchedule("rev", caller, "", "", &enabled); err != nil {
		t.Fatal(err)
	}
	resumed := calls.Load() + 1
	deadline = time.Now().Add(5 * time.Second)
	for calls.Load() < resumed && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if calls.Load() < resumed {
		t.Fatal("re-enabled stream was never respawned")
	}
}

// S6.4 — the gateway's stream loops run against the real tool-call path:
// startStreamLoops drives a live stream through ToolHub HTTP and unwinds
// on cancellation.
func TestStartStreamLoops(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"text":"{\"event\":false,\"reason\":\"timeout\"}","is_error":false}`))
	}))
	defer server.Close()
	g.config.ToolHubURL = server.URL
	g.config.Supervised = true
	g.config.RuntimeAuth = "tok"
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "live", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := g.startStreamLoops(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream loops ignored cancellation")
	}
	if calls.Load() == 0 {
		t.Fatal("wired stream loops never reached ToolHub")
	}
}

// S6.4 — /watches marks a stream subscription's delivery mode so the owner
// can tell a reactive wait-loop from a cron poll at a glance.
func TestWatchesMarksStream(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wm", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-a"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	user := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	out := g.watchesCommand(user, 11)
	if !strings.Contains(out, "wm") || !strings.Contains(out, ", stream") {
		t.Fatalf("stream delivery not rendered: %q", out)
	}
}

// TestHookIntakeEndpoint (SPEC-0081 R5.2): POST /v1/hooks/<id> stages the
// posted body in the schedule's durable inbox — only for the owning caller
// and only for probe.kind=hook schedules.
func TestHookIntakeEndpoint(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	sched := Schedule{ScheduleID: "hook-1", Envelope: env, UserID: "alice", Enabled: true, Timezone: "UTC", Expression: "* * * * *", JobKind: "event", Channel: "telegram_bot", ChatID: 11, Probe: &WatchProbe{Kind: "hook", URL: "hook:github"}}
	if err := atomicJSON(g.spool.rt.SchedulePath("hook-1"), sched); err != nil {
		t.Fatal(err)
	}
	w := destinationRequest(g, http.MethodPost, "/v1/hooks/hook-1", `{"events":[{"id":"h1","text":"push"}]}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("hook intake rejected: %d %s", w.Code, w.Body.String())
	}
	if got := g.spool.rt.HookPendingCount("hook-1"); got != 1 {
		t.Fatalf("payload not staged: %d", got)
	}
	// Non-hook schedules reject intake.
	plain := sched
	plain.ScheduleID = "plain-1"
	plain.Probe = &WatchProbe{Kind: "http", URL: "https://example.com"}
	if err := atomicJSON(g.spool.rt.SchedulePath("plain-1"), plain); err != nil {
		t.Fatal(err)
	}
	w = destinationRequest(g, http.MethodPost, "/v1/hooks/plain-1", `{"events":[]}`)
	if w.Code == http.StatusAccepted {
		t.Fatal("intake accepted a non-hook schedule")
	}
	// Unauthorized callers never reach intake.
	r := httptest.NewRequest(http.MethodPost, "/v1/hooks/hook-1", strings.NewReader(`{"events":[]}`))
	w = httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated intake must 401, got %d", w.Code)
	}
	// Empty payloads are rejected.
	if w = destinationRequest(g, http.MethodPost, "/v1/hooks/hook-1", ""); w.Code == http.StatusAccepted {
		t.Fatal("empty payload accepted")
	}
	// GET is not intake.
	if w = destinationRequest(g, http.MethodGet, "/v1/hooks/hook-1", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET must 405, got %d", w.Code)
	}
	// A foreign principal's hook schedule stays invisible.
	bob := sched
	bob.ScheduleID = "bob-hook"
	bob.Envelope = identity.TelegramEnvelope("bob", 22, "bob", "policy-1")
	bob.UserID = "bob"
	if err := atomicJSON(g.spool.rt.SchedulePath("bob-hook"), bob); err != nil {
		t.Fatal(err)
	}
	if w = destinationRequest(g, http.MethodPost, "/v1/hooks/bob-hook", `{"events":[]}`); w.Code != http.StatusNotFound {
		t.Fatalf("foreign hook must 404, got %d", w.Code)
	}
}

// SPEC-0081 R4.1 — a filter that fails to evaluate must not silently admit
// the event: the occurrence settles failed, the event stays unseen for a
// later retry, and nothing reaches the sink.
func TestEventFilterErrorFailsClosed(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-failclosed", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("keep")`}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, _, _ string, _ bool, _ []string) (string, error) {
		return "", errors.New("script runtime exploded")
	}
	now := time.Now().UTC()
	body := []byte(`{"events":[{"id":"x1","text":"boss pinged you"}]}`)
	o.ScheduleID = "str-failclosed"
	o.Job.Trigger = "cron"
	o.State = "probing"
	if err := atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}
	s.settleEvents(o, sched, body, now, nil, g.authorizeOccurrence)
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("filter error admitted the event: %+v", d)
	}
	st, err := s.EventStateOf("str-failclosed")
	if err != nil {
		t.Fatal(err)
	}
	if st.seen("x1") {
		t.Fatal("filter error must not mark the event seen — it would never retry")
	}
	if st.LastError == "" {
		t.Fatal("filter error must land on the durable state for /watches")
	}
	raw, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var live RoutineOccurrence
	if err := json.Unmarshal(raw, &live); err != nil || live.State != "failed" {
		t.Fatalf("filter error must settle the occurrence failed: %+v %v", live, err)
	}
}

// SPEC-0081 R4.1 — the per-event filter performs I/O (the script runs in a
// disposable runtime over HTTP); holding the spool lock across it starves
// every other writer for the whole script duration.
func TestEventFilterRunsOutsideSpoolLock(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-lockfree", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("keep")`}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, _, _ string, _ bool, _ []string) (string, error) {
		once.Do(func() { close(started) })
		<-release
		return "keep", nil
	}
	now := time.Now().UTC()
	body := []byte(`{"events":[{"id":"y1","text":"deploy finished"}]}`)
	o.ScheduleID = "str-lockfree"
	o.Job.Trigger = "cron"
	o.State = "probing"
	if err := atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
		t.Fatal(err)
	}
	settled := make(chan struct{})
	go func() {
		s.settleEvents(o, sched, body, now, nil, g.authorizeOccurrence)
		close(settled)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("filter script never ran")
	}
	read := make(chan struct{})
	go func() {
		_, _ = s.EventStateOf("str-lockfree")
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(2 * time.Second):
		t.Fatal("spool lock held across the filter script")
	}
	close(release)
	select {
	case <-settled:
	case <-time.After(3 * time.Second):
		t.Fatal("settle never finished")
	}
	if d, _ := s.ClaimDelivery(); d == nil {
		t.Fatal("kept event must deliver after the filter releases")
	}
}

// SPEC-0081 R4.1 — an uncompilable filter is rejected at create time so the
// schedule never persists a gate that can only fail-open at poll time.
func TestEventFilterValidationAtCreate(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	mk := func(id, filter string) Schedule {
		return Schedule{ScheduleID: id, Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: filter}}
	}
	if _, err := s.CreateSchedule(mk("f-good", `emit("keep")`), caller, ""); err != nil {
		t.Fatalf("valid filter rejected: %v", err)
	}
	if _, err := s.CreateSchedule(mk("f-bad", `emit("keep"`), caller, ""); err == nil {
		t.Fatal("uncompilable filter accepted at create")
	}
	if _, err := s.CreateSchedule(mk("f-empty", ""), caller, ""); err != nil {
		t.Fatalf("absent filter must stay valid: %v", err)
	}
}

// SPEC-0081 R4.1 — stream delivery applies the same per-event gate as poll
// delivery: a dropped event is marked seen without reaching the sink.
func TestStreamPollOnceAppliesFilter(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "strf", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("drop" if "noise" in input() else "keep")`}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, _, input string, _ bool, _ []string) (string, error) {
		if strings.Contains(input, "noise") {
			return "drop", nil
		}
		return "keep", nil
	}
	toolCall := func(context.Context, string, *WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"n1","text":"noise spam"},{"id":"k1","text":"boss pinged you"}]}`), nil
	}
	if _, blocked := g.streamPollOnce(context.Background(), sched, toolCall); blocked {
		t.Fatal("healthy filtered poll reported blocked")
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("kept stream event must deliver: %+v %v", d, err)
	}
	if strings.Contains(d.Text, "noise") {
		t.Fatalf("dropped event must not reach the sink: %q", d.Text)
	}
	st, _ := s.EventStateOf("strf")
	if !st.seen("n1") {
		t.Fatal("dropped stream event must be marked seen")
	}
}

// SPEC-0081 R4.1 — a stream filter that cannot evaluate fails closed:
// events stay unseen for a retry and the round reports blocked so the loop
// paces instead of hot-spinning.
func TestStreamPollOnceFilterErrorBlocks(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "strfe", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("keep")`}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, _, _ string, _ bool, _ []string) (string, error) {
		return "", errors.New("script runtime exploded")
	}
	toolCall := func(context.Context, string, *WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"e1","text":"boss pinged you"}]}`), nil
	}
	if _, blocked := g.streamPollOnce(context.Background(), sched, toolCall); !blocked {
		t.Fatal("failed filter must block the round")
	}
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("filter error admitted a stream event: %+v", d)
	}
	st, err := s.EventStateOf("strfe")
	if err != nil {
		t.Fatal(err)
	}
	if st.seen("e1") {
		t.Fatal("filter error must not mark the stream event seen")
	}
	if st.LastError == "" {
		t.Fatal("filter error must land on the durable state for /watches")
	}
}
