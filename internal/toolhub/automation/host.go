// Package automation hosts the routines plane inside ToolHub (SPEC-0099 A2):
// one routines.Engine per principal over per-user durable roots, with the
// engine ports backed by the host's real stores. Production startup wiring
// is #523.
package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/commops"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory/night"
	"github.com/letya999/hermes-hub/internal/routines"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/shared/locale"
)

const capability = "tasks"

func scheduleOperation(item commodel.Schedule) string {
	if item.AssignmentID != "" {
		return "delegate"
	}
	if item.JobKind == "event" {
		return "observe"
	}
	return "schedule"
}

var errUnauthorized = errors.New("automation: unauthorized")

// SupervisorClient admits a durably queued job to the supervisor.
type SupervisorClient interface {
	AdmissionEpoch(context.Context, identity.Envelope) (uint64, error)
	RunJob(ctx context.Context, key string, job commodel.Job) (hubruntime.ExecuteResponse, error)
	RunScript(ctx context.Context, occurrence commodel.RoutineOccurrence) ([]string, error)
	ProbeScript(ctx context.Context, occurrence commodel.RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error)
}

// CommunicationClient posts a durable delivery intent to communication.
type CommunicationClient interface {
	PostDelivery(ctx context.Context, principal, contextID string, d commodel.Delivery) error
}

type DecisionStore interface {
	Decide(auth identity.Envelope, capability, operation, scope string, now time.Time) (api.RightDecision, error)
}

type Config struct {
	Root               string
	OwnersDir          string
	LegacySpoolDir     string
	RequiredPrincipals []string
	Environment        string
	Store              DecisionStore
	AuthForPrincipal   func(string) (identity.Envelope, error)
	Now                func() time.Time
	Channels           map[string]routines.OccurrenceChannel
	Supervisor         SupervisorClient
	Communication      CommunicationClient
	AuthorizeChannel   func(context.Context, string, commodel.Job) error
	RoutingUser        func(string) (commodel.User, error)
	WorkspaceDir       func(principal string) string
	LangOf             func(principal string) locale.Lang
	// Fetch performs real probe I/O (URL watches/event sources); defaults
	// to a plain HTTP GET when nil. ToolCall runs mcp_tool probes through
	// the connector; nil leaves tool probes failing closed.
	Fetch        func(context.Context, string) ([]byte, error)
	ToolCall     func(context.Context, string, *commodel.WatchProbe) ([]byte, error)
	DreamingPlan func(identity.Envelope) (night.Schedule, error)
	DreamingRun  func(context.Context, identity.Envelope, string, night.Schedule, locale.Lang, func(context.Context, night.IntentProposal) error) (string, error)
	// Overloaded reports that the serving plane's interactive path is hot;
	// a true value defers the whole tick pass — due work keeps its catch-up
	// window and the next tick settles it. Nil means never overloaded.
	Overloaded func() bool
}

// Host owns the automation plane: per-principal engines and durable stores.
type Host struct {
	cfg        Config
	now        func() time.Time
	mu         sync.Mutex
	engines    map[string]*routines.Engine
	queue      *dispatchQueue
	outbox     *deliveryOutbox
	events     *eventStore
	tasks      *taskStore
	inFlight   sync.Map // principal + job ID -> job while its supervisor call is live
	nightLocks sync.Map // principal -> *sync.Mutex; model work never holds h.mu
}

