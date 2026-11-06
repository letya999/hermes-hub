package communication

import (
	"context"
	"errors"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

// Routines semantics (validation, calendar math, extract pipelines, ids) live
// in internal/routines; these delegates keep the package's existing call
// sites and exported contract stable. Automation ownership (writer, clock,
// dispatch) lives in internal/toolhub/automation — no engine runs here.

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

func ValidateSchedule(in Schedule, caller identity.Envelope, now time.Time) error {
	return routines.ValidateSchedule(in, caller, now, scheduleDraftHooks)
}

func validateScheduleDraft(in Schedule, caller identity.Envelope) error {
	return routines.ValidateScheduleDraft(in, caller, scheduleDraftHooks)
}

func normalizeScheduleDraft(in Schedule) Schedule { return routines.NormalizeScheduleDraft(in) }

func probeValue(probe *WatchProbe, body []byte) (string, error) {
	return routines.ProbeValue(probe, body)
}

var ErrScheduleConflict = routines.ErrScheduleConflict

type RoutineDestination = routines.RoutineDestination
type RoutineDestinationPage = routines.RoutineDestinationPage

type WatchState = routines.WatchState
type DigestState = routines.DigestState

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
