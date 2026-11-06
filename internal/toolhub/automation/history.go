package automation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

type routineRun struct {
	ExecutionState string    `json:"execution_state,omitempty"`
	DeliveryCount  int       `json:"delivery_count,omitempty"`
	ID             string    `json:"occurrence_id"`
	Revision       uint64    `json:"revision"`
	DueAt          time.Time `json:"due_at"`
	Kind           string    `json:"job_kind"`
	State          string    `json:"state"`
	DeliveryState  string    `json:"delivery_state,omitempty"`
}

// routineHistoryKey lets communication join its own delivery receipts without
// sending the occurrence prompt or source payload across the service RPC.
type routineHistoryKey struct {
	OccurrenceID string `json:"occurrence_id"`
	Channel      string `json:"channel"`
	Target       string `json:"target"`
	TaskID       string `json:"task_id"`
	SlackChannel string `json:"slack_channel"`
	SlackThread  string `json:"slack_thread"`
	ChatID       int64  `json:"chat_id"`
	TopicID      int64  `json:"topic_id"`
}

type routineHistoryEntry struct {
	Run routineRun        `json:"run"`
	Key routineHistoryKey `json:"key"`
}

func historyKey(job commodel.Job) routineHistoryKey {
	return routineHistoryKey{OccurrenceID: job.ID, Channel: job.Channel, Target: job.DeliveryTargetID, TaskID: job.TaskID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, ChatID: job.ChatID, TopicID: job.TopicID}
}

func (h *Host) RoutineHistory(ctx context.Context, auth identity.Envelope, id string) ([]routineHistoryEntry, error) {
	card, err := h.RoutineCard(ctx, auth, id)
	if err != nil {
		return nil, err
	}
	h.queue.mu.Lock()
	intents, err := h.queue.scanLenient(auth.PrincipalID)
	h.queue.mu.Unlock()
	if err != nil {
		return nil, err
	}
	mappings := make(map[string]commodel.JobMapping, len(intents))
	for _, in := range intents {
		mappings[in.Job.ID] = in.Mapping
	}
	pending, err := h.outbox.pending()
	if err != nil {
		return nil, err
	}
	queued := map[string]int{}
	for _, rec := range pending {
		if rec.Principal == auth.PrincipalID {
			queued[rec.Delivery.OccurrenceID]++
		}
	}
	found := map[string]routineHistoryEntry{}
	for _, dir := range []string{"", "archive", "receipts"} {
		root := filepath.Join(h.ownerRoot(auth.PrincipalID), "occurrences", dir)
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errUnauthorized
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(root, entry.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Size() > 128*1024 {
				return nil, errUnauthorized
			}
			body, err := os.ReadFile(path) // #nosec G703 -- bounded owner-rooted durable occurrence
			if err != nil {
				return nil, errUnauthorized
			}
			var item routineHistoryEntry
			if dir == "receipts" {
				var receipt struct {
					ID         string            `json:"id"`
					ScheduleID string            `json:"schedule_id"`
					Envelope   identity.Envelope `json:"envelope"`
					Run        routineRun        `json:"run"`
				}
				if json.Unmarshal(body, &receipt) != nil || receipt.ScheduleID != id || !routines.OwnsHistoryEnvelope(card.Schedule, receipt.Envelope) || receipt.ID != receipt.Run.ID {
					continue
				}
				item.Run = receipt.Run
			} else {
				var o commodel.RoutineOccurrence
				if json.Unmarshal(body, &o) != nil || o.ScheduleID != id || !routines.OwnsHistoryEnvelope(card.Schedule, o.Job.Envelope) || o.Job.ID != routines.OccurrenceID(o) {
					continue
				}
				item.Run = routineRun{ID: o.Job.ID, Revision: o.Revision, DueAt: o.DueAt, Kind: o.JobKind, State: o.State}
				if mapping, ok := mappings[o.Job.ID]; ok {
					admitted := o.Job
					admitted.ConversationID = routines.RoutineConversation(o.ScheduleID)
					item.Run.ExecutionState = "unavailable"
					if mapping.MatchesJob(admitted) && mapping.TaskID == o.Job.TaskID && mapping.TopicID == o.Job.TopicID {
						item.Run.ExecutionState = mapping.Status
					}
				}
				item.Key = historyKey(o.Job)
				item.Run.DeliveryCount = queued[o.Job.ID]
				if item.Run.DeliveryCount > 0 {
					item.Run.DeliveryState = "queued"
				}
			}
			if entry.Name() != item.Run.ID+".json" || item.Run.ID == "" {
				continue
			}
			if _, exists := found[item.Run.ID]; !exists {
				found[item.Run.ID] = item
			}
		}
	}
	out := make([]routineHistoryEntry, 0, len(found))
	for _, item := range found {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Run.DueAt.Equal(out[j].Run.DueAt) {
			return out[i].Run.ID < out[j].Run.ID
		}
		return out[i].Run.DueAt.After(out[j].Run.DueAt)
	})
	return out, nil
}