func NewHost(cfg Config) (*Host, error) {
	if !filepath.IsAbs(cfg.Root) || cfg.OwnersDir != "" && !filepath.IsAbs(cfg.OwnersDir) || cfg.LegacySpoolDir != "" && !filepath.IsAbs(cfg.LegacySpoolDir) || (cfg.Environment != "dev" && cfg.Environment != "prod") || cfg.Store == nil || cfg.AuthForPrincipal == nil || cfg.Supervisor == nil || cfg.Communication == nil || cfg.AuthorizeChannel == nil || cfg.LangOf == nil && cfg.WorkspaceDir == nil || len(cfg.Channels) == 0 {
		return nil, errors.New("automation: invalid host configuration")
	}
	for _, principal := range cfg.RequiredPrincipals {
		if !identity.ValidID(principal) {
			return nil, errors.New("automation: invalid required principal")
		}
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
	h.events.lookup = h.queue.loadMapping
	h.events.filter = cfg.Supervisor.ProbeScript
	h.events.langOf = h.langOf
	return h, nil
}

func (h *Host) ownersDir() string {
	if h.cfg.OwnersDir != "" {
		return h.cfg.OwnersDir
	}
	return filepath.Join(h.cfg.Root, "users")
}

func (h *Host) ownerRoot(principal string) string {
	return filepath.Join(h.ownersDir(), principal)
}

func (h *Host) validateOwnerRoot(root string) error {
	for _, dir := range []string{h.ownersDir(), root} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("automation: unsafe owner storage")
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "records.activated")); err == nil {
		return errors.New("automation: unsupported activated record layout")
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, name := range []string{"schedules", "occurrences", "assignments", "watchers", "digests", "event-state", "system"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("automation: unsafe owner storage")
		}
	}
	if entries, err := os.ReadDir(filepath.Join(root, "event-state")); err == nil && len(entries) != 0 {
		return errors.New("automation: unsupported legacy event state")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
	root := h.ownerRoot(principal)
	for _, dir := range []string{h.ownersDir(), root} {
		if h.cfg.OwnersDir == "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
		}
	}
	if err := h.validateOwnerRoot(root); err != nil {
		return nil, err
	}
	for _, dir := range []string{filepath.Join(root, "occurrences"), filepath.Join(root, "schedules")} {
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			if err = os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
			info, err = os.Lstat(dir)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("automation: unsafe owner storage")
		}
	}
	loadMapping := func(id string) (commodel.JobMapping, error) { return h.queue.loadMapping(principal, id) }
	loadTask, listTasks, topicTask := h.tasks.load, h.tasks.list, h.tasks.topic
	if h.cfg.LegacySpoolDir != "" {
		bindings := legacyBindings{LegacySpoolReader: commops.LegacySpoolReader{Root: h.cfg.LegacySpoolDir}}
		loadTask, listTasks, topicTask = bindings.loadTask, bindings.listTasks, bindings.topicTask
		loadMapping = func(id string) (commodel.JobMapping, error) {
			mapping, err := h.queue.loadMapping(principal, id)
			if os.IsNotExist(err) {
				return bindings.loadMapping(id)
			}
			return mapping, err
		}
	}
	var e *routines.Engine
	e = routines.NewEngine(root, routines.Deps{
		Hooks: routines.DraftHooks{
			Channel: func(in commodel.Schedule) error {
				ch, ok := h.cfg.Channels[in.Channel]
				if !ok {
					return fmt.Errorf("unknown schedule channel %q", in.Channel)
				}
				validator, ok := ch.(interface{ ValidateSchedule(commodel.Schedule) error })
				if !ok {
					return errors.New("automation: channel cannot validate schedules")
				}
				return validator.ValidateSchedule(in)
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
			PrepareJob:  h.prepareJob,
			EnqueueJob:  h.admitJob,
			LoadMapping: loadMapping,
			LoadTask:    loadTask,
			ListTasks:   listTasks,
			TopicTask:   topicTask,
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
		LangOf:      h.langOf,
	})
	h.engines[principal] = e
	return e, nil
}

func (h *Host) admitJob(j commodel.Job) (bool, error) {
	return h.admitEventJob(j, "")
}

func (h *Host) prepareJob(job *commodel.Job) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	epoch, err := h.cfg.Supervisor.AdmissionEpoch(ctx, job.Envelope)
	if err != nil {
		return err
	}
	job.OperationalEpoch = epoch
	return nil
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

// authorize is the single gate for user-facing operations: the caller's
// channel envelope selects a principal, but the ToolHub-enrolled envelope
// supplies the managed profile and environment for the live rights decision.
func (h *Host) authorize(auth identity.Envelope, operation string) (*routines.Engine, error) {
	if operation == "read" || operation == "write" {
		operation = "schedule"
	}
	if auth.PrincipalID == "" || (auth.Environment != "" && auth.Environment != h.cfg.Environment) {
		return nil, errUnauthorized
	}
	policy, err := h.policyAuth(auth.PrincipalID)
	if err != nil {
		return nil, err
	}
	dec, err := h.cfg.Store.Decide(policy, capability, operation, "", h.now())
	if err != nil || !dec.Allow {
		return nil, errUnauthorized
	}
	return h.engineFor(auth.PrincipalID)
}

func (h *Host) authorizeSchedule(auth identity.Envelope, id string) (*routines.Engine, commodel.Schedule, error) {
	if auth.PrincipalID == "" || id == routines.SystemDreamingID || (auth.Environment != "" && auth.Environment != h.cfg.Environment) || !commodel.IDPattern.MatchString(id) {
		return nil, commodel.Schedule{}, errUnauthorized
	}
	e, err := h.engineFor(auth.PrincipalID)
	if err != nil {
		return nil, commodel.Schedule{}, err
	}
	item, err := e.LoadSchedule(id)
	if err != nil || item.Envelope.PrincipalID != auth.PrincipalID || item.Envelope.ContextID != auth.ContextID {
		return nil, commodel.Schedule{}, routines.ErrRoutineNotOwned
	}
	if _, err := h.authorize(auth, scheduleOperation(item)); err != nil {
		return nil, commodel.Schedule{}, err
	}
	return e, item, nil
}

// authorizeJob is the engine's pre-admission gate for background
// execution: it re-decides the live right on the persisted job envelope —
// never on tool-call arguments — so a grant revoked between schedule
// creation and the due tick blocks the occurrence instead of dispatching.
func (h *Host) authorizeJob(job commodel.Job, scheduleID ...string) error {
	env := job.Envelope
	if !identity.ValidID(env.PrincipalID) || (env.Environment != "" && env.Environment != h.cfg.Environment) {
		return errUnauthorized
	}
	policy, err := h.policyAuth(env.PrincipalID)
	if err != nil {
		return err
	}
	operation := "schedule"
	if job.AssignmentID != "" {
		operation = "delegate"
	} else {
		id := job.ID
		bound := len(scheduleID) != 0
		if len(scheduleID) != 0 {
			id = scheduleID[0]
		} else if id != "" {
			var occurrence commodel.RoutineOccurrence
			if err := loadJSON(filepath.Join(h.ownerRoot(env.PrincipalID), "occurrences", commodel.FileID(id)+".json"), &occurrence); err == nil && occurrence.Job.ID == id && occurrence.Job.PrincipalID == env.PrincipalID {
				id = occurrence.ScheduleID
				bound = true
			}
		}
		if id != "" && commodel.IDPattern.MatchString(id) {
			var item commodel.Schedule
			if err := loadJSON(filepath.Join(h.ownerRoot(env.PrincipalID), "schedules", commodel.FileID(id)+".json"), &item); err == nil && item.ScheduleID == id && item.Envelope.PrincipalID == env.PrincipalID {
				operation = scheduleOperation(item)
			} else if bound {
				return errUnauthorized
			}
		} else if bound {
			return errUnauthorized
		}
	}
	dec, err := h.cfg.Store.Decide(policy, capability, operation, "", h.now())
	if err != nil || !dec.Allow {
		return errUnauthorized
	}
	return nil
}

// authorizeDueJob adds the channel binding when the engine has persisted an
// occurrence. TickSchedules calls authorizeJob before an occurrence has an ID.
func (h *Host) authorizeDueJob(ctx context.Context, job commodel.Job) error {
	if job.Channel == "system" {
		return h.authorizeDreaming(job)
	}
	if err := h.authorizeJob(job); err != nil || job.ID == "" {
		return err
	}
	var occurrence commodel.RoutineOccurrence
	err := loadJSON(filepath.Join(h.ownerRoot(job.PrincipalID), "occurrences", commodel.FileID(job.ID)+".json"), &occurrence)
	if os.IsNotExist(err) {
		return nil // event-derived jobs are checked again against their source at submit
	}
	if err != nil || occurrence.Job.ID != job.ID || occurrence.Job.PrincipalID != job.PrincipalID {
		return errUnauthorized
	}
	return h.cfg.AuthorizeChannel(ctx, occurrence.ScheduleID, job)
}

// authorizeScheduleJob fences a materializing occurrence: the envelope rights
// check plus the live channel binding — a stale task/topic pointer must block
// the schedule before its reminder or agent job is ever admitted.
func (h *Host) authorizeScheduleJob(ctx context.Context, scheduleID string, job commodel.Job) error {
	if scheduleID == routines.SystemDreamingID {
		return h.authorizeDreaming(job)
	}
	if err := h.authorizeJob(job, scheduleID); err != nil {
		return &routines.ScheduleAuthorizationFailure{Code: "owner_rights_unavailable"}
	}
	if err := h.cfg.AuthorizeChannel(ctx, scheduleID, job); err != nil {
		return &routines.ScheduleAuthorizationFailure{Code: "channel_binding_unavailable"}
	}
	return nil
}

// AuthorizeOccurrence re-authorizes a routine-produced delivery at send time:
// the occurrence must still exist under the caller's owner root, stay
// dispatched on a current schedule, and pass the live right and channel
// binding — a schedule deleted or a grant revoked between dispatch and send
// retires the pending delivery instead of sending it.
func (h *Host) AuthorizeOccurrence(ctx context.Context, auth identity.Envelope, occurrenceID, kind string) error {
	if auth.PrincipalID == "" || (auth.Environment != "" && auth.Environment != h.cfg.Environment) || !commodel.IDPattern.MatchString(occurrenceID) {
		return errUnauthorized
	}
	var occurrence commodel.RoutineOccurrence
	path := filepath.Join(h.ownerRoot(auth.PrincipalID), "occurrences", commodel.FileID(occurrenceID)+".json")
	err := loadJSON(path, &occurrence)
	archived := false
	if os.IsNotExist(err) && kind == "" {
		path = filepath.Join(h.ownerRoot(auth.PrincipalID), "occurrences", "archive", commodel.FileID(occurrenceID)+".json")
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() && info.Size() <= 128*1024 {
			err = loadJSON(path, &occurrence)
			archived = err == nil
		}
	}
	if err != nil {
		return errUnauthorized
	}
	if occurrence.Job.ID != occurrenceID || occurrence.Job.PrincipalID != auth.PrincipalID {
		return errUnauthorized
	}
	if err := h.authorizeDueJob(ctx, occurrence.Job); err != nil {
		return err
	}
	eng, err := h.engineFor(auth.PrincipalID)
	// A routine-failure notice legitimately references its settled
	// occurrence (a dead probe settles "failed"), and a job claimed after
	// dispatch may find the occurrence still "pending" after a crash
	// replay — both fences require the record to stay current, not a
	// live dispatch. Ordinary routine payloads still require "dispatched".
	if archived {
		if err != nil || !h.completedAssignmentResult(eng, auth, occurrence, kind) || h.cfg.AuthorizeChannel(ctx, occurrence.ScheduleID, occurrence.Job) != nil {
			return errUnauthorized
		}
		return nil
	}
	if err != nil || (!eng.OccurrenceCurrent(occurrence) && !h.completedAssignmentResult(eng, auth, occurrence, kind)) ||
		(kind != "routine-failure" && kind != "job" && occurrence.State != "dispatched") {
		return errUnauthorized
	}
	return nil
}

func (h *Host) completedAssignmentResult(eng *routines.Engine, auth identity.Envelope, occurrence commodel.RoutineOccurrence, kind string) bool {
	job := occurrence.Job
	if kind != "" || job.AssignmentID == "" || occurrence.State != "dispatched" {
		return false
	}
	assignment, err := eng.LoadAssignment(job.AssignmentID, auth)
	if err != nil || assignment.State != "completed" || assignment.Progress.LastJobID != job.ID || assignment.AppliedRev != job.AssignmentPlanRevision || assignment.Plan == nil || assignment.Plan.Revision != job.AssignmentPlanRevision {
		return false
	}
	var retired commodel.Schedule
	return loadJSON(eng.CancelPath(occurrence.ScheduleID), &retired) == nil && retired.ScheduleID == occurrence.ScheduleID && retired.AssignmentID == job.AssignmentID && retired.AssignmentPlanRevision == job.AssignmentPlanRevision && retired.Revision == occurrence.Revision+1 && retired.Envelope.Equal(job.Envelope)
}

// RecordDeliveryOutcome settles a routine-failure notice's dedupe digest: a
// delivered notice promotes the pending marker to LastFailureSHA256, an
// unconfirmed one releases it so the next identical failure notifies again.
func (h *Host) RecordDeliveryOutcome(ctx context.Context, auth identity.Envelope, occurrenceID, status, text string) error {
	if status != "delivered" && status != "uncertain" {
		return errors.New("automation: invalid delivery outcome")
	}
	var occurrence commodel.RoutineOccurrence
	if err := loadJSON(filepath.Join(h.ownerRoot(auth.PrincipalID), "occurrences", commodel.FileID(occurrenceID)+".json"), &occurrence); err != nil {
		return errUnauthorized
	}
	if occurrence.Job.ID != occurrenceID || occurrence.Job.PrincipalID != auth.PrincipalID {
		return errUnauthorized
	}
	if err := h.authorizeDueJob(ctx, occurrence.Job); err != nil {
		return err
	}
	eng, err := h.engineFor(auth.PrincipalID)
	if err != nil {
		return err
	}
	digest := routines.RoutineFailureDigest(occurrenceID, text)
	if status == "uncertain" {
		return eng.ReleaseRoutineFailurePending(occurrence.ScheduleID, digest)
	}
	_, err = eng.RecordRoutineFailure(occurrence.ScheduleID, digest)
	return err
}

func (h *Host) policyAuth(principal string) (identity.Envelope, error) {
	auth, err := h.cfg.AuthForPrincipal(principal)
	if err != nil || auth.PrincipalID != principal || auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion) != nil || auth.Environment != "" && auth.Environment != h.cfg.Environment {
		return identity.Envelope{}, errUnauthorized
	}
	return auth, nil
}

