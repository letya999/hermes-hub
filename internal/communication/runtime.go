package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// ErrUncertain means the runtime may have completed the job before the HTTP
// connection failed. The gateway must not blindly retry it.
var ErrUncertain = errors.New("runtime result uncertain")

type HTTPRunner struct {
	URL   string
	Auth  string
	HTTP  *http.Client
	Limit time.Duration
}

func (r HTTPRunner) Run(ctx context.Context, job Job, _ User) (string, error) {
	outcome, err := r.RunOutcome(ctx, job)
	return outcome.Text, err
}

func (r HTTPRunner) RunOutcome(ctx context.Context, job Job) (RunOutcome, error) {
	if strings.TrimSpace(job.Text) == "" {
		return RunOutcome{}, errors.New("empty Hermes prompt")
	}
	body, err := json.Marshal(hubruntime.ExecuteRequest{
		Envelope:       job.Envelope,
		JobID:          job.ID,
		OrganizationID: job.OrganizationID,
		UserID:         job.UserID,
		ActorID:        job.ActorID,
		ScopeID:        job.ScopeID,
		Channel:        job.Channel,
		Trigger:        job.Trigger,
		IdempotencyKey: job.IdempotencyKey,
		Text:           job.Text,
	})
	if err != nil {
		return RunOutcome{}, err
	}
	jobCtx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(jobCtx, http.MethodPost, strings.TrimRight(r.URL, "/")+"/v1/execute", bytes.NewReader(body))
	if err != nil {
		return RunOutcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.Auth)
	response, err := r.client().Do(req)
	if err != nil {
		return RunOutcome{}, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		var failure struct {
			JobID             string `json:"job_id"`
			SessionID         string `json:"session_id"`
			RunID             string `json:"run_id"`
			RuntimeGeneration string `json:"runtime_generation"`
			Status            string `json:"status"`
			LastEvent         string `json:"last_event"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&failure)
		outcome := RunOutcome{JobID: failure.JobID, SessionID: failure.SessionID, RunID: failure.RunID, RuntimeGeneration: failure.RuntimeGeneration, Status: failure.Status, LastEvent: failure.LastEvent}
		if failure.Status == "uncertain" {
			return outcome, fmt.Errorf("%w: runtime returned HTTP %d", ErrUncertain, response.StatusCode)
		}
		return outcome, fmt.Errorf("runtime returned HTTP %d", response.StatusCode)
	}
	var result hubruntime.ExecuteResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || strings.TrimSpace(result.Text) == "" {
		return RunOutcome{}, errors.New("runtime returned an invalid response")
	}
	return RunOutcome{Text: result.Text, JobID: result.JobID, SessionID: result.SessionID, RunID: result.RunID, RuntimeGeneration: result.RuntimeGeneration, Status: result.Status, LastEvent: result.LastEvent}, nil
}

func (r HTTPRunner) timeout() time.Duration {
	if r.Limit > 0 {
		return r.Limit
	}
	return 150 * time.Second
}

func (r HTTPRunner) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: r.timeout() + 5*time.Second}
}

func runtimeRestart(url, auth string) func(context.Context) error {
	if url == "" {
		return nil
	}
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(url, "/")+"/v1/restart", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+auth)
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode/100 != 2 {
			return fmt.Errorf("runtime restart returned HTTP %d", response.StatusCode)
		}
		return nil
	}
}
