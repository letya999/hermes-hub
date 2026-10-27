package communication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// streamSourceConcurrency bounds the parallel wait/poll calls one
// multi-source round holds open — beyond it, remaining inputs run in the
// next rounds' slots (the subscription still merges one wake per round).
const streamSourceConcurrency = 8

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

// streamMinInterval is the probe's declared source cadence: seconds between
// stream rounds for a poll-style tool. Wait-style tools pace themselves.
func streamMinInterval(probe *WatchProbe) time.Duration {
	if probe == nil {
		return 0
	}
	return time.Duration(probe.MinIntervalSeconds) * time.Second
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
			// The source's declared cadence (probe.min_interval_seconds)
			// paces plain polls; wait-style tools hold the call open and
			// carry no interval, so they fall through to the next round.
			if interval := streamMinInterval(sched.Probe); interval > 0 {
				timer := time.NewTimer(interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
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
	return streamProbeWithin(probe, 30*time.Second)
}

// streamProbeWithin clamps owner-declared wait bounds so the connector call
// returns before the caller's budget expires: a bound at or past the budget
// turns every round into a context deadline and the subscription goes silent.
func streamProbeWithin(probe *WatchProbe, budget time.Duration) *WatchProbe {
	cap := float64(budget/time.Second) - 5
	p := *probe
	args := make(map[string]any, len(probe.Args)+1)
	for k, v := range probe.Args {
		args[k] = v
	}
	p.Args = args
	if strings.Contains(p.Tool, "settled") {
		if _, ok := p.Args["max_wait_ms"]; !ok {
			p.Args["max_wait_ms"] = 20000
		} else if v := argSeconds(p.Args["max_wait_ms"]); v > cap*1000 {
			p.Args["max_wait_ms"] = int(cap * 1000)
		}
		return &p
	}
	if strings.Contains(p.Tool, "wait") || strings.Contains(p.Tool, "new_message") {
		if _, ok := p.Args["timeout"]; !ok {
			p.Args["timeout"] = 20
		} else if v := argSeconds(p.Args["timeout"]); v > cap {
			p.Args["timeout"] = int(cap)
		}
	}
	return &p
}

// argSeconds reads a numeric wait bound from either JSON (float64) or a
// literal (int) argument; non-numeric values return 0 and pass through.
func argSeconds(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

// sourceLabel identifies one fan-out input for state and attribution: the
// declared name, else its position — stable across restarts because the
// sources list is part of the probe revision.
func sourceLabel(i int, src WatchProbeSource) string {
	if src.Name != "" {
		return src.Name
	}
	return "s" + strconv.Itoa(i)
}

// probeForSource merges one fan-out input onto the probe: the shared Args
// are the base, the source's own Args override keys, and a source Tool
// narrows to a sibling read of the same backend.
func probeForSource(probe *WatchProbe, src WatchProbeSource) *WatchProbe {
	p := *probe
	if src.Tool != "" {
		p.Tool = src.Tool
	}
	args := make(map[string]any, len(probe.Args)+len(src.Args))
	for k, v := range probe.Args {
		args[k] = v
	}
	for k, v := range src.Args {
		args[k] = v
	}
	p.Args = args
	p.Sources = nil
	return &p
}

// sourceResult is one fan-out input's round outcome, labelled for
// per-source cursor, error and event attribution.
type sourceResult struct {
	label string
	body  []byte
	err   error
}

// sampleSourceReport is one fan-out input's bounded look at what the
// connector emits: up to four events with their raw connector JSON, the
// source's own error when it failed, and the candidate filter's verdict
// per event id. watch_sample returns these without touching dedup or the
// sink — inspecting shape must never wake the agent or consume events.
type sampleSourceReport struct {
	Source   string            `json:"source"`
	Events   []sampleEvent     `json:"events,omitempty"`
	Error    string            `json:"error,omitempty"`
	Verdicts map[string]string `json:"verdicts,omitempty"`
}

type sampleEvent struct {
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
	Raw  string `json:"raw,omitempty"`
}

type watchSample struct {
	ScheduleID string               `json:"schedule_id"`
	Sources    []sampleSourceReport `json:"sources"`
}

// probeRound runs one bounded round over the probe's acquisition inputs:
// a single unlabelled result for a plain probe, or one result per source
// under bounded concurrency for probe.sources. The caller's toolCall sets
// the per-call budget and wait-bound injection.
func (g *Gateway) probeRound(ctx context.Context, schedule Schedule, toolCall func(context.Context, string, *WatchProbe) ([]byte, error)) []sourceResult {
	probe := schedule.Probe
	if len(probe.Sources) == 0 {
		body, err := toolCall(ctx, schedule.Envelope.PrincipalID, probe)
		return []sourceResult{{body: body, err: err}}
	}
	results := make([]sourceResult, len(probe.Sources))
	var wg sync.WaitGroup
	slots := make(chan struct{}, streamSourceConcurrency)
	for i, src := range probe.Sources {
		results[i].label = sourceLabel(i, src)
		wg.Add(1)
		go func(i int, src WatchProbeSource) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			body, err := toolCall(ctx, schedule.Envelope.PrincipalID, probeForSource(probe, src))
			results[i].body, results[i].err = body, err
		}(i, src)
	}
	wg.Wait()
	return results
}

// sampleRoutine is the owner-facing read behind POST /v1/routines/{id}
// /sample and watch_sample: one bounded probe round through the caller's
// toolCall, returning each source's event shape (id/text/raw) plus the
// watch filter's verdict per event id. It carries no durable side
// effects: no seen-set writes, no cursor moves, no sink admission.
func (g *Gateway) sampleRoutine(ctx context.Context, id string, caller identity.Envelope, filterOverride string, toolCall func(context.Context, string, *WatchProbe) ([]byte, error)) (watchSample, error) {
	sched, err := g.spool.scheduleByID(id)
	if err != nil {
		return watchSample{}, err
	}
	if sched.Envelope.PrincipalID != caller.PrincipalID {
		return watchSample{}, ErrRoutineNotOwned
	}
	bounded := func(ctx context.Context, principal string, probe *WatchProbe) ([]byte, error) {
		return toolCall(ctx, principal, streamProbeWithin(probe, 15*time.Second))
	}
	results := g.probeRound(ctx, sched, bounded)
	out := watchSample{ScheduleID: id, Sources: make([]sampleSourceReport, 0, len(results))}
	probe := sched.Probe
	filter := probe.Filter
	if filterOverride != "" {
		filter = filterOverride
	}
	job := Job{Envelope: sched.Envelope, OrganizationID: sched.OrganizationID, UserID: sched.UserID, ActorID: sched.ActorID, ScopeID: sched.ScopeID, Channel: sched.Channel, ChatID: sched.ChatID, TaskID: sched.TaskID, TopicID: sched.TopicID, SlackChannel: sched.SlackChannel, SlackThread: sched.SlackThread, Text: sched.Input, Trigger: "cron", CreatedAt: g.now().UTC()}
	proxy := RoutineOccurrence{ScheduleID: id, DueAt: job.CreatedAt, Job: job}
	for _, res := range results {
		rep := sampleSourceReport{Source: res.label}
		if res.err != nil {
			rep.Error = boundedWatchText(res.err.Error())
			out.Sources = append(out.Sources, rep)
			continue
		}
		events, _ := extractEvents(probe, res.body)
		for _, ev := range events {
			rep.Events = append(rep.Events, sampleEvent{ID: ev.ID, Text: ev.Text, Raw: ev.Raw})
			if filter != "" && g.spool.probeScript != nil {
				if rep.Verdicts == nil {
					rep.Verdicts = map[string]string{}
				}
				verdict, _ := g.spool.probeScript(ctx, proxy, filter, filterInput(ev), false, nil)
				rep.Verdicts[ev.ID] = strings.TrimSpace(verdict)
			}
		}
		out.Sources = append(out.Sources, rep)
	}
	return out, nil
}

// streamPollOnce runs one bounded round through the probe path and settles
// the batch; overflowed events get a best-effort requeue call that carries
// only the connector-native identifier, never owner content (R5.3). With
// probe.sources the round fans out over every input under a bounded
// concurrency and still merges one settle — dedup and the filter see each
// event scoped by its source label so connector id namespaces never
// collide across inputs.
func (g *Gateway) streamPollOnce(ctx context.Context, schedule Schedule, toolCall func(context.Context, string, *WatchProbe) ([]byte, error)) ([]Event, bool) {
	results := g.probeRound(ctx, schedule, toolCall)
	overflowed, blocked := g.spool.settleStreamResults(schedule, results, g.now().UTC(), g.authorizeOccurrence)
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

// settleStreamEvents is the single-probe settle: the unlabelled result
// keeps legacy unscoped event ids and the flat Cursor field.
func (s *Spool) settleStreamEvents(schedule Schedule, body []byte, now time.Time, probeErr error, authorize func(Job) error) ([]Event, bool) {
	return s.settleStreamResults(schedule, []sourceResult{{body: body, err: probeErr}}, now, authorize)
}

// settleStreamResults settles one stream round across every fan-out input
// (or the single unlabelled probe, when label is empty — keeping legacy
// unscoped event ids and the flat Cursor field for old state files).
// Events dedupe per source label, cursor and error state are written per
// source, and the round still merges into one filter pass and one sink
// admission — multi-source batches cost one wake, not one per input. A
// dead input is isolated to its own SourceErrors entry; only a round where
// every input failed blocks the loop.
func (s *Spool) settleStreamResults(schedule Schedule, results []sourceResult, now time.Time, authorize func(Job) error) ([]Event, bool) {
	s.mu.Lock()
	live, err := s.loadScheduleLocked(schedule.ScheduleID)
	if err != nil || live.Revision != schedule.Revision || !streamActive(&live, now) {
		s.mu.Unlock()
		return nil, true
	}
	st, err := s.loadEventStateLocked(schedule.ScheduleID)
	if err != nil {
		s.mu.Unlock()
		return nil, true
	}
	failed := 0
	var firstErr error
	for _, res := range results {
		if res.err != nil {
			failed++
			if firstErr == nil {
				firstErr = res.err
			}
		}
	}
	if failed == len(results) {
		st.LastError = boundedWatchText(firstErr.Error())
		if authError(firstErr) {
			st.Degraded = true
		}
		_ = atomicJSON(s.eventPath(schedule.ScheduleID), st)
		s.mu.Unlock()
		return nil, true
	}
	job := Job{Envelope: schedule.Envelope, OrganizationID: schedule.OrganizationID, UserID: schedule.UserID, ActorID: schedule.ActorID, ScopeID: schedule.ScopeID, Channel: schedule.Channel, ChatID: schedule.ChatID, TaskID: schedule.TaskID, TopicID: schedule.TopicID, SlackChannel: schedule.SlackChannel, SlackThread: schedule.SlackThread, Text: schedule.Input, Trigger: "cron", CreatedAt: now.UTC()}
	if authorize != nil && authorize(job) != nil {
		st.LastError = "routine owner unavailable"
		_ = atomicJSON(s.eventPath(schedule.ScheduleID), st)
		s.mu.Unlock()
		return nil, true
	}
	var env struct {
		Cursor string `json:"next_cursor"`
	}
	sourceErrs := map[string]string{}
	sourceCursors := map[string]string{}
	cursor := ""
	events := []Event{}
	for _, res := range results {
		if res.err != nil {
			sourceErrs[res.label] = boundedWatchText(res.err.Error())
			if authError(res.err) {
				st.Degraded = true
			}
			continue
		}
		extracted, exErr := extractEvents(schedule.Probe, res.body)
		if exErr != nil {
			s.mu.Unlock()
			return nil, true
		}
		if json.Unmarshal(res.body, &env) == nil && env.Cursor != "" {
			if res.label == "" {
				cursor = env.Cursor
			} else {
				sourceCursors[res.label] = env.Cursor
			}
		}
		for _, ev := range extracted {
			if res.label != "" {
				ev.Source = res.label
				ev.ID = res.label + "|" + ev.ID
			}
			events = append(events, ev)
		}
	}
	if len(events) == 0 {
		// A healthy empty poll still proves liveness for /watches.
		st.LastSuccess = now
		st.LastError = ""
		_ = atomicJSON(s.eventPath(schedule.ScheduleID), st)
		s.mu.Unlock()
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
	sourceID := schedule.ScheduleID
	if schedule.ProbeRevision > 0 {
		sourceID += "-" + strconv.FormatUint(schedule.ProbeRevision, 10)
	}
	seed := "stream-" + sourceID + "-" + watchHash(strings.Join(ids, "|"))[:16]
	job.ID = seed
	job.IdempotencyKey = seed
	s.mu.Unlock()

	// The stream path applies the same deterministic per-event gate as the
	// occurrence path (SPEC-0081 R4.1), outside the spool lock — the script
	// runs in the disposable runtime under a synthetic occurrence carrying
	// the owner's envelope. A filter that cannot evaluate fails closed:
	// events stay unseen and the round reports blocked so the loop paces.
	var verdicts map[string]string
	var filterErr error
	if live.Probe != nil && live.Probe.Filter != "" && s.probeScript != nil {
		verdicts = make(map[string]string, len(fresh))
		proxy := RoutineOccurrence{ScheduleID: live.ScheduleID, DueAt: now, Job: job}
		for _, ev := range fresh {
			verdict, verr := s.probeScript(context.Background(), proxy, live.Probe.Filter, filterInput(ev), false, nil)
			if verr != nil {
				filterErr = verr
				break
			}
			verdicts[ev.ID] = strings.TrimSpace(verdict)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// The schedule may have been paused or re-probed while the filter ran;
	// re-validate before admitting anything to the sink.
	live, err = s.loadScheduleLocked(schedule.ScheduleID)
	if err != nil || live.Revision != schedule.Revision || !streamActive(&live, now) {
		return nil, true
	}
	st, err = s.loadEventStateLocked(schedule.ScheduleID)
	if err != nil {
		return nil, true
	}
	st.Cursor = cursor
	for label, c := range sourceCursors {
		if st.SourceCursors == nil {
			st.SourceCursors = map[string]string{}
		}
		st.SourceCursors[label] = c
	}
	if len(sourceErrs) > 0 {
		st.SourceErrors = sourceErrs
	} else {
		st.SourceErrors = nil
	}
	if filterErr != nil {
		st.LastError = boundedWatchText(filterErr.Error())
		_ = atomicJSON(s.eventPath(schedule.ScheduleID), st)
		return nil, true
	}
	kept := make([]Event, 0, len(fresh))
	for _, ev := range fresh {
		if st.seen(ev.ID) {
			continue
		}
		if verdicts[ev.ID] == "drop" {
			st.markSeen(ev.ID, now)
			continue
		}
		kept = append(kept, ev)
	}
	overflowed := s.fanoutEventsLocked(&st, schedule, job, kept, seed, schedule.ScheduleID, now)
	_ = atomicJSON(s.eventPath(schedule.ScheduleID), st)
	return overflowed, false
}
