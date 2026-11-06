// Package automation hosts the routines plane inside ToolHub (SPEC-0099 A2):
// one routines.Engine per principal over per-user durable roots, with the
// engine ports backed by the host's real stores. Production startup wiring
// is #523.
package automation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/routines"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

const capability = "automation"

var errUnauthorized = errors.New("automation: unauthorized")

// SupervisorClient admits a durably queued job to the supervisor.
type SupervisorClient interface {
	RunJob(ctx context.Context, key string, job commodel.Job) (hubruntime.ExecuteResponse, error)
	RunScript(ctx context.Context, occurrence commodel.RoutineOccurrence) ([]string, error)
	ProbeScript(ctx context.Context, occurrence commodel.RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error)
}

// CommunicationClient posts a durable delivery intent to communication.
type CommunicationClient interface {
	PostDelivery(ctx context.Context, principal string, d commodel.Delivery) error
}

type DecisionStore interface {
	Decide(auth identity.Envelope, capability, operation, scope string, now time.Time) (api.RightDecision, error)
}

type Config struct {
	Root          string
	Environment   string
	Store         DecisionStore
	Now           func() time.Time
	Channels      map[string]routines.OccurrenceChannel
	Supervisor    SupervisorClient
	Communication CommunicationClient
	WorkspaceDir  func(principal string) string
	// Fetch performs real probe I/O (URL watches/event sources); defaults
	// to a plain HTTP GET when nil. ToolCall runs mcp_tool probes through
	// the connector; nil leaves tool probes failing closed.
	Fetch    func(context.Context, string) ([]byte, error)
	ToolCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error)
}

// Host owns the automation plane: per-principal engines and durable stores.
type Host struct {
	cfg     Config
	now     func() time.Time
	mu      sync.Mutex
	engines map[string]*routines.Engine
	queue   *dispatchQueue
	outbox  *deliveryOutbox
	events  *eventStore
	tasks   *taskStore
}

