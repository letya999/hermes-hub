// Package audit appends privacy-preserving control-plane events.
package audit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const Schema = 1

var (
	ErrInvalid = errors.New("invalid audit event")
	ErrWrite   = errors.New("audit ledger write failed")
)

type Event struct {
	Schema             int       `json:"schema"`
	EventID            string    `json:"event_id"`
	At                 time.Time `json:"at"`
	Kind               string    `json:"kind"`
	PrincipalID        string    `json:"principal_id"`
	ContextID          string    `json:"context_id,omitempty"`
	RuntimeID          string    `json:"runtime_id,omitempty"`
	ConnectionID       string    `json:"connection_id,omitempty"`
	PolicyRevision     string    `json:"policy_revision,omitempty"`
	CredentialRevision uint64    `json:"credential_revision,omitempty"`
	ProjectionRevision uint64    `json:"projection_revision,omitempty"`
	Outcome            string    `json:"outcome"`
	JobID              string    `json:"job_id,omitempty"`
	HermesRunID        string    `json:"hermes_run_id,omitempty"`
	ToolCallID         string    `json:"tool_call_id,omitempty"`
	CorrelationID      string    `json:"correlation_id,omitempty"`
	Name               string    `json:"name,omitempty"`
	// Receipt is a bounded provider mutation proof such as a Slack
	// channel:timestamp pair. It never carries a credential or message body.
	Receipt string `json:"receipt,omitempty"`
}

type Ledger struct {
	mu   sync.Mutex
	path string
}

func Open(path string) (*Ledger, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: ledger path must be absolute", ErrInvalid)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return &Ledger{path: path}, nil
}

func (l *Ledger) Append(event Event) error {
	if l == nil {
		return fmt.Errorf("%w: nil ledger", ErrWrite)
	}
	if err := validate(event); err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if containsSensitive(body) {
		return fmt.Errorf("%w: event contains forbidden material", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0600) // #nosec G304 -- ledger path is created by Open.
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	return nil
}

func (l *Ledger) List(principal string) ([]Event, error) {
	if principal == "" {
		return nil, fmt.Errorf("%w: principal", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path) // #nosec G304 -- ledger path is created by Open.
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	events := make([]Event, 0)
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if event.PrincipalID != principal {
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

func validate(event Event) error {
	if event.Schema != Schema || event.EventID == "" || event.PrincipalID == "" || event.Kind == "" || event.Outcome == "" || event.At.IsZero() {
		return fmt.Errorf("%w: required fields", ErrInvalid)
	}
	switch event.Kind {
	case "tool-call", "credential-change", "terminal-exposure", "oauth", "job":
	default:
		return fmt.Errorf("%w: kind", ErrInvalid)
	}
	for _, value := range []string{event.EventID, event.PrincipalID, event.ContextID, event.RuntimeID, event.ConnectionID, event.PolicyRevision, event.JobID, event.HermesRunID, event.ToolCallID, event.CorrelationID, event.Name, event.Outcome, event.Kind, event.Receipt} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%w: newline in field", ErrInvalid)
		}
	}
	if len(event.Receipt) > 256 {
		return fmt.Errorf("%w: receipt is too large", ErrInvalid)
	}
	return nil
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
