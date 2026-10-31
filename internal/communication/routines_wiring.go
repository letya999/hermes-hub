package communication

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

// Routines semantics (validation, calendar math, extract pipelines, ids) live
// in internal/routines; these delegates keep the package's existing call
// sites and exported contract stable. Engine concerns (spool IO, dispatch,
// watchers) stay here.

var errRoutineExhausted = routines.ErrExhausted

// scheduleDraftHooks resolves the two checks draft validation cannot own:
// the provider's own field rules through the channel registry, and the
// Starlark compiler for script bodies.
var scheduleDraftHooks = routines.DraftHooks{
	Channel: func(in commodel.Schedule) error {
		if ch := channelFor(nil, in.Channel); ch == nil {
			return errors.New("invalid schedule channel")
		} else if err := ch.ValidateSchedule(in); err != nil {
			return err
		}
		return nil
	},
	Script: ScriptSyntaxError,
}

func scheduleIntent(in Schedule) string { return routines.ScheduleIntent(in) }

func ValidateSchedule(in Schedule, caller identity.Envelope, now time.Time) error {
	return routines.ValidateSchedule(in, caller, now, scheduleDraftHooks)
}

func validateScheduleDraft(in Schedule, caller identity.Envelope) error {
	return routines.ValidateScheduleDraft(in, caller, scheduleDraftHooks)
}

func normalizeScheduleDraft(in Schedule) Schedule { return routines.NormalizeScheduleDraft(in) }

func extractValue(probe *WatchProbe, body []byte) (string, error) {
	return routines.ExtractValue(probe, body)
}

func nextSchedule(in Schedule, loc *time.Location, from time.Time) (time.Time, error) {
	return routines.NextSchedule(in, loc, from)
}

func OccurrenceID(o RoutineOccurrence) string { return routines.OccurrenceID(o) }

func occurrenceID(o RoutineOccurrence) string { return routines.OccurrenceID(o) }

func routineConversation(scheduleID string) string { return routines.RoutineConversation(scheduleID) }

func routineFailureDigest(jobID, message string) string {
	return routines.RoutineFailureDigest(jobID, message)
}

func nextCron(expr string, loc *time.Location, from time.Time) (time.Time, error) {
	return routines.NextCron(expr, loc, from)
}

func watchValue(body []byte, re *regexp.Regexp) string { return routines.WatchValue(body, re) }

// Engine delegates: the routines engine (internal/routines) owns schedule,
// occurrence, watcher and digest orchestration; these wrappers keep existing
// spool call sites stable. Methods whose names end in Locked run under the
// shared spool/engine lock.

type RoutineDestination = routines.RoutineDestination
type RoutineDestinationPage = routines.RoutineDestinationPage

func (s *Spool) reminderStateLocked(item Schedule) (string, string) {
	return s.rt.ReminderStateLocked(item)
}

func (s *Spool) routineDestinationLocked(item Schedule) RoutineDestination {
	return s.rt.RoutineDestinationLocked(item)
}

func (s *Spool) bindRoutineDestinationLocked(in Schedule, caller identity.Envelope, user User) (Schedule, error) {
	return s.rt.BindRoutineDestinationLocked(in, caller, user)
}

func (s *Spool) ListRoutineDestinations(user User, caller identity.Envelope, after string, limit int, query string) (RoutineDestinationPage, error) {
	return s.rt.ListRoutineDestinations(user, caller, after, limit, query)
}

var ErrScheduleConflict = routines.ErrScheduleConflict

func (s *Spool) CreateSchedule(in Schedule, caller identity.Envelope, nativeCron string) (Schedule, error) {
	return s.rt.CreateSchedule(in, caller, nativeCron)
}

func (s *Spool) createScheduleAt(in Schedule, caller identity.Envelope, nativeCron string, initialValue *string, now time.Time, routingUser ...User) (Schedule, error) {
	return s.rt.CreateScheduleAt(in, caller, nativeCron, initialValue, now, routingUser...)
}

func (s *Spool) ListSchedules(caller identity.Envelope) ([]Schedule, error) {
	return s.rt.ListSchedules(caller)
}

func (s *Spool) UpdateSchedule(id string, caller identity.Envelope, expression, input string, enabled *bool) (Schedule, error) {
	return s.rt.UpdateSchedule(id, caller, expression, input, enabled)
}

func (s *Spool) PauseSchedule(id string, caller identity.Envelope, expected ...uint64) error {
	return s.rt.PauseSchedule(id, caller, expected...)
}

func (s *Spool) DeleteSchedule(id string, caller identity.Envelope, expected ...uint64) error {
	return s.rt.DeleteSchedule(id, caller, expected...)
}

func (s *Spool) SnoozeSchedule(id string, caller identity.Envelope, until time.Time, expected ...uint64) error {
	return s.rt.SnoozeSchedule(id, caller, until, expected...)
}

func (s *Spool) RecordRoutineFailure(scheduleID, digest string) (bool, error) {
	return s.rt.RecordRoutineFailure(scheduleID, digest)
}

func (s *Spool) MarkRoutineFailurePending(scheduleID, digest string) (bool, error) {
	return s.rt.MarkRoutineFailurePending(scheduleID, digest)
}

