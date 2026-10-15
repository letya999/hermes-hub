package communication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// streamSource reloads the schedule each poll round so pause, snooze,
// delete or owner changes stop the loop on live durable state.
func (g *Gateway) streamSource(id string) (Schedule, error) {
	sched, err := g.spool.scheduleByID(id)
	if err != nil {
		return Schedule{}, err
	}
	if !streamActive(&sched, g.now().UTC()) {
		return Schedule{}, errors.New("stream source no longer active")
	}
	return sched, nil
}

// streamActive is the eligibility rule a running loop re-checks each round:
// still an event schedule in stream delivery with its probe intact,
// enabled, unpaused and unsnoozed.
func streamActive(s *Schedule, now time.Time) bool {
	return s.JobKind == "event" && s.Delivery == "stream" && s.Enabled && !s.Paused &&
		s.Probe != nil && s.Probe.Kind == "mcp_tool" && !now.Before(s.SnoozedUntil)
}

// scheduleByID loads one schedule for hub-internal supervision; the
// caller-ownership filter belongs to the API surface, not the loop.
func (s *Spool) scheduleByID(id string) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadScheduleLocked(id)
}

// streamBackoff paces consecutive failures 1s → 30s so a dead connector
// costs one bounded retry at a time for the subscription's whole life.
func streamBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > 6 {
		failures = 6
	}
	return min(time.Second<<(failures-1), 30*time.Second)
}

