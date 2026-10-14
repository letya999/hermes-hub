package communication

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("stream batch admitted no delivery: %+v %v", d, err)
	}
	if !strings.Contains(d.Text, "Danil") || !strings.HasPrefix(d.Text, "watch dms") {
		t.Fatalf("stream delivery text: %q", d.Text)
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatal("one burst produced more than one delivery")
	}
	// Same batch replayed: deduped by the durable seen-set.
	s.settleStreamEvents(sched, body, now.Add(time.Second), nil, g.authorizeOccurrence)
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
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
