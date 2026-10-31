package routines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// WatchState is the durable baseline a watcher compares against. It lives in
// spool/watchers/<scheduleID>.json and is the only thing the deterministic
// poll needs between ticks — no model sees an unchanged source.
type WatchState struct {
	ProbeRevision uint64    `json:"probe_revision,omitempty"`
	Generation    uint64    `json:"generation,omitempty"`
	ScheduleID    string    `json:"schedule_id"`
	Baseline      string    `json:"baseline"`
	Value         string    `json:"value"`
	PrevValue     string    `json:"prev_value,omitempty"`
	ChangedAt     time.Time `json:"changed_at"`
	LastSuccess   time.Time `json:"last_success"`
	LastError     string    `json:"last_error,omitempty"`
	// NotifyDay/NotifyCount implement the owner-timezone daily notification
	// cap (SPEC-0054 R2.2): the day rolls the counter back to zero.
	NotifyDay   string `json:"notify_day,omitempty"`
	NotifyCount int    `json:"notify_count,omitempty"`
}

// DigestState marks when the owner's watches were last aggregated.
type DigestState struct {
	Errors       map[string]string `json:"errors,omitempty"`
	Generations  map[string]uint64 `json:"generations,omitempty"`
	ScheduleID   string            `json:"schedule_id"`
	LastDigestAt time.Time         `json:"last_digest_at"`
}

func (e *Engine) DigestPath(id string) string {
	return filepath.Join(e.root, "digests", commodel.FileID(id)+".json")
}

func (e *Engine) WatchPath(id string) string {
	return filepath.Join(e.root, "watchers", commodel.FileID(id)+".json")
}

func (e *Engine) LoadWatchStateLocked(id string) (WatchState, error) {
	var st WatchState
	schedule, scheduleErr := e.LoadScheduleLocked(id)
	b, err := os.ReadFile(e.WatchPath(id))
	if os.IsNotExist(err) || (err == nil && json.Unmarshal(b, &st) == nil && scheduleErr == nil && st.ProbeRevision != schedule.ProbeRevision) {
		if schedule, err := e.LoadScheduleLocked(id); err == nil && schedule.InitialProbeValue != nil {
			value := *schedule.InitialProbeValue
			return WatchState{ScheduleID: id, ProbeRevision: schedule.ProbeRevision, Baseline: WatchHash(value), Value: value, LastSuccess: schedule.UpdatedAt}, nil
		}
		return WatchState{ScheduleID: id, ProbeRevision: schedule.ProbeRevision}, nil
	}
	if err != nil {
		return st, err
	}
	if json.Unmarshal(b, &st) != nil {
		return st, errors.New("invalid watch state")
	}
	return st, nil
}

// SeedWatchBaseline records the creation-time baseline a check schedule
// evaluates against — the world as it was when the user set the follow-up.
func (e *Engine) SeedWatchBaseline(id string, value string, now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.atomicJSON(e.WatchPath(id), WatchState{ScheduleID: id, Baseline: WatchHash(value), Value: value})
}

// taskProbeValueLocked resolves a task: probe under the schedule owner's
// principal — a foreign or missing id is an error, never a synthetic state.
func (e *Engine) TaskProbeValueLocked(principal, taskID string) (string, error) {
	task, err := e.deps.LoadTask(principal, taskID)
	if err != nil {
		return "", err
	}
	if task.Archived {
		return "closed", nil
	}
	return "open", nil
}

func WatchHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// ProbeDueOccurrences executes the deterministic probe for every occurrence
// the dispatcher moved to "probing", settles it and updates durable watch
// state. Fetches run outside the spool lock; each settle re-locks and
// re-validates the occurrence is still probing (crash-safe: a stale probing
// occurrence is probed again — delivery ids dedupe).
func (e *Engine) ProbeDueOccurrences(now time.Time, fetch func(context.Context, string) ([]byte, error)) error {
	return e.ProbeDueOccurrencesContext(context.Background(), now, nil, fetch, nil, nil)
}

