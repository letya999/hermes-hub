package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	State      string    `json:"state"`
}

func occurrenceID(o RoutineOccurrence) string {
	sum := sha256.Sum256([]byte(o.ScheduleID + "\x00" + strconv.FormatUint(o.Revision, 10) + "\x00" + o.DueAt.UTC().Format(time.RFC3339Nano)))
	return "routine-" + hex.EncodeToString(sum[:])
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
		if mapping.Fingerprint != jobFingerprint(o.Job) {
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
		if now.Sub(o.DueAt) > time.Hour {
			o.State = "missed"
		} else if authorize(o.Job) != nil {
			o.State = "blocked"
		} else {
			key := o.Job.PrincipalID + "\x00" + o.Job.ContextID + "\x00" + o.ScheduleID
			if running := active[key]; running != "" && running != o.Job.ID {
				continue
			}
			if _, err = s.enqueueLocked(o.Job); err != nil {
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
func (g *Gateway) authorizeOccurrence(job Job) error {
	user, ok := g.users[job.ChatID]
	if !ok || !user.Enabled || user.ID != job.UserID || job.ActorID != user.ID || job.ScopeID != "user:"+user.ID || job.OrganizationID != g.config.OrganizationID || job.Trigger != "cron" {
		return errors.New("routine owner unavailable")
	}
	expected := user.envelope(job.ChatID)
	if job.Envelope != expected {
		return errors.New("routine policy changed")
	}
	return nil
}

func validateOccurrence(o RoutineOccurrence, caller identity.Envelope) error {
	if len(o.ScheduleID) > 80 || !spoolIDPattern.MatchString(o.ScheduleID) || o.Revision == 0 || o.DueAt.IsZero() || caller != o.Job.Envelope || caller.Validate(o.Job.UserID, o.Job.ContextID, o.Job.RuntimeID, o.Job.PolicyVersion) != nil || o.Job.ActorID != caller.PrincipalID || o.Job.ScopeID != "user:"+o.Job.UserID || o.Job.ChatID <= 0 || o.Job.Channel != "telegram_bot" || caller != identity.TelegramEnvelope(o.Job.UserID, o.Job.ChatID, o.Job.RuntimeID, o.Job.PolicyVersion) {
		return errors.New("invalid occurrence ownership")
	}
	if strings.TrimSpace(o.Job.Text) == "" || len(o.Job.Text) > 64*1024 || o.Job.Sensitive || envstore.LooksLikeEnv(o.Job.Text) {
		return errors.New("invalid routine input")
	}
	return nil
}
