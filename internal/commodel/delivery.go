package commodel

import (
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Job is immutable routing context plus the user message. Sensitive prompts are
// held only in memory by the spool and are intentionally absent from job files.
type Job struct {
	identity.Envelope
	OperationalEpoch uint64 `json:"operational_epoch,omitempty"`
	ID               string `json:"id"`
	OrganizationID   string `json:"organization_id"`
	UserID           string `json:"user_id"`
	ActorID          string `json:"actor_id"`
	ScopeID          string `json:"scope_id"`
	Channel          string `json:"channel"`
	Trigger          string `json:"trigger"`
	IdempotencyKey   string `json:"idempotency_key"`
	ChatID           int64  `json:"chat_id"`
	MessageID        int    `json:"message_id"`
	// TaskID/TopicID pin the durable task binding at acceptance: every reply,
	// progress event, approval, artifact and voice note returns to the same
	// task/topic. Model output never re-routes them.
	TaskID       string    `json:"task_id,omitempty"`
	TopicID      int64     `json:"topic_id,omitempty"`
	SlackChannel string    `json:"slack_channel,omitempty"`
	SlackThread  string    `json:"slack_thread,omitempty"`
	Text         string    `json:"text,omitempty"`
	InboundID    string    `json:"inbound_id,omitempty"`
	Sensitive    bool      `json:"sensitive"`
	TextSHA256   string    `json:"text_sha256,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Delivery struct {
	BlockedBy        string `json:"blocked_by,omitempty"`
	ID               string `json:"id"`
	OccurrenceID     string `json:"occurrence_id,omitempty"`
	JobID            string `json:"job_id"`
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
	ConversationID   string `json:"conversation_id"`
	DeliveryTargetID string `json:"delivery_target_id"`
	Channel          string `json:"channel,omitempty"`
	ChatID           int64  `json:"chat_id"`
	TaskID           string `json:"task_id,omitempty"`
	// TopicID is the Telegram topic the delivery must land in; sent as
	// direct_messages_topic_id for private chats or message_thread_id for
	// group forum topics (see topicField).
	TopicID      int64  `json:"topic_id,omitempty"`
	SlackChannel string `json:"slack_channel,omitempty"`
	SlackThread  string `json:"slack_thread,omitempty"`
	Text         string `json:"text"`
	Format       string `json:"format,omitempty"`
	// NotBefore defers quiet-window deliveries: the entry stays durable in
	// pending until the schedule's quiet window ends (SPEC-0054 R2.1).
	NotBefore time.Time `json:"not_before,omitempty"`
	// PartCount/SentParts persist multi-part progress so a mid-sequence failure
	// leaves an uncertain delivery with an exact sent boundary; SourceSent marks
	// the optional raw-source document part of the same ordered sequence.
	PartCount  int  `json:"part_count,omitempty"`
	SentParts  int  `json:"sent_parts,omitempty"`
	SourceSent bool `json:"source_sent,omitempty"`
	// Artifacts are run-generated files fetched into outbox/blobs while the
	// runtime was answering; SentArtifacts is their durable send boundary.
	Artifacts     []hubruntime.ArtifactRef `json:"artifacts,omitempty"`
	SentArtifacts int                      `json:"sent_artifacts,omitempty"`
	// Voice is the run-requested spoken payload (upstream VOICE: marker). The
	// synthesized audio is sent straight to the channel and never persisted.
	Voice string `json:"voice,omitempty"`
	// VoiceSent distinguishes accepted sendVoice from the intentional text fallback.
	VoiceSent bool `json:"voice_sent,omitempty"`
	// Buttons carries Telegram inline-keyboard rows (one button per row) for
	// channels that support them; Data is the opaque callback payload.
	Buttons            []DeliveryButton `json:"buttons,omitempty"`
	ApprovalJobID      string           `json:"approval_job_id,omitempty"`
	ApprovalID         string           `json:"approval_id,omitempty"`
	ApprovalRunID      string           `json:"approval_run_id,omitempty"`
	ApprovalGeneration string           `json:"approval_generation,omitempty"`
	TelegramMessageID  int              `json:"telegram_message_id,omitempty"`
	// EditMessageID targets a status message already delivered: the delivery
	// worker edits it instead of sending a new message (SPEC-0053 R2.2).
	EditMessageID int `json:"edit_message_id,omitempty"`
	// StreamPosition orders status updates so a replayed event can never move
	// the rendered status backwards.
	StreamPosition uint64 `json:"stream_position,omitempty"`
	// StatusClosed marks the run's status message finalized: late events must
	// not resurrect or edit it.
	StatusClosed bool      `json:"status_closed,omitempty"`
	Attempts     int       `json:"attempts"`
	CreatedAt    time.Time `json:"created_at"`
}

type DeliveryButton struct {
	Text string `json:"text"`
	Data string `json:"data"`
}

type Task struct {
	Schema         int       `json:"schema"`
	TaskID         string    `json:"task_id"`
	Name           string    `json:"name"`
	PrincipalID    string    `json:"principal_id"`
	ContextID      string    `json:"context_id"`
	Channel        string    `json:"channel"`
	Peer           string    `json:"peer"`
	ChatID         int64     `json:"chat_id"`
	TopicID        int64     `json:"topic_id,omitempty"`
	ConversationID string    `json:"conversation_id"`
	Style          string    `json:"style,omitempty"`
	StyleVersion   uint64    `json:"style_version,omitempty"`
	Archived       bool      `json:"archived,omitempty"`
	NameAuto       bool      `json:"name_auto,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
