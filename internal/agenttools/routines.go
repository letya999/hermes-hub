package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

var routineDescriptions = map[string]string{
	"routine_create": "Create an owner-scoped routine. kind=reminder delivers exact text; expression after:180s resolves once on the server; show the returned deadline. kind=watcher polls a public url plus optional regex capture, or probe_kind=mcp_tool with a tool name for a projected read-only connector tool: baseline and unchanged values are silent, changes include bounded old/new values; errors preserve baseline. kind=check tests expect (changed|unchanged|equals:value) against a url or task:<task_id>, retrying a failed probe up to max_retries times (default 3); kind=script runs bounded Starlark; kind=digest aggregates changed watches; kind=brief composes changed sources and open tasks into one model brief. quiet_from/quiet_to set an HH:MM owner-timezone deferral; daily_limit (0..32) caps same-day notifications. notify_mode=digest feeds the digest; notify_mode=agent escalates a real change as untrusted evidence. Deterministic kinds never wake an agent or invoke a model at firing.",
	"routine_list":   "List owned routines and their current delivery state; observe the same schedule ID after an uncertain create before proposing another routine.",
	"routine_update": "Update an owned routine schedule or text. Use an absolute once:RFC3339 expression or cron; after: is creation-only.",
	"routine_snooze": "Snooze one owned routine until expression (RFC3339); this updates the existing schedule ID.",
	"routine_pause":  "Pause an owned routine; already queued occurrences recheck its state before execution.",
	"routine_delete": "Retire an owned routine; the same schedule ID cannot reactivate it. This does not recall messages already sent.",
}

func (t *Tools) Routine(ctx context.Context, op string, r Input) (map[string]any, error) {
	if t.CommunicationURL == "" || strings.Contains(t.CommunicationURL, "${") {
		return nil, errors.New("routines require HUB_COMMUNICATION_CONTROL_URL")
	}
	if strings.Contains(t.CommunicationAuth, "${") {
		return nil, errors.New("routine control authorization is unavailable")
	}
	principal := os.Getenv("HUB_PRINCIPAL_ID")
	if principal == "" {
		principal = os.Getenv("HUB_USER_ID")
	}
	var method, path string
	var payload any
	switch op {
	case "routine_list":
		method, path = http.MethodGet, "/v1/routines"
	case "routine_create":
		method, path = http.MethodPost, "/v1/routines"
		kind := r.Kind
		if kind == "" {
			kind = "agent"
		}
		if kind == "script" {
			if len(r.Text) > 16*1024 {
				return nil, errors.New("script source exceeds 16 KiB")
			}
			// The hub executes script routines in the bounded Starlark
			// runner; reject dead-on-arrival source before it is scheduled.
			opts := syntax.LegacyFileOptions()
			opts.TopLevelControl = true
			if _, _, err := starlark.SourceProgramOptions(opts, "routine.star", r.Text, func(string) bool { return true }); err != nil {
				return nil, fmt.Errorf("script does not compile: %w", err)
			}
		}
		create := map[string]any{"schedule_id": r.ID, "timezone": r.Timezone, "expression": r.Expression, "input": r.Text, "job_kind": kind}
		if kind == "watcher" || kind == "check" {
			create["probe"] = map[string]any{"kind": r.ProbeKind, "url": r.URL, "regex": r.Regex, "tool": r.Tool}
		}
		if kind == "check" {
			create["expect"] = r.Expect
		}
		if r.NotifyMode != "" {
			create["notify_mode"] = r.NotifyMode
		}
		if r.QuietFrom != "" {
			create["quiet_from"] = r.QuietFrom
			create["quiet_to"] = r.QuietTo
		}
		if r.DailyLimit > 0 {
			create["daily_limit"] = r.DailyLimit
		}
		if r.MaxRetries > 0 {
			create["max_retries"] = r.MaxRetries
		}
		if len(r.SourceIDs) > 0 {
			create["source_ids"] = r.SourceIDs
		}
		payload = create
	case "routine_update":
		method, path = http.MethodPost, "/v1/routines/"+r.ID
		payload = map[string]any{"expression": r.Expression, "input": r.Text}
	case "routine_snooze":
		method, path = http.MethodPost, "/v1/routines/"+r.ID+"/snooze"
		payload = map[string]any{"until": r.Expression}
	case "routine_pause":
		method, path = http.MethodPost, "/v1/routines/"+r.ID+"/pause"
	case "routine_delete":
		method, path = http.MethodDelete, "/v1/routines/"+r.ID
	default:
		return nil, errors.New("unknown routine operation")
	}
	if strings.Contains(path, "//") || strings.Contains(r.ID, "/") {
		return nil, errors.New("invalid schedule id")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(t.CommunicationURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if t.CommunicationAuth != "" {
		req.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	}
	req.Header.Set("X-Hub-Principal", principal)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := t.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("routine hub rejected the request")
	}
	out := map[string]any{"status": "ok"}
	if len(raw) > 0 && json.Valid(raw) {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			out["result"] = decoded
		}
	}
	return out, nil
}