func (h *Host) langOf(principal string) locale.Lang {
	if h.cfg.LangOf != nil {
		return h.cfg.LangOf(principal)
	}
	return locale.LangOf(h.cfg.WorkspaceDir(principal))
}

// submit posts one intent to the supervisor when un-acked; a failure is
// non-fatal — the durable intent is what reconcile retries.
func (h *Host) submit(ctx context.Context, principal, key string) error {
	if h.cfg.Supervisor == nil {
		return nil
	}
	in, err := h.queue.intent(principal, key)
	if err != nil || in.Acked || in.RetryStopped {
		return err
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
	if err := loadJSON(filepath.Join(h.ownerRoot(principal), "occurrences", commodel.FileID(sourceID)+".json"), &occurrence); err != nil {
		return err
	}
	if occurrence.Job.ID != sourceID || occurrence.Job.PrincipalID != in.Job.PrincipalID || occurrence.Job.ContextID != in.Job.ContextID || occurrence.Job.DeliveryTargetID != in.Job.DeliveryTargetID {
		return nil
	}
	if e.OccurrenceRetired(occurrence) {
		return h.queue.stopRetry(principal, key)
	}
	// An intent can outlive a grant or schedule revision while the
	// supervisor is unavailable. Recheck both at the actual I/O boundary.
	if h.authorizeJob(in.Job) != nil || !e.OccurrenceCurrent(occurrence) {
		return nil
	}
	if err := h.authorizeJob(in.Job, occurrence.ScheduleID); err != nil {
		return nil
	}
	schedule, err := e.LoadSchedule(occurrence.ScheduleID)
	settledFinalSlot := err == nil && !schedule.Enabled && schedule.Paused && schedule.LastOccurrenceID == occurrence.Job.ID
	if err != nil || (!schedule.Enabled || schedule.Paused) && !settledFinalSlot || schedule.BlockedReason != "" {
		return nil
	}
	if err := h.cfg.AuthorizeChannel(ctx, occurrence.ScheduleID, in.Job); err != nil {
		return nil
	}
	flightKey := principal + "\x00" + in.Job.ID
	if _, loaded := h.inFlight.LoadOrStore(flightKey, in.Job); loaded {
		return nil
	}
	defer h.inFlight.Delete(flightKey)
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
	if err := h.cfg.Communication.PostDelivery(ctx, rec.Principal, rec.Delivery.OwnerContextID, rec.Delivery); err != nil {
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
// pending dispatch and delivery intents. Under overload the pass defers
// wholesale — due work survives through its catch-up window.
func (h *Host) Tick(ctx context.Context, now time.Time) error {
	if h.cfg.Overloaded != nil && h.cfg.Overloaded() {
		return nil
	}
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
	errs := make(chan error, len(engines)*8)
	var workers sync.WaitGroup
	for i := 0; i < len(engines) && i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for owner := range jobs {
				e := owner.engine
				dueAuth := func(job commodel.Job) error { return h.authorizeDueJob(ctx, job) }
				for i, step := range []func() error{
					func() error { return h.syncDreaming(owner.engine, owner.principal, now) },
					func() error {
						return e.TickSchedules(now, "", func(scheduleID string, job commodel.Job) error { return h.authorizeScheduleJob(ctx, scheduleID, job) })
					},
					func() error { return h.tickStreams(ctx, e, owner.principal, now) },
					func() error { return e.DispatchDueOccurrences(now, dueAuth) },
					func() error { return e.RunDueDreaming(ctx, now, dueAuth, h.runDreamingOccurrence) },
					func() error { return e.RunDueScripts(ctx, now, dueAuth, h.cfg.Supervisor.RunScript) },
					func() error {
						return e.ProbeDueOccurrencesContext(ctx, now, dueAuth, h.cfg.Fetch, h.cfg.ToolCall, nil)
					},
				} {
					// A failed stage never cancels the others and never swallows
					// its cause — the redacted one-line record is the operator's
					// only durable trace before the next tick retries.
					if err := step(); err != nil {
						cause := strings.Join(strings.Fields(diagnostics.Redact(err.Error())), " ")
						if len(cause) > 256 {
							cause = cause[:256]
						}
						log.Printf("routine tick failed stage=%d: %s; work remains pending", i, cause)
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

func (h *Host) tickStreams(ctx context.Context, e *routines.Engine, principal string, now time.Time) error {
	names, err := os.ReadDir(filepath.Join(h.ownerRoot(principal), "schedules"))
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
		if err := h.prepareJob(&o.Job); err != nil {
			return err
		}
		epoch := o.Job.OperationalEpoch
		if h.authorizeJob(o.Job) != nil || h.cfg.AuthorizeChannel(ctx, schedule.ScheduleID, o.Job) != nil {
			continue
		}
		h.events.mu.Lock()
		st, path, err := h.events.load(o)
		if err != nil {
			h.events.mu.Unlock()
			return err
		}
		if st.Pending != nil && st.Pending.Revision != schedule.Revision {
			// resume fences a batch from an older schedule revision forever; drop its
			// unadmitted events as a recorded gap instead of holding the stream.
			seen := make(map[string]bool, len(st.Seen))
			for _, id := range st.Seen {
				seen[id] = true
			}
			for _, ev := range st.Pending.Events {
				if !seen[ev.ID] {
					st.GapCount++
				}
			}
			st.LastGapAt = now.UTC()
			st.Pending = nil
		}
		if err := rebindEventState(&st, schedule); err != nil {
			h.events.mu.Unlock()
			// One fenced stream must not stop the principal's other streams.
			log.Printf("routine stream %s skipped: %s", safeName(schedule.ScheduleID), strings.Join(strings.Fields(diagnostics.Redact(err.Error())), " "))
			continue
		}
		if st.StreamRevision != 0 && st.StreamRevision != schedule.Revision {
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
			readErr := loadJSON(filepath.Join(h.ownerRoot(principal), "occurrences", routines.OccurrenceID(o)+".json"), &live)
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
		o.Job.OperationalEpoch = epoch
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
	return h.CreateScheduleWithBaseline(ctx, auth, in, nativeCron, nil)
}

func (h *Host) CheckCreationReplay(ctx context.Context, auth identity.Envelope, in commodel.Schedule) (commodel.Schedule, bool, error) {
	if in.ScheduleID == routines.SystemDreamingID || in.JobKind == "dreaming" {
		return commodel.Schedule{}, false, errUnauthorized
	}
	e, err := h.authorize(auth, scheduleOperation(in))
	if err != nil {
		return commodel.Schedule{}, false, err
	}
	if h.cfg.RoutingUser == nil {
		if in.OriginJobID != "" || in.DestinationTaskID != "" {
			return commodel.Schedule{}, false, errors.New("automation: routing owner unavailable")
		}
		return e.CheckCreationReplay(in, auth)
	}
	user, err := h.cfg.RoutingUser(auth.PrincipalID)
	if err != nil {
		return commodel.Schedule{}, false, err
	}
	return e.CheckCreationReplay(in, auth, user)
}

func (h *Host) CreateScheduleWithBaseline(ctx context.Context, auth identity.Envelope, in commodel.Schedule, nativeCron string, baseline *string) (commodel.Schedule, error) {
	if in.ScheduleID == routines.SystemDreamingID || in.JobKind == "dreaming" {
		return commodel.Schedule{}, errUnauthorized
	}
	e, err := h.authorize(auth, scheduleOperation(in))
	if err != nil {
		return commodel.Schedule{}, err
	}
	if h.cfg.RoutingUser == nil {
		if in.OriginJobID != "" || in.DestinationTaskID != "" {
			return commodel.Schedule{}, errors.New("automation: routing owner unavailable")
		}
		return e.CreateScheduleAt(in, auth, nativeCron, baseline, h.now())
	}
	user, err := h.cfg.RoutingUser(auth.PrincipalID)
	if err != nil {
		return commodel.Schedule{}, err
	}
	return e.CreateScheduleAt(in, auth, nativeCron, baseline, h.now(), user)
}

// Recover reloads durable state and reconciles un-acked intents — the
// restart path after process loss.
func (h *Host) CreateAssignment(ctx context.Context, auth identity.Envelope, in commodel.Assignment) (commodel.Assignment, bool, error) {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return commodel.Assignment{}, false, err
	}
	return e.CreateAssignment(in, auth)
}

func (h *Host) LoadAssignment(ctx context.Context, auth identity.Envelope, id string) (commodel.Assignment, error) {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return commodel.Assignment{}, err
	}
	return e.LoadAssignment(id, auth)
}

// AuthorizeAssignmentRun checks the live owner grant and the exact applied
// plan whose wake was admitted by supervisor.
func (h *Host) AuthorizeAssignmentRun(auth identity.Envelope, id string, planRev uint64) error {
	if len(id) > 256 || !commodel.IDPattern.MatchString(id) || planRev == 0 {
		return errUnauthorized
	}
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return err
	}
	item, ok := e.AssignmentForWake(id, planRev)
	if !ok || item.Envelope.PrincipalID != auth.PrincipalID || item.Envelope.ContextID != auth.ContextID ||
		item.Envelope.RuntimeID != auth.RuntimeID {
		return errUnauthorized
	}
	return nil
}

func (h *Host) RecordAssignmentProgress(ctx context.Context, auth identity.Envelope, id string, expected uint64, jobID, summary, finding, nextStep string, complete bool) (commodel.Assignment, error) {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return commodel.Assignment{}, err
	}
	item, err := e.LoadAssignment(id, auth)
	if err != nil {
		return commodel.Assignment{}, err
	}
	if item.Progress.LastJobID == jobID && jobID != "" {
		return item, nil
	}
	value, ok := h.inFlight.Load(auth.PrincipalID + "\x00" + jobID)
	if !ok {
		return commodel.Assignment{}, errUnauthorized
	}
	job := value.(commodel.Job)
	if job.ID != jobID || job.AssignmentID != id || job.PrincipalID != auth.PrincipalID || job.ContextID != auth.ContextID || job.RuntimeID != auth.RuntimeID || job.PolicyVersion != auth.PolicyVersion || job.AssignmentPlanRevision != item.AppliedRev {
		return commodel.Assignment{}, errUnauthorized
	}
	return e.RecordAssignmentProgress(id, auth, expected, jobID, summary, finding, nextStep, complete)
}

func (h *Host) ApplyAssignment(ctx context.Context, auth identity.Envelope, id string, expected ...uint64) (commodel.Assignment, error) {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return commodel.Assignment{}, err
	}
	return e.ApplyAssignment(id, auth, expected...)
}

func (h *Host) UpdateAssignmentPlan(ctx context.Context, auth identity.Envelope, id string, plan commodel.AssignmentPlan, grants commodel.AssignmentGrants, expected ...uint64) (commodel.Assignment, error) {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return commodel.Assignment{}, err
	}
	return e.UpdateAssignmentPlan(id, plan, grants, auth, expected...)
}

func (h *Host) AuthorizeAssignmentGrants(ctx context.Context, auth identity.Envelope, id string, planRev uint64, grants commodel.AssignmentGrants) error {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return err
	}
	return e.AuthorizeAssignmentGrants(id, planRev, grants, auth)
}

func (h *Host) DeleteAssignment(ctx context.Context, auth identity.Envelope, id string, expected ...uint64) error {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return err
	}
	return e.DeleteAssignment(id, auth, expected...)
}

