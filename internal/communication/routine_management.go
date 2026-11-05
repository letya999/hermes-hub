package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

var ErrRoutineRevision = routines.ErrRoutineRevision
var ErrRoutineNotOwned = routines.ErrRoutineNotOwned
var ErrRoutineSnoozeNotLater = routines.ErrRoutineSnoozeNotLater

func effectiveRoutineNextDue(item Schedule) time.Time { return routines.EffectiveNextDue(item) }

func (g *Gateway) routineEditBaseline(ctx context.Context, id string, caller identity.Envelope, patch map[string]json.RawMessage) (*string, error) {
	if _, changed := patch["probe"]; !changed {
		return nil, nil
	}
	card, err := g.spool.GetRoutine(id, caller)
	if err != nil {
		return nil, err
	}
	if raw, ok := patch["expected_revision"]; ok {
		var revision uint64
		if err := json.Unmarshal(raw, &revision); err != nil {
			return nil, err
		}
		if revision != card.Schedule.Revision {
			return nil, ErrRoutineRevision
		}
	}
	draft := card.Schedule
	if _, err := patchRoutineFields(&draft, patch); err != nil {
		return nil, err
	}
	if err := validateScheduleDraft(draft, draft.Envelope); err != nil {
		return nil, err
	}
	if draft.JobKind != "check" {
		return nil, nil
	}
	patch["expected_revision"], err = json.Marshal(card.Schedule.Revision)
	if err != nil {
		return nil, err
	}
	value, err := g.checkBaseline(ctx, caller.PrincipalID, draft.Probe)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

type RoutinePreview struct {
	State    string      `json:"state"`
	Times    []time.Time `json:"times"`
	Timezone string      `json:"timezone"`
}

func (s *Spool) PreviewRoutine(id string, caller identity.Envelope, count int, now time.Time) (RoutinePreview, error) {
	card, err := s.GetRoutine(id, caller)
	if err != nil {
		return RoutinePreview{}, err
	}
	item := card.Schedule
	loc, err := time.LoadLocation(item.Timezone)
	if err != nil {
		return RoutinePreview{}, err
	}
	out := RoutinePreview{Times: []time.Time{}, Timezone: item.Timezone, State: card.State}
	if card.State == "completed" || card.State == "deleted" {
		return out, nil
	}
	next := item.NextDue
	if item.SnoozedUntil.After(next) {
		next = item.SnoozedUntil
	}
	for i := 0; i < count; i++ {
		if !next.After(now) {
			next, err = nextSchedule(item, loc, now)
			if errors.Is(err, errRoutineExhausted) {
				break
			}
			if err != nil {
				return out, err
			}
		}
		out.Times = append(out.Times, next)
		if strings.HasPrefix(strings.TrimSpace(item.Expression), "once:") {
			break
		}
		now = next
		next = time.Time{}
	}
	return out, nil
}

type RoutineSummary struct {
	Kind      string    `json:"job_kind"`
	Timezone  string    `json:"timezone"`
	ID        string    `json:"schedule_id"`
	Revision  uint64    `json:"revision"`
	State     string    `json:"state"`
	NextDue   time.Time `json:"next_due"`
	LastError string    `json:"last_error,omitempty"`
}
type RoutinePage struct {
	Items      []RoutineSummary `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func routinePage(items []Schedule, after string, limit int, kind, state, query string, lastError func(id string) string) RoutinePage {
	sort.Slice(items, func(i, j int) bool { return items[i].ScheduleID < items[j].ScheduleID })
	out := RoutinePage{Items: []RoutineSummary{}}
	query = strings.ToLower(query)
	for _, item := range items {
		if !strings.Contains(strings.ToLower(item.ScheduleID+" "+item.Input), query) {
			continue
		}
		itemState := routineState(item)
		if (kind != "" && item.JobKind != kind) || (state != "" && itemState != state) {
			continue
		}
		if item.ScheduleID <= after {
			continue
		}
		if len(out.Items) == limit {
			out.NextCursor = out.Items[len(out.Items)-1].ID
			break
		}
		summary := RoutineSummary{ID: item.ScheduleID, Revision: item.Revision, State: itemState, NextDue: item.NextDue, Kind: item.JobKind, Timezone: item.Timezone}
		if lastError != nil {
			summary.LastError = lastError(item.ScheduleID)
		}
		out.Items = append(out.Items, summary)
	}
	return out
}

func routineState(item Schedule) string {
	if item.BlockedReason != "" {
		return "blocked"
	}
	if !item.Enabled && item.NextDue.IsZero() {
		return "completed"
	}
	if item.Paused {
		return "paused"
	}
	return "active"
}

type RoutineCard struct {
	Destination *RoutineDestination `json:"destination,omitempty"`
	EventState  *EventState         `json:"event_state,omitempty"`
	Schedule    Schedule            `json:"schedule"`
	State       string              `json:"state"`
	WatchState  WatchState          `json:"watch_state"`
}

func (g *Gateway) writeRoutineCard(w http.ResponseWriter, id string, caller identity.Envelope) {
	card, err := g.getRoutineCard(id, caller)
	if err != nil {
		http.Error(w, "routine persisted; current card unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, card)
}

func (g *Gateway) getRoutineCard(id string, caller identity.Envelope) (RoutineCard, error) {
	card, err := g.spool.GetRoutine(id, caller)
	if err == nil && card.Destination != nil && !slices.Contains(g.user(caller.PrincipalID).TelegramIDs, card.Schedule.ChatID) {
		card.Destination.State = "unavailable"
	}
	return card, err
}

func (s *Spool) GetRoutine(id string, caller identity.Envelope) (RoutineCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.loadScheduleLocked(id)
	state := routineState(item)
	if os.IsNotExist(err) {
		var body []byte
		body, err = s.fsys.ReadFile(s.cancelPath(id)) // #nosec G703 -- id is sanitized by commodel.FileID
		if err == nil {
			err = json.Unmarshal(body, &item)
			state = "deleted"
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return RoutineCard{}, err
	}
	if err != nil || item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return RoutineCard{}, ErrRoutineNotOwned
	}
	watch, err := s.loadWatchStateLocked(id)
	if err != nil {
		return RoutineCard{}, err
	}
	if item.JobKind == "reminder" && item.LastOccurrenceID != "" {
		item.LastOccurrenceState, item.LastDeliveryState = s.reminderStateLocked(item)
	}
	card := RoutineCard{Schedule: item, State: state, WatchState: watch}
	if ch := channelFor(nil, item.Channel); ch != nil && ch.Caps().Destinations {
		destination := s.routineDestinationLocked(item)
		card.Destination = &destination
	}
	card.Schedule.NextDue = effectiveRoutineNextDue(item)
	if item.JobKind == "event" {
		event, err := s.loadEventStateLocked(id)
		if err != nil {
			return RoutineCard{}, err
		}
		card.EventState = &event
	}
	return card, nil
}

func routineRevisionRequest(r *http.Request) ([]uint64, error) {
	if r.ContentLength == 0 {
		return nil, nil
	}
	var in struct {
		ExpectedRevision *uint64 `json:"expected_revision"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return nil, err
	}
	if in.ExpectedRevision == nil {
		return nil, nil
	}
	return []uint64{*in.ExpectedRevision}, nil
}

type routineEditContext struct {
	User     User
	Baseline *string
}

// editRoutine delegates the owner edit to the engine: the read-modify-write
// runs under the engine lock, so callers must not hold s.mu.
func (s *Spool) editRoutine(id string, caller identity.Envelope, patch map[string]json.RawMessage, resume bool, now time.Time, extra ...routineEditContext) (Schedule, error) {
	var opts routines.EditOptions
	if len(extra) > 0 {
		opts = routines.EditOptions{User: extra[0].User, Baseline: extra[0].Baseline}
	}
	return s.rt.EditRoutine(id, caller, patch, resume, now, opts)
}

func patchRoutineFields(item *Schedule, patch map[string]json.RawMessage) (bool, error) {
	return routines.PatchRoutineFields(item, patch)
}
