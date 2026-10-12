package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/communication"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Toggle one non-authority display setting and restore only that override.
func settingsAgentCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, mode string) (status, reason string) {
	for _, name := range []string{"settings_get", "settings_set"} {
		if !admittedHubTool(ctx, client, config, user, name) {
			return "BLOCKED", "owner settings grants required"
		}
	}
	warm := func() (string, string) {
		job := ""
		status, reason := mockPromptJobCase(ctx, client, config, user, "text", "", "", &job)
		if status != "PASS" {
			return status, reason
		}
		if err := waitSettingsRestart(ctx, client, config, user.ID, job); err != nil {
			return "FAIL", err.Error()
		}
		return status, reason
	}
	if status, reason := warm(); status != "PASS" {
		return status, reason
	}
	session, err := ownerHubSession(ctx, client, config, user)
	if err != nil {
		return "BLOCKED", "owner settings session unavailable"
	}
	defer session.Close()
	const key = "display.long_running_notifications"
	before, err := hubToolObject(ctx, session, "settings_get", map[string]any{"key": key})
	if err != nil {
		return "FAIL", "settings baseline unavailable"
	}
	present, ok := before["overridden"].(bool)
	if !ok {
		return "FAIL", "settings override contract absent"
	}
	old := false
	if present {
		old, ok = before["override"].(bool)
		if !ok {
			return "FAIL", "settings baseline has wrong type"
		}
	}
	if !present {
		effective, ok := before["effective"].(map[string]any)
		if !ok {
			return "BLOCKED", "effective display baseline unavailable"
		}
		old, ok = effective[key].(bool)
		if !ok {
			return "BLOCKED", "effective display baseline has wrong type"
		}
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "FAIL", "random setting identity unavailable"
	}
	marker := "E2E " + hex.EncodeToString(nonce)
	baseline := filepath.Join(config.SpoolDir, "e2e-acceptance", "settings-baseline-"+hex.EncodeToString(nonce)+".json")
	data, _ := json.Marshal(map[string]any{"key": key, "overridden": present, "value": old})
	if err := os.MkdirAll(filepath.Dir(baseline), 0700); err != nil {
		return "FAIL", "private settings baseline directory unavailable"
	}
	if err := os.WriteFile(baseline, data, 0600); err != nil {
		return "FAIL", "private settings baseline unavailable; mutation not submitted"
	}
	restore := ""
	if present {
		restore = strconv.FormatBool(old)
	}
	restoreOriginal := func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := hubToolObject(cleanup, session, "settings_set", map[string]any{"key": key, "value": restore})
		if err != nil || result["restart_scheduled"] != true {
			return fmt.Errorf("display override restoration failed")
		}
		return nil
	}
	value := !old
	prompt := fmt.Sprintf("Owner-authorized acceptance test. Use settings_set only with key %s and value %s. Return exactly %s after restart_scheduled=true. Do not change other settings.", key, strconv.FormatBool(value), marker)
	run := mockPromptJobCase
	if mode == "telegram-server" {
		run = serverPromptJobCase
	}
	job := ""
	status, reason = run(ctx, client, config, user, "text", prompt, marker, &job)
	if status != "PASS" {
		if regexp.MustCompile(`^(mock|routine)-[a-f0-9]{64}$`).MatchString(job) {
			data, err := os.ReadFile(filepath.Join(config.SpoolDir, "mappings", job+".json"))
			var mapping communication.JobMapping
			if err == nil && json.Unmarshal(data, &mapping) == nil && mapping.JobID == job && mapping.PrincipalID == user.ID && (mapping.Status == "completed" || mapping.Status == "cancelled" || mapping.Status == "failed") {
				if err := restoreOriginal(); err == nil {
					return "FAIL", reason + "; original override reset after terminal failed test; effective restart unverified; private baseline retained"
				}
			}
		}
		return status, reason + "; display override may remain; no automatic retry"
	}
	if status, reason := warm(); status != "PASS" {
		if err := restoreOriginal(); err != nil {
			return "FAIL", reason + "; original override restoration failed; private baseline retained"
		}
		return "FAIL", reason + "; original override reset; effective restart unverified; private baseline retained"
	}
	actual, err := hubToolObject(ctx, session, "settings_get", map[string]any{"key": key})
	if err != nil || actual["overridden"] != true || actual["override"] != value {
		return "FAIL", "agent claimed setting change without independent persisted value"
	}
	effective, ok := actual["effective"].(map[string]any)
	applied := ok && effective[key] == value
	if err := restoreOriginal(); err != nil {
		return "FAIL", err.Error() + "; private baseline retained"
	}
	// A fresh warm-up applies the deferred restoration through normal readiness.
	if status, reason := warm(); status != "PASS" {
		return status, reason + "; restored setting restart not verified"
	}
	check, err := hubToolObject(ctx, session, "settings_get", map[string]any{"key": key})
	if err != nil || check["overridden"] != present || (present && check["override"] != old) {
		return "FAIL", "original display override not restored"
	}
	restoredEffective, ok := check["effective"].(map[string]any)
	if !ok || restoredEffective[key] != old {
		return "FAIL", "original effective display setting not restored"
	}
	if err := os.Remove(baseline); err != nil {
		return "FAIL", "restored setting verified but private baseline cleanup failed"
	}
	if !applied {
		return "FAIL", "setting saved but changed value was not effective after restart; original override restored"
	}
	return "PASS", reason + "; changed value independently read and original override restored"
}

// A delivery receipt precedes the normal synchronous restart and readiness.
func waitSettingsRestart(ctx context.Context, client *http.Client, config communication.Config, principal, jobID string) error {
	if config.RuntimeURL == "" {
		return nil
	}
	if !regexp.MustCompile(`^(mock|routine)-[a-f0-9]{64}$`).MatchString(jobID) {
		return fmt.Errorf("invalid settings job identity")
	}
	data, err := os.ReadFile(filepath.Join(config.SpoolDir, "done", jobID+".json"))
	var job communication.Job
	if err != nil || json.Unmarshal(data, &job) != nil || job.ID != jobID || job.Envelope.PrincipalID != principal {
		return fmt.Errorf("completed settings job owner mismatch")
	}
	body, err := json.Marshal(hubruntime.ExecuteRequest{Envelope: job.Envelope, JobID: job.ID, OrganizationID: job.OrganizationID, UserID: job.UserID, ActorID: job.ActorID, ScopeID: job.ScopeID, Channel: job.Channel, Trigger: job.Trigger, IdempotencyKey: job.IdempotencyKey})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(config.RuntimeURL, "/")+"/v1/restart", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+config.RuntimeAuth)
	request.Header.Set("Content-Type", "application/json")
	restartClient := *client
	restartClient.Timeout = 4 * time.Minute
	response, err := restartClient.Do(request)
	if err != nil {
		return fmt.Errorf("settings restart readiness unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("settings restart HTTP %d", response.StatusCode)
	}
	return nil
}
