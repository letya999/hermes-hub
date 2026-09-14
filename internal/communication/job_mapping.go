package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// RunOutcome is the small result envelope shared by the gateway and supervisor.
// Text is deliberately bounded by the HTTP contract; mapping files never store
// the input prompt.
type RunOutcome struct {
	Text              string
	JobID             string
	SessionID         string
	RunID             string
	RuntimeGeneration string
	Status            string
	LastEvent         string
}

// JobMapping is durable routing/lifecycle metadata. Binding fields are copied
// at acceptance and are never changed by a retry or a caller-supplied payload.
type JobMapping struct {
	IdentitySchema      int             `json:"identity_schema"`
	ExternalIdentityID  string          `json:"external_identity_id"`
	ObservationAttempts uint64          `json:"observation_attempts,omitempty"`
	NextObservationAt   time.Time       `json:"next_observation_at,omitempty"`
	Control             *PendingControl `json:"control,omitempty"`
	CancelRequested     bool            `json:"cancel_requested,omitempty"`
	JobID               string          `json:"job_id"`
	IdempotencyKey      string          `json:"idempotency_key"`
	Fingerprint         string          `json:"fingerprint"`
	PrincipalID         string          `json:"principal_id"`
	ActorID             string          `json:"actor_id"`
	ContextID           string          `json:"context_id"`
	ScopeID             string          `json:"scope_id"`
	RuntimeID           string          `json:"runtime_id"`
	ConversationID      string          `json:"conversation_id"`
	DeliveryTargetID    string          `json:"delivery_target_id"`
	OrganizationID      string          `json:"organization_id"`
	UserID              string          `json:"user_id"`
	PolicyVersion       string          `json:"policy_version"`
	Channel             string          `json:"channel"`
	Trigger             string          `json:"trigger"`
	Status              string          `json:"status"`
	SessionID           string          `json:"session_id,omitempty"`
	RunID               string          `json:"run_id,omitempty"`
	RuntimeGeneration   string          `json:"runtime_generation,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	TerminalAt          time.Time       `json:"terminal_at,omitempty"`
	LastKnownEvent      string          `json:"last_known_event,omitempty"`
	EventPosition       uint64          `json:"event_position,omitempty"`
	LastEventID         string          `json:"last_event_id,omitempty"`
	ApprovalID          string          `json:"approval_id,omitempty"`
	ApprovalChoices     []string        `json:"approval_choices,omitempty"`
	ApprovalDeadline    time.Time       `json:"approval_deadline,omitempty"`
	ApprovalState       string          `json:"approval_state,omitempty"`
	Result              string          `json:"result,omitempty"`
}

type ConversationMapping struct {
	PrincipalID    string    `json:"principal_id"`
	ContextID      string    `json:"context_id"`
	ConversationID string    `json:"conversation_id"`
	SessionID      string    `json:"session_id"`
	VoiceReplies   bool      `json:"voice_replies,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func jobFingerprint(job Job) string {
	textHash := job.TextSHA256
	if textHash == "" {
		h := sha256.Sum256([]byte(job.Text))
		textHash = hex.EncodeToString(h[:])
	}
	value := struct {
		Envelope       any
		OrganizationID string
		UserID         string
		ActorID        string
		ScopeID        string
		Channel        string
		Trigger        string
		TextHash       string
	}{job.Envelope, job.OrganizationID, job.UserID, job.ActorID, job.ScopeID, job.Channel, job.Trigger, textHash}
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// MatchesJob is used by the read-only migration audit before changing executors.
func (mapping JobMapping) MatchesJob(job Job) bool {
	return mapping.JobID == job.ID && mapping.IdempotencyKey == job.IdempotencyKey && mapping.Fingerprint == jobFingerprint(job) && mapping.UserID == job.UserID && mapping.ContextID == job.ContextID && mapping.PrincipalID == job.PrincipalID && mapping.RuntimeID == job.RuntimeID && mapping.ConversationID == job.ConversationID && mapping.DeliveryTargetID == job.DeliveryTargetID && mapping.PolicyVersion == job.PolicyVersion && mapping.ExternalIdentityID == job.ExternalIdentityID && mapping.IdentitySchema == job.Schema && mapping.ScopeID == job.ScopeID && mapping.ActorID == job.ActorID && mapping.OrganizationID == job.OrganizationID && mapping.Channel == job.Channel && mapping.Trigger == job.Trigger
}

func mappingFromJob(job Job, now time.Time) JobMapping {
	return JobMapping{IdentitySchema: job.Envelope.Schema, ExternalIdentityID: job.Envelope.ExternalIdentityID, JobID: job.ID, IdempotencyKey: job.IdempotencyKey, Fingerprint: jobFingerprint(job), PrincipalID: job.PrincipalID, ActorID: job.ActorID, ContextID: job.ContextID, ScopeID: job.ScopeID, RuntimeID: job.RuntimeID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, OrganizationID: job.OrganizationID, UserID: job.UserID, PolicyVersion: job.PolicyVersion, Channel: job.Channel, Trigger: job.Trigger, Status: "accepted", CreatedAt: now, UpdatedAt: now, LastKnownEvent: "job.accepted"}
}

func (s *Spool) mappingPath(id string) string {
	return filepath.Join(s.root, "mappings", spoolFileID(id)+".json")
}

func (s *Spool) conversationPath(principal, context, conversation string) string {
	h := sha256.Sum256([]byte(principal + "\x00" + context + "\x00" + conversation))
	return filepath.Join(s.root, "conversations", hex.EncodeToString(h[:])+".json")
}

func (s *Spool) writeMappingLocked(mapping JobMapping) error {
	return atomicJSON(s.mappingPath(mapping.JobID), mapping)
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
		if existing, err := os.ReadFile(s.conversationPath(mapping.PrincipalID, mapping.ContextID, mapping.ConversationID)); err == nil {
			var prior ConversationMapping
			if json.Unmarshal(existing, &prior) == nil {
				conversation.VoiceReplies = prior.VoiceReplies
			}
		}
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
	}
	return s.writeMappingLocked(mapping)
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

func (s *Spool) SetConversationVoice(principal, contextID, conversation string, enabled bool) error {
	if !identity.ValidID(principal) || !identity.ValidID(contextID) || !identity.ValidID(conversation) {
		return errors.New("invalid conversation identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping := ConversationMapping{PrincipalID: principal, ContextID: contextID, ConversationID: conversation, VoiceReplies: enabled, UpdatedAt: time.Now().UTC()}
	if b, err := os.ReadFile(s.conversationPath(principal, contextID, conversation)); err == nil {
		var existing ConversationMapping
		if json.Unmarshal(b, &existing) == nil {
			mapping.SessionID = existing.SessionID
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicJSON(s.conversationPath(principal, contextID, conversation), mapping)
}

func (s *Spool) ConversationVoice(principal, contextID, conversation string) (bool, error) {
	mapping, found, err := s.SessionFor(principal, contextID, conversation)
	if err != nil || !found {
		return false, err
	}
	return mapping.VoiceReplies, nil
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
