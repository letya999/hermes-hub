package communication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// reminderDeliveryStateLocked mirrors the owner-side receipt read: the
// durable receipt dirs live here now, so the state word comes from the
// communication outbox, not the engine root. s.mu must be held.
func (s *Spool) reminderDeliveryStateLocked(occurrenceID string) string {
	for _, delivery := range []struct{ dir, state string }{{"failed", "uncertain"}, {"done", "delivered"}} {
		_, err := s.fsys.Lstat(filepath.Join(s.root, "outbox", delivery.dir, "reminder-"+occurrenceID+".json"))
		if err == nil {
			return delivery.state
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "unavailable"
		}
	}
	return ""
}

// listSchedules is the owner-scoped schedule list with communication-owned
// receipt state joined in: the owner records occurrence truth, only this
// outbox knows whether the reminder actually reached the channel.
func (g *Gateway) listSchedules(ctx context.Context, caller identity.Envelope) ([]Schedule, error) {
	var items []Schedule
	if err := g.automationCall(ctx, caller, "schedule.list", nil, &items); err != nil {
		return nil, err
	}
	g.spool.mu.Lock()
	defer g.spool.mu.Unlock()
	for i := range items {
		if items[i].JobKind != "reminder" || items[i].LastOccurrenceID == "" {
			continue
		}
		items[i].LastDeliveryState = g.spool.reminderDeliveryStateLocked(items[i].LastOccurrenceID)
	}
	return items, nil
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
		// The owner records the schedule-bound identity; the intake stamps
		// the channel-derived target on the durable delivery. Join on the
		// same normalized identity or owner cards lose every receipt.
		if entry.Key.Target == "" {
			d := Delivery{Channel: entry.Key.Channel, ChatID: entry.Key.ChatID, TaskID: entry.Key.TaskID, TopicID: entry.Key.TopicID, SlackChannel: entry.Key.SlackChannel, SlackThread: entry.Key.SlackThread}
			if ch := channelFor(nil, d.Channel); ch != nil && ch.NormalizeDelivery(&d) == nil {
				entry.Key.Target = d.DeliveryTargetID
			}
		}
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
