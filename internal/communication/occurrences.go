package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

// RoutineOccurrence is the durable handoff from a schedule owner. Recurrence
// calculation belongs to that owner; each occurrence enters the ordinary queue.
type RoutineOccurrence struct {
	ScheduleID string    `json:"schedule_id"`
	Revision   uint64    `json:"revision"`
	DueAt      time.Time `json:"due_at"`
	Job        Job       `json:"job"`
	// JobKind mirrors the schedule kind. "reminder" occurrences deliver their
	// text through the outbox directly — no ordinary job, no runtime, no model.
	JobKind string `json:"job_kind,omitempty"`
	// RetryCount counts probe-error retries (check kind, SPEC-0054 R1.1) and
	// RetryAfter holds them back until the backoff deadline passes.
	RetryCount int       `json:"retry_count,omitempty"`
	RetryAfter time.Time `json:"retry_after,omitempty"`
	State      string    `json:"state"`
}

func OccurrenceID(o RoutineOccurrence) string { return occurrenceID(o) }

func occurrenceID(o RoutineOccurrence) string {
	sum := sha256.Sum256([]byte(o.ScheduleID + "\x00" + strconv.FormatUint(o.Revision, 10) + "\x00" + o.DueAt.UTC().Format(time.RFC3339Nano)))
	return "routine-" + hex.EncodeToString(sum[:])
}

// routineConversation isolates routine sessions: every occurrence of one
// schedule shares a stable "routine-<id>" conversation instead of the owner's
// interactive task conversation. TaskID/TopicID remain on the job for
// delivery routing only.
func routineConversation(scheduleID string) string {
	return "routine-" + scheduleID
}
func (s *Spool) PutOccurrence(o RoutineOccurrence, caller identity.Envelope) error {
	if err := validateOccurrence(o, caller); err != nil {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "occurrences", id+".json")
	if b, err := os.ReadFile(path); err == nil {
		var existing RoutineOccurrence
		if json.Unmarshal(b, &existing) != nil || jobFingerprint(existing.Job) != jobFingerprint(o.Job) {
			return errors.New("occurrence identity conflict")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicJSON(path, o)
}

// DispatchDueOccurrences persists the ordinary job before settling the occurrence.
// A crash between writes is repaired by the ordinary queue's idempotency key.
func (s *Spool) DispatchDueOccurrences(now time.Time, authorize func(Job) error) error {
	if authorize == nil {
		return errors.New("occurrence authorization required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil {
		return err
	}
	occurrences := make([]RoutineOccurrence, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.root, "occurrences", entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var o RoutineOccurrence
		if len(b) > 128*1024 || json.Unmarshal(b, &o) != nil || validateOccurrence(o, o.Job.Envelope) != nil || entry.Name() != occurrenceID(o)+".json" || o.Job.ID != occurrenceID(o) || o.Job.IdempotencyKey != o.Job.ID {
			return errors.New("invalid durable occurrence")
		}
		occurrences = append(occurrences, o)
	}
	sort.Slice(occurrences, func(i, j int) bool { return occurrences[i].DueAt.Before(occurrences[j].DueAt) })
	// ponytail: single-host occurrence scan; index by schedule if history becomes large.
	active := map[string]string{}
	for _, o := range occurrences {
		mapping, err := s.loadMappingLocked(o.Job.ID)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		admitted := o.Job
		admitted.ConversationID = routineConversation(o.ScheduleID)
		if !mapping.MatchesJob(admitted) {
			// In-flight occurrences admitted before routine conversations
			// existed still carry the task (or canonical) conversation.
			admitted.ConversationID = o.Job.ConversationID
			if o.Job.TaskID != "" && o.Job.TaskID != defaultTaskID {
				admitted.ConversationID = taskConversation(o.Job.ChatID, o.Job.TaskID)
			}
		}
		if mapping.TaskID != o.Job.TaskID || mapping.TopicID != o.Job.TopicID || !mapping.MatchesJob(admitted) {
			return errors.New("occurrence job binding changed")
		}
		key := o.Job.PrincipalID + "\x00" + o.Job.ContextID + "\x00" + o.ScheduleID
		if !terminalStatus(mapping.Status) && active[key] == "" {
			active[key] = o.Job.ID
		}
	}
	dispatched := 0
	for _, o := range occurrences {
		path := filepath.Join(s.root, "occurrences", occurrenceID(o)+".json")
		if o.State != "pending" || now.Before(o.DueAt) {
			continue
		}
		if !s.occurrenceCurrentLocked(o) {
			o.State = "cancelled"
		} else if now.Sub(o.DueAt) > time.Hour {
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
					b, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(source)+".json"))
					var live RoutineOccurrence
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
				if err := s.dispatchBriefLocked(o, now, authorize); err != nil {
					return err
				}
			} else if err := s.dispatchDigestLocked(o, now, authorize); err != nil {
				return err
			}
			o.State = "dispatched"
		} else if o.JobKind == "reminder" {
			d, err := normalizeDelivery(Delivery{ID: "reminder-" + o.Job.ID, OccurrenceID: o.Job.ID, Channel: o.Job.Channel, ConversationID: o.Job.ConversationID, DeliveryTargetID: o.Job.DeliveryTargetID, ChatID: o.Job.ChatID, TaskID: o.Job.TaskID, TopicID: o.Job.TopicID, SlackChannel: o.Job.SlackChannel, SlackThread: o.Job.SlackThread, Text: o.Job.Text, CreatedAt: now.UTC()})
			if err != nil {
				return err
			}
			if err = s.enqueueDeliveryLocked(d); err != nil {
				return err
			}
			o.State = "dispatched"
		} else {
			key := o.Job.PrincipalID + "\x00" + o.Job.ContextID + "\x00" + o.ScheduleID
			if running := active[key]; running != "" && running != o.Job.ID {
				continue
			}
			admitted := o.Job
			admitted.Envelope.ConversationID = routineConversation(o.ScheduleID)
			if _, err = s.enqueueLocked(admitted); err != nil {
				return err
			}
			o.State = "dispatched"
			active[key] = o.Job.ID
		}
		if err := atomicJSON(path, o); err != nil {
			return err
		}
		dispatched++
		if dispatched >= 16 {
			break
		}
	}
	return nil
}

