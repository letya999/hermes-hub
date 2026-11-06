package automation

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

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/routines"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// HTTPSupervisorClient executes durably admitted jobs through the supervisor's
// existing idempotent jobs endpoint.
type HTTPSupervisorClient struct {
	URL   string
	Token string
	// JobPath is /v1/execute only for the single-owner static runtime.
	JobPath string
}

func (c *HTTPSupervisorClient) RunJob(ctx context.Context, key string, job commodel.Job) (hubruntime.ExecuteResponse, error) {
	body, err := json.Marshal(hubruntime.ExecuteRequest{
		Envelope: job.Envelope, OperationalEpoch: job.OperationalEpoch,
		JobID: job.ID, OrganizationID: job.OrganizationID, UserID: job.UserID,
		ActorID: job.ActorID, ScopeID: job.ScopeID, Channel: job.Channel,
		Trigger: job.Trigger, IdempotencyKey: key, Text: job.Text,
		InboundID: job.InboundID,
	})
	if err != nil {
		return hubruntime.ExecuteResponse{}, err
	}
	path := c.JobPath
	if path == "" {
		path = "/v1/jobs"
	}
	if path != "/v1/jobs" && path != "/v1/execute" {
		return hubruntime.ExecuteResponse{}, errors.New("automation: unsupported job path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return hubruntime.ExecuteResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := (&http.Client{Timeout: hubruntime.RunBudget() + time.Minute}).Do(req)
	if err != nil {
		return hubruntime.ExecuteResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && !(path == "/v1/execute" && resp.StatusCode == http.StatusInternalServerError) {
		return hubruntime.ExecuteResponse{}, fmt.Errorf("automation: supervisor job status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil || len(data) > 2*1024*1024 {
		return hubruntime.ExecuteResponse{}, errors.New("automation: supervisor response exceeds limit")
	}
	var result hubruntime.ExecuteResponse
	if json.Unmarshal(data, &result) != nil || result.JobID != job.ID {
		return hubruntime.ExecuteResponse{}, errors.New("automation: invalid supervisor result")
	}
	if result.Status == "" && strings.TrimSpace(result.Text) != "" {
		result.Status = "completed"
	}
	if !commodel.TerminalStatus(result.Status) || result.Status == "completed" && strings.TrimSpace(result.Text) == "" || resp.StatusCode == http.StatusInternalServerError && result.Status != "failed" {
		return hubruntime.ExecuteResponse{}, errors.New("automation: invalid supervisor result")
	}
	return result, nil
}

func (c *HTTPSupervisorClient) RunScript(ctx context.Context, o commodel.RoutineOccurrence) ([]string, error) {
	request := hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{
		Envelope: o.Job.Envelope, JobID: o.Job.ID, OrganizationID: o.Job.OrganizationID,
		UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, Trigger: o.Job.Trigger, IdempotencyKey: o.Job.IdempotencyKey,
		Text: o.Job.Text,
	}, DueAt: o.DueAt}
	return c.postScript(ctx, request)
}

func (c *HTTPSupervisorClient) ProbeScript(ctx context.Context, o commodel.RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
	request := hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{
		Envelope: o.Job.Envelope, JobID: o.Job.ID, OrganizationID: o.Job.OrganizationID,
		UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, Trigger: o.Job.Trigger, IdempotencyKey: o.Job.IdempotencyKey,
		Text: src,
	}, DueAt: o.DueAt, Input: input, MountSpace: mountSpace, Needs: needs}
	texts, err := c.postScript(ctx, request)
	if err != nil {
		return "", err
	}
	if len(texts) != 1 {
		return "", errors.New("automation: probe script must emit one value")
	}
	return texts[0], nil
}

func (c *HTTPSupervisorClient) postScript(ctx context.Context, request hubruntime.ScriptRequest) ([]string, error) {
	if c.JobPath == "/v1/execute" {
		return nil, errors.New("automation: isolated script runtime unavailable")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/v1/script", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("automation: script status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024+1))
	if err != nil || len(data) > 128*1024 {
		return nil, fmt.Errorf("automation: script response exceeds limit")
	}
	var texts []string
	if json.Unmarshal(data, &texts) != nil || len(texts) > routines.ScriptMaxEmits {
		return nil, fmt.Errorf("automation: invalid script response")
	}
	for _, text := range texts {
		if len(text) > routines.ScriptMaxEmitBytes {
			return nil, fmt.Errorf("automation: script emit exceeds limit")
		}
	}
	return texts, nil
}

// HTTPCommunicationClient posts delivery intents to the communication
// outbox endpoint over real HTTP.
type HTTPCommunicationClient struct {
	URL   string
	Token string
}

func (c *HTTPCommunicationClient) OwnerHome(ctx context.Context, principal string) (int64, locale.Lang, error) {
	if !commodel.IDPattern.MatchString(principal) || c.URL == "" || c.Token == "" {
		return 0, "", errors.New("automation: invalid owner-home request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/v1/automation/owner-home", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Hub-Principal", principal)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("automation: owner-home status %d", resp.StatusCode)
	}
	var home struct {
		ChatID int64       `json:"chat_id"`
		Lang   locale.Lang `json:"lang"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&home); err != nil {
		return 0, "", err
	}
	return home.ChatID, home.Lang, nil
}

func (c *HTTPCommunicationClient) PostDelivery(ctx context.Context, principal string, d commodel.Delivery) error {
	if principal == "" || d.ID == "" || c.Token == "" {
		return errors.New("automation: invalid delivery request")
	}
	d.IdempotencyKey = d.ID
	body, err := json.Marshal(d)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/automation/deliveries", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Hub-Principal", principal)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("automation: delivery post status %d", resp.StatusCode)
	}
	return nil
}
