package api

import "time"

// CaptureEvent is the communication→ToolHub durable capture record
// (SPEC-0098 M4). The producer persists it in the spool outbox until
// ToolHub commits; producer/event ID is the durable dedup key so a lost
// acknowledgement or a restarted replay can never write the fact twice.
// Claimed fields (scope, trigger, run) are cross-checked against the
// lifecycle record server-side; kind/provenance/audience are classified
// from the verified fields, never trusted from the wire.
type CaptureEvent struct {
	Producer    string    `json:"producer"`
	EventID     string    `json:"event_id"`
	Kind        string    `json:"kind"` // "turn" accepted | "reply" completed-run
	JobID       string    `json:"job_id"`
	RunID       string    `json:"run_id,omitempty"`
	UserID      string    `json:"user_id"`
	PrincipalID string    `json:"principal_id"`
	ScopeID     string    `json:"scope_id"`
	Trigger     string    `json:"trigger"`
	Assignment  bool      `json:"assignment,omitempty"`
	Probe       bool      `json:"probe,omitempty"`
	Sensitive   bool      `json:"sensitive,omitempty"`
	Untrusted   bool      `json:"untrusted,omitempty"`
	Channel     string    `json:"channel"`
	Body        string    `json:"body"`
	ObservedAt  time.Time `json:"observed_at"`
}