// tickProactive is the proactive pass of the control loop: schedule -> due
// occurrence -> dispatch -> deterministic probing for watcher/check kinds.
func (g *Gateway) tickProactive(now time.Time) { g.tickProactiveContext(context.Background(), now) }

func (g *Gateway) tickProactiveContext(ctx context.Context, now time.Time) {
	steps := []func() error{
		func() error { return g.spool.TickSchedules(now, g.config.NativeCron, g.authorizeOccurrence) },
		func() error { return g.spool.DispatchDueOccurrences(now, g.authorizeOccurrence) },
		func() error { return g.spool.RunDueScripts(ctx, now, g.authorizeOccurrence, g.runIsolatedScript) },
		func() error {
			return g.spool.ProbeDueOccurrencesContext(ctx, now, g.authorizeOccurrence, g.fetchWatch, g.toolCallProbe)
		},
		func() error { return g.spool.DispatchDueOccurrences(now, g.authorizeOccurrence) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			if ctx.Err() == nil {
				log.Printf("routine tick failed stage=%d; work remains pending", i)
			}
			return
		}
	}
}

func (g *Gateway) authorizeOccurrence(job Job) error {
	user := g.user(job.UserID)
	configured := user.ID != ""
	if user.ID == "" {
		if mapped, ok := g.users[job.ChatID]; ok {
			user = mapped
		}
	}
	if !user.Enabled || user.ID != job.UserID || job.ActorID != user.ID || job.ScopeID != "user:"+user.ID || job.OrganizationID != g.config.OrganizationID || job.Trigger != "cron" {
		return errors.New("routine owner unavailable")
	}
	switch job.Channel {
	case "telegram_bot":
		mapped, ok := g.users[job.ChatID]
		if !ok || mapped.ID != user.ID || (configured && !slices.Contains(user.TelegramIDs, job.ChatID)) || !g.ownsRoutineEnvelope(job, user.envelope(job.ChatID)) {
			return errors.New("routine policy changed")
		}
	case "slack_app":
		if job.Envelope.PrincipalID != user.ID || !strings.HasPrefix(job.Envelope.ExternalIdentityID, "slack-") {
			return errors.New("routine policy changed")
		}
	case "mock":
		owner, err := g.mockLinkOwner(job.ChatID)
		if err != nil || owner != user.ID || !g.ownsRoutineEnvelope(job, user.mockEnvelope(job.ChatID)) {
			return errors.New("routine policy changed")
		}
	default:
		return errors.New("routine owner unavailable")
	}
	return nil
}

