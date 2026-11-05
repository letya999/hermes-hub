package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
)

// RunContext is the typed, authority-verified lifecycle record ToolHub reads
// from supervisor /v1/jobs/<id> (SPEC-0098 M2). Every field comes from the
// supervisor's durable job record or its own environment — the caller's
// correlation headers only select which record to fetch and are then
// compared against it; they never fill the context.
type RunContext struct {
	JobID     string `json:"job_id"`
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
	// RuntimeGeneration is the supervisor-bound generation the job was
	// dispatched to; CurrentGeneration is the binding's live generation —
	// a run whose generation is superseded is no longer live.
	RuntimeGeneration string            `json:"runtime_generation"`
	CurrentGeneration string            `json:"current_generation"`
	Environment       string            `json:"environment"`
	Status            string            `json:"status"`
	ScopeID           string            `json:"scope_id"`
	Trigger           string            `json:"trigger"`
	Channel           string            `json:"channel"`
	Envelope          identity.Envelope `json:"identity"`
}

// RunKind is the admission-relevant type of a recorded run.
type RunKind string

const (
	// RunKindPrivate is an interactive owner turn in private scope — the
	// only kind a model-facing memory call may borrow.
	RunKindPrivate RunKind = "private"
	// RunKindGroup is any shared/group-audience run.
	RunKindGroup RunKind = "group"
	// RunKindBackground covers autonomous and system runs (goal, cron,
	// protected forms) — they carry verified service authority on their own
	// paths and are never equivalent to a private run.
	RunKindBackground RunKind = "background"
)

// Kind classifies the recorded run: the scope fixes the audience first —
// unknown scopes resolve to group so a crafted scope can never mint private
// context — and the trigger then separates the interactive owner turn from
// background/system work.
func (r RunContext) Kind() RunKind {
	if memory.AudienceOf(r.ScopeID) != memory.AudiencePrivate {
		return RunKindGroup
	}
	if r.Trigger != "message" {
		return RunKindBackground
	}
	return RunKindPrivate
}

// RunAuthority is the lifecycle-authority seam: Lookup returns the durable
// job record for a claimed job id.
type RunAuthority interface {
	Lookup(ctx context.Context, jobID string) (RunContext, error)
}

// SupervisorRunAuthority reads the run context over the existing
// ToolHub→supervisor control-plane channel: GET /v1/jobs/<id> under the same
// bearer the restart notifier uses. Every non-200 and every malformed body is
// an error; the caller denies on error, never on absence.
type SupervisorRunAuthority struct {
	// URL is the supervisor base (HUB_RUNTIME_SUPERVISOR_URL); Token is the
	// control-plane bearer (HUB_COMMUNICATION_AUTH — the supervisor
	// RuntimeAuth token rendered to ToolHub).
	URL, Token string
	// HTTP allows tests to stub the transport; nil uses a short-timeout
	// default client.
	HTTP *http.Client
}

// RunAuthorityFromEnv returns nil when the supervisor control plane is not
// configured — the gate then denies every run-scoped claim.
func RunAuthorityFromEnv() *SupervisorRunAuthority {
	u := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL")), "/")
	token := strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_AUTH"))
	if u == "" || token == "" {
		return nil
	}
	return &SupervisorRunAuthority{URL: u, Token: token}
}

func (a *SupervisorRunAuthority) Lookup(ctx context.Context, jobID string) (RunContext, error) {
	client := a.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL+"/v1/jobs/"+url.PathEscape(jobID), nil)
	if err != nil {
		return RunContext{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	resp, err := client.Do(req)
	if err != nil {
		return RunContext{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return RunContext{}, fmt.Errorf("job status returned %d", resp.StatusCode)
	}
	var rc RunContext
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&rc); err != nil {
		return RunContext{}, err
	}
	return rc, nil
}