func (h *Host) PauseAssignment(ctx context.Context, auth identity.Envelope, id string, expected ...uint64) error {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return err
	}
	return e.PauseAssignment(id, auth, expected...)
}

func (h *Host) ResumeAssignment(ctx context.Context, auth identity.Envelope, id string, expected ...uint64) error {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return err
	}
	return e.ResumeAssignment(id, auth, expected...)
}

func (h *Host) CompleteAssignment(ctx context.Context, auth identity.Envelope, id string, expected ...uint64) (commodel.Assignment, error) {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return commodel.Assignment{}, err
	}
	return e.CompleteAssignment(id, auth, expected...)
}

func (h *Host) SeedWatchBaseline(ctx context.Context, auth identity.Envelope, id, value string, now time.Time) error {
	e, _, err := h.authorizeSchedule(auth, id)
	if err != nil {
		return err
	}
	return e.SeedWatchBaseline(id, value, now)
}

func (h *Host) LoadWatchState(ctx context.Context, auth identity.Envelope, id string) (routines.WatchState, error) {
	e, _, err := h.authorizeSchedule(auth, id)
	if err != nil {
		return routines.WatchState{}, err
	}
	return e.LoadWatchState(id)
}

func (h *Host) IntakeHook(ctx context.Context, auth identity.Envelope, id string, body []byte) error {
	e, _, err := h.authorizeSchedule(auth, id)
	if err != nil {
		return err
	}
	item, err := e.LoadSchedule(id)
	if err != nil || item.Envelope.PrincipalID != auth.PrincipalID || item.Envelope.ContextID != auth.ContextID || item.Probe == nil || item.Probe.Kind != "hook" || len(body) == 0 || len(body) > 64*1024 {
		return routines.ErrRoutineNotOwned
	}
	return e.IntakeHook(id, body, h.now())
}