func (e *Engine) ProbeDueOccurrencesContext(ctx context.Context, now time.Time, authorize func(commodel.Job) error, fetch func(context.Context, string) ([]byte, error), toolCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error), serviceCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error)) error {
	e.mu.Lock()
	e.resetAcquireBurstLocked()
	e.mu.Unlock()
	return e.ProcessProactive(ctx, "probing", func(o commodel.RoutineOccurrence) { e.probeOne(ctx, o, now, authorize, fetch, toolCall, serviceCall) })
}

func (e *Engine) probeOne(ctx context.Context, o commodel.RoutineOccurrence, now time.Time, authorize func(commodel.Job) error, fetch func(context.Context, string) ([]byte, error), toolCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error), serviceCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error)) {
	if o.RetryAfter.After(now) {
		return // bounded check retry waiting out its backoff (R1.1)
	}
	e.mu.Lock()
	schedule, err := e.LoadScheduleLocked(o.ScheduleID)
	current := e.OccurrenceCurrentLocked(o) && (authorize == nil || authorize(o.Job) == nil)
	e.mu.Unlock()
	if !current {
		o.State = "cancelled"
		_ = e.atomicJSON(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"), o)
		return
	}
	if err != nil || schedule.Probe == nil {
		return
	}
	receipt := filepath.Join(e.root, "occurrences", occurrenceID(o)+".probe-result")
	if body, err := os.ReadFile(receipt); err == nil {
		var result ProbeResult
		if json.Unmarshal(body, &result) != nil {
			return
		}
		e.mu.Lock()
		_ = e.ApplyProbeResultLocked(result)
		e.mu.Unlock()
		return
	} else if !os.IsNotExist(err) {
		return
	}
	if schedule.Probe.Kind == "command" {
		// File-state watcher: no fetch — the isolated script reads the
		// owner's mounted watch/ dir and emits the compared value (SPEC-0059).
		if e.probeScript() == nil {
			e.SettleProbe(o, schedule, "", "", now, errors.New("command probe runtime unavailable"), authorize)
			return
		}
		needs, needsErr := prefetchNeeds(ctx, o.Job.PrincipalID, schedule.Probe, toolCall)
		if needsErr != nil {
			e.SettleProbe(o, schedule, "", "", now, needsErr, authorize)
			return
		}
		value, valueErr := e.probeScript()(ctx, o, schedule.Probe.Script, "", true, needs)
		e.SettleProbe(o, schedule, value, WatchHash(value), now, valueErr, authorize)
		return
	}
	var body []byte
	var fetchErr error
	if strings.HasPrefix(schedule.Probe.URL, "task:") {
		// Hub-internal predicate: the compared value is the owner's task
		// state, resolved under the schedule owner's principal (R1.3).
		e.mu.Lock()
		state, taskErr := e.TaskProbeValueLocked(o.Job.PrincipalID, strings.TrimPrefix(schedule.Probe.URL, "task:"))
		e.mu.Unlock()
		if taskErr != nil {
			e.SettleProbe(o, schedule, "", "", now, taskErr, authorize)
			return
		}
		body = []byte(state)
	} else if schedule.Probe.Kind == "mcp_tool" {
		// ToolHub probe: credentials never leave the connector workload —
		// the hub only sees the flattened tool text (SPEC-0054 R3.2).
		if toolCall == nil {
			fetchErr = errors.New("tool-call probe path unavailable")
		} else {
			body, fetchErr = toolCall(ctx, o.Job.PrincipalID, schedule.Probe)
		}
	} else if schedule.Probe.Kind == "hook" {
		// Inbound hook (SPEC-0081 R5.1): the compared body is a staged
		// inbox payload written durably by IntakeHook — no fetch happens.
		// An empty inbox is a normal idle tick, not a probe failure.
		e.mu.Lock()
		body = e.popHookLocked(o.ScheduleID)
		e.mu.Unlock()
		if len(body) == 0 {
			if o.JobKind == "event" {
				// Event settlement still runs once so the occurrence
				// terminates — with a well-formed empty envelope.
				body = []byte(`{"events":[]}`)
			} else {
				e.SettleHookIdle(o, now, authorize)
				return
			}
		}
	} else if schedule.Probe.Kind == "render" || schedule.Probe.Kind == "service" {
		// Registered-service probe: an operator-declared endpoint (external
		// renderer, parser API) answers for the probe; the response body
		// feeds the normal extract chain (SPEC-0059 R5.4/R5.5).
		if serviceCall == nil {
			fetchErr = errors.New("service probe path unavailable")
		} else {
			body, fetchErr = serviceCall(ctx, o.Job.PrincipalID, schedule.Probe)
		}
	} else {
		// Shared acquisition (SPEC-0081): a fresh pooled body serves
		// same-principal subscribers without a second fetch.
		e.mu.Lock()
		if loc, lerr := time.LoadLocation(schedule.Timezone); lerr == nil {
			if next, nerr := NextSchedule(schedule, loc, now); nerr == nil {
				e.AcquireSubscribeLocked(o.Job.PrincipalID, schedule.Probe.URL, schedule.ScheduleID, next.Sub(now), now)
			}
		}
		key := acquireKey(o.Job.PrincipalID, schedule.Probe.URL)
		cached, hit := e.AcquirePeekLocked(o.Job.PrincipalID, schedule.Probe.URL, now)
		blocked := false
		var flight *acquireFlight
		leader := false
		if !hit {
			if st, aerr := e.loadAcquireLocked(o.Job.PrincipalID, schedule.Probe.URL); aerr == nil && st.NextPollAt.After(now) {
				blocked = true
			} else if f, ok := e.acquireInflight[key]; ok {
				flight = f
			} else {
				if e.acquireInflight == nil {
					e.acquireInflight = map[string]*acquireFlight{}
				}
				flight = &acquireFlight{done: make(chan struct{})}
				e.acquireInflight[key] = flight
				leader = true
			}
		}
		e.mu.Unlock()
		switch {
		case hit:
			body = cached
		case blocked:
			fetchErr = errors.New("source is in retry-after backoff")
		case !leader:
			// Another occurrence is already fetching this source for this
			// sweep: wait for it, then serve the burst body or its error —
			// the pool never issues a second fetch (SPEC-0081 R2.1).
			<-flight.done
			e.mu.Lock()
			if b, ok := e.acquireBurst[key]; ok {
				body = b
			} else if fetchErr = flight.err; fetchErr == nil {
				fetchErr = errors.New("shared acquisition fetch failed")
			}
			e.mu.Unlock()
		default:
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			body, fetchErr = fetch(ctx, schedule.Probe.URL)
			cancel()
			e.mu.Lock()
			flight.err = fetchErr
			if fetchErr == nil {
				_ = e.AcquireRecordLocked(o.Job.PrincipalID, schedule.Probe.URL, body, now)
			} else {
				var ra *RetryAfterError
				if errors.As(fetchErr, &ra) {
					e.AcquireBackoffLocked(o.Job.PrincipalID, schedule.Probe.URL, now.Add(ra.Delay), now)
				}
			}
			if e.acquireInflight[key] == flight {
				delete(e.acquireInflight, key)
			}
			close(flight.done)
			e.mu.Unlock()
		}
	}
	if o.JobKind == "event" {
		for _, ev := range e.deps.SettleEvents(o, schedule, body, now, fetchErr, authorize) {
			// Best-effort requeue to the connector: the call carries only
			// the event's native identifier, never owner content (R5.3).
			if toolCall != nil && schedule.Probe.RequeueTool != "" {
				args := ev.Requeue
				if len(args) == 0 {
					args = map[string]any{"event_id": ev.ID}
				}
				_, _ = toolCall(ctx, o.Job.PrincipalID, &commodel.WatchProbe{Kind: "mcp_tool", Tool: schedule.Probe.RequeueTool, Args: args})
			}
		}
		return
	}
	if fetchErr != nil {
		e.SettleProbe(o, schedule, "", "", now, fetchErr, authorize)
		return
	}
	if schedule.Probe.Kind == "script" {
		// Isolated-script probe: the fetched body is the script's input; its
		// single emitted value is the compared watch value (SPEC-0059).
		if e.probeScript() == nil {
			e.SettleProbe(o, schedule, "", "", now, errors.New("script probe runtime unavailable"), authorize)
			return
		}
		input := body
		if len(schedule.Probe.Extract) > 0 {
			fragment, extractErr := ExtractValue(schedule.Probe, body)
			if extractErr != nil {
				e.SettleProbe(o, schedule, "", "", now, extractErr, authorize)
				return
			}
			input = []byte(fragment)
		}
		needs, needsErr := prefetchNeeds(ctx, o.Job.PrincipalID, schedule.Probe, toolCall)
		if needsErr != nil {
			e.SettleProbe(o, schedule, "", "", now, needsErr, authorize)
			return
		}
		value, valueErr := e.probeScript()(ctx, o, schedule.Probe.Script, string(input), false, needs)
		e.SettleProbe(o, schedule, value, WatchHash(value), now, valueErr, authorize)
		return
	}
	value, valueErr := ExtractValue(schedule.Probe, body)
	if valueErr != nil {
		e.SettleProbe(o, schedule, "", "", now, valueErr, authorize)
		return
	}
	e.SettleProbe(o, schedule, value, WatchHash(value), now, nil, authorize)
}

// settleProbe persists the tick outcome: silent baseline seed, no_change,
// changed + one delivery, or failed — and lands the occurrence in a terminal
// state. The watch state write and the occurrence write share the lock.
type ProbeResult struct {
	Watch      WatchState                 `json:"watch"`
	Occurrence commodel.RoutineOccurrence `json:"occurrence"`
	Delivery   *commodel.Delivery         `json:"delivery,omitempty"`
}

func (e *Engine) ApplyProbeResultLocked(result ProbeResult) error {
	o := result.Occurrence
	if !e.OccurrenceCurrentLocked(o) {
		o.State = "cancelled"
		return e.atomicJSON(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"), o)
	}
	if result.Delivery != nil {
		if err := e.deps.EnqueueDelivery(*result.Delivery); err != nil {
			return err
		}
	}
	live, err := e.LoadWatchStateLocked(o.ScheduleID)
	if err != nil {
		return err
	}
	if live.Generation <= result.Watch.Generation {
		if err := e.atomicJSON(e.WatchPath(o.ScheduleID), result.Watch); err != nil {
			return err
		}
	}
	return e.atomicJSON(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"), o)
}

// SettleHookIdle lands an empty-inbox hook tick as no_change: there was no
// staged payload to compare — never a failure, never a notification
// (SPEC-0081 R5.1).
func (e *Engine) SettleHookIdle(o commodel.RoutineOccurrence, now time.Time, authorize func(commodel.Job) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	path := filepath.Join(e.root, "occurrences", occurrenceID(o)+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var live commodel.RoutineOccurrence
	if json.Unmarshal(body, &live) != nil || live.State != "probing" {
		return
	}
	if !e.OccurrenceCurrentLocked(live) || (authorize != nil && authorize(live.Job) != nil) {
		live.State = "cancelled"
		_ = e.atomicJSON(path, live)
		return
	}
	live.State = "no_change"
	_ = e.atomicJSON(path, live)
}

func (e *Engine) SettleProbe(o commodel.RoutineOccurrence, schedule commodel.Schedule, value, hash string, now time.Time, probeErr error, authorize func(commodel.Job) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	path := filepath.Join(e.root, "occurrences", occurrenceID(o)+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var live commodel.RoutineOccurrence
	if json.Unmarshal(body, &live) != nil || live.State != "probing" {
		return
	}
	if !e.OccurrenceCurrentLocked(live) || (authorize != nil && authorize(live.Job) != nil) {
		live.State = "cancelled"
		_ = e.atomicJSON(path, live)
		return
	}
	st, err := e.LoadWatchStateLocked(live.ScheduleID)
	if err != nil {
		return
	}
	result := ProbeResult{Watch: st, Occurrence: live}
	if probeErr == nil && strings.TrimSpace(value) == "" {
		// A vacuous value is a broken probe, not a baseline: an empty body
		// or a capture that matched nothing must never seed sha256("").
		probeErr = errors.New("probe produced empty value")
	}
	if probeErr != nil {
		st.LastError = BoundedWatchText(probeErr.Error())
		e.notifyProbeFailureLocked(live, st.LastError, now)
		limit := schedule.MaxRetries
		if limit <= 0 {
			limit = 3
		}
		if live.JobKind == "check" && live.RetryCount < limit {
			// unknown != false: stay probing with a retry deadline instead
			// of settling failed on the first probe error (R1.1). DueAt is
			// immutable — it keys the occurrence id. The retry writes go
			// straight to the durable files: a probe-result receipt here
			// would replay forever and block the actual retry.
			live.RetryCount++
			live.RetryAfter = now.Add(time.Duration(live.RetryCount) * time.Minute).UTC()
			_ = e.atomicJSON(e.WatchPath(live.ScheduleID), st)
			_ = e.atomicJSON(path, live)
			return
		}
		live.State = "failed"
	} else {
		st.LastSuccess, st.LastError = now.UTC(), ""
		e.clearProbeFailureLocked(live.ScheduleID)
		notify, text := false, live.Job.Text
		if live.JobKind == "check" {
			notify = (st.Baseline != "" && ((hash != st.Baseline && schedule.Expect == "changed") || (hash == st.Baseline && schedule.Expect == "unchanged"))) || (strings.HasPrefix(schedule.Expect, "equals:") && value == strings.TrimPrefix(schedule.Expect, "equals:"))
			live.State = "skipped"
			if notify {
				live.State = "dispatched"
				// The verdict travels with the delivery: the owner sees which
				// predicate fired and the anchored/current values (R1.2).
				text = fmt.Sprintf("%s\ncheck %s met\nwas: %s\nnow: %s", live.Job.Text, schedule.Expect, BoundedWatchText(st.Value), BoundedWatchText(value))
			}
		} else if st.Baseline == "" {
			st.Baseline, st.Value = hash, value
			live.State = "dispatched"
		} else if hash == st.Baseline {
			live.State = "no_change"
		} else {
			st.PrevValue, st.Value, st.Baseline = st.Value, value, hash
			st.ChangedAt = now.UTC()
			st.Generation++
			text = fmt.Sprintf("%s\n%s\nwas: %s\nnow: %s", live.Job.Text, schedule.Probe.URL, BoundedWatchText(st.PrevValue), BoundedWatchText(st.Value))
			notify = schedule.NotifyMode != "digest"
			live.State = "dispatched"
		}
		if live.JobKind == "watcher" && (schedule.DeliverMode == "content" || schedule.DeliverMode == "artifact") {
			// Report modes ship the probed value on every successful tick,
			// baseline included; the change tracker still advances (R6).
			notify = true
			live.State = "dispatched"
			text = fmt.Sprintf("%s\n%s", live.Job.Text, BoundedWatchText(value))
		}
		if notify && schedule.DailyLimit > 0 {
			day := now.UTC().Format("2006-01-02")
			if loc, err := time.LoadLocation(schedule.Timezone); err == nil {
				day = now.In(loc).Format("2006-01-02")
			}
			if st.NotifyDay != day {
				st.NotifyDay, st.NotifyCount = day, 0
			}
			if st.NotifyCount >= schedule.DailyLimit {
				// The baseline already advanced above; the owner sees the
				// occurrence as capped rather than a lost notification.
				live.State = "capped"
				notify = false
			} else {
				st.NotifyCount++
			}
		}
		if notify && schedule.NotifyMode == "agent" {
			if QuietDefer(schedule, now).After(now) {
				// An agent wake cannot be deferred like a delivery, so the
				// quiet window suppresses it outright — capped, same as the
				// daily-limit suppression above.
				live.State = "capped"
			} else if schedule.AssignmentID != "" && !e.admitAssignmentWakeLocked(schedule.AssignmentID, now, schedule.AssignmentPlanRevision) {
				// Assignment-owned wakes are fenced at admission: paused,
				// cancelled or over-budget assignments never reach the model
				// (SPEC-0080 R2.2).
				live.State = "capped"
			} else {
				// Semantic escalation is opt-in per revision: the change
				// evidence reaches the model flagged untrusted, through the
				// same idempotent job admission the agent kind uses
				// (SPEC-0054 R2.3).
				admitted := live.Job
				admitted.Envelope.ConversationID = RoutineConversation(live.ScheduleID)
				instruction := schedule.WakePrompt
				if schedule.AssignmentID != "" && schedule.AssignmentPlanRevision != 0 {
					assignment, _ := e.AssignmentForWakeLocked(schedule.AssignmentID, schedule.AssignmentPlanRevision)
					instruction = fmt.Sprintf("Standing assignment %s (revision %d), trigger job %s.\nOwner intent: %s\nSpecialist instructions: %s\nProgress: %s\nNext step: %s\nAfter handling this trigger, call assignment_get then assignment_progress with this job ID, current expected_revision, summary, optional finding and next_step; complete only when the owner's completion condition is met.", assignment.AssignmentID, assignment.Revision, admitted.ID, assignment.Intent, instruction, assignment.Progress.Summary, assignment.Progress.NextStep)
				}
				admitted.Text = instruction + "\n[untrusted watcher evidence] " + text
				if _, err := e.deps.EnqueueJob(admitted); err != nil {
					return
				}
			}
		} else if notify {
			prefix := "watch-"
			if live.JobKind == "check" {
				prefix = "check-"
			}
			d := commodel.Delivery{ID: prefix + live.Job.ID, OccurrenceID: live.Job.ID, Channel: live.Job.Channel, ConversationID: live.Job.ConversationID, DeliveryTargetID: live.Job.DeliveryTargetID, ChatID: live.Job.ChatID, TaskID: live.Job.TaskID, TopicID: live.Job.TopicID, SlackChannel: live.Job.SlackChannel, SlackThread: live.Job.SlackThread, Text: text, NotBefore: QuietDefer(schedule, now), CreatedAt: now.UTC()}
			if schedule.DeliverMode == "artifact" {
				// The emitted value is the file body; staging rides the
				// artifact blob path channels already drain (SPEC-0037).
				blob, blobErr := e.deps.WriteDeliveryBlob(d.ID, schedule.ArtifactName, []byte(value))
				if blobErr != nil {
					return
				}
				d.Artifacts = []hubruntime.ArtifactRef{{Name: schedule.ArtifactName, Blob: blob}}
			}
			nd, err := e.normalizeDelivery(d)
			if err != nil {
				return
			}
			result.Delivery = &nd
		}
	}
	result.Watch, result.Occurrence = st, live
	receipt := filepath.Join(e.root, "occurrences", occurrenceID(o)+".probe-result")
	if e.atomicJSON(receipt, result) != nil {
		return
	}
	_ = e.ApplyProbeResultLocked(result)
}

// notifyProbeFailureLocked surfaces a dead probe to the owner once per
// distinct normalized error, riding the same LastFailureSHA256 dedupe as
// routine job failures and blocked notices. Without it a broken source
// looks like a quiet watch forever. e.mu must be held.
func (e *Engine) notifyProbeFailureLocked(o commodel.RoutineOccurrence, reason string, now time.Time) {
	item, err := e.LoadScheduleLocked(o.ScheduleID)
	if err != nil {
		return
	}
	text := item.Input + "\nprobe failed: " + reason
	digest := RoutineFailureDigest(o.Job.ID, text)
	if item.LastFailureSHA256 == digest || item.PendingFailureSHA256 == digest {
		return
	}
	// The delivered digest is recorded only when the notice reaches the
	// owner (gateway deliverOne); the pending marker stands while the notice
	// is in flight and releases if it dies unconfirmed, so a dead notice
	// never silences the next identical failure.
	item.PendingFailureSHA256 = digest
	item.UpdatedAt = now.UTC()
	d := commodel.Delivery{ID: "watch-fail-" + o.Job.ID, OccurrenceID: o.Job.ID, Channel: o.Job.Channel, ConversationID: o.Job.ConversationID, DeliveryTargetID: o.Job.DeliveryTargetID, ChatID: o.Job.ChatID, TaskID: o.Job.TaskID, TopicID: o.Job.TopicID, SlackChannel: o.Job.SlackChannel, SlackThread: o.Job.SlackThread, Text: text, NotBefore: QuietDefer(item, now), CreatedAt: now.UTC()}
	if nd, err := e.normalizeDelivery(d); err == nil {
		_ = e.deps.EnqueueDelivery(nd)
	}
	_ = e.atomicJSON(e.SchedulePath(item.ScheduleID), item)
}

// clearProbeFailureLocked drops the stored failure digest after a probe
// success so the next failure episode notifies again. e.mu must be held.
func (e *Engine) clearProbeFailureLocked(scheduleID string) {
	item, err := e.LoadScheduleLocked(scheduleID)
	if err != nil || (item.LastFailureSHA256 == "" && item.PendingFailureSHA256 == "") {
		return
	}
	item.LastFailureSHA256, item.PendingFailureSHA256 = "", ""
	item.UpdatedAt = time.Now().UTC()
	_ = e.atomicJSON(e.SchedulePath(scheduleID), item)
}

// quietDefer reports when a delivery may leave the outbox: zero outside the
// owner's quiet window, the next window end inside it. Overnight windows
// (22:00-07:00) wrap. Bad input defers nothing — validation already rejected
// it, and a corrupt record must not silently silence notifications.
func QuietDefer(schedule commodel.Schedule, now time.Time) time.Time {
	if schedule.QuietFrom == "" || schedule.QuietTo == "" {
		return time.Time{}
	}
	loc, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return time.Time{}
	}
	from, err1 := time.Parse("15:04", schedule.QuietFrom)
	to, err2 := time.Parse("15:04", schedule.QuietTo)
	if err1 != nil || err2 != nil {
		return time.Time{}
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), from.Hour(), from.Minute(), 0, 0, loc)
	end := time.Date(local.Year(), local.Month(), local.Day(), to.Hour(), to.Minute(), 0, 0, loc)
	if to.Before(from) || to.Equal(from) {
		// Overnight window: the part after midnight belongs to the day that
		// started at `from` yesterday.
		if local.Before(start) {
			start = start.AddDate(0, 0, -1)
		} else {
			end = end.AddDate(0, 0, 1)
		}
	}
	if local.Before(start) || !local.Before(end) {
		return time.Time{}
	}
	return end.UTC()
}

func BoundedWatchText(value string) string {
	runes := []rune(value)
	if len(runes) > 1024 {
		return string(runes[:1024]) + "…"
	}
	return value
}

func ProbeValue(probe *commodel.WatchProbe, body []byte) (string, error) {
	return ExtractValue(probe, body)
}

// prefetchNeeds runs a script probe's declared tool needs through the same
// read-only ToolHub path as mcp_tool probes (SPEC-0059 R5.2): the sandboxed
// script stays networkless while owner-granted results arrive as needs().
// A failed need is a probe error — partial data never reaches the script.
func prefetchNeeds(ctx context.Context, principal string, probe *commodel.WatchProbe, toolCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error)) ([]string, error) {
	if len(probe.Needs) == 0 {
		return nil, nil
	}
	if toolCall == nil {
		return nil, errors.New("tool-call probe path unavailable")
	}
	results := make([]string, 0, len(probe.Needs))
	for _, need := range probe.Needs {
		out, err := toolCall(ctx, principal, &commodel.WatchProbe{Kind: "mcp_tool", Tool: need.Tool, Args: need.Args})
		if err != nil {
			return nil, fmt.Errorf("probe need %s failed: %w", need.Tool, err)
		}
		if len(out) > 64*1024 {
			out = out[:64*1024]
		}
		results = append(results, string(out))
	}
	return results, nil
}
