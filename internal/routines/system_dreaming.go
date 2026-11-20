package routines

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

const SystemDreamingID = "system-dreaming"

// SyncDreamingSchedule projects the published memory clock into the same
// schedule store and due calculator as user routines. Only the host calls it.
func (e *Engine) SyncDreamingSchedule(auth identity.Envelope, localTime, zone, revision string, enabled, rearm bool, now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	path := e.SchedulePath(SystemDreamingID)
	if !enabled {
		if err := e.removeOwned(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	clock, err := time.Parse("15:04", localTime)
	if err != nil || clock.Format("15:04") != localTime || revision == "" {
		return errors.New("invalid published dreaming clock")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return err
	}
	expression := localTime[3:] + " " + localTime[:2] + " * * *"
	var prior commodel.Schedule
	body, err := e.readFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil {
		if json.Unmarshal(body, &prior) != nil {
			return errors.New("invalid protected dreaming schedule")
		}
		if prior.ScheduleID != SystemDreamingID || prior.JobKind != "dreaming" || prior.Envelope.PrincipalID != auth.PrincipalID {
			return errors.New("invalid protected dreaming schedule")
		}
		if prior.Expression == expression && prior.Timezone == zone && prior.Input == revision && prior.Envelope.Equal(auth) && prior.Enabled && (prior.BlockedReason == "" || !rearm) {
			return nil
		}
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).Add(-time.Nanosecond)
	item := commodel.Schedule{ScheduleID: SystemDreamingID, Revision: prior.Revision + 1,
		Envelope: auth, UserID: auth.PrincipalID, ActorID: auth.PrincipalID, ScopeID: "user:" + auth.PrincipalID,
		Channel: "system", Timezone: zone, Expression: expression, JobKind: "dreaming", Input: revision,
		Enabled: true, CatchUpWindow: 48 * time.Hour, CreatedAt: prior.CreatedAt, UpdatedAt: now.UTC()}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now.UTC()
	}
	item.NextDue, err = NextSchedule(item, loc, start)
	if err != nil {
		return err
	}
	return e.atomicJSON(path, item)
}

// dreamingAttempts bounds one night's operation failures. The proactive pass
// always picks a schedule's oldest occurrence, so a night that never settles
// would block every later night.
const dreamingAttempts = 3

// dreamingRunTimeout bounds one night's operation: a wedged run cannot hold
// the schedule's oldest slot (and every later night) forever. A var so the
// timeout is exercisable in tests; a timeout counts as one attempt.
var dreamingRunTimeout = 10 * time.Minute

var errDreamingTimeout = errors.New("dreaming run timed out")

// runBounded executes the occurrence operation under a deadline. A run that
// ignores cancellation still leaks its goroutine but no longer blocks the
// proactive pass; the parent's own cancellation passes through so a host
// shutdown leaves the night pending instead of burning an attempt.
func runBounded(ctx context.Context, run func(context.Context, commodel.RoutineOccurrence) error, o commodel.RoutineOccurrence) error {
	runCtx, cancel := context.WithTimeout(ctx, dreamingRunTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(runCtx, o) }()
	select {
	case err := <-done:
		return err
	case <-runCtx.Done():
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errDreamingTimeout
	}
}

// RunDueDreaming resumes occurrence work after a host restart. The memory
// processor owns its finer checkpoints; this routine owns occurrence state.
func (e *Engine) RunDueDreaming(ctx context.Context, now time.Time, authorize func(commodel.Job) error, run func(context.Context, commodel.RoutineOccurrence) error) error {
	var failures []error
	err := e.ProcessProactive(ctx, "dreaming", func(o commodel.RoutineOccurrence) {
		if o.JobKind != "dreaming" || o.ScheduleID != SystemDreamingID {
			return
		}
		e.mu.Lock()
		current := e.OccurrenceCurrent(o) && authorize(o.Job) == nil
		e.mu.Unlock()
		if !current {
			o.State = "cancelled"
			if err := e.atomicJSON(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
				e.mu.Lock()
				failures = append(failures, err)
				e.mu.Unlock()
			}
			return
		}
		if o.RetryAfter.After(now) {
			return
		}
		if err := runBounded(ctx, run, o); err != nil {
			if ctx.Err() != nil {
				// Host shutdown, not a failure: keep the night pending for
				// the next pass rather than spend one of its attempts.
				return
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			failures = append(failures, err)
			o.RetryCount++
			o.RetryAfter = now.Add(time.Duration(o.RetryCount) * time.Minute).UTC()
			if o.RetryCount >= dreamingAttempts {
				o.State = "failed"
			}
			if err := e.atomicJSON(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
				failures = append(failures, err)
			}
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		// The operation has committed its own checkpoint and rechecked rights
		// before that commit; a later revoke cannot undo the applied result.
		o.State = "dispatched"
		if err := e.atomicJSON(filepath.Join(e.root, "occurrences", occurrenceID(o)+".json"), o); err != nil {
			failures = append(failures, err)
		}
	})
	return errors.Join(append(failures, err)...)
}