func NewHost(cfg Config) (*Host, error) {
	if !filepath.IsAbs(cfg.Root) || (cfg.Environment != "dev" && cfg.Environment != "prod") || cfg.Store == nil || cfg.Supervisor == nil || cfg.Communication == nil || cfg.WorkspaceDir == nil {
		return nil, errors.New("automation: invalid host configuration")
	}
	if cfg.Fetch == nil {
		cfg.Fetch = fetchWatch
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	h := &Host{
		cfg:     cfg,
		now:     now,
		engines: map[string]*routines.Engine{},
		queue:   &dispatchQueue{dir: filepath.Join(cfg.Root, "dispatch")},
		outbox:  &deliveryOutbox{dir: filepath.Join(cfg.Root, "outbox")},
		events:  &eventStore{dir: filepath.Join(cfg.Root, "events")},
		tasks:   &taskStore{dir: filepath.Join(cfg.Root, "tasks")},
	}
	h.events.deliver = func(principal string, d commodel.Delivery, now time.Time) error {
		_, err := h.outbox.enqueue(principal, d, now)
		return err
	}
	h.events.admit = h.admitEventJob
	h.events.filter = cfg.Supervisor.ProbeScript
	return h, nil
}

// engineFor returns (creating on first use) the engine rooted at the
// principal's own subtree — storage isolation is filesystem-real.
func (h *Host) engineFor(principal string) (*routines.Engine, error) {
	if !identity.ValidID(principal) {
		return nil, errUnauthorized
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.engines[principal]; ok {
		return e, nil
	}
	root := filepath.Join(h.cfg.Root, "users", principal)
	for _, dir := range []string{filepath.Join(h.cfg.Root, "users"), root, filepath.Join(root, "occurrences"), filepath.Join(root, "schedules")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			return nil, errors.New("automation: unsafe owner storage")
		}
	}
	var e *routines.Engine
	e = routines.NewEngine(root, routines.Deps{
		Hooks: routines.DraftHooks{
			Channel: func(in commodel.Schedule) error {
				if _, ok := h.cfg.Channels[in.Channel]; !ok {
					return fmt.Errorf("unknown schedule channel %q", in.Channel)
				}
				return nil
			},
			Script: routines.ScriptSyntaxError,
		},
		Storage: routines.StoragePort{
			WriteJSONAtomic: writeJSONAtomic,
			WriteBytes:      func(path string, body []byte) error { return os.WriteFile(path, body, 0o600) },
			MkdirAll:        func(path string, _ bool) error { return os.MkdirAll(path, 0o700) },
			Rename:          os.Rename,
			Remove:          os.Remove,
			ReadFile:        os.ReadFile,
			ReadDir:         readDirNames,
			Lstat:           os.Lstat,
		},
		Dispatch: routines.DispatchPort{
			EnqueueJob:  h.admitJob,
			LoadMapping: func(id string) (commodel.JobMapping, error) { return h.queue.loadMapping(principal, id) },
			LoadTask:    h.tasks.load,
			ListTasks:   h.tasks.list,
			TopicTask:   h.tasks.topic,
		},
		Delivery: routines.DeliveryPort{
			EnqueueDelivery:   func(d commodel.Delivery) error { _, err := h.outbox.enqueue(principal, d, h.now()); return err },
			WriteDeliveryBlob: h.writeDeliveryBlob,
			NormalizeDelivery: h.normalizeDelivery,
		},
		Events: routines.EventsPort{
			AcquireEvents: h.events.acquire,
			ResumeEvents: func(o commodel.RoutineOccurrence, sc commodel.Schedule, now time.Time, authorize func(commodel.Job) error) (bool, error) {
				return h.events.resume(e, o, sc, now, authorize)
			},
			SettleEvents: func(o commodel.RoutineOccurrence, sc commodel.Schedule, body []byte, now time.Time, probeErr error, authorize func(commodel.Job) error) ([]commodel.Event, error) {
				return h.events.settle(e, o, sc, body, now, probeErr, authorize)
			},
		},
		Signal:      func() {},
		ChannelFor:  func(id string) routines.OccurrenceChannel { return h.cfg.Channels[id] },
		ProbeScript: func() routines.ProbeScriptFunc { return h.cfg.Supervisor.ProbeScript },
	})
	h.engines[principal] = e
	return e, nil
}

func (h *Host) admitJob(j commodel.Job) (bool, error) {
	return h.admitEventJob(j, "")
}

func (h *Host) admitEventJob(j commodel.Job, sourceOccurrenceID string) (bool, error) {
	// The result path needs this binding before the dispatch intent is visible.
	taskID := j.TaskID
	if taskID == "" {
		taskID = j.ID
	}
	if err := h.tasks.put(commodel.Task{
		TaskID: taskID, Name: j.Trigger, PrincipalID: j.PrincipalID,
		ContextID: j.ContextID, Channel: j.Channel, Peer: j.DeliveryTargetID,
		ChatID: j.ChatID, TopicID: j.TopicID, ConversationID: j.ConversationID,
		CreatedAt: h.now(), UpdatedAt: h.now(),
	}); err != nil {
		return false, err
	}
	return h.queue.admit(j, h.now(), sourceOccurrenceID)
}

// authorize is the single gate for user-facing operations: the verified
// envelope, the host environment pin (binds managed envelopes that carry
// one), and the live rights decision — identity is never taken from tool
// arguments.
func (h *Host) authorize(auth identity.Envelope, op string) (*routines.Engine, error) {
	if auth.PrincipalID == "" || (auth.Environment != "" && auth.Environment != h.cfg.Environment) {
		return nil, errUnauthorized
	}
	dec, err := h.cfg.Store.Decide(auth, capability, op, "", h.now())
	if err != nil || !dec.Allow {
		return nil, errUnauthorized
	}
	return h.engineFor(auth.PrincipalID)
}

// authorizeJob is the engine's pre-admission gate for background
// execution: it re-decides the live right on the persisted job envelope —
// never on tool-call arguments — so a grant revoked between schedule
// creation and the due tick blocks the occurrence instead of dispatching.
func (h *Host) authorizeJob(job commodel.Job) error {
	env := job.Envelope
	if !identity.ValidID(env.PrincipalID) || (env.Environment != "" && env.Environment != h.cfg.Environment) {
		return errUnauthorized
	}
	dec, err := h.cfg.Store.Decide(env, capability, "write", "", h.now())
	if err != nil || !dec.Allow {
		return errUnauthorized
	}
	return nil
}

// submit posts one intent to the supervisor when un-acked; a failure is
// non-fatal — the durable intent is what reconcile retries.
func (h *Host) submit(ctx context.Context, principal, key string) error {
	if h.cfg.Supervisor == nil {
		return nil
	}
	in, err := h.queue.intent(principal, key)
	if err != nil || in.Acked {
		return err
	}
	// An intent can outlive a grant or schedule revision while the
	// supervisor is unavailable. Recheck both at the actual I/O boundary.
	if err := h.authorizeJob(in.Job); err != nil {
		return nil
	}
	e, err := h.engineFor(principal)
	if err != nil {
		return err
	}
	var occurrence commodel.RoutineOccurrence
	sourceID := in.Job.ID
	if in.SourceOccurrenceID != "" {
		sourceID = in.SourceOccurrenceID
	}
	if err := loadJSON(filepath.Join(h.cfg.Root, "users", principal, "occurrences", commodel.FileID(sourceID)+".json"), &occurrence); err != nil {
		return err
	}
	if occurrence.Job.ID != sourceID || occurrence.Job.PrincipalID != in.Job.PrincipalID || occurrence.Job.ContextID != in.Job.ContextID || occurrence.Job.DeliveryTargetID != in.Job.DeliveryTargetID || !e.OccurrenceCurrent(occurrence) {
		return nil
	}
	schedule, err := e.LoadSchedule(occurrence.ScheduleID)
	if err != nil || !schedule.Enabled || schedule.Paused || schedule.BlockedReason != "" {
		return nil
	}
	result, err := h.cfg.Supervisor.RunJob(ctx, in.Key, in.Job)
	if err != nil {
		return nil // durable intent stays pending; reconcile retries
	}
	if err := h.ReportResult(ctx, principal, in.Job.ID, result.Status, result.Text); err != nil {
		return err
	}
	return h.queue.markAcked(principal, key)
}

// reconcile re-submits every un-acked dispatch intent (lost-ack recovery).
func (h *Host) reconcile(ctx context.Context, principal string) error {
	pending, err := h.queue.pending()
	if err != nil {
		return err
	}
	byPrincipal := map[string][]dispatchIntent{}
	for _, in := range pending {
		if principal != "" && in.Job.PrincipalID != principal {
			continue
		}
		byPrincipal[in.Job.PrincipalID] = append(byPrincipal[in.Job.PrincipalID], in)
	}
	jobs := make(chan []dispatchIntent)
	errs := make(chan error, len(pending))
	var workers sync.WaitGroup
	for i := 0; i < len(byPrincipal) && i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for group := range jobs {
				for _, in := range group {
					if err := h.submit(ctx, in.Job.PrincipalID, in.Key); err != nil {
						errs <- err
					}
				}
			}
		}()
	}
	for _, group := range byPrincipal {
		jobs <- group
	}
	close(jobs)
	workers.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	return nil
}

