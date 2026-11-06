package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

var ErrRoutineRevision = routines.ErrRoutineRevision
var ErrRoutineNotOwned = routines.ErrRoutineNotOwned
var ErrRoutineSnoozeNotLater = routines.ErrRoutineSnoozeNotLater

func (g *Gateway) routineEditBaseline(ctx context.Context, id string, caller identity.Envelope, patch map[string]json.RawMessage) (*string, error) {
	if _, changed := patch["probe"]; !changed {
		return nil, nil
	}
	card, err := g.getRoutineCard(ctx, id, caller)
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

type RoutinePreview = routines.RoutinePreview

// watchSample mirrors the owner's schedule.sample wire shape for decode only.
type sampleEvent struct {
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
	Raw  string `json:"raw,omitempty"`
}
type sampleSource struct {
	Source   string            `json:"source"`
	Events   []sampleEvent     `json:"events,omitempty"`
	Error    string            `json:"error,omitempty"`
	Verdicts map[string]string `json:"verdicts,omitempty"`
}
type watchSample struct {
	ScheduleID string         `json:"schedule_id"`
	Sources    []sampleSource `json:"sources"`
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
	return routines.ScheduleState(item)
}

type RoutineCard struct {
	Destination *RoutineDestination `json:"destination,omitempty"`
	EventState  *EventState         `json:"event_state,omitempty"`
	Schedule    Schedule            `json:"schedule"`
	State       string              `json:"state"`
	WatchState  WatchState          `json:"watch_state"`
}

func (g *Gateway) writeRoutineCard(w http.ResponseWriter, ctx context.Context, id string, caller identity.Envelope) {
	card, err := g.getRoutineCard(ctx, id, caller)
	if err != nil {
		http.Error(w, "routine persisted; current card unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, card)
}

func (g *Gateway) getRoutineCard(ctx context.Context, id string, caller identity.Envelope) (RoutineCard, error) {
	var card RoutineCard
	err := g.automationCall(ctx, caller, "schedule.card", map[string]any{"id": id}, &card)
	var control automation.ControlError
	if errors.As(err, &control) && control.StatusCode == http.StatusNotFound {
		return RoutineCard{}, ErrRoutineNotOwned
	}
	if err == nil && card.Destination != nil && !slices.Contains(g.user(caller.PrincipalID).TelegramIDs, card.Schedule.ChatID) {
		card.Destination.State = "unavailable"
	}
	if err == nil && card.Schedule.JobKind == "reminder" && card.Schedule.LastOccurrenceID != "" {
		runs, historyErr := g.routineHistory(ctx, id, caller)
		if historyErr != nil {
			return RoutineCard{}, historyErr
		}
		for _, run := range runs {
			if run.ID == card.Schedule.LastOccurrenceID {
				card.Schedule.LastOccurrenceState, card.Schedule.LastDeliveryState = run.State, run.DeliveryState
				break
			}
		}
	}
	return card, err
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

func scheduleRevisionPayload(id string, expected []uint64) map[string]any {
	args := map[string]any{"id": id}
	if len(expected) != 0 {
		args["expected_revision"] = expected[0]
	}
	return args
}

func patchRoutineFields(item *Schedule, patch map[string]json.RawMessage) (bool, error) {
	return routines.PatchRoutineFields(item, patch)
}
