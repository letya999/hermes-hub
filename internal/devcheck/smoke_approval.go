//go:build integration

package devcheck

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/communication"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// smokeWebPluginIsolation inspects the real spawned container: with the web
// feature on, the per-space materialized hub-web plugin must be mounted
// read-only from inside this space's own generated dir, and the Hermes state
// dir (which carries the web extract cache and tool spillover artifacts) must
// bind to this space's state, never a shared or foreign path.
func smokeWebPluginIsolation(ctx context.Context, m *supervisor.Manager, runtime supervisor.Runtime, contextRoot string) error {
	id, err := smokeOwnedContainerID(ctx, m, runtime)
	if err != nil {
		return err
	}
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Mounts}}", id).Output()
	if err != nil {
		return fmt.Errorf("runtime mount inspection failed: %w", err)
	}
	var mounts []smokeMount
	if err := json.Unmarshal(raw, &mounts); err != nil {
		return fmt.Errorf("runtime mount inspection parse failed: %w", err)
	}
	wantPlugin := filepath.ToSlash(filepath.Join(contextRoot, "generated", "plugins", "hub-web"))
	wantHermes := filepath.ToSlash(filepath.Join(contextRoot, "hermes"))
	pluginOK, hermesOK := false, false
	for _, mt := range mounts {
		src := smokeBindSource(mt.Source)
		switch mt.Destination {
		case "/opt/hermes/plugins/hub-web":
			if mt.RW {
				return fmt.Errorf("hub-web plugin mount is writable")
			}
			if !strings.EqualFold(src, wantPlugin) {
				return fmt.Errorf("hub-web plugin mount source %s is not this space's materialized copy %s", src, wantPlugin)
			}
			pluginOK = true
		case "/state/hermes":
			hermesOK = strings.EqualFold(src, wantHermes)
		}
	}
	if !pluginOK {
		return fmt.Errorf("web feature on but spawned runtime lacks the hub-web plugin mount")
	}
	if !hermesOK {
		return fmt.Errorf("/state/hermes does not bind to this space's hermes dir %s", wantHermes)
	}
	return nil
}

