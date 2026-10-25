package routines

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

func (e *Engine) PutOccurrence(o commodel.RoutineOccurrence, caller identity.Envelope) error {
	if err := ValidateOccurrence(o, caller, e.deps.ChannelFor); err != nil {
		return err
	}
	o.DueAt = o.DueAt.UTC()
	o.State = "pending"
	id := occurrenceID(o)
	o.Job.ID = id
	o.Job.IdempotencyKey = id
	o.Job.Trigger = "cron"
	o.Job.CreatedAt = o.DueAt
	o.Job.MessageID = 0
	encoded, err := json.Marshal(o)
	if err != nil || len(encoded) > 128*1024 {
		return errors.New("occurrence exceeds durable bound")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	path := filepath.Join(e.root, "occurrences", id+".json")
	if b, err := os.ReadFile(path); err == nil {
		var existing commodel.RoutineOccurrence
		if json.Unmarshal(b, &existing) != nil || commodel.JobFingerprint(existing.Job) != commodel.JobFingerprint(o.Job) {
			return errors.New("occurrence identity conflict")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, dir := range []string{"receipts", "archive", "quarantine"} {
		_, err := os.Stat(filepath.Join(e.root, "occurrences", dir, id+".json"))
		if err == nil {
			if dir == "quarantine" {
				return errors.New("occurrence quarantined")
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
	}
	return e.atomicJSON(path, o)
}

// quarantineOccurrenceLocked preserves a bad durable record outside hot scans.
func (e *Engine) quarantineOccurrenceLocked(path string) error {
	dir := filepath.Join(e.root, "occurrences", "quarantine")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Rename(path, filepath.Join(dir, filepath.Base(path))); err != nil {
		return err
	}
	log.Printf("quarantined invalid occurrence %s", filepath.Base(path))
	return nil
}

// DispatchDueOccurrences persists the ordinary job before settling the occurrence.
// A crash between writes is repaired by the ordinary queue's idempotency key.
func (e *Engine) DispatchDueOccurrences(now time.Time, authorize func(commodel.Job) error) error {
	if authorize == nil {
		return errors.New("occurrence authorization required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(e.root, "occurrences"))
	if err != nil {
		return err
	}
	occurrences := make([]commodel.RoutineOccurrence, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(e.root, "occurrences", entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var o commodel.RoutineOccurrence
		if len(b) > 128*1024 || json.Unmarshal(b, &o) != nil || ValidateOccurrence(o, o.Job.Envelope, e.deps.ChannelFor) != nil || entry.Name() != occurrenceID(o)+".json" || o.Job.ID != occurrenceID(o) || o.Job.IdempotencyKey != o.Job.ID {
			if err := e.quarantineOccurrenceLocked(path); err != nil {
				return err
			}
			continue
		}
		occurrences = append(occurrences, o)
	}
	eligible := func(o commodel.RoutineOccurrence) bool {
		window := o.CatchUpWindow
		if window <= 0 {
			window = DefaultCatchUp
		}
		return o.State == "pending" && !now.Before(o.DueAt) && now.Sub(o.DueAt) <= window
	}
	// Expired history must not spend the bounded pass before a deliverable reminder.
	sort.Slice(occurrences, func(i, j int) bool {
		iReady, jReady := eligible(occurrences[i]), eligible(occurrences[j])
		if iReady != jReady {
			return iReady
		}
		return occurrences[i].DueAt.Before(occurrences[j].DueAt)
	})
	// ponytail: single-host occurrence scan; index by schedule if history becomes large.
	active := map[string]string{}
	blocked := map[string]bool{}
	for _, o := range occurrences {
		if o.JobKind == "watcher" || o.JobKind == "check" || o.JobKind == "event" {
			// Probe kinds never admit jobs through this loop; the only
			// mapping one can carry is the escalation product — a job
			// deliberately rewritten at settle time, so the binding check
			// below can only false-positive on it.
			continue
		}
		mapping, err := e.deps.LoadMapping(o.Job.ID)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if commodel.TerminalStatus(mapping.Status) {
			continue
		}
		admitted := o.Job
		admitted.ConversationID = RoutineConversation(o.ScheduleID)
		if !mapping.MatchesJob(admitted) {
			// In-flight occurrences admitted before routine conversations
			// existed still carry the task (or canonical) conversation.
			admitted.ConversationID = o.Job.ConversationID
			if o.Job.TaskID != "" && o.Job.TaskID != commodel.DefaultTaskID {
				admitted.ConversationID = commodel.TaskConversation(o.Job.ChatID, o.Job.TaskID)
			}
		}
		key := o.Job.PrincipalID + "\x00" + o.Job.ContextID + "\x00" + o.ScheduleID
		if mapping.TaskID != o.Job.TaskID || mapping.TopicID != o.Job.TopicID || !mapping.MatchesJob(admitted) {
			// Preserve the admitted job and block overlap for this schedule.
			// Other schedules can still make progress.
			active[key] = o.Job.ID
			blocked[o.Job.ID] = true
			if e.mappingDriftReported == nil {
				e.mappingDriftReported = make(map[string]bool)
			}
			if !e.mappingDriftReported[o.Job.ID] {
				log.Printf("occurrence job binding drift: %s", o.Job.ID)
				e.mappingDriftReported[o.Job.ID] = true
			}
			continue
		}
		if !commodel.TerminalStatus(mapping.Status) && active[key] == "" {
			active[key] = o.Job.ID
		}
	}
	dispatched := 0
	for _, o := range occurrences {
		path := filepath.Join(e.root, "occurrences", occurrenceID(o)+".json")
		if o.State != "pending" || now.Before(o.DueAt) || blocked[o.Job.ID] {
			continue
		}
		window := o.CatchUpWindow
		if window <= 0 {
			window = DefaultCatchUp
		}
		if !e.OccurrenceCurrentLocked(o) {
			o.State = "cancelled"
		} else if now.Sub(o.DueAt) > window {
			o.State = "missed"
		} else if authorize(o.Job) != nil {
			o.State = "blocked"
		} else if o.JobKind == "script" {
			o.State = "scripting"
		} else if o.JobKind == "watcher" || o.JobKind == "check" || o.JobKind == "event" {
			// Probing runs outside the lock in ProbeDueOccurrences.
			o.State = "probing"
		} else if o.JobKind == "digest" || o.JobKind == "brief" {
			pendingProbe := false
			for _, source := range occurrences {
				if (source.State == "pending" || source.State == "probing") && (source.JobKind == "watcher" || source.JobKind == "check") && source.Job.PrincipalID == o.Job.PrincipalID && source.Job.ContextID == o.Job.ContextID && !source.DueAt.After(now) {
					b, err := os.ReadFile(filepath.Join(e.root, "occurrences", occurrenceID(source)+".json"))
					var live commodel.RoutineOccurrence
					if err == nil && json.Unmarshal(b, &live) == nil && (live.State == "pending" || live.State == "probing") {
						pendingProbe = true
						break
					}
				}
			}
			if pendingProbe {
				continue
			}

			if o.JobKind == "brief" {
				if item, err := e.LoadScheduleLocked(o.ScheduleID); err == nil && QuietDefer(item, now).After(now) {
					// A brief's effect is a model call, which cannot ride a
					// delivery not_before: the whole slot waits out the quiet
					// window. Still-due occurrences fire after the window;
					// a window longer than catch-up settles "missed", never
					// silent, and the unread cursor carries changes forward.
					continue
				}
				if err := e.dispatchBriefLocked(o, now, authorize); err != nil {
					return err
				}
			} else if err := e.dispatchDigestLocked(o, now, authorize); err != nil {
				return err
			}
			o.State = "dispatched"
		} else if o.JobKind == "reminder" {
			d, err := e.normalizeDelivery(commodel.Delivery{ID: "reminder-" + o.Job.ID, OccurrenceID: o.Job.ID, Channel: o.Job.Channel, ConversationID: o.Job.ConversationID, DeliveryTargetID: o.Job.DeliveryTargetID, ChatID: o.Job.ChatID, TaskID: o.Job.TaskID, TopicID: o.Job.TopicID, SlackChannel: o.Job.SlackChannel, SlackThread: o.Job.SlackThread, Text: o.Job.Text, CreatedAt: now.UTC()})
			if err != nil {
				return err
			}
			if err = e.deps.EnqueueDelivery(d); err != nil {
				return err
			}
			o.State = "dispatched"
		} else {
			key := o.Job.PrincipalID + "\x00" + o.Job.ContextID + "\x00" + o.ScheduleID
			if running := active[key]; running != "" && running != o.Job.ID {
				continue
			}
			admitted := o.Job
			admitted.Envelope.ConversationID = RoutineConversation(o.ScheduleID)
			if _, err = e.deps.EnqueueJob(admitted); err != nil {
				return err
			}
			o.State = "dispatched"
			active[key] = o.Job.ID
		}
		if err := e.atomicJSON(path, o); err != nil {
			return err
		}
		dispatched++
		if dispatched >= 16 {
			break
		}
	}
	return nil
}

func ValidateOccurrence(o commodel.RoutineOccurrence, caller identity.Envelope, channelFor func(id string) OccurrenceChannel) error {
	if len(o.ScheduleID) > 80 || !commodel.IDPattern.MatchString(o.ScheduleID) || o.Revision == 0 || o.DueAt.IsZero() || !caller.Equal(o.Job.Envelope) || caller.Validate(o.Job.UserID, o.Job.ContextID, o.Job.RuntimeID, o.Job.PolicyVersion) != nil || o.Job.ActorID != caller.PrincipalID || o.Job.ScopeID != "user:"+o.Job.UserID {
		return errors.New("invalid occurrence ownership")
	}
	ch := channelFor(o.Job.Channel)
	if ch == nil {
		return errors.New("invalid occurrence ownership")
	}
	if err := ch.ValidateOccurrence(o, caller); err != nil {
		return err
	}
	if strings.TrimSpace(o.Job.Text) == "" || len(o.Job.Text) > 64*1024 || o.Job.Sensitive || envstore.LooksLikeEnv(o.Job.Text) {
		return errors.New("invalid routine input")
	}
	return nil
}
