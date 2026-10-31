package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type replayExportOpts struct {
	JobID, SpoolDir, SpaceDir, OutDir, PromptFile string
}

// replayBundle is the sanitized incident record: durable envelope + ids +
// prompt (only when the durable job file carried it or the operator supplied
// it and the recorded digest matches) + recorded outcome + a digest of the
// space's non-secret documents. Secret files are never opened.
type replayBundle struct {
	Schema         int               `json:"schema"`
	JobID          string            `json:"job_id"`
	Envelope       identity.Envelope `json:"envelope"`
	OrganizationID string            `json:"organization_id"`
	UserID         string            `json:"user_id"`
	ActorID        string            `json:"actor_id"`
	ScopeID        string            `json:"scope_id"`
	Channel        string            `json:"channel"`
	Trigger        string            `json:"trigger"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	ChatID         int64             `json:"chat_id"`
	MessageID      int               `json:"message_id,omitempty"`
	TaskID         string            `json:"task_id,omitempty"`
	TopicID        int64             `json:"topic_id,omitempty"`
	Sensitive      bool              `json:"sensitive"`
	TextSHA256     string            `json:"text_sha256,omitempty"`
	Text           string            `json:"text,omitempty"`
	Versions       map[string]string `json:"versions,omitempty"`
	Outcome        replayOutcome     `json:"outcome"`
	ConfigDigest   string            `json:"config_digest,omitempty"`
}

type replayOutcome struct {
	Status    string `json:"status"`
	Result    string `json:"result,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// replaySpaceDocs is the allowlist of non-secret space documents folded into
// the config digest. Files like secrets.env are never opened (allowlist, not
// denylist — issue 206).
var replaySpaceDocs = []string{"agent.yaml", "workspace.yaml", "scope.yaml"}

// runReplayExport writes a sanitized incident bundle for one admitted job
// (issue 206). Reads only the spool's job/mapping records and the space's
// allowlisted documents.
func runReplayExport(out io.Writer, opts replayExportOpts) error {
	job, err := replayJob(opts.SpoolDir, opts.JobID)
	if err != nil {
		return err
	}
	bundle := replayBundle{
		Schema:         1,
		JobID:          job.ID,
		Envelope:       job.Envelope,
		OrganizationID: job.OrganizationID,
		UserID:         job.UserID,
		ActorID:        job.ActorID,
		ScopeID:        job.ScopeID,
		Channel:        job.Channel,
		Trigger:        job.Trigger,
		IdempotencyKey: job.IdempotencyKey,
		ChatID:         job.ChatID,
		MessageID:      job.MessageID,
		TaskID:         job.TaskID,
		TopicID:        job.TopicID,
		Sensitive:      job.Sensitive,
		TextSHA256:     job.TextSHA256,
		Text:           job.Text,
		Versions:       map[string]string{"policy_version": job.PolicyVersion, "runtime_id": job.RuntimeID},
	}
	if job.Sensitive {
		// The durable record never carries a sensitive prompt; the operator may
		// supply it, but only if it matches the recorded digest.
		if opts.PromptFile == "" {
			return errors.New("sensitive job: --prompt-file required to replay the original text")
		}
		prompt, err := os.ReadFile(opts.PromptFile)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(prompt)
		if hex.EncodeToString(sum[:]) != job.TextSHA256 {
			return errors.New("prompt file does not match the recorded digest")
		}
		bundle.Text = string(prompt)
	}
	if mapping, ok, err := replayMapping(opts.SpoolDir, opts.JobID); err != nil {
		return err
	} else if ok {
		bundle.Outcome = replayOutcome{Status: mapping.Status, Result: mapping.Result, RunID: mapping.RunID, SessionID: mapping.SessionID}
	}
	if opts.SpaceDir != "" {
		digest, err := replaySpaceDigest(opts.SpaceDir)
		if err != nil {
			return err
		}
		bundle.ConfigDigest = digest
	}
	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(opts.OutDir, "bundle.json"), bundle); err != nil {
		return err
	}
	_, err = io.WriteString(out, "bundle written: "+filepath.Join(opts.OutDir, "bundle.json")+"\n")
	return err
}