func (g *Gateway) ownsRoutineEnvelope(job Job, canonical identity.Envelope) bool {
	if job.TaskID != "" && job.TaskID != defaultTaskID {
		// The record is atomically replaced; this read also runs under the spool
		// lock during dispatch, so taking that lock here would deadlock.
		task, err := g.spool.loadTaskLocked(job.PrincipalID, job.TaskID)
		if err != nil || task.Archived || task.ContextID != job.ContextID || task.ChatID != job.ChatID || task.TopicID != job.TopicID {
			return false
		}
	}
	if job.ConversationID != canonical.ConversationID {
		// Before admission the envelope is canonical; dispatch pins the
		// schedule-derived conversation. In-flight occurrences admitted before
		// that change still carry the owned task conversation.
		if job.ConversationID != g.routineConversationOf(job.ID) &&
			(job.TaskID == "" || job.TaskID == defaultTaskID || job.ConversationID != taskConversation(job.ChatID, job.TaskID)) {
			return false
		}
		canonical.ConversationID = job.ConversationID
	}
	return job.Envelope == canonical
}

// routineConversationOf resolves the schedule-pinned conversation for an
// admitted occurrence job from its durable occurrence record.
func (g *Gateway) routineConversationOf(jobID string) string {
	if o, ok := g.occurrenceFor(jobID); ok {
		return routineConversation(o.ScheduleID)
	}
	return ""
}

func (g *Gateway) occurrenceFor(jobID string) (RoutineOccurrence, bool) {
	body, err := os.ReadFile(filepath.Join(g.spool.root, "occurrences", spoolFileID(jobID)+".json"))
	var o RoutineOccurrence
	if err != nil || json.Unmarshal(body, &o) != nil {
		return RoutineOccurrence{}, false
	}
	return o, true
}

// routineFailureDigest normalizes an owner-facing error text so repeated
// occurrences of the same failure hash identically — the per-occurrence job
// ID is stripped before hashing.
func routineFailureDigest(jobID, message string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(message, jobID, "")))
	return hex.EncodeToString(sum[:])
}

func validateOccurrence(o RoutineOccurrence, caller identity.Envelope) error {
	if len(o.ScheduleID) > 80 || !spoolIDPattern.MatchString(o.ScheduleID) || o.Revision == 0 || o.DueAt.IsZero() || caller != o.Job.Envelope || caller.Validate(o.Job.UserID, o.Job.ContextID, o.Job.RuntimeID, o.Job.PolicyVersion) != nil || o.Job.ActorID != caller.PrincipalID || o.Job.ScopeID != "user:"+o.Job.UserID {
		return errors.New("invalid occurrence ownership")
	}
	switch o.Job.Channel {
	case "telegram_bot":
		if o.Job.ChatID <= 0 || caller != identity.TelegramEnvelope(o.Job.UserID, o.Job.ChatID, o.Job.RuntimeID, o.Job.PolicyVersion) {
			return errors.New("invalid occurrence ownership")
		}
	case "slack_app":
		if !strings.HasPrefix(caller.ExternalIdentityID, "slack-") {
			return errors.New("invalid occurrence ownership")
		}
	case "mock":
		if o.Job.ChatID <= 0 || caller != identity.MockEnvelope(o.Job.UserID, o.Job.ChatID, o.Job.RuntimeID, o.Job.PolicyVersion) {
			return errors.New("invalid occurrence ownership")
		}
	default:
		return errors.New("invalid occurrence ownership")
	}
	if strings.TrimSpace(o.Job.Text) == "" || len(o.Job.Text) > 64*1024 || o.Job.Sensitive || envstore.LooksLikeEnv(o.Job.Text) {
		return errors.New("invalid routine input")
	}
	return nil
}