func (s *Spool) ReleaseRoutineFailurePending(scheduleID, digest string) error {
	return s.rt.ReleaseRoutineFailurePending(scheduleID, digest)
}

func (s *Spool) ClearRoutineFailure(scheduleID string) error {
	return s.rt.ClearRoutineFailure(scheduleID)
}

func (s *Spool) TickSchedules(now time.Time, nativeCron string, authorize func(Job) error) error {
	return s.rt.TickSchedules(now, nativeCron, authorize)
}

func (s *Spool) PutOccurrence(o RoutineOccurrence, caller identity.Envelope) error {
	return s.rt.PutOccurrence(o, caller)
}

func (s *Spool) DispatchDueOccurrences(now time.Time, authorize func(Job) error) error {
	return s.rt.DispatchDueOccurrences(now, authorize)
}

func (s *Spool) loadScheduleLocked(id string) (Schedule, error) { return s.rt.LoadScheduleLocked(id) }

func (s *Spool) creationReplayLocked(in Schedule, caller identity.Envelope) (Schedule, bool, error) {
	return s.rt.CreationReplayLocked(in, caller)
}

func (s *Spool) schedulePath(id string) string { return s.rt.SchedulePath(id) }

func (s *Spool) cancelPath(id string) string { return s.rt.CancelPath(id) }

func (s *Spool) occurrenceCurrentLocked(o RoutineOccurrence) bool {
	return s.rt.OccurrenceCurrentLocked(o)
}

func (s *Spool) blockScheduleLocked(item *Schedule, reason string, now time.Time) error {
	return s.rt.BlockScheduleLocked(item, reason, now)
}

func validateOccurrence(o RoutineOccurrence, caller identity.Envelope) error {
	return routines.ValidateOccurrence(o, caller, func(id string) routines.OccurrenceChannel { return channelFor(nil, id) })
}

type WatchState = routines.WatchState
type DigestState = routines.DigestState
type probeResult = routines.ProbeResult

func watchHash(value string) string { return routines.WatchHash(value) }

func quietDefer(schedule Schedule, now time.Time) time.Time {
	return routines.QuietDefer(schedule, now)
}

func boundedWatchText(value string) string { return routines.BoundedWatchText(value) }

func probeValue(probe *WatchProbe, body []byte) (string, error) {
	return routines.ProbeValue(probe, body)
}

func (s *Spool) watchPath(id string) string { return s.rt.WatchPath(id) }

func (s *Spool) digestPath(id string) string { return s.rt.DigestPath(id) }

func (s *Spool) loadWatchStateLocked(id string) (WatchState, error) {
	return s.rt.LoadWatchStateLocked(id)
}

// watchLastError surfaces a watcher's durable probe failure in routine
// listings — a dead source must not masquerade as a healthy quiet one.
func (s *Spool) watchLastError(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.loadWatchStateLocked(id)
	if err != nil {
		return ""
	}
	return st.LastError
}

func (s *Spool) taskProbeValueLocked(principal, taskID string) (string, error) {
	return s.rt.TaskProbeValueLocked(principal, taskID)
}

func (s *Spool) SeedWatchBaseline(id string, value string, now time.Time) error {
	return s.rt.SeedWatchBaseline(id, value, now)
}

func (s *Spool) ProbeDueOccurrences(now time.Time, fetch func(context.Context, string) ([]byte, error)) error {
	return s.rt.ProbeDueOccurrences(now, fetch)
}

func (s *Spool) ProbeDueOccurrencesContext(ctx context.Context, now time.Time, authorize func(Job) error, fetch func(context.Context, string) ([]byte, error), toolCall func(context.Context, string, *WatchProbe) ([]byte, error), serviceCall func(context.Context, string, *WatchProbe) ([]byte, error)) error {
	return s.rt.ProbeDueOccurrencesContext(ctx, now, authorize, fetch, toolCall, serviceCall)
}

func (s *Spool) processProactive(ctx context.Context, state string, execute func(RoutineOccurrence)) error {
	return s.rt.ProcessProactive(ctx, state, execute)
}

func (s *Spool) applyProbeResultLocked(result probeResult) error {
	return s.rt.ApplyProbeResultLocked(result)
}

func (s *Spool) RunDueScripts(ctx context.Context, now time.Time, authorize func(Job) error, run func(context.Context, RoutineOccurrence) ([]string, error)) error {
	return s.rt.RunDueScripts(ctx, now, authorize, run)
}

// ScriptSyntaxError compiles a scheduled-script body under the routines
// runner's file options — the check lives with the engine.
func ScriptSyntaxError(src string) error { return routines.ScriptSyntaxError(src) }

const scriptMaxSource = routines.ScriptMaxSource

func RunScript(ctx context.Context, src string, now time.Time) ([]string, error) {
	return routines.RunScript(ctx, src, now)
}

func RunScriptProbe(ctx context.Context, src string, now time.Time, input, watchRoot string, needs []string) ([]string, error) {
	return routines.RunScriptProbe(ctx, src, now, input, watchRoot, needs)
}