func (h *Host) ListSchedules(ctx context.Context, auth identity.Envelope) ([]commodel.Schedule, error) {
	e, err := h.engineFor(auth.PrincipalID)
	if err != nil {
		return nil, err
	}
	items, err := e.ListSchedules(auth)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, operation := range []string{"schedule", "observe", "delegate"} {
		_, err := h.authorize(auth, operation)
		allowed[operation] = err == nil
	}
	if !allowed["schedule"] && !allowed["observe"] && !allowed["delegate"] {
		return nil, errUnauthorized
	}
	filtered := items[:0]
	for _, item := range items {
		if allowed[scheduleOperation(item)] {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (h *Host) LoadSchedule(ctx context.Context, auth identity.Envelope, id string) (commodel.Schedule, error) {
	_, item, err := h.authorizeSchedule(auth, id)
	return item, err
}

func (h *Host) EditRoutine(ctx context.Context, auth identity.Envelope, id string, patch map[string]json.RawMessage, resume bool, baseline *string) (commodel.Schedule, error) {
	e, item, err := h.authorizeSchedule(auth, id)
	if err != nil {
		return commodel.Schedule{}, err
	}
	if raw, ok := patch["job_kind"]; ok {
		var kind string
		if json.Unmarshal(raw, &kind) != nil {
			return commodel.Schedule{}, errUnauthorized
		}
		item.JobKind = kind
		if _, err := h.authorize(auth, scheduleOperation(item)); err != nil {
			return commodel.Schedule{}, err
		}
	}
	var user commodel.User
	if h.cfg.RoutingUser != nil {
		user, err = h.cfg.RoutingUser(auth.PrincipalID)
		if err != nil {
			return commodel.Schedule{}, err
		}
	} else if _, move := patch["destination_task_id"]; move {
		return commodel.Schedule{}, errors.New("automation: destination routing unavailable")
	}
	return e.EditRoutine(id, auth, patch, resume, h.now(), routines.EditOptions{User: user, Baseline: baseline})
}

func (h *Host) ListRoutineDestinations(ctx context.Context, auth identity.Envelope, after string, limit int, query string) (routines.RoutineDestinationPage, error) {
	e, err := h.authorize(auth, "read")
	if err != nil {
		return routines.RoutineDestinationPage{}, err
	}
	if h.cfg.RoutingUser == nil || limit < 1 || limit > 100 {
		return routines.RoutineDestinationPage{}, errors.New("automation: destination routing unavailable")
	}
	user, err := h.cfg.RoutingUser(auth.PrincipalID)
	if err != nil {
		return routines.RoutineDestinationPage{}, err
	}
	return e.ListRoutineDestinations(user, auth, after, limit, query)
}

func (h *Host) PauseSchedule(ctx context.Context, auth identity.Envelope, id string, expected ...uint64) error {
	e, _, err := h.authorizeSchedule(auth, id)
	if err != nil {
		return err
	}
	return e.PauseSchedule(id, auth, expected...)
}

// PauseAllForPrincipal pauses every routine owned by the caller's exact
// principal/context. The control plane already authenticated the bulk stage;
// the engine still re-checks ownership on every durable write. A principal
// whose owner subtree was never materialized owns no schedules — revocation
// must stay idempotent for users that never ran automation.
func (h *Host) PauseAllForPrincipal(ctx context.Context, auth identity.Envelope) (int, error) {
	if _, err := os.Lstat(h.ownerRoot(auth.PrincipalID)); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	e, err := h.engineFor(auth.PrincipalID)
	if err != nil {
		return 0, err
	}
	items, err := e.ListSchedules(auth)
	if err != nil {
		return 0, err
	}
	paused := 0
	for _, item := range items {
		if item.Paused {
			continue
		}
		if err := e.PauseSchedule(item.ScheduleID, auth); err != nil {
			if errors.Is(err, routines.ErrRoutineNotOwned) {
				continue
			}
			return paused, err
		}
		paused++
	}
	return paused, nil
}

func (h *Host) UpdateSchedule(ctx context.Context, auth identity.Envelope, id, expression, input string, enabled *bool) (commodel.Schedule, error) {
	e, _, err := h.authorizeSchedule(auth, id)
	if err != nil {
		return commodel.Schedule{}, err
	}
	return e.UpdateSchedule(id, auth, expression, input, enabled)
}

func (h *Host) SnoozeSchedule(ctx context.Context, auth identity.Envelope, id string, until time.Time, expected ...uint64) error {
	e, _, err := h.authorizeSchedule(auth, id)
	if err != nil {
		return err
	}
	return e.SnoozeSchedule(id, auth, until, expected...)
}

func (h *Host) DeleteSchedule(ctx context.Context, auth identity.Envelope, id string, expected ...uint64) error {
	e, _, err := h.authorizeSchedule(auth, id)
	if err != nil {
		return err
	}
	return e.DeleteSchedule(id, auth, expected...)
}

func (h *Host) ListAssignments(ctx context.Context, auth identity.Envelope) ([]commodel.Assignment, error) {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return nil, err
	}
	return e.ListAssignments(auth)
}

func (h *Host) CancelAssignment(ctx context.Context, auth identity.Envelope, id string, expected ...uint64) error {
	e, err := h.authorize(auth, "delegate")
	if err != nil {
		return err
	}
	return e.CancelAssignment(id, auth, expected...)
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
	if err := h.ValidateStorage(); err != nil {
		return err
	}
	entries, err := os.ReadDir(h.ownersDir())
	if os.IsNotExist(err) && h.cfg.OwnersDir == "" {
		return nil
	}
	if err != nil {
		return err
	}
	for _, en := range entries {
		if _, err := h.engineFor(en.Name()); err != nil {
			return err
		}
	}
	return nil
}

// ValidateStorage is a read-only preflight for startup before any listener or
// clock is enabled. Recover repeats it before writing durable state.
func (h *Host) ValidateStorage() error {
	if h.cfg.LegacySpoolDir != "" {
		if err := commops.ValidateLegacySpool(h.cfg.LegacySpoolDir); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(h.ownersDir())
	if os.IsNotExist(err) && h.cfg.OwnersDir == "" && len(h.cfg.RequiredPrincipals) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if h.cfg.OwnersDir != "" && len(entries) == 0 {
		return errors.New("automation: owner mounts missing")
	}
	seen := make(map[string]bool, len(entries))
	for _, en := range entries {
		if !identity.ValidID(en.Name()) || en.Type()&os.ModeSymlink != 0 || !en.IsDir() {
			return errors.New("automation: unsafe owner storage")
		}
		if err := h.validateOwnerRoot(h.ownerRoot(en.Name())); err != nil {
			return err
		}
		seen[en.Name()] = true
	}
	for _, principal := range h.cfg.RequiredPrincipals {
		if !seen[principal] {
			return errors.New("automation: required owner mount missing")
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
		text = locale.T(h.langOf(principal), "job."+status, jobID)
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
	var occurrenceID, failureSchedule string
	if in.Job.Trigger == "cron" {
		sourceID := in.Job.ID
		if in.SourceOccurrenceID != "" {
			sourceID = in.SourceOccurrenceID
		}
		var occurrence commodel.RoutineOccurrence
		if loadErr := loadJSON(filepath.Join(h.ownerRoot(in.Job.PrincipalID), "occurrences", commodel.FileID(sourceID)+".json"), &occurrence); loadErr == nil && occurrence.Job.ID == sourceID && occurrence.Job.PrincipalID == in.Job.PrincipalID {
			occurrenceID, failureSchedule = sourceID, occurrence.ScheduleID
		}
	}
	if failureSchedule != "" {
		eng, err := h.engineFor(in.Job.PrincipalID)
		if err != nil {
			return err
		}
		if in.Mapping.Status == "completed" {
			if err := eng.ClearRoutineFailure(failureSchedule); err != nil {
				return err
			}
		} else {
			// Routine failures ride the schedule's dedupe digest: identical
			// failures notify once while a notice is delivered or in flight.
			digest := routines.RoutineFailureDigest(in.Job.ID, in.Mapping.Result)
			fresh, err := eng.MarkRoutineFailurePending(failureSchedule, digest)
			if err != nil {
				return err
			}
			if !fresh {
				return nil
			}
		}
	}
	delivery := commodel.Delivery{
		ID:               "result-" + in.Job.ID,
		JobID:            in.Job.ID,
		IdempotencyKey:   "result-" + in.Job.ID,
		OwnerPrincipalID: in.Job.PrincipalID,
		OwnerContextID:   in.Job.ContextID,
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
	}
	if failureSchedule != "" && in.Mapping.Status != "completed" {
		// Marks the notice for the delivery-outcome callback: delivered
		// promotes the pending digest, uncertain releases it.
		delivery.Kind, delivery.OccurrenceID = "routine-failure", occurrenceID
	}
	_, err = h.outbox.enqueue(in.Job.PrincipalID, delivery, h.now())
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
