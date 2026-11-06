package communication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

type RoutineRun struct {
	ExecutionState string    `json:"execution_state,omitempty"`
	DeliveryCount  int       `json:"delivery_count,omitempty"`
	ID             string    `json:"occurrence_id"`
	Revision       uint64    `json:"revision"`
	DueAt          time.Time `json:"due_at"`
	Kind           string    `json:"job_kind"`
	State          string    `json:"state"`
	DeliveryState  string    `json:"delivery_state,omitempty"`
}
type routineHistoryReceipt struct {
	ID         string            `json:"id"`
	ScheduleID string            `json:"schedule_id"`
	Envelope   identity.Envelope `json:"envelope"`
	Run        RoutineRun        `json:"run"`
}

func routineOwnsHistoryEnvelope(item Schedule, envelope identity.Envelope) bool {
	return routines.OwnsHistoryEnvelope(item, envelope)
}

type routineDeliveryKey struct {
	OccurrenceID string `json:"occurrence_id"`
	Channel      string `json:"channel"`
	Target       string `json:"target"`
	TaskID       string `json:"task_id"`
	SlackChannel string `json:"slack_channel"`
	SlackThread  string `json:"slack_thread"`
	ChatID       int64  `json:"chat_id"`
	TopicID      int64  `json:"topic_id"`
}
type routineDeliveryOutcome struct{ Total, Done, Uncertain int }

func (s *Spool) routineDeliveryStatesLocked() (map[routineDeliveryKey]routineDeliveryOutcome, error) {
	out := map[routineDeliveryKey]routineDeliveryOutcome{}
	for _, dir := range []string{"pending", "sending", "done", "failed"} {
		entries, err := s.fsys.ReadDir(filepath.Join(s.root, "outbox", dir))
		if err != nil {
			return nil, err
		}
		for _, name := range entries {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			body, err := s.fsys.ReadFile(filepath.Join(s.root, "outbox", dir, name))
			if err != nil {
				return nil, err
			}
			var d Delivery
			if json.Unmarshal(body, &d) != nil {
				return nil, errors.New("invalid routine delivery evidence")
			}
			if d.OccurrenceID == "" {
				continue
			}
			key := routineDeliveryKey{OccurrenceID: d.OccurrenceID, Channel: d.Channel, Target: d.DeliveryTargetID, TaskID: d.TaskID, SlackChannel: d.SlackChannel, SlackThread: d.SlackThread, ChatID: d.ChatID, TopicID: d.TopicID}
			state := out[key]
			state.Total++
			if dir == "done" {
				state.Done++
			}
			if dir == "failed" {
				state.Uncertain++
			}
			out[key] = state
		}
	}
	return out, nil
}

func (s *Spool) routineRunLocked(o RoutineOccurrence, outcomes map[routineDeliveryKey]routineDeliveryOutcome) RoutineRun {
	run := RoutineRun{ID: o.Job.ID, Revision: o.Revision, DueAt: o.DueAt, Kind: o.JobKind, State: o.State}
	if mapping, err := s.loadMappingLocked(o.Job.ID); err == nil {
		admitted := o.Job
		admitted.ConversationID = routineConversation(o.ScheduleID)
		run.ExecutionState = "unavailable"
		if mapping.MatchesJob(admitted) && mapping.TaskID == o.Job.TaskID && mapping.TopicID == o.Job.TopicID {
			run.ExecutionState = mapping.Status
		}
	}
	key := routineDeliveryKey{OccurrenceID: o.Job.ID, Channel: o.Job.Channel, Target: o.Job.DeliveryTargetID, TaskID: o.Job.TaskID, SlackChannel: o.Job.SlackChannel, SlackThread: o.Job.SlackThread, ChatID: o.Job.ChatID, TopicID: o.Job.TopicID}
	state := outcomes[key]
	run.DeliveryCount = state.Total
	if state.Total > 0 {
		switch {
		case state.Uncertain > 0:
			run.DeliveryState = "uncertain"
		case state.Done == state.Total:
			run.DeliveryState = "delivered"
		case state.Done > 0:
			run.DeliveryState = "partial"
		default:
			run.DeliveryState = "queued"
		}
	}
	return run
}
func (s *Spool) RoutineHistory(id string, caller identity.Envelope) ([]RoutineRun, error) {
	card, err := s.GetRoutine(id, caller)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	outcomes, err := s.routineDeliveryStatesLocked()
	if err != nil {
		return nil, err
	}
	found := map[string]RoutineRun{}
	for _, dir := range []string{"", "archive", "receipts"} {
		entries, err := s.fsys.ReadDir(filepath.Join(s.root, "occurrences", dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, name := range entries {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			body, err := s.fsys.ReadFile(filepath.Join(s.root, "occurrences", dir, name))
			if err != nil {
				return nil, err
			}
			var run RoutineRun
			if dir == "receipts" {
				var receipt routineHistoryReceipt
				if json.Unmarshal(body, &receipt) != nil || receipt.ScheduleID != id || !routineOwnsHistoryEnvelope(card.Schedule, receipt.Envelope) || receipt.ID != receipt.Run.ID {
					continue
				}
				run = receipt.Run
			} else {
				var o RoutineOccurrence
				if json.Unmarshal(body, &o) != nil || o.ScheduleID != id || !routineOwnsHistoryEnvelope(card.Schedule, o.Job.Envelope) || o.Job.ID != occurrenceID(o) {
					continue
				}
				run = s.routineRunLocked(o, outcomes)
			}
			if name != run.ID+".json" {
				continue
			}
			if _, ok := found[run.ID]; !ok {
				found[run.ID] = run
			}
		}
	}
	out := make([]RoutineRun, 0, len(found))
	for _, run := range found {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DueAt.Equal(out[j].DueAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].DueAt.After(out[j].DueAt)
	})
	return out, nil
}

func (g *Gateway) routineHistory(ctx context.Context, id string, caller identity.Envelope) ([]RoutineRun, error) {
	var entries []struct {
		Run RoutineRun         `json:"run"`
		Key routineDeliveryKey `json:"key"`
	}
	if err := g.automationCall(ctx, caller, "schedule.history", map[string]any{"id": id}, &entries); err != nil {
		return nil, err
	}
	g.spool.mu.Lock()
	outcomes, err := g.spool.routineDeliveryStatesLocked()
	g.spool.mu.Unlock()
	if err != nil {
		return nil, err
	}
	items := make([]RoutineRun, 0, len(entries))
	for _, entry := range entries {
		run := entry.Run
		state := outcomes[entry.Key]
		if state.Total > 0 {
			run.DeliveryCount = state.Total
			switch {
			case state.Uncertain > 0:
				run.DeliveryState = "uncertain"
			case state.Done == state.Total:
				run.DeliveryState = "delivered"
			case state.Done > 0:
				run.DeliveryState = "partial"
			default:
				run.DeliveryState = "queued"
			}
		}
		items = append(items, run)
	}
	return items, nil
}

type RoutineHistoryPage struct {
	Items      []RoutineRun `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

func routineHistoryPage(items []RoutineRun, after string, limit int) RoutineHistoryPage {
	out := RoutineHistoryPage{Items: []RoutineRun{}}
	ready := after == ""
	for _, item := range items {
		if !ready {
			ready = item.ID == after
			continue
		}
		if len(out.Items) == limit {
			out.NextCursor = out.Items[len(out.Items)-1].ID
			break
		}
		out.Items = append(out.Items, item)
	}
	return out
}