func (h *Host) post(ctx context.Context, rec outboxRecord) error {
	if h.cfg.Communication == nil {
		return nil
	}
	if err := h.cfg.Communication.PostDelivery(ctx, rec.Principal, rec.Delivery); err != nil {
		return err
	}
	return h.outbox.markSent(rec)
}

// flush attempts pending deliveries; an outage delays, never drops.
func (h *Host) flush(ctx context.Context) error {
	pending, err := h.outbox.pending()
	if err != nil {
		return err
	}
	for _, rec := range pending {
		_ = h.post(ctx, rec)
	}
	return nil
}

// Tick drives one clock pass over every loaded engine, then reconciles
// pending dispatch and delivery intents.
func (h *Host) Tick(ctx context.Context, now time.Time) error {
	h.mu.Lock()
	type ownerEngine struct {
		principal string
		engine    *routines.Engine
	}
	engines := make([]ownerEngine, 0, len(h.engines))
	for principal, e := range h.engines {
		engines = append(engines, ownerEngine{principal, e})
	}
	h.mu.Unlock()
	jobs := make(chan ownerEngine)
	errs := make(chan error, len(engines)*6)
	var workers sync.WaitGroup
	for i := 0; i < len(engines) && i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for owner := range jobs {
				e := owner.engine
				for _, step := range []func() error{
					func() error { return e.TickSchedules(now, "", h.authorizeJob) },
					func() error { return h.tickStreams(e, owner.principal, now) },
					func() error { return e.DispatchDueOccurrences(now, h.authorizeJob) },
					func() error { return e.RunDueScripts(ctx, now, h.authorizeJob, h.cfg.Supervisor.RunScript) },
					func() error {
						return e.ProbeDueOccurrencesContext(ctx, now, h.authorizeJob, h.cfg.Fetch, h.cfg.ToolCall, nil)
					},
				} {
					if err := step(); err != nil {
						errs <- err
					}
				}
				if err := h.reconcile(ctx, owner.principal); err != nil {
					errs <- err
				}
			}
		}()
	}
	for _, owner := range engines {
		jobs <- owner
	}
	close(jobs)
	workers.Wait()
	close(errs)
	var firstErr error
	for err := range errs {
		if firstErr == nil {
			firstErr = err
		}
	}
	if err := h.reconcile(ctx, ""); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := h.flush(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func streamOccurrence(schedule commodel.Schedule, due time.Time) commodel.RoutineOccurrence {
	job := commodel.Job{Envelope: schedule.Envelope, OrganizationID: schedule.OrganizationID, UserID: schedule.UserID, ActorID: schedule.ActorID, ScopeID: schedule.ScopeID, Channel: schedule.Channel, ChatID: schedule.ChatID, TaskID: schedule.TaskID, TopicID: schedule.TopicID, SlackChannel: schedule.SlackChannel, SlackThread: schedule.SlackThread, Text: schedule.Input, Trigger: "cron", AssignmentID: schedule.AssignmentID, AssignmentPlanRevision: schedule.AssignmentPlanRevision}
	return commodel.RoutineOccurrence{ScheduleID: schedule.ScheduleID, Revision: schedule.Revision, DueAt: due, JobKind: "event", Job: job, CatchUpWindow: schedule.CatchUpWindow}
}

func (h *Host) tickStreams(e *routines.Engine, principal string, now time.Time) error {
	names, err := os.ReadDir(filepath.Join(h.cfg.Root, "users", principal, "schedules"))
	if err != nil {
		return err
	}
	for _, name := range names {
		if name.IsDir() || !strings.HasSuffix(name.Name(), ".json") {
			continue
		}
		schedule, err := e.LoadSchedule(strings.TrimSuffix(name.Name(), ".json"))
		if err != nil {
			return err
		}
		if schedule.Delivery != "stream" || schedule.JobKind != "event" || schedule.Probe == nil || !schedule.Enabled || schedule.Paused || schedule.BlockedReason != "" || now.Before(schedule.SnoozedUntil) || schedule.Envelope.PrincipalID != principal {
			continue
		}
		o := streamOccurrence(schedule, now.UTC())
		if h.authorizeJob(o.Job) != nil {
			continue
		}
		h.events.mu.Lock()
		st, path, err := h.events.load(o)
		if err != nil {
			h.events.mu.Unlock()
			return err
		}
		if err := rebindEventState(&st, schedule); err != nil {
			h.events.mu.Unlock()
			return err
		}
		if st.StreamRevision != 0 && st.StreamRevision != schedule.Revision {
			if st.Pending != nil {
				h.events.mu.Unlock()
				return errors.New("automation: pending event batch revision changed; recovery required")
			}
			st.StreamAt = time.Time{}
			st.LastPollAt = time.Time{}
		}
		if st.Pending != nil {
			h.events.mu.Unlock()
			continue
		}
		if !st.StreamAt.IsZero() {
			o = streamOccurrence(schedule, st.StreamAt)
			var live commodel.RoutineOccurrence
			readErr := loadJSON(filepath.Join(h.cfg.Root, "users", principal, "occurrences", routines.OccurrenceID(o)+".json"), &live)
			if readErr == nil && (live.State == "pending" || live.State == "probing") {
				h.events.mu.Unlock()
				continue
			}
			if readErr != nil && !os.IsNotExist(readErr) {
				h.events.mu.Unlock()
				return readErr
			}
			if readErr == nil {
				st.StreamAt = time.Time{}
			}
		}
		if st.StreamAt.IsZero() {
			interval := time.Duration(schedule.Probe.MinIntervalSeconds) * time.Second
			if now.Sub(st.LastPollAt) < interval {
				h.events.mu.Unlock()
				continue
			}
			due := now.UTC()
			if !due.After(st.LastPollAt) {
				due = st.LastPollAt.Add(time.Nanosecond)
			}
			o = streamOccurrence(schedule, due)
			st.StreamAt, st.StreamRevision, st.LastPollAt = due, schedule.Revision, now.UTC()
			if err := writeJSONAtomic(path, st); err != nil {
				h.events.mu.Unlock()
				return err
			}
		}
		h.events.mu.Unlock()
		if err := e.PutOccurrence(o, schedule.Envelope); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) Close() error { return nil }

func (h *Host) writeDeliveryBlob(deliveryID, name string, data []byte) (string, error) {
	dir := filepath.Join(h.cfg.Root, "blobs", deliveryID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, filepath.Base(name))
	return path, os.WriteFile(path, data, 0o600)
}

func (h *Host) normalizeDelivery(d commodel.Delivery) (commodel.Delivery, error) {
	if d.ID == "" {
		d.ID = "del-" + d.IdempotencyKey
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = h.now()
	}
	return d, nil
}

func (h *Host) CreateSchedule(ctx context.Context, auth identity.Envelope, in commodel.Schedule, nativeCron string) (commodel.Schedule, error) {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return commodel.Schedule{}, err
	}
	return e.CreateSchedule(in, auth, nativeCron)
}

// Recover reloads durable state and reconciles un-acked intents — the
// restart path after process loss.
func (h *Host) CreateAssignment(ctx context.Context, auth identity.Envelope, in commodel.Assignment) (commodel.Assignment, bool, error) {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return commodel.Assignment{}, false, err
	}
	return e.CreateAssignment(in, auth)
}

