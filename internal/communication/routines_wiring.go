package communication

import (
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

const defaultCatchUp = routines.DefaultCatchUp

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

func resolveRelativeReminder(in Schedule, now time.Time) (Schedule, error) {
	return routines.ResolveRelativeReminder(in, now)
}

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
