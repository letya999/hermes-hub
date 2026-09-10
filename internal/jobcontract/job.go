// Package jobcontract defines the private communication-hub to runtime wire model.
package jobcontract

import "time"

const Version = 1

type Job struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organization_id"`
	UserID             string    `json:"user_id"`
	ActorID            string    `json:"actor_id"`
	ScopeID            string    `json:"scope_id"`
	OrganizationAction string    `json:"organization_action,omitempty"`
	Channel            string    `json:"channel"`
	Trigger            string    `json:"trigger"`
	IdempotencyKey     string    `json:"idempotency_key"`
	ChatID             int64     `json:"chat_id"`
	MessageID          int       `json:"message_id"`
	Text               string    `json:"text,omitempty"`
	Sensitive          bool      `json:"sensitive"`
	TextSHA256         string    `json:"text_sha256,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

type ExecutionRequest struct {
	Version int `json:"version"`
	Job     Job `json:"job"`
}

type ExecutionResponse struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}
