package communication

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
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
	return envelope.PrincipalID == item.Envelope.PrincipalID && envelope.ContextID == item.Envelope.ContextID && (envelope == item.Envelope || slices.Contains(item.PriorEnvelopes, envelope))
}

type routineDeliveryKey struct {
	OccurrenceID, Channel, Target, TaskID, SlackChannel, SlackThread string
	ChatID, TopicID                                                  int64
}
type routineDeliveryOutcome struct{ Total, Done, Uncertain int }

func (s *Spool) routineDeliveryStatesLocked() (map[routineDeliveryKey]routineDeliveryOutcome, error) {
	out := map[routineDeliveryKey]routineDeliveryOutcome{}
	for _, dir := range []string{"pending", "sending", "done", "failed"} {
		entries, err := os.ReadDir(filepath.Join(s.root, "outbox", dir))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(s.root, "outbox", dir, entry.Name()))
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
		entries, err := os.ReadDir(filepath.Join(s.root, "occurrences", dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(s.root, "occurrences", dir, entry.Name()))
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
			if entry.Name() != run.ID+".json" {
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