// streamPoll keeps one bounded wait-call outstanding for a subscription:
// every returned batch settles into the durable pipeline; cancellation or
// the source going inactive ends the loop.
func (g *Gateway) streamPoll(ctx context.Context, id string, toolCall func(context.Context, string, *WatchProbe) ([]byte, error)) {
	failures := 0
	for ctx.Err() == nil {
		sched, err := g.streamSource(id)
		if err != nil {
			return
		}
		_, blocked := g.streamPollOnce(ctx, sched, toolCall)
		if ctx.Err() != nil {
			return
		}
		if !blocked {
			failures = 0
			continue
		}
		failures++
		timer := time.NewTimer(streamBackoff(failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// streamRun is one supervised poll loop: its cancel plus a done edge the
// supervisor uses to join cleanly on unwind.
type streamRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// startStreamLoops is the gateway's stream-supervision seam: it spawns the
// supervisor bound to the real tool-call path and returns its done edge so
// Run can join it on shutdown.
func (g *Gateway) startStreamLoops(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.streamSupervisor(ctx, g.streamToolCall)
	}()
	return done
}

// streamSupervisor keeps a streamPoll running per live stream schedule:
// each reconcile pass prunes exited loops, cancels sources that stopped
// being live streams, and spawns loops for newly active subscriptions.
// Cancellation unwinds every running poll.
func (g *Gateway) streamSupervisor(ctx context.Context, toolCall func(context.Context, string, *WatchProbe) ([]byte, error)) {
	running := map[string]*streamRun{}
	unwind := func() {
		for _, run := range running {
			run.cancel()
		}
		for _, run := range running {
			<-run.done
		}
	}
	defer unwind()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		active := g.streamSchedules()
		for id, run := range running {
			select {
			case <-run.done:
				delete(running, id)
			default:
				if !active[id] {
					run.cancel()
				}
			}
		}
		for id := range active {
			if running[id] != nil {
				continue
			}
			pollCtx, cancel := context.WithCancel(ctx)
			run := &streamRun{cancel: cancel, done: make(chan struct{})}
			running[id] = run
			go func(id string, run *streamRun) {
				defer close(run.done)
				g.streamPoll(pollCtx, id, toolCall)
			}(id, run)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// streamSchedules is the supervisor's scan of the live stream set.
func (g *Gateway) streamSchedules() map[string]bool {
	return g.spool.streamIDs(g.now().UTC())
}

// streamIDs lists every schedule currently eligible for a stream loop:
// the reconcile pass diffed against the running set.
func (s *Spool) streamIDs(now time.Time) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(s.root, "schedules"))
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		item, err := s.loadScheduleLocked(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		if streamActive(&item, now) {
			out[item.ScheduleID] = true
		}
	}
	return out
}

// streamToolCall runs one stream call under a 30s budget (bounded wait
// tools hold the call near a batch) with the wait bound injected.
func (g *Gateway) streamToolCall(ctx context.Context, principalID string, probe *WatchProbe) ([]byte, error) {
	return g.toolCallProbeWithin(ctx, principalID, streamProbe(probe), 30*time.Second)
}

// streamProbe returns the probe with a wait bound inside the call budget
// when the subscriber gave none: connectors expose "wait"-style tools that
// honor caller bounds, so the loop stays near-instant without overshooting
// the probe timeout. Unknown tools pass through untouched.
func streamProbe(probe *WatchProbe) *WatchProbe {
	if probe == nil {
		return nil
	}
	p := *probe
	args := make(map[string]any, len(probe.Args)+1)
	for k, v := range probe.Args {
		args[k] = v
	}
	p.Args = args
	if strings.Contains(p.Tool, "settled") {
		if _, ok := p.Args["max_wait_ms"]; !ok {
			p.Args["max_wait_ms"] = 20000
		}
		return &p
	}
	if strings.Contains(p.Tool, "wait") || strings.Contains(p.Tool, "new_message") {
		if _, ok := p.Args["timeout"]; !ok {
			p.Args["timeout"] = 20
		}
	}
	return &p
}

// streamPollOnce runs one bounded wait-call through the probe path and
// settles the batch; overflowed events get a best-effort requeue call that
// carries only the connector-native identifier, never owner content (R5.3).
func (g *Gateway) streamPollOnce(ctx context.Context, schedule Schedule, toolCall func(context.Context, string, *WatchProbe) ([]byte, error)) ([]Event, bool) {
	body, err := toolCall(ctx, schedule.Envelope.PrincipalID, schedule.Probe)
	overflowed, blocked := g.spool.settleStreamEvents(schedule, body, g.now().UTC(), err, g.authorizeOccurrence)
	for _, ev := range overflowed {
		if schedule.Probe == nil || schedule.Probe.RequeueTool == "" {
			break
		}
		args := ev.Requeue
		if len(args) == 0 {
			args = map[string]any{"event_id": ev.ID}
		}
		_, _ = toolCall(ctx, schedule.Envelope.PrincipalID, &WatchProbe{Kind: "mcp_tool", Tool: schedule.Probe.RequeueTool, Args: args})
	}
	return overflowed, blocked
}

// settleStreamEvents settles one stream poll batch without any occurrence:
// the stream loop owns the schedule end to end, so the durable record is
// the event state plus the emitted jobs/deliveries. Job and delivery ids
// are deterministic from the schedule id and the batch's event ids, which
// makes a replayed batch a no-op. Returns events the sink could not admit
// for best-effort connector requeue outside the lock, plus whether the
// round was blocked (probe error, lost authorization, or malformed body)
// so the loop can pace instead of hot-spinning.
func (s *Spool) settleStreamEvents(schedule Schedule, body []byte, now time.Time, probeErr error, authorize func(Job) error) ([]Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.loadEventStateLocked(schedule.ScheduleID)
	if err != nil {
		return nil, true
	}
	if probeErr != nil {
		st.LastError = boundedWatchText(probeErr.Error())
		if authError(probeErr) {
			st.Degraded = true
		}
		_ = atomicJSON(s.eventPath(schedule.ScheduleID), st)
		return nil, true
	}
	job := Job{Envelope: schedule.Envelope, OrganizationID: schedule.OrganizationID, UserID: schedule.UserID, ActorID: schedule.ActorID, ScopeID: schedule.ScopeID, Channel: schedule.Channel, ChatID: schedule.ChatID, TaskID: schedule.TaskID, TopicID: schedule.TopicID, SlackChannel: schedule.SlackChannel, SlackThread: schedule.SlackThread, Text: schedule.Input, Trigger: "cron", CreatedAt: now.UTC()}
	if authorize != nil && authorize(job) != nil {
		st.LastError = "routine owner unavailable"
		_ = atomicJSON(s.eventPath(schedule.ScheduleID), st)
		return nil, true
	}
	events, err := extractEvents(schedule.Probe, body)
	if err != nil {
		return nil, true
	}
	if len(events) == 0 {
		// A healthy empty poll still proves liveness for /watches.
		st.LastSuccess = now
		st.LastError = ""
		_ = atomicJSON(s.eventPath(schedule.ScheduleID), st)
		return nil, false
	}
	fresh := []Event{}
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ID)
		if !st.seen(ev.ID) {
			fresh = append(fresh, ev)
		}
	}
	seed := "stream-" + schedule.ScheduleID + "-" + watchHash(strings.Join(ids, "|"))[:16]
	job.ID = seed
	job.IdempotencyKey = seed
	overflowed := s.fanoutEventsLocked(&st, schedule, job, fresh, seed, schedule.ScheduleID, now)
	_ = atomicJSON(s.eventPath(schedule.ScheduleID), st)
	return overflowed, false
}