func (h *Host) PauseAssignment(ctx context.Context, auth identity.Envelope, id string) error {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return err
	}
	return e.PauseAssignment(id, auth)
}

func (h *Host) ResumeAssignment(ctx context.Context, auth identity.Envelope, id string) error {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return err
	}
	return e.ResumeAssignment(id, auth)
}

func (h *Host) CompleteAssignment(ctx context.Context, auth identity.Envelope, id string) (commodel.Assignment, error) {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return commodel.Assignment{}, err
	}
	return e.CompleteAssignment(id, auth)
}

func (h *Host) SeedWatchBaseline(ctx context.Context, auth identity.Envelope, id, value string, now time.Time) error {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return err
	}
	return e.SeedWatchBaseline(id, value, now)
}

func (h *Host) LoadWatchState(ctx context.Context, auth identity.Envelope, id string) (routines.WatchState, error) {
	e, err := h.authorize(auth, "read")
	if err != nil {
		return routines.WatchState{}, err
	}
	return e.LoadWatchState(id)
}

func (h *Host) ListSchedules(ctx context.Context, auth identity.Envelope) ([]commodel.Schedule, error) {
	e, err := h.authorize(auth, "read")
	if err != nil {
		return nil, err
	}
	return e.ListSchedules(auth)
}

func (h *Host) PauseSchedule(ctx context.Context, auth identity.Envelope, id string) error {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return err
	}
	return e.PauseSchedule(id, auth)
}

