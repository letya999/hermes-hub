package commodel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// PendingControl is the caller-issued control command recorded on a mapping
// until the owning job reaches a state that can honor or refuse it.
type PendingControl struct {
	NextAttemptAt time.Time         `json:"next_attempt_at,omitempty"`
	Caller        identity.Envelope `json:"caller"`
	Action        string            `json:"action"`
	RequestID     string            `json:"request_id,omitempty"`
	Choice        string            `json:"choice,omitempty"`
	State         string            `json:"state"`
}

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
	// Artifacts carry generated files already fetched into the spool; Blob is
	// set on success, Error records the explicit failure reason otherwise.
	Artifacts []hubruntime.ArtifactRef
	// Voice is the run-requested spoken payload (upstream VOICE: marker).
	Voice string
}

// JobMapping is durable routing/lifecycle metadata. Binding fields are copied
// at acceptance and are never changed by a retry or a caller-supplied payload.
type JobMapping struct {
	RoutineOriginVersion uint64          `json:"routine_origin_version,omitempty"`
	IdentitySchema       int             `json:"identity_schema"`
	ExternalIdentityID   string          `json:"external_identity_id"`
	ObservationAttempts  uint64          `json:"observation_attempts,omitempty"`
	NextObservationAt    time.Time       `json:"next_observation_at,omitempty"`
	Control              *PendingControl `json:"control,omitempty"`
	CancelRequested      bool            `json:"cancel_requested,omitempty"`
	JobID                string          `json:"job_id"`
	IdempotencyKey       string          `json:"idempotency_key"`
	Fingerprint          string          `json:"fingerprint"`
	PrincipalID          string          `json:"principal_id"`
	ActorID              string          `json:"actor_id"`
	ContextID            string          `json:"context_id"`
	ScopeID              string          `json:"scope_id"`
	RuntimeID            string          `json:"runtime_id"`
	ConversationID       string          `json:"conversation_id"`
	DeliveryTargetID     string          `json:"delivery_target_id"`
	OrganizationID       string          `json:"organization_id"`
	UserID               string          `json:"user_id"`
	PolicyVersion        string          `json:"policy_version"`
	Channel              string          `json:"channel"`
	Trigger              string          `json:"trigger"`
	// TaskID/TopicID pin the durable task binding; Style/StyleVersion/
	// StyleSnapshotted pin the admitted-run presentation snapshot.
	TaskID  string `json:"task_id,omitempty"`
	TopicID int64  `json:"topic_id,omitempty"`
	ChatID  int64  `json:"chat_id,omitempty"`
	// StatusText is the latest user-facing status line for live progress:
	// the periodic tick re-renders it with elapsed time, so the durable copy
	// lets a restarted gateway resume the status message without the stream.
	StatusText        string `json:"status_text,omitempty"`
	Style             string `json:"style,omitempty"`
	StyleVersion      uint64 `json:"style_version,omitempty"`
	StyleSnapshotted  bool   `json:"style_snapshotted,omitempty"`
	Status            string `json:"status"`
	SessionID         string `json:"session_id,omitempty"`
	RunID             string `json:"run_id,omitempty"`
	RuntimeGeneration string `json:"runtime_generation,omitempty"`
	// UncertainSince anchors the reconciliation window: it is stamped once
	// when the outcome first becomes unknown and cleared when the run names
	// a terminal cause. Repeated uncertain observations never extend it.
	UncertainSince   time.Time `json:"uncertain_since,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	TerminalAt       time.Time `json:"terminal_at,omitempty"`
	LastKnownEvent   string    `json:"last_known_event,omitempty"`
	EventPosition    uint64    `json:"event_position,omitempty"`
	LastEventID      string    `json:"last_event_id,omitempty"`
	ApprovalID       string    `json:"approval_id,omitempty"`
	ApprovalChoices  []string  `json:"approval_choices,omitempty"`
	ApprovalDeadline time.Time `json:"approval_deadline,omitempty"`
	ApprovalState    string    `json:"approval_state,omitempty"`
	Result           string    `json:"result,omitempty"`
}

func JobFingerprint(job Job) string {
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
		SlackChannel   string
		SlackThread    string
		TextHash       string
	}{job.Envelope, job.OrganizationID, job.UserID, job.ActorID, job.ScopeID, job.Channel, job.Trigger, job.SlackChannel, job.SlackThread, textHash}
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// MatchesJob is used by the read-only migration audit before changing executors.
func (mapping JobMapping) MatchesJob(job Job) bool {
	return mapping.JobID == job.ID && mapping.IdempotencyKey == job.IdempotencyKey && mapping.Fingerprint == JobFingerprint(job) && mapping.UserID == job.UserID && mapping.ContextID == job.ContextID && mapping.PrincipalID == job.PrincipalID && mapping.RuntimeID == job.RuntimeID && mapping.ConversationID == job.ConversationID && mapping.DeliveryTargetID == job.DeliveryTargetID && mapping.PolicyVersion == job.PolicyVersion && mapping.ExternalIdentityID == job.ExternalIdentityID && mapping.IdentitySchema == job.Schema && mapping.ScopeID == job.ScopeID && mapping.ActorID == job.ActorID && mapping.OrganizationID == job.OrganizationID && mapping.Channel == job.Channel && mapping.Trigger == job.Trigger
}

func MappingFromJob(job Job, now time.Time) JobMapping {
	return JobMapping{RoutineOriginVersion: 1, IdentitySchema: job.Envelope.Schema, ExternalIdentityID: job.Envelope.ExternalIdentityID, JobID: job.ID, IdempotencyKey: job.IdempotencyKey, Fingerprint: JobFingerprint(job), PrincipalID: job.PrincipalID, ActorID: job.ActorID, ContextID: job.ContextID, ScopeID: job.ScopeID, RuntimeID: job.RuntimeID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, OrganizationID: job.OrganizationID, UserID: job.UserID, PolicyVersion: job.PolicyVersion, Channel: job.Channel, Trigger: job.Trigger, TaskID: job.TaskID, TopicID: job.TopicID, ChatID: job.ChatID, Status: "accepted", CreatedAt: now, UpdatedAt: now, LastKnownEvent: "job.accepted"}
}
