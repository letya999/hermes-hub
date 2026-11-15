package automation

import (
	"context"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

type routineCard struct {
	Destination *routines.RoutineDestination `json:"destination,omitempty"`
	EventState  *eventCardState              `json:"event_state,omitempty"`
	Schedule    commodel.Schedule            `json:"schedule"`
	State       string                       `json:"state"`
	WatchState  routines.WatchState          `json:"watch_state"`
}

// eventCardState projects only owner-visible state. Pending batches contain
// source payloads and planned jobs, so they never leave the ToolHub owner.
type eventCardState struct {
	ProbeRevision uint64            `json:"probe_revision,omitempty"`
	ScheduleID    string            `json:"schedule_id"`
	Seen          []string          `json:"seen,omitempty"`
	Degraded      bool              `json:"degraded,omitempty"`
	Overflow      int               `json:"overflow,omitempty"`
	LastSuccess   time.Time         `json:"last_success,omitempty"`
	LastError     string            `json:"last_error,omitempty"`
	NotifyDay     string            `json:"notify_day,omitempty"`
	NotifyCount   int               `json:"notify_count,omitempty"`
	Cursor        string            `json:"cursor,omitempty"`
	GapCount      int               `json:"gap_count,omitempty"`
	LastGapAt     time.Time         `json:"last_gap_at,omitempty"`
	SourceCursors map[string]string `json:"source_cursors,omitempty"`
	SourceErrors  map[string]string `json:"source_errors,omitempty"`
}

func (h *Host) RoutineCard(ctx context.Context, auth identity.Envelope, id string) (routineCard, error) {
	if auth.PrincipalID == "" || auth.Environment != "" && auth.Environment != h.cfg.Environment {
		return routineCard{}, errUnauthorized
	}
	e, err := h.engineFor(auth.PrincipalID)
	if err != nil {
		return routineCard{}, err
	}
	item, err := e.LoadSchedule(id)
	if err != nil && !os.IsNotExist(err) {
		return routineCard{}, err
	}
	state := routines.ScheduleState(item)
	if os.IsNotExist(err) {
		err = loadJSON(e.CancelPath(id), &item)
		state = "deleted"
	}
	if err != nil || item.ScheduleID != id || item.Envelope.PrincipalID != auth.PrincipalID || item.Envelope.ContextID != auth.ContextID {
		return routineCard{}, routines.ErrRoutineNotOwned
	}
	if _, err := h.authorize(auth, scheduleOperation(item)); err != nil {
		return routineCard{}, err
	}
	watch, err := e.LoadWatchState(id)
	if err != nil {
		return routineCard{}, err
	}
	card := routineCard{Schedule: item, State: state, WatchState: watch}
	if caps, ok := h.cfg.Channels[item.Channel].(interface{ Caps() channel.Caps }); ok && caps.Caps().Destinations {
		destination := e.RoutineDestination(item)
		if h.cfg.RoutingUser != nil {
			user, err := h.cfg.RoutingUser(auth.PrincipalID)
			if err != nil || !slices.Contains(user.TelegramIDs, item.ChatID) {
				destination.State = "unavailable"
			}
		}
		card.Destination = &destination
	}
	card.Schedule.NextDue = routines.EffectiveNextDue(item)
	if item.JobKind == "reminder" && item.LastOccurrenceID != "" {
		card.Schedule.LastOccurrenceState, card.Schedule.LastDeliveryState = e.ReminderState(item)
	}
	if item.JobKind == "event" {
		h.events.mu.Lock()
		st, _, err := h.events.load(commodel.RoutineOccurrence{ScheduleID: id, Job: commodel.Job{Envelope: item.Envelope}})
		h.events.mu.Unlock()
		if err != nil {
			return routineCard{}, err
		}
		if st.ProbeRevision != item.ProbeRevision {
			if st.Pending != nil {
				return routineCard{}, errUnauthorized
			}
			st = eventState{ScheduleID: id, ProbeRevision: item.ProbeRevision, GapCount: st.GapCount, LastGapAt: st.LastGapAt}
		}
		lastError := ""
		if len(st.SourceErrors) != 0 {
			keys := make([]string, 0, len(st.SourceErrors))
			for key := range st.SourceErrors {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			lastError = st.SourceErrors[keys[0]]
		}
		card.EventState = &eventCardState{ProbeRevision: st.ProbeRevision, ScheduleID: id, Seen: st.Seen, Degraded: st.GapCount != 0 || lastError != "", Overflow: st.Overflow, LastSuccess: st.LastPollAt, LastError: lastError, NotifyDay: st.NotifyDay, NotifyCount: st.NotifyCount, Cursor: st.Cursor, GapCount: st.GapCount, LastGapAt: st.LastGapAt, SourceCursors: st.SourceCursors, SourceErrors: st.SourceErrors}
	}
	return card, nil
}

func (h *Host) PreviewRoutine(ctx context.Context, auth identity.Envelope, id string, count int) (routines.RoutinePreview, error) {
	card, err := h.RoutineCard(ctx, auth, id)
	if err != nil {
		return routines.RoutinePreview{}, err
	}
	return routines.PreviewSchedule(card.Schedule, card.State, count, h.now())
}