func (h *Host) UpdateSchedule(ctx context.Context, auth identity.Envelope, id, expression, input string, enabled *bool) (commodel.Schedule, error) {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return commodel.Schedule{}, err
	}
	return e.UpdateSchedule(id, auth, expression, input, enabled)
}

func (h *Host) SnoozeSchedule(ctx context.Context, auth identity.Envelope, id string, until time.Time) error {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return err
	}
	return e.SnoozeSchedule(id, auth, until)
}

func (h *Host) DeleteSchedule(ctx context.Context, auth identity.Envelope, id string) error {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return err
	}
	return e.DeleteSchedule(id, auth)
}

func (h *Host) ListAssignments(ctx context.Context, auth identity.Envelope) ([]commodel.Assignment, error) {
	e, err := h.authorize(auth, "read")
	if err != nil {
		return nil, err
	}
	return e.ListAssignments(auth)
}

func (h *Host) CancelAssignment(ctx context.Context, auth identity.Envelope, id string) error {
	e, err := h.authorize(auth, "write")
	if err != nil {
		return err
	}
	return e.CancelAssignment(id, auth)
}

func (h *Host) Recover(ctx context.Context) error {
	if err := h.loadEngines(); err != nil {
		return err
	}
	completed, err := h.queue.completed()
	if err != nil {
		return err
	}
	for _, in := range completed {
		if err := h.enqueueResult(in); err != nil {
			return err
		}
	}
	if err := h.reconcile(ctx, ""); err != nil {
		return err
	}
	return h.flush(ctx)
}

