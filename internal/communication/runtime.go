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
	JobsAPI bool
	URL     string
	Auth    string
	HTTP    *http.Client
	Limit   time.Duration
	Spool   *Spool
	Resume  bool
}

const defaultHTTPRunTimeout = 30 * time.Minute

func (r HTTPRunner) Run(ctx context.Context, job Job, _ User) (string, error) {
	outcome, err := r.RunOutcome(ctx, job)
	return outcome.Text, err
}

func (r HTTPRunner) RunOutcome(ctx context.Context, job Job) (RunOutcome, error) {
	if r.Spool != nil {
		mapping, ok, err := r.Spool.Mapping(job.ID)
		if err != nil {
			return RunOutcome{}, err
		}
		if ok && mapping.RunID != "" && mapping.SessionID != "" {
			r.Resume = true
		}
	}
	if !r.Resume && strings.TrimSpace(job.Text) == "" {
		return RunOutcome{}, errors.New("empty Hermes prompt")
	}
	if r.Resume {
		job.Text = ""
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
	path := "/v1/execute"
	if r.JobsAPI {
		path = "/v1/jobs"
	}
	if r.Resume {
		path = "/v1/resume"
	}
	req, err := http.NewRequestWithContext(jobCtx, http.MethodPost, strings.TrimRight(r.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return RunOutcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.Auth)
	if r.Spool != nil {
		req.Header.Set("Accept", hubruntime.RunStreamContentType)
	}
	response, err := r.client().Do(req)
	if err != nil {
		return RunOutcome{}, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK && response.Header.Get("Content-Type") == hubruntime.RunStreamContentType {
		var last hubruntime.ExecuteResponse
		err := hubruntime.ReadRunStream(response.Body, func(event hubruntime.ExecuteResponse) error {
			if event.JobID != job.ID {
				return errors.New("stream job identity mismatch")
			}
			if r.Resume && event.LastEvent == "run.admitted" {
				if err := r.Spool.RebindObservation(job, event); err != nil {
					return err
				}
			}
			if err := r.Spool.RecordStreamEvent(job, event); err != nil {
				return err
			}
			last = event
			return nil
		})
		outcome := outcomeFromEvent(last)
		if !terminalStatus(last.Status) {
			outcome.Status, outcome.LastEvent = "uncertain", "run.unknown"
			return outcome, ErrUncertain
		}
		_ = err // The durable terminal receipt is authoritative even if the socket then breaks.
		if last.Status != "completed" {
			return outcome, errors.New("hermes run ended without completion")
		}
		return outcome, nil
	}
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
	if err := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024+64*1024)).Decode(&result); err != nil || (strings.TrimSpace(result.Text) == "" && (result.Status == "completed" || !terminalStatus(result.Status))) {
		return RunOutcome{}, errors.New("runtime returned an invalid response")
	}
	if r.Spool != nil {
		if result.JobID != job.ID {
			return RunOutcome{}, ErrUncertain
		}
		var persistErr error
		if terminalStatus(result.Status) && result.RunID != "" && result.SessionID != "" && result.RuntimeGeneration != "" {
			if r.Resume {
				mapping, ok, err := r.Spool.Mapping(job.ID)
				if err != nil || !ok {
					return RunOutcome{}, ErrUncertain
				}
				if mapping.RuntimeGeneration != result.RuntimeGeneration {
					if err := r.Spool.RebindObservation(job, result); err != nil {
						return RunOutcome{}, ErrUncertain
					}
				}
			}
			// Cached supervisor results need the same write-ahead delivery receipt
			// as streamed terminal events; the worker may crash before enqueueing.
			result.EventID = "terminal:" + result.Status
			result.LastEvent = "run." + result.Status
			persistErr = r.Spool.RecordStreamEvent(job, result)
		} else {
			persistErr = r.Spool.RecordOutcome(job.ID, outcomeFromEvent(result))
		}
		if persistErr != nil {
			return RunOutcome{}, ErrUncertain
		}
	}
	if terminalStatus(result.Status) && result.Status != "completed" {
		return outcomeFromEvent(result), errors.New("hermes run ended without completion")
	}
	return RunOutcome{Text: result.Text, JobID: result.JobID, SessionID: result.SessionID, RunID: result.RunID, RuntimeGeneration: result.RuntimeGeneration, Status: result.Status, LastEvent: result.LastEvent}, nil
}

func (r HTTPRunner) timeout() time.Duration {
	if r.Limit > 0 {
		return r.Limit
	}
	return defaultHTTPRunTimeout
}

func (r HTTPRunner) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: r.timeout() + 5*time.Second}
}

// runtimeRestart returns the restart hook. Unsupervised gateways restart the
// resident runtime with an empty POST; supervised gateways must carry the
// job's durable identity so the supervisor can route the call to the runtime
// that actually ran the work.
func runtimeRestart(url, auth string, supervised bool) func(context.Context, hubruntime.ExecuteRequest) error {
	if url == "" {
		return nil
	}
	return func(ctx context.Context, request hubruntime.ExecuteRequest) error {
		var body io.Reader
		if supervised {
			encoded, err := json.Marshal(request)
			if err != nil {
				return err
			}
			body = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(url, "/")+"/v1/restart", body)
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
