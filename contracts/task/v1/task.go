// Package task is the v1 contract of task-engine: the durable run record a
// job id resolves to (ADR-0107, SPEC-0100 R2.4). Until task-engine lands the
// server is the supervisor's GET /v1/jobs/<id>.
package task

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/letya999/hermes-hub/contracts/internal/wire"
	"github.com/letya999/hermes-hub/internal/identity"
)

// Typed errors (SPEC-0100 R2.5).
var (
	ErrNotFound    = wire.ErrNotFound
	ErrConflict    = wire.ErrConflict
	ErrForbidden   = wire.ErrForbidden
	ErrInvalid     = wire.ErrInvalid
	ErrUnavailable = wire.ErrUnavailable
)

// RunRecord is the authority-verified lifecycle record of one job. Every
// field comes from the durable job record; callers' correlation headers only
// select which record to read.
type RunRecord struct {
	JobID       string `json:"job_id"`
	AdmissionID string `json:"admission_id,omitempty"`
	RunID       string `json:"run_id"`
	SessionID   string `json:"session_id"`
	// RuntimeGeneration is the generation the job was dispatched to;
	// CurrentGeneration is the binding's live one. A superseded generation
	// means the run is no longer live.
	RuntimeGeneration string            `json:"runtime_generation"`
	CurrentGeneration string            `json:"current_generation"`
	Environment       string            `json:"environment"`
	Status            string            `json:"status"`
	ScopeID           string            `json:"scope_id"`
	Trigger           string            `json:"trigger"`
	Channel           string            `json:"channel"`
	UserID            string            `json:"user_id"`
	TextSHA256        string            `json:"text_sha256"`
	ResponseSHA256    string            `json:"response_sha256"`
	Sensitive         bool              `json:"sensitive"`
	Untrusted         bool              `json:"untrusted"`
	Assignment        bool              `json:"assignment"`
	Probe             bool              `json:"probe"`
	Envelope          identity.Envelope `json:"identity"`
}

// Client reads run records.
type Client interface {
	Run(ctx context.Context, jobID string) (RunRecord, error)
}

// ValidJobID is the shared id rule: non-empty, bounded, no path or line
// separators.
func ValidJobID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsAny(id, "/\r\n")
}

type inProc struct{ server Client }

// NewInProc wraps a server for same-process callers.
func NewInProc(server Client) Client { return inProc{server} }

func (c inProc) Run(ctx context.Context, jobID string) (RunRecord, error) {
	if !ValidJobID(jobID) {
		return RunRecord{}, wire.Errorf(ErrInvalid, "invalid job ID")
	}
	record, err := c.server.Run(ctx, jobID)
	if err != nil {
		return RunRecord{}, wire.Normalize(err)
	}
	return wire.Copy(record)
}

type httpClient struct{ wire.Client }

// NewHTTPClient calls a server at baseURL with a bearer token; hc may be nil.
func NewHTTPClient(baseURL, token string, hc *http.Client) Client {
	return httpClient{wire.Client{BaseURL: baseURL, Token: token, HTTP: hc}}
}

func (c httpClient) Run(ctx context.Context, jobID string) (RunRecord, error) {
	if !ValidJobID(jobID) {
		return RunRecord{}, wire.Errorf(ErrInvalid, "invalid job ID")
	}
	var record RunRecord
	err := c.Do(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(jobID), nil, &record)
	return record, err
}

// Handler serves GET /v1/jobs/<id> for a server.
func Handler(server Client, token string) http.Handler {
	return wire.Handle(token, func(r *http.Request) (any, error) {
		if r.Method != http.MethodGet {
			return nil, wire.Errorf(ErrInvalid, "method not allowed")
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
		return NewInProc(server).Run(r.Context(), id)
	})
}
