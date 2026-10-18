package communication

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/identity"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

var ErrRoutineRevision = errors.New("routine revision changed; read current state before retrying")
var ErrRoutineNotOwned = errors.New("routine not found in this context")
var ErrRoutineSnoozeNotLater = errors.New("snooze must be later than the next run")

func effectiveRoutineNextDue(item Schedule) time.Time {
	if !item.NextDue.IsZero() && item.NextDue.Before(item.SnoozedUntil) {
		return item.SnoozedUntil
	}
	return item.NextDue
}

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
	Kind     string    `json:"job_kind"`
	Timezone string    `json:"timezone"`
	ID       string    `json:"schedule_id"`
	Revision uint64    `json:"revision"`
	State    string    `json:"state"`
	NextDue  time.Time `json:"next_due"`
}
type RoutinePage struct {
	Items      []RoutineSummary `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func routinePage(items []Schedule, after string, limit int, kind, state, query string) RoutinePage {
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
		out.Items = append(out.Items, RoutineSummary{ID: item.ScheduleID, Revision: item.Revision, State: itemState, NextDue: item.NextDue, Kind: item.JobKind, Timezone: item.Timezone})
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
		body, err = os.ReadFile(s.cancelPath(id))
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
	if item.Channel == "telegram_bot" {
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

func (s *Spool) editRoutine(id string, caller identity.Envelope, patch map[string]json.RawMessage, resume bool, now time.Time, extra ...routineEditContext) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.loadScheduleLocked(id)
	if err != nil || item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return Schedule{}, ErrRoutineNotOwned
	}
	var expected uint64
	if _, move := patch["destination_task_id"]; move {
		if err := json.Unmarshal(patch["expected_revision"], &expected); err != nil || expected == 0 {
			return Schedule{}, errors.New("destination edit requires positive expected_revision")
		}
	}
	if raw, ok := patch["expected_revision"]; ok {
		if err := json.Unmarshal(raw, &expected); err != nil {
			return Schedule{}, err
		}
		if expected != item.Revision {
			return Schedule{}, ErrRoutineRevision
		}
	}
	oldKind := item.JobKind
	var options routineEditContext
	if len(extra) > 0 {
		options = extra[0]
	}
	rawDestination, move := patch["destination_task_id"]
	if _, hasOrigin := patch["origin_job_id"]; hasOrigin && !move {
		return Schedule{}, errors.New("origin_job_id requires destination_task_id")
	}
	if move {
		previousEnvelope := item.Envelope
		var target, origin string
		if err := json.Unmarshal(rawDestination, &target); err != nil || target == "" {
			return Schedule{}, errors.New("destination_task_id must be a nonempty string")
		}
		if raw, ok := patch["origin_job_id"]; ok {
			if err := json.Unmarshal(raw, &origin); err != nil {
				return Schedule{}, err
			}
		}
		item.DestinationTaskID, item.OriginJobID = target, origin
		item, err = s.bindRoutineDestinationLocked(item, caller, options.User)
		if err != nil {
			return Schedule{}, err
		}
		item.DestinationTaskID, item.OriginJobID = "", ""
		if previousEnvelope != item.Envelope && !slices.Contains(item.PriorEnvelopes, previousEnvelope) {
			item.PriorEnvelopes = append(item.PriorEnvelopes, previousEnvelope)
		}
	}
	timingChanged, err := patchRoutineFields(&item, patch)
	if err != nil {
		return Schedule{}, err
	}
	if _, changed := patch["probe"]; changed || oldKind != item.JobKind {
		item.ProbeRevision = item.Revision + 1
		item.InitialProbeValue = nil
		item.InitialProbeValue = options.Baseline
	}
	if err := validateScheduleDraft(item, item.Envelope); err != nil {
		return Schedule{}, err
	}
	if timingChanged || resume {
		if timingChanged {
			item.IntervalAnchor = now
		}
		loc, err := time.LoadLocation(item.Timezone)
		if err != nil {
			return Schedule{}, err
		}
		next, err := nextSchedule(item, loc, now)
		if err != nil {
			return Schedule{}, err
		}
		item.NextDue = next
		item.SnoozedUntil = time.Time{}
	}
	if resume {
		item.Enabled, item.Paused = true, false
	}
	// A successful owner edit is the documented unblock path: the schedule
	// rejoins the tick on its next due.
	item.BlockedReason, item.BlockedAt = "", time.Time{}
	item.Revision++
	item.UpdatedAt = now
	if err := atomicJSON(s.schedulePath(id), item); err != nil {
		return Schedule{}, err
	}
	return item, nil
}

func patchRoutineFields(item *Schedule, patch map[string]json.RawMessage) (bool, error) {
	timingChanged := false
	if raw, ok := patch["job_kind"]; ok {
		var kind string
		if err := json.Unmarshal(raw, &kind); err != nil {
			return false, err
		}
		if kind != item.JobKind {
			item.Probe, item.InitialProbeValue = nil, nil
			item.Expect, item.NotifyMode = "", ""
			item.MaxRetries = 0
			item.Sink, item.Delivery = "", ""
			item.SourceIDs = nil
		}
	}
	for key, raw := range patch {
		if (key == "input" || key == "expression" || key == "timezone" || key == "job_kind") && strings.TrimSpace(string(raw)) == "null" {
			return false, errors.New("required routine field cannot be null: " + key)
		}
		var target any
		switch key {
		case "destination_task_id", "origin_job_id":
			continue
		case "expected_revision":
			continue
		case "input":
			target = &item.Input
		case "expression":
			target = &item.Expression
			timingChanged = true
		case "timezone":
			target = &item.Timezone
			timingChanged = true
		case "timing":
			target = &item.Timing
			timingChanged = true
		case "job_kind":
			target = &item.JobKind
		case "source_ids":
			target = &item.SourceIDs
		case "sink":
			target = &item.Sink
		case "delivery":
			target = &item.Delivery
		case "probe":
			target = &item.Probe
		case "expect":
			target = &item.Expect
		case "max_retries":
			target = &item.MaxRetries
		case "notify_mode":
			target = &item.NotifyMode
		case "quiet_from":
			target = &item.QuietFrom
		case "quiet_to":
			target = &item.QuietTo
		case "daily_limit":
			target = &item.DailyLimit
		case "catch_up_window_ns":
			target = &item.CatchUpWindow
		default:
			return false, errors.New("unknown or read-only routine field: " + key)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return false, err
		}
	}
	return timingChanged, nil
}
