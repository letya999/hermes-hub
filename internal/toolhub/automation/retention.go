package automation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/routines"
)

type deliveryHistory struct {
	pending, sent, delivered, uncertain, jobs, results int
}

func deliveryOccurrence(d commodel.Delivery) string {
	if d.OccurrenceID != "" {
		return d.OccurrenceID
	}
	if d.JobID != "" && d.ID == "result-"+d.JobID {
		return d.JobID
	}
	return ""
}

// CompactOccurrences is the automation owner's history maintenance. The
// gateway spool is read only: a still-pending channel delivery keeps its
// occurrence available for send-time authorization.
func (h *Host) CompactOccurrences(now time.Time, retention time.Duration) error {
	if retention <= 0 {
		retention = 72 * time.Hour
	}
	h.mu.Lock()
	owners := make(map[string]*routines.Engine, len(h.engines))
	for principal, eng := range h.engines {
		owners[principal] = eng
	}
	h.mu.Unlock()
	for principal, eng := range owners {
		var history map[string]deliveryHistory
		err := eng.CompactOccurrences(now, retention, func(o commodel.RoutineOccurrence) (any, bool, error) {
			if history == nil {
				var err error
				history, err = h.deliveryHistory(principal)
				if err != nil {
					return nil, false, err
				}
			}
			state := history[o.Job.ID]
			if state.pending != 0 || h.cfg.LegacySpoolDir == "" && state.sent != 0 {
				return nil, false, nil
			}
			run := routineRun{ID: o.Job.ID, Revision: o.Revision, DueAt: o.DueAt, Kind: o.JobKind, State: o.State}
			if o.State == "dispatched" {
				if state.sent == 0 || state.results < state.jobs {
					return nil, false, nil
				}
				if o.JobKind == "agent" {
					mapping, err := h.queue.loadMapping(principal, o.Job.ID)
					admitted := o.Job
					admitted.ConversationID = routines.RoutineConversation(o.ScheduleID)
					if err != nil || !commodel.TerminalStatus(mapping.Status) || !mapping.MatchesJob(admitted) {
						return nil, false, nil
					}
					run.ExecutionState = mapping.Status
				}
			}
			run.DeliveryCount = state.delivered + state.uncertain
			switch {
			case state.uncertain != 0:
				run.DeliveryState = "uncertain"
			case state.delivered != 0:
				run.DeliveryState = "delivered"
			case state.sent != 0:
				// The gateway may have compacted its receipt already.
				run.DeliveryState = "unavailable"
			}
			return struct {
				ID         string     `json:"id"`
				ScheduleID string     `json:"schedule_id"`
				Envelope   any        `json:"envelope"`
				Run        routineRun `json:"run"`
			}{o.Job.ID, o.ScheduleID, o.Job.Envelope, run}, true, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) deliveryHistory(principal string) (map[string]deliveryHistory, error) {
	out := map[string]deliveryHistory{}
	h.queue.mu.Lock()
	intents, err := h.queue.scan(principal)
	h.queue.mu.Unlock()
	if err != nil {
		return nil, err
	}
	jobOccurrence := map[string]string{}
	for _, in := range intents {
		id := in.Job.ID
		if in.SourceOccurrenceID != "" {
			id = in.SourceOccurrenceID
		}
		jobOccurrence[in.Job.ID] = id
		v := out[id]
		if commodel.TerminalStatus(in.Mapping.Status) {
			v.jobs++
		} else {
			v.pending++
		}
		out[id] = v
	}
	ownerID := func(d commodel.Delivery) string {
		id := deliveryOccurrence(d)
		if d.OccurrenceID == "" {
			if source := jobOccurrence[id]; source != "" {
				return source
			}
		}
		return id
	}
	if h.cfg.LegacySpoolDir != "" {
		info, err := os.Lstat(h.cfg.LegacySpoolDir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("automation: gateway spool unavailable for compaction")
		}
		info, err = os.Lstat(filepath.Join(h.cfg.LegacySpoolDir, "outbox"))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("automation: gateway outbox unavailable for compaction")
		}
		for _, dir := range []struct {
			name, status string
		}{{"pending", "pending"}, {"sending", "pending"}, {"done", "delivered"}, {"failed", "uncertain"}} {
			root := filepath.Join(h.cfg.LegacySpoolDir, "outbox", dir.name)
			entries, err := os.ReadDir(root)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
					return nil, errors.New("automation: unsafe delivery history")
				}
				body, err := os.ReadFile(filepath.Join(root, entry.Name())) // #nosec G703 -- bounded read-only gateway spool
				var d commodel.Delivery
				if err != nil || json.Unmarshal(body, &d) != nil {
					return nil, errors.New("automation: invalid delivery history")
				}
				id := ownerID(d)
				if id == "" || d.OwnerPrincipalID != "" && d.OwnerPrincipalID != principal {
					continue
				}
				v := out[id]
				switch dir.status {
				case "pending":
					v.pending++
				case "delivered":
					v.delivered++
				case "uncertain":
					v.uncertain++
				}
				out[id] = v
			}
		}
	}
	h.outbox.mu.Lock()
	defer h.outbox.mu.Unlock()
	root := filepath.Join(h.outbox.dir, safeName(principal))
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, errors.New("automation: unsafe owner delivery history")
		}
		var rec outboxRecord
		if err := loadJSON(filepath.Join(root, entry.Name()), &rec); err != nil || rec.Principal != principal || rec.Key == "" || h.outbox.recordPath(principal, rec.Key) != filepath.Join(root, entry.Name()) {
			return nil, errors.New("automation: invalid owner delivery history")
		}
		id := ownerID(rec.Delivery)
		if id == "" {
			continue
		}
		v := out[id]
		if rec.Sent {
			v.sent++
			if rec.Delivery.JobID != "" && rec.Delivery.ID == "result-"+rec.Delivery.JobID {
				v.results++
			}
		} else {
			v.pending++
		}
		out[id] = v
	}
	return out, nil
}
