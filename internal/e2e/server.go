package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
)

// Server runs inside the gateway container. Credentials and routing come from
// its existing configuration; reports never contain peers, tokens or replies.
// It uses the existing routine API, not a new impersonation endpoint.
func Server(ctx context.Context, cases []Case, principal string, progress io.Writer) (Report, error) {
	return server(ctx, cases, principal, "telegram-server", progress)
}

func MockServer(ctx context.Context, cases []Case, principal string, progress io.Writer) (Report, error) {
	return server(ctx, cases, principal, "mock-server", progress)
}

func server(ctx context.Context, cases []Case, principal, mode string, progress io.Writer) (Report, error) {
	report := Report{Schema: 1, Mode: mode, Started: time.Now().UTC()}
	config, err := communication.ConfigFromEnv()
	if err != nil {
		return report, fmt.Errorf("gateway configuration unavailable")
	}
	var user communication.User
	for _, candidate := range config.Users {
		if candidate.ID == principal && candidate.Enabled {
			user = candidate
		}
	}
	if user.ID == "" || (mode == "telegram-server" && len(user.TelegramIDs) == 0) || config.ControlAuth == "" {
		return report, fmt.Errorf("explicit enabled principal with Telegram binding required")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	stopped := false
	for _, c := range cases {
		started := time.Now()
		level := "agent-real-provider-telegram-outbound"
		if mode == "mock-server" {
			level = "agent-real-provider-mock-transport"
		}
		if mode == "telegram-server" && c.Surface == "channel" && c.Name == "reminder" {
			level = "deterministic-real-telegram-outbound"
		}
		r := Result{ID: c.ID, Status: "BLOCKED", Level: level, Reason: "automatic server oracle not implemented for this case"}
		if stopped {
			r.Reason = "previous case failed; no next job submitted while its outcome or cleanup may be unresolved"
			report.Results = append(report.Results, r)
			fmt.Fprintf(progress, "%s %s (%s)\n", c.ID, r.Status, r.Level)
			continue
		}
		if mode == "telegram-server" && c.Surface == "channel" && c.Name == "voice-in" {
			r.Level = "real-speech-services"
			if config.TTSURL != "" && config.STTURL != "" {
				r.Status, r.Reason = speechCase(ctx, communication.HTTPSynthesizer{Base: config.TTSURL, Auth: config.TTSAuth, Voice: config.TTSVoice}, communication.HTTPTranscriber{Base: config.STTURL, Auth: config.STTAuth, Timeout: config.STTTimeout})
			}
		}
		if c.Surface == "channel" && (c.Name == "text" || c.Name == "voice-out" || c.Name == "reminder" || (mode == "mock-server" && c.Name == "voice-in")) {
			if mode == "mock-server" {
				if c.Name == "reminder" {
					r.Status, r.Reason = serverPromptModeJobCase(ctx, client, config, user, c.Name, "", "", mode, nil)
				} else {
					r.Status, r.Reason = mockServerCase(ctx, client, config, user, c.Name)
				}
			} else {
				r.Status, r.Reason = serverCase(ctx, client, config, user, c.Name)
			}
		}
		if c.Surface == "channel" && (c.Name == "script" || c.Name == "watch" || c.Name == "check" || c.Name == "digest") {
			r.Status, r.Reason = serverPromptModeJobCase(ctx, client, config, user, c.Name, "", "", mode, nil)
			r.Level = "deterministic-real-" + mode + "-outbound"
		}
		if c.Surface == "hub" && strings.HasPrefix(c.Name, "routine_") {
			if admittedHubTool(ctx, client, config, user, c.Name) {
				r.Status, r.Reason = routineAgentCase(ctx, client, config, user, mode, c.Name)
			} else {
				r.Reason = "owner-bound ToolHub tools/list does not admit this tool, or admission unavailable; no agent job submitted"
			}
		}
		if c.Surface == "hub" && (strings.HasPrefix(c.Name, "file_") || strings.HasPrefix(c.Name, "document_") || c.Name == "artifact_remove" || c.Name == "code_exec") {
			if admittedHubTool(ctx, client, config, user, c.Name) {
				r.Status, r.Reason = fileAgentCase(ctx, client, config, user, mode, c.Name)
			} else {
				r.Reason = "owner-bound ToolHub does not admit this file tool; no agent job submitted"
			}
		}
		if c.Surface == "integration" && c.Name == "remote-mcp" {
			r.Status, r.Reason = mcpAgentCase(ctx, client, config, user, mode)
		}
		if c.Surface == "integration" && c.Name == "cli-install" {
			r.Status, r.Reason = installationAgentCase(ctx, client, config, user, mode, true)
		}
		if c.Surface == "hub" && (c.Name == "settings_get" || c.Name == "service_catalog") {
			r.Status, r.Reason = readAgentCase(ctx, client, config, user, mode, c.Name)
		}
		if c.Surface == "toolhub" && (c.Name == "discover" || c.Name == "status" || c.Name == "required_credentials" || c.Name == "diagnostics") {
			r.Status, r.Reason = readAgentCase(ctx, client, config, user, mode, c.Name)
		}
		if c.Surface == "hub" && c.Name == "settings_set" {
			r.Status, r.Reason = settingsAgentCase(ctx, client, config, user, mode)
		}
		if c.Surface == "hub" && (c.Name == "image_convert" || c.Name == "image_inspect" || c.Name == "image_generate" || c.Name == "image_edit") {
			r.Status, r.Reason = imageAgentCase(ctx, client, config, user, mode, c.Name)
		}
		if c.Surface == "channel" && c.Name == "usage" {
			r.Status, r.Reason = usageAgentCase(ctx, client, config, user, mode)
		}
		r.Seconds = time.Since(started).Seconds()
		stopped = r.Status == "FAIL"
		report.Results = append(report.Results, r)
		fmt.Fprintf(progress, "%s %s (%s)\n", c.ID, r.Status, r.Level)
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
	}
	return report, nil
}

func routineRequest(ctx context.Context, client *http.Client, config communication.Config, principal, method, path string, body any, result any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:8081/v1"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.ControlAuth)
	req.Header.Set("X-Hub-Principal", principal)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("routine transport failed; do not blindly retry")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("routine HTTP %d", response.StatusCode)
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(result)
	}
	return nil
}

func serverCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, name string) (string, string) {
	return serverPromptCase(ctx, client, config, user, name, "", "")
}

func serverPromptCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, name, override, expected string) (string, string) {
	return serverPromptJobCase(ctx, client, config, user, name, override, expected, nil)
}

func serverPromptJobCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, name, override, expected string, admittedJob *string) (string, string) {
	return serverPromptModeJobCase(ctx, client, config, user, name, override, expected, "telegram-server", admittedJob)
}

func serverPromptModeJobCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, name, override, expected, mode string, admittedJob *string) (status, reason string) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "FAIL", "random identity unavailable"
	}
	id := "e2e-" + hex.EncodeToString(nonce)
	marker := "E2E " + hex.EncodeToString(nonce)
	var task communication.Task
	chat := int64(0)
	channel := "telegram_bot"
	if mode == "mock-server" {
		warmJob := ""
		if status, reason := mockPromptJobCase(ctx, client, config, user, "text", "", "", &warmJob); status != "PASS" {
			return status, reason
		}
		data, err := os.ReadFile(filepath.Join(config.SpoolDir, "mock-sent", "job-"+warmJob+"-response.json"))
		var receipt struct {
			Principal string                 `json:"principal"`
			Delivery  communication.Delivery `json:"delivery"`
		}
		if err != nil || json.Unmarshal(data, &receipt) != nil || receipt.Principal != user.ID || receipt.Delivery.JobID != warmJob {
			return "FAIL", "mock routine fixture audience unavailable"
		}
		chat, task.TaskID, channel = receipt.Delivery.ChatID, receipt.Delivery.TaskID, "mock"
	} else {
		chat = user.TelegramIDs[0]
		if err := routineRequest(ctx, client, config, user.ID, http.MethodPost, "/tasks", map[string]string{"name": id}, &task); err != nil {
			return "FAIL", "isolated task creation failed"
		}
	}
	// Keep a timed-out task for diagnosis; deleting it while a job runs is unsafe.
	terminal := false
	defer func() {
		if terminal && mode != "mock-server" {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := routineRequest(cleanup, client, config, user.ID, http.MethodDelete, "/tasks/"+task.TaskID, nil, nil); err != nil {
				status, reason = "FAIL", reason+"; own terminal task cleanup failed"
			}
		}
	}()
	prompt := "Acceptance test. Return exactly this text, no tools or explanation: " + marker
	kind := "agent"
	if name == "voice-out" {
		prompt = "Acceptance test. Return only the upstream VOICE: marker followed by these spoken words, no other text: " + marker
	}
	if name == "reminder" {
		kind, prompt = "reminder", marker
	}
	if name == "script" {
		kind, prompt = "script", fmt.Sprintf("emit(%q)", marker)
	}
	if name == "watch" || name == "check" || name == "digest" {
		kind, prompt = "watcher", marker
		if name == "check" {
			kind = "check"
		}
		if name == "digest" {
			kind = "digest"
		}
	}
	if override != "" {
		prompt, marker = override, expected
	}
	draft := communication.Schedule{ScheduleID: id, TaskID: task.TaskID, Channel: channel, ChatID: chat, Timezone: "UTC", Expression: "once:" + time.Now().UTC().Add(3*time.Second).Format(time.RFC3339), JobKind: kind, Input: prompt, Enabled: true}
	if kind == "watcher" || kind == "check" || kind == "digest" {
		probe := &communication.WatchProbe{Kind: "http", URL: "https://httpbin.org/uuid?e2e=" + id, Regex: `"uuid"\s*:\s*"([a-f0-9-]{36})"`}
		draft.Probe = probe
		if kind == "watcher" {
			draft.Expression = "* * * * *"
		}
		if kind == "check" {
			draft.Expect = "changed"
		}
		if kind == "digest" {
			source := draft
			source.ScheduleID, source.JobKind, source.NotifyMode = id+"-source", "watcher", "digest"
			source.Expression = "* * * * *"
			var createdSource communication.Schedule
			if err := routineRequest(ctx, client, config, user.ID, http.MethodPost, "/routines", source, &createdSource); err != nil {
				return "FAIL", "digest source creation failed"
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := routineRequest(cleanup, client, config, user.ID, http.MethodDelete, "/routines/"+source.ScheduleID, nil, nil); err != nil {
					status, reason = "FAIL", reason+"; own digest source cleanup failed"
				}
			}()
			if _, err := waitAcceptanceWatch(ctx, config.SpoolDir, source.ScheduleID, true); err != nil {
				return "FAIL", err.Error()
			}
			sourceJob := communication.OccurrenceID(communication.RoutineOccurrence{ScheduleID: source.ScheduleID, Revision: createdSource.Revision, DueAt: createdSource.NextDue.Add(time.Minute)})
			if _, err := os.Stat(filepath.Join(config.SpoolDir, "mappings", sourceJob+".json")); !os.IsNotExist(err) {
				return "FAIL", "digest source entered agent queue or queue lookup failed"
			}
			draft.Probe, draft.InitialProbeValue = nil, nil
			draft.SourceIDs = []string{source.ScheduleID}
			draft.Expression = "once:" + time.Now().UTC().Add(3*time.Second).Format(time.RFC3339)
		}
	}
	var created communication.Schedule
	if err := routineRequest(ctx, client, config, user.ID, http.MethodPost, "/routines", draft, &created); err != nil {
		return "FAIL", err.Error()
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := routineRequest(cleanup, client, config, user.ID, http.MethodDelete, "/routines/"+id, nil, nil); err != nil {
			status, reason = "FAIL", reason+"; own schedule cleanup failed"
		}
	}()
	if kind == "watcher" {
		if _, err := waitAcceptanceWatch(ctx, config.SpoolDir, id, false); err != nil {
			return "FAIL", err.Error()
		}
		// Initial observation establishes a quiet baseline; the next minute
		// observes a new public UUID without changing the probe or its state.
		first := communication.OccurrenceID(communication.RoutineOccurrence{ScheduleID: id, Revision: created.Revision, DueAt: created.NextDue})
		quiet := filepath.Join(config.SpoolDir, "outbox", "done", "watch-"+first+".json")
		if mode == "mock-server" {
			quiet = filepath.Join(config.SpoolDir, "mock-sent", "watch-"+first+".json")
		}
		if _, err := os.Stat(quiet); !os.IsNotExist(err) {
			return "FAIL", "first watcher observation unexpectedly notified or receipt lookup failed"
		}
		created.NextDue = created.NextDue.Add(time.Minute)
	}
	jobID := communication.OccurrenceID(communication.RoutineOccurrence{ScheduleID: id, Revision: created.Revision, DueAt: created.NextDue})
	if admittedJob != nil {
		*admittedJob = jobID
	}
	deliveryID := "job-" + jobID + "-response"
	if kind == "reminder" {
		deliveryID = "reminder-" + jobID
	}
	if kind == "script" {
		deliveryID = "script-" + jobID + "-0"
	}
	if kind == "watcher" {
		deliveryID = "watch-" + jobID
	}
	if kind == "check" || kind == "digest" {
		deliveryID = kind + "-" + jobID
	}
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if kind == "watcher" || kind == "check" || kind == "digest" {
			data, err := os.ReadFile(filepath.Join(config.SpoolDir, "occurrences", jobID+".json"))
			var occurrence communication.RoutineOccurrence
			if err == nil && json.Unmarshal(data, &occurrence) == nil && occurrence.ScheduleID == id && occurrence.State == "failed" {
				terminal = true
				return "FAIL", "own deterministic occurrence failed before delivery; no automatic retry"
			}
		}
		if kind == "agent" {
			data, err := os.ReadFile(filepath.Join(config.SpoolDir, "mappings", jobID+".json"))
			var mapping communication.JobMapping
			if err == nil && json.Unmarshal(data, &mapping) == nil && mapping.JobID == jobID && mapping.PrincipalID == user.ID && mapping.TaskID == task.TaskID && (mapping.Status == "failed" || mapping.Status == "cancelled" || mapping.Status == "interrupted") {
				terminal = true
				return "FAIL", "agent terminated before successful delivery"
			}
		}
		receiptPath := filepath.Join(config.SpoolDir, "outbox", "done", deliveryID+".json")
		if mode == "mock-server" {
			receiptPath = filepath.Join(config.SpoolDir, "mock-sent", deliveryID+".json")
		}
		data, readErr := os.ReadFile(receiptPath)
		if readErr == nil {
			var delivery communication.Delivery
			if mode == "mock-server" {
				var receipt struct {
					Principal string                 `json:"principal"`
					Delivery  communication.Delivery `json:"delivery"`
				}
				if json.Unmarshal(data, &receipt) != nil || receipt.Principal != user.ID {
					return "FAIL", "mock deterministic receipt owner mismatch"
				}
				delivery = receipt.Delivery
			} else if json.Unmarshal(data, &delivery) != nil {
				return "FAIL", "invalid delivery receipt"
			}
			terminal = true
			if delivery.ID != deliveryID || delivery.ChatID != chat || delivery.TaskID != task.TaskID || delivery.Channel != channel {
				return "FAIL", "delivery binding differs from isolated task"
			}
			if (kind == "watcher" || kind == "check" || kind == "digest") && delivery.OccurrenceID != jobID {
				return "FAIL", "probe receipt occurrence differs from the scheduled tick"
			}
			if name == "voice-out" {
				if !delivery.VoiceSent || !strings.Contains(delivery.Voice, marker) {
					return "FAIL", "real voice send not proven; text fallback is not voice success"
				}
			} else if kind == "watcher" || kind == "digest" {
				sourceID := id
				if kind == "digest" {
					sourceID = id + "-source"
				}
				state, err := waitAcceptanceWatch(ctx, config.SpoolDir, sourceID, true)
				if err != nil || !strings.Contains(delivery.Text, marker) || !strings.Contains(delivery.Text, state.Value) {
					return "FAIL", "real probe value absent from deterministic receipt"
				}
			} else if strings.TrimSpace(delivery.Text) != marker {
				return "FAIL", "response differs from unique marker"
			}
			var mapping communication.JobMapping
			mapData, mapErr := os.ReadFile(filepath.Join(config.SpoolDir, "mappings", jobID+".json"))
			found := mapErr == nil
			if found {
				mapErr = json.Unmarshal(mapData, &mapping)
			}
			if os.IsNotExist(mapErr) {
				mapErr = nil
			}
			if mapErr != nil {
				return "FAIL", "mapping lookup failed"
			}
			if kind != "agent" && found {
				return "FAIL", "deterministic reminder entered agent queue"
			}
			if kind == "watcher" || kind == "check" {
				if _, err := waitAcceptanceWatch(ctx, config.SpoolDir, id, kind == "watcher"); err != nil {
					return "FAIL", err.Error()
				}
			}
			if kind == "digest" {
				data, err := os.ReadFile(filepath.Join(config.SpoolDir, "digests", id+".json"))
				var cursor communication.DigestState
				if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.ScheduleID != id || cursor.Generations[id+"-source"] != 1 || cursor.LastDigestAt.IsZero() {
					return "FAIL", "own digest cursor not committed"
				}
			}
			if kind == "agent" && (!found || mapping.JobID != jobID || mapping.PrincipalID != user.ID || mapping.TaskID != task.TaskID || mapping.SessionID == "" || mapping.RunID == "" || mapping.Status != "completed") {
				return "FAIL", "successful agent execution not proven"
			}
			if kind != "agent" {
				return "PASS", "deterministic " + kind + "; bound " + mode + " receipt; firing job absent from agent queue"
			}
			return "PASS", "server injection; real provider and Bot API receipt; inbound polling not tested"
		}
		if !os.IsNotExist(readErr) {
			return "FAIL", "delivery receipt unreadable"
		}
		if _, err := os.Stat(filepath.Join(config.SpoolDir, "outbox", "failed", deliveryID+".json")); err == nil {
			return "FAIL", "delivery uncertain; no automatic resend"
		}
		select {
		case <-deadline.Done():
			return "FAIL", "timed out; isolated task retained for diagnosis"
		case <-ticker.C:
		}
	}
}

func waitAcceptanceWatch(ctx context.Context, root, id string, changed bool) (communication.WatchState, error) {
	deadline, cancel := context.WithTimeout(ctx, 125*time.Second)
	defer cancel()
	for {
		data, err := os.ReadFile(filepath.Join(root, "watchers", id+".json"))
		var state communication.WatchState
		if err == nil && json.Unmarshal(data, &state) == nil && state.ScheduleID == id && !state.LastSuccess.IsZero() && (!changed || state.Generation > 0) {
			if state.LastError != "" || !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(state.Value) || (changed && state.Generation != 1) {
				return state, fmt.Errorf("own probe did not commit the expected public value")
			}
			return state, nil
		}
		select {
		case <-deadline.Done():
			return state, fmt.Errorf("own probe did not settle before deadline")
		case <-time.After(time.Second):
		}
	}
}
