package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Only controlled status labels reach chat; upstream error text may contain credentials.
func failureMessage(jobID, status string) string {
	switch status {
	case "cancelled":
		return "Задача отменена (run.cancelled). ID: " + jobID
	case "interrupted":
		return "Выполнение задачи прервано (run.interrupted). ID: " + jobID
	default:
		return "Не удалось завершить задачу: ошибка выполнения агента (run.failed). ID: " + jobID
	}
}

type ConversationMapping struct {
	PrincipalID    string    `json:"principal_id"`
	ContextID      string    `json:"context_id"`
	ConversationID string    `json:"conversation_id"`
	SessionID      string    `json:"session_id"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (s *Spool) mappingPath(id string) string {
	return filepath.Join(s.root, "mappings", spoolFileID(id)+".json")
}

// idempotencyIndexPath locates the by-key index entry for an idempotency key
// (SPEC-0053 R2.3): a constant-time pointer to the owning job mapping.
func (s *Spool) idempotencyIndexPath(key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(s.root, "mappings", "by-key", hex.EncodeToString(h[:])+".json")
}

func (s *Spool) conversationPath(principal, context, conversation string) string {
	h := sha256.Sum256([]byte(principal + "\x00" + context + "\x00" + conversation))
	return filepath.Join(s.root, "conversations", hex.EncodeToString(h[:])+".json")
}

func (s *Spool) writeIdempotencyIndexLocked(key, jobID string) error {
	return atomicJSON(s.idempotencyIndexPath(key), struct {
		JobID string `json:"job_id"`
	}{jobID})
}

func (s *Spool) writeMappingLocked(mapping JobMapping) error {
	if err := atomicJSON(s.mappingPath(mapping.JobID), mapping); err != nil {
		return err
	}
	if s.activeMappings != nil {
		if terminalStatus(mapping.Status) {
			delete(s.activeMappings, mapping.JobID)
		} else {
			s.activeMappings[mapping.JobID] = true
		}
	}
	if strings.TrimSpace(mapping.IdempotencyKey) != "" {
		return s.writeIdempotencyIndexLocked(mapping.IdempotencyKey, mapping.JobID)
	}
	return nil
}

// Active IDs avoid reading terminal history on every control tick. Callers still
// reload each durable mapping under the spool lock before acting on it.
func (s *Spool) activeMappingIDs() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeMappings == nil {
		entries, err := os.ReadDir(filepath.Join(s.root, "mappings"))
		if err != nil {
			return nil, err
		}
		active := map[string]bool{}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(s.root, "mappings", entry.Name()))
			var mapping JobMapping
			if err != nil || json.Unmarshal(body, &mapping) != nil {
				continue
			}
			if mapping.JobID != "" && !terminalStatus(mapping.Status) && entry.Name() == spoolFileID(mapping.JobID)+".json" {
				active[mapping.JobID] = true
			}
		}
		s.activeMappings = active
	}
	ids := make([]string, 0, len(s.activeMappings))
	for id := range s.activeMappings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// ActiveJobsForTask lists the caller's non-terminal mappings bound to one task.
func (s *Spool) ActiveJobsForTask(principal, taskID string) ([]JobMapping, error) {
	ids, err := s.activeMappingIDs()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []JobMapping{}
	for _, id := range ids {
		mapping, err := s.loadMappingLocked(id)
		if err != nil || mapping.PrincipalID != principal || terminalStatus(mapping.Status) || mapping.CancelRequested {
			continue
		}
		if mapping.TaskID == taskID {
			out = append(out, mapping)
		}
	}
	return out, nil
}

func (s *Spool) loadMappingLocked(id string) (JobMapping, error) {
	b, err := os.ReadFile(s.mappingPath(id))
	if err != nil {
		return JobMapping{}, err
	}
	var mapping JobMapping
	if err := json.Unmarshal(b, &mapping); err != nil || mapping.JobID != id {
		return JobMapping{}, errors.New("invalid job mapping")
	}
	return mapping, nil
}

// Mapping returns a copy of durable metadata for a job.
func (s *Spool) Mapping(id string) (JobMapping, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, err := s.loadMappingLocked(id)
	if errors.Is(err, os.ErrNotExist) {
		return JobMapping{}, false, nil
	}
	return mapping, err == nil, err
}

func (s *Spool) updateMappingLocked(jobID string, outcome RunOutcome, status string, terminal bool) error {
	mapping, err := s.loadMappingLocked(jobID)
	if err != nil {
		return err
	}
	if terminalStatus(mapping.Status) && status != "" && status != mapping.Status {
		return errors.New("job already terminal")
	}
	if terminalStatus(mapping.Status) && mapping.Result != "" && outcome.Text != "" && outcome.Text != mapping.Result {
		return errors.New("terminal result cannot change")
	}
	if outcome.JobID != "" && outcome.JobID != mapping.JobID {
		return errors.New("runtime returned a different job identity")
	}
	if mapping.RuntimeGeneration != "" && outcome.RuntimeGeneration != "" && outcome.RuntimeGeneration != mapping.RuntimeGeneration {
		return errors.New("runtime returned a stale generation")
	}
	if mapping.RunID != "" && outcome.RunID != "" && outcome.RunID != mapping.RunID {
		return errors.New("runtime returned a different run identity")
	}
	if mapping.SessionID != "" && outcome.SessionID != "" && outcome.SessionID != mapping.SessionID {
		return errors.New("runtime returned a different session identity")
	}
	if outcome.SessionID != "" {
		mapping.SessionID = outcome.SessionID
		conversation := ConversationMapping{PrincipalID: mapping.PrincipalID, ContextID: mapping.ContextID, ConversationID: mapping.ConversationID, SessionID: outcome.SessionID, UpdatedAt: time.Now().UTC()}
		if err := atomicJSON(s.conversationPath(mapping.PrincipalID, mapping.ContextID, mapping.ConversationID), conversation); err != nil {
			return err
		}
	}
	if outcome.RunID != "" {
		mapping.RunID = outcome.RunID
	}
	if outcome.RuntimeGeneration != "" {
		mapping.RuntimeGeneration = outcome.RuntimeGeneration
	}
	if outcome.LastEvent != "" {
		mapping.LastKnownEvent = outcome.LastEvent
	}
	if status != "" {
		if status == "uncertain" && mapping.UncertainSince.IsZero() {
			mapping.UncertainSince = time.Now().UTC()
		}
		mapping.Status = status
	}
	if terminal && mapping.Control != nil {
		control := *mapping.Control
		control.State = "closed"
		mapping.Control = &control
	}
	if strings.TrimSpace(outcome.Text) != "" && len(outcome.Text) <= 2*1024*1024 {
		mapping.Result = outcome.Text
	}
	mapping.UpdatedAt = time.Now().UTC()
	if terminal {
		mapping.TerminalAt = mapping.UpdatedAt
		mapping.UncertainSince = time.Time{}
	}
	if err := s.writeMappingLocked(mapping); err != nil {
		return err
	}
	if !mappingHoldsContext(mapping) {
		key := jobContextKey(mapping.PrincipalID, mapping.ContextID)
		if s.inFlight[key] == mapping.JobID {
			delete(s.inFlight, key)
		}
	}
	if terminal && s.JobDone != nil && mapping.PrincipalID != "" {
		// Task-tier cli cells keyed by this job die with it. Fire-and-forget:
		// ToolHub unreachable only delays teardown to the controller ladder.
		go s.JobDone(mapping.JobID, mapping.PrincipalID)
	}
	return nil
}

// mappingHoldsContext reports whether a mapping keeps its context key held:
// only an admitted run (same check as RequeueObservations) may still be
// executing. Uncertain without an admitted run has nothing in flight, so
// holding the key would deadlock the context forever (ADR-0025).
func mappingHoldsContext(mapping JobMapping) bool {
	if terminalStatus(mapping.Status) {
		return false
	}
	return mapping.RunID != "" && mapping.SessionID != "" && mapping.RuntimeGeneration != ""
}

// jobContextKey mirrors the supervisor runtime key without the fixed gateway
// runtime mode: work is serialized per (principal, context), parallel across
// different keys (ADR-0025).
func jobContextKey(principal, contextID string) string {
	return principal + "\x00" + contextID
}

// releaseInFlightLocked frees the context owned by jobID; needed for jobs
// without durable mappings, which bypass updateMappingLocked's release.
func (s *Spool) releaseInFlightLocked(jobID string) {
	for key, holder := range s.inFlight {
		if holder == jobID {
			delete(s.inFlight, key)
		}
	}
}

func (s *Spool) SessionFor(principal, context, conversation string) (ConversationMapping, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.conversationPath(principal, context, conversation))
	if errors.Is(err, os.ErrNotExist) {
		return ConversationMapping{}, false, nil
	}
	if err != nil {
		return ConversationMapping{}, false, err
	}
	var mapping ConversationMapping
	if err := json.Unmarshal(b, &mapping); err != nil {
		return ConversationMapping{}, false, errors.New("invalid conversation mapping")
	}
	return mapping, true, nil
}

func (s *Spool) RecordOutcome(jobID string, outcome RunOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := outcome.Status
	if status == "" {
		status = "completed"
	}
	terminal := status == "completed" || status == "failed" || status == "cancelled" || status == "interrupted"
	return s.updateMappingLocked(jobID, outcome, status, terminal)
}