// supervisorApprovalSmoke uses the production HTTP runner and durable audience,
// with a real upstream dangerous-command approval and native continuation.
func supervisorApprovalSmoke(ctx context.Context, m *supervisor.Manager, binding supervisor.Binding, runner communication.HTTPRunner, spool *communication.Spool, template communication.Job, runtimeAuth string) error {
	policyBefore, err := os.ReadFile(filepath.Join(binding.ContextRoot, "settings.yaml"))
	if err != nil {
		return err
	}
	for _, expire := range []bool{false, true} {
		job := template
		job.ID = fmt.Sprintf("real-approval-%t", expire)
		job.IdempotencyKey, job.Text = job.ID, "approval probe"
		if _, err := spool.Enqueue(job); err != nil {
			return err
		}
		runner.Resume = false
		streamCtx, cancel := context.WithCancel(ctx)
		type result struct {
			outcome communication.RunOutcome
			err     error
		}
		done := make(chan result, 1)
		go func() { outcome, err := runner.RunOutcome(streamCtx, job); done <- result{outcome, err} }()
		var mapping communication.JobMapping
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			current, _, err := spool.Mapping(job.ID)
			if err != nil {
				cancel()
				return err
			}
			if current.ApprovalID != "" && current.Status == "waiting_for_approval" {
				mapping = current
				break
			}
			select {
			case result := <-done:
				cancel()
				return fmt.Errorf("native approval never became actionable: %s %v", result.outcome.Status, result.err)
			default:
			}
			if err := contractSleep(ctx, 20*time.Millisecond); err != nil {
				cancel()
				return err
			}
		}
		if mapping.ApprovalID == "" {
			cancel()
			return fmt.Errorf("native approval missing")
		}
		if err := m.Reap(ctx, time.Now().Add(10*time.Second)); err != nil {
			cancel()
			return err
		}
		if runtime, _, err := m.Status(binding); err != nil || runtime.State != supervisor.Busy || runtime.Leases == 0 {
			cancel()
			return fmt.Errorf("actionable native approval lost its hold: %v", err)
		}
		control := hubruntime.RunControl{RunReference: hubruntime.RunReference{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: job.Envelope, JobID: job.ID, OrganizationID: job.OrganizationID, UserID: job.UserID, ActorID: job.ActorID, ScopeID: job.ScopeID, Channel: job.Channel, Trigger: job.Trigger, IdempotencyKey: job.IdempotencyKey}, RunID: mapping.RunID, SessionID: mapping.SessionID, RuntimeGeneration: mapping.RuntimeGeneration}, Action: "approve", RequestID: mapping.ApprovalID, Choice: "once"}
		call := func(request hubruntime.RunControl) (int, error) {
			body, _ := json.Marshal(request)
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, runner.URL+"/v1/control", bytes.NewReader(body))
			if err != nil {
				return 0, err
			}
			r.Header.Set("Authorization", "Bearer "+runner.Auth)
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				return 0, err
			}
			defer response.Body.Close()
			return response.StatusCode, nil
		}
		other := job.Envelope
		other.ConversationID = "other"
		if err := spool.RequestApproval(job.ID, other, mapping.ApprovalID, "once"); err == nil {
			cancel()
			return fmt.Errorf("native approval accepted another conversation")
		}
		bad := control
		bad.RuntimeGeneration = "obsolete"
		if code, err := call(bad); err != nil || code != 409 {
			cancel()
			return fmt.Errorf("native stale-generation approval accepted: %d %v", code, err)
		}
		if expire {
			// Exercise the expiry sweep's explicit clock at the saved deadline.
			m.ExpireApprovals(ctx, mapping.ApprovalDeadline.Add(time.Second))
			m.ExpireApprovals(ctx, mapping.ApprovalDeadline.Add(time.Second))
			if code, err := call(control); err != nil || code != 409 {
				cancel()
				return fmt.Errorf("late native approval accepted: %d %v", code, err)
			}
		} else {
			if err := spool.RequestApproval(job.ID, job.Envelope, mapping.ApprovalID, "once"); err != nil {
				cancel()
				return err
			}
			communication.ProcessControlProbe(ctx, spool, communication.Config{RuntimeURL: runner.URL, RuntimeAuth: runner.Auth})
			decided, _, err := spool.Mapping(job.ID)
			if err != nil || decided.Control == nil || (decided.Control.State != "applied" && decided.Control.State != "closed") {
				cancel()
				return fmt.Errorf("native durable approval dispatch failed: %v", err)
			}
			if code, err := call(control); err != nil || code != 200 {
				cancel()
				return fmt.Errorf("native approval duplicate failed: %d %v", code, err)
			}
		}
		select {
		case result := <-done:
			if (!expire && (result.err != nil || result.outcome.Status != "completed" || result.outcome.Text != "contract final answer")) || (expire && result.outcome.Status != "cancelled") {
				cancel()
				reason := "unavailable"
				if runtime, _, err := m.Status(binding); err == nil {
					if id, err := smokeOwnedContainerID(ctx, m, runtime); err == nil {
						out := func(args ...string) ([]byte, error) { return dockerContractOutput(ctx, args...) }
						if native, err := hermesHTTP(ctx, out, id, runtimeAuth, http.MethodGet, "/v1/runs/"+mapping.RunID, "", ""); err == nil {
							reason, _ = jsonString(native.body, "error")
						}
					}
				}
				return fmt.Errorf("native approval terminal mismatch: expiry=%t status=%s native=%s err=%v", expire, result.outcome.Status, reason, result.err)
			}
		case <-time.After(30 * time.Second):
			cancel()
			return fmt.Errorf("native approval did not settle")
		}
		cancel()
		if runtime, _, err := m.Status(binding); err != nil || runtime.Leases != 0 {
			return fmt.Errorf("settled native approval retained runtime holds: expiry=%t err=%v", expire, err)
		}
		approvals := 0
		for range 16 {
			delivery, err := spool.ClaimDelivery()
			if err != nil {
				return err
			}
			if delivery == nil {
				break
			}
			if delivery.ChatID != job.ChatID || strings.Contains(delivery.Text, "HUB_TOOL_SECRET_CANARY") {
				return fmt.Errorf("native approval escaped original private audience")
			}
			if strings.Contains(delivery.Text, mapping.ApprovalID) {
				approvals++
			}
			if err := spool.CompleteDelivery(delivery.ID); err != nil {
				return err
			}
		}
		if approvals != 1 {
			return fmt.Errorf("native approval audience count=%d", approvals)
		}
	}
	fmt.Println("Real native approval passed: original audience, once/duplicate, generation fence, idle hold, expiry and late rejection")
	policyAfter, err := os.ReadFile(filepath.Join(binding.ContextRoot, "settings.yaml"))
	if err != nil || !bytes.Equal(policyBefore, policyAfter) {
		return fmt.Errorf("native approval changed host authorization policy")
	}
	return nil
}

func smokeOwnedContainerID(ctx context.Context, m *supervisor.Manager, runtime supervisor.Runtime) (string, error) {
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Id}}", runtime.Container).Output()
	id := strings.TrimSpace(string(raw))
	if err != nil || len(id) != 64 {
		return "", fmt.Errorf("smoke container ID unavailable")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", err
	}
	runtime.Container = id
	if err := m.VerifyOwnership(ctx, runtime); err != nil {
		return "", err
	}
	return id, nil
}