func (h *Host) loadEngines() error {
	entries, err := os.ReadDir(filepath.Join(h.cfg.Root, "users"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, en := range entries {
		if !identity.ValidID(en.Name()) || en.Type()&os.ModeSymlink != 0 {
			return errors.New("automation: unsafe owner storage")
		}
		if en.IsDir() {
			if _, err := h.engineFor(en.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReportResult persists an execution result under service authority and
// enqueues the durable delivery intent — a communication outage leaves the
// intent pending, never loses the result.
func (h *Host) ReportResult(ctx context.Context, principal, jobID, status, text string) error {
	mapping, err := h.queue.loadMapping(principal, jobID)
	if err != nil {
		return fmt.Errorf("automation: result for unknown job %q: %w", jobID, err)
	}
	if commodel.TerminalStatus(mapping.Status) && mapping.Status == status {
		text = mapping.Result
	} else if status == "failed" || status == "cancelled" || status == "interrupted" {
		text = locale.T(locale.LangOf(h.cfg.WorkspaceDir(principal)), "job."+status, jobID)
	}
	taskID := mapping.TaskID
	if taskID == "" {
		taskID = jobID
	}
	if _, err := h.tasks.load(principal, taskID); err != nil {
		return fmt.Errorf("automation: result for unknown job %q: %w", jobID, err)
	}
	in, err := h.queue.recordResult(principal, jobID, status, text, h.now())
	if err != nil {
		return err
	}
	if err := h.enqueueResult(in); err != nil {
		return err
	}
	return h.flush(ctx)
}

func (h *Host) enqueueResult(in dispatchIntent) error {
	taskID := in.Mapping.TaskID
	if taskID == "" {
		taskID = in.Job.ID
	}
	task, err := h.tasks.load(in.Job.PrincipalID, taskID)
	if err != nil {
		return err
	}
	_, err = h.outbox.enqueue(in.Job.PrincipalID, commodel.Delivery{
		ID:               "result-" + in.Job.ID,
		JobID:            in.Job.ID,
		IdempotencyKey:   "result-" + in.Job.ID,
		ConversationID:   task.ConversationID,
		DeliveryTargetID: task.Peer,
		Channel:          task.Channel,
		ChatID:           task.ChatID,
		TaskID:           task.TaskID,
		TopicID:          task.TopicID,
		SlackChannel:     in.Job.SlackChannel,
		SlackThread:      in.Job.SlackThread,
		Text:             in.Mapping.Result,
		CreatedAt:        in.Mapping.TerminalAt.UTC(),
	}, h.now())
	return err
}

// PendingDispatches counts durable dispatch intents awaiting supervisor ack.
func (h *Host) PendingDispatches(principal string) (int, error) {
	pending, err := h.queue.pending()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, in := range pending {
		if in.Job.PrincipalID == principal {
			n++
		}
	}
	return n, nil
}

// PendingDeliveries counts durable delivery intents awaiting send.
func (h *Host) PendingDeliveries(principal string) (int, error) {
	pending, err := h.outbox.pending()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range pending {
		if rec.Principal == principal {
			n++
		}
	}
	return n, nil
}
