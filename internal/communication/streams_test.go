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
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "str-filter", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("drop" if "noise" in input else "keep")`}}, caller, "")
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
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "strf", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch dms", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-tg-wait-abc", Filter: `emit("drop" if "noise" in input else "keep")`}}, caller, "")
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
