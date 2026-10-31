// Package audit appends privacy-preserving control-plane events.
package audit

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const Schema = 2

var (
	ErrInvalid = errors.New("invalid audit event")
	ErrWrite   = errors.New("audit ledger write failed")
)

type Event struct {
	Schema                    int       `json:"schema"`
	EventID                   string    `json:"event_id"`
	At                        time.Time `json:"at"`
	Kind                      string    `json:"kind"`
	PrincipalID               string    `json:"principal_id"`
	ContextID                 string    `json:"context_id,omitempty"`
	RuntimeID                 string    `json:"runtime_id,omitempty"`
	ConnectionID              string    `json:"connection_id,omitempty"`
	SenderID                  string    `json:"sender_id,omitempty"`
	TeamID                    string    `json:"team_id,omitempty"`
	BindingID                 string    `json:"binding_id,omitempty"`
	PolicyRevision            string    `json:"policy_revision,omitempty"`
	CredentialRevision        uint64    `json:"credential_revision,omitempty"`
	ProjectionRevision        uint64    `json:"projection_revision,omitempty"`
	CapabilityID              string    `json:"capability_id,omitempty"`
	CapabilityProfile         string    `json:"capability_profile,omitempty"`
	ImplementationDigest      string    `json:"implementation_digest,omitempty"`
	CapabilityPolicyRevision  uint64    `json:"capability_policy_revision,omitempty"`
	CapabilityProfileRevision uint64    `json:"capability_profile_revision,omitempty"`
	Environment               string    `json:"environment,omitempty"`
	Generation                uint64    `json:"generation,omitempty"`
	OperationID               string    `json:"operation_id,omitempty"`
	CleanupAction             string    `json:"cleanup_action,omitempty"`
	CleanupOf                 string    `json:"cleanup_of,omitempty"`
	Phase                     string    `json:"phase,omitempty"`
	Action                    string    `json:"action,omitempty"`
	Authorization             string    `json:"authorization,omitempty"`
	Execution                 string    `json:"execution,omitempty"`
	Delivery                  string    `json:"delivery,omitempty"`
	Outcome                   string    `json:"outcome"`
	JobID                     string    `json:"job_id,omitempty"`
	HermesRunID               string    `json:"hermes_run_id,omitempty"`
	ToolCallID                string    `json:"tool_call_id,omitempty"`
	CorrelationID             string    `json:"correlation_id,omitempty"`
	Name                      string    `json:"name,omitempty"`
	// Receipt is a bounded provider mutation proof such as a Slack
	// channel:timestamp pair. It never carries a credential or message body.
	Receipt string `json:"receipt,omitempty"`
}

func validate(event Event) error {
	if (event.Schema != Schema && event.Schema != 1) || event.EventID == "" || event.PrincipalID == "" || event.Kind == "" || event.Outcome == "" || event.At.IsZero() {
		return fmt.Errorf("%w: required fields", ErrInvalid)
	}
	switch event.Kind {
	case "tool-call", "credential-change", "terminal-exposure", "oauth", "job", "ssh", "exposure", "dns":
	default:
		return fmt.Errorf("%w: kind", ErrInvalid)
	}
	logicalBytes := 0
	for _, value := range []string{event.EventID, event.PrincipalID, event.ContextID, event.RuntimeID, event.ConnectionID, event.PolicyRevision, event.JobID, event.HermesRunID, event.ToolCallID, event.CorrelationID, event.Name, event.Outcome, event.Kind, event.Receipt} {
		if len(value) > MaxRecordBytes-logicalBytes {
			return fmt.Errorf("%w: record size", ErrInvalid)
		}
		logicalBytes += len(value)
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: control character in field", ErrInvalid)
		}
	}
	if !ValidReceipt(event.Receipt) {
		return fmt.Errorf("%w: unsafe receipt", ErrInvalid)
	}
	for _, value := range []string{event.CapabilityID, event.CapabilityProfile, event.ImplementationDigest, event.Environment, event.SenderID, event.TeamID, event.BindingID} {
		if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: capability metadata", ErrInvalid)
		}
	}
	return validateOperation(event)
}

func containsSensitive(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, needle := range []string{"\"prompt\"", "\"text\"", "\"body\"", "\"ciphertext\"", "\"refresh_token\"", "\"access_token\"", "\"result\""} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func NewEvent(kind, principal, outcome string) Event {
	return Event{Schema: Schema, EventID: newID(), At: time.Now().UTC(), Kind: kind, PrincipalID: principal, Outcome: outcome}
}

func newID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	return "evt-" + hex.EncodeToString(raw)
}