// replayJob loads the durable job record from whichever lifecycle dir holds it.
func replayJob(spoolDir, jobID string) (commodel.Job, error) {
	var job commodel.Job
	for _, dir := range []string{"pending", "running", "done", "failed"} {
		body, err := os.ReadFile(filepath.Join(spoolDir, dir, commodel.FileID(jobID)+".json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return job, err
		}
		if err := json.Unmarshal(body, &job); err != nil || job.ID != jobID {
			return job, errors.New("invalid job record")
		}
		return job, nil
	}
	return job, errors.New("job not found in spool")
}

func replayMapping(spoolDir, jobID string) (commodel.JobMapping, bool, error) {
	var mapping commodel.JobMapping
	body, err := os.ReadFile(filepath.Join(spoolDir, "mappings", commodel.FileID(jobID)+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return mapping, false, nil
	}
	if err != nil {
		return mapping, false, err
	}
	if err := json.Unmarshal(body, &mapping); err != nil || mapping.JobID != jobID {
		return mapping, false, errors.New("invalid job mapping")
	}
	return mapping, true, nil
}

type replayRunOpts struct {
	BundlePath, ReplayDir, RuntimeURL, RuntimeAuth string
}

// runReplayRun re-admits the bundle's preserved job on an isolated scratch
// spool, channel forced to mock (issue 206).
func runReplayRun(out io.Writer, opts replayRunOpts) error {
	body, err := os.ReadFile(opts.BundlePath)
	if err != nil {
		return err
	}
	var bundle replayBundle
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&bundle) != nil || d.Decode(new(any)) != io.EOF || bundle.Schema != 1 || bundle.Text == "" || commodel.FileID(bundle.JobID) != bundle.JobID || !identity.ValidID(bundle.Envelope.PrincipalID) {
		return errors.New("invalid replay bundle")
	}
	// A bundle that carries the recorded prompt digest must still match its
	// text — otherwise someone edited the prompt after export.
	if bundle.TextSHA256 != "" {
		sum := sha256.Sum256([]byte(bundle.Text))
		if hex.EncodeToString(sum[:]) != bundle.TextSHA256 {
			return errors.New("invalid replay bundle: text no longer matches text_sha256")
		}
	}
	spool, err := communication.OpenSpool(filepath.Join(opts.ReplayDir, "spool"))
	if err != nil {
		return err
	}
	mockRoot := filepath.Join(opts.ReplayDir, "mock")
	if err := os.MkdirAll(filepath.Join(mockRoot, "mock-sent"), 0o700); err != nil {
		return err
	}
	job := commodel.Job{
		Envelope:       bundle.Envelope,
		ID:             bundle.JobID,
		OrganizationID: bundle.OrganizationID,
		UserID:         bundle.UserID,
		ActorID:        bundle.ActorID,
		ScopeID:        bundle.ScopeID,
		// The replayed job's channel is always mock — a tampered bundle can
		// never steer a send toward a real channel.
		Channel:        "mock",
		Trigger:        bundle.Trigger,
		IdempotencyKey: bundle.IdempotencyKey,
		ChatID:         bundle.ChatID,
		MessageID:      bundle.MessageID,
		TaskID:         bundle.TaskID,
		TopicID:        bundle.TopicID,
		Text:           bundle.Text,
		CreatedAt:      time.Now().UTC(),
	}
	if _, err := spool.Enqueue(job); err != nil {
		return err
	}
	claimed, err := spool.ClaimJob()
	if err != nil {
		return err
	}
	if claimed == nil || claimed.ID != job.ID {
		return errors.New("replayed job was not claimed")
	}
	outcome, err := replayExecute(context.Background(), job, opts.RuntimeURL, opts.RuntimeAuth)
	if err != nil {
		return err
	}
	if err := spool.RecordOutcome(job.ID, outcome); err != nil {
		return err
	}
	if err := spool.CompleteJob(job.ID); err != nil {
		return err
	}
	delivered := 0
	delivery := commodel.Delivery{
		ID:               "replay-" + job.ID,
		JobID:            job.ID,
		Channel:          "mock",
		ConversationID:   job.ConversationID,
		DeliveryTargetID: job.DeliveryTargetID,
		ChatID:           job.ChatID,
		Text:             outcome.Text,
		CreatedAt:        time.Now().UTC(),
	}
	if err := spool.EnqueueDelivery(delivery); err != nil {
		return err
	}
	claimedDelivery, err := spool.ClaimDelivery()
	if err != nil {
		return err
	}
	if claimedDelivery != nil {
		mock := channel.NewMock(channel.MockDeps{
			Capture: func(d *commodel.Delivery) error {
				return writeJSONFile(filepath.Join(mockRoot, "mock-sent", commodel.FileID(d.ID)+".json"), map[string]any{"principal": bundle.Envelope.PrincipalID, "delivery": d})
			},
			LinkOwner:       func(chatID int64) (string, error) { return bundle.Envelope.PrincipalID, nil },
			UserByPrincipal: func(id string) commodel.User { return commodel.User{ID: id} },
			OwnsRoutineEnvelope: func(job commodel.Job, canonical identity.Envelope) bool {
				return true
			},
		})
		if err := mock.Deliver(context.Background(), claimedDelivery); err != nil {
			return err
		}
		if err := spool.CompleteDelivery(claimedDelivery.ID); err != nil {
			return err
		}
		delivered = 1
	}
	mapping, _, err := spool.Mapping(job.ID)
	if err != nil {
		return err
	}
	divergence := mapping.Status != bundle.Outcome.Status
	_, err = fmt.Fprintf(out, "replay job=%s status=%s deliveries=%d recorded_status=%s divergence=%v\n",
		job.ID, mapping.Status, delivered, bundle.Outcome.Status, divergence)
	return err
}

// replayExecute runs the preserved prompt. Without --runtime-url the stub
// runner completes the job with a recorded marker so the claim → outcome →
// delivery chain is still exercised end-to-end; with it, the job posts to the
// replay supervisor's real /v1/execute.
func replayExecute(ctx context.Context, job commodel.Job, runtimeURL, auth string) (commodel.RunOutcome, error) {
	if runtimeURL == "" {
		return commodel.RunOutcome{JobID: job.ID, Status: "completed", Text: "[replay stub] run not executed", LastEvent: "run.completed"}, nil
	}
	request := hubruntime.ExecuteRequest{
		Envelope: job.Envelope, JobID: job.ID, OrganizationID: job.OrganizationID,
		UserID: job.UserID, ActorID: job.ActorID, ScopeID: job.ScopeID,
		Channel: job.Channel, Trigger: job.Trigger, IdempotencyKey: job.IdempotencyKey,
		Text: job.Text,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return commodel.RunOutcome{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(runtimeURL, "/")+"/v1/execute", bytes.NewReader(body))
	if err != nil {
		return commodel.RunOutcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return commodel.RunOutcome{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return commodel.RunOutcome{}, fmt.Errorf("replay supervisor: HTTP %d", response.StatusCode)
	}
	var result hubruntime.ExecuteResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
		return commodel.RunOutcome{}, err
	}
	return commodel.RunOutcome{
		JobID: result.JobID, RunID: result.RunID, SessionID: result.SessionID,
		RuntimeGeneration: result.RuntimeGeneration, Status: result.Status,
		LastEvent: result.LastEvent, Text: result.Text,
	}, nil
}

// replaySpaceDigest hashes only the allowlisted documents: names + contents.
func replaySpaceDigest(spaceDir string) (string, error) {
	h := sha256.New()
	for _, name := range replaySpaceDocs {
		body, err := os.ReadFile(filepath.Join(spaceDir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(body)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
