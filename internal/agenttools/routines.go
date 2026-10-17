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
	"routine_create":  "Create an owner-scoped routine. kind=reminder delivers exact text; expression after:180s resolves once; show its deadline. kind=watcher polls a url plus regex capture, an extract JSON array (css/html2text/regex/grep/drop_regex/drop_lines/sort_lines/strip/json_path/rss), recipe=rss_feed, probe_kind=script (probe_script gets the body via input(), emits one value), probe_kind=command (probe_script reads the owner watch/ dir, no fetch), or probe_kind=mcp_tool: baseline/unchanged are silent, changes deliver old/new; errors surface in routine_list err. probes see raw HTML — JS pages stay blind. kind=check tests expect (changed|unchanged|equals:value) on a url or task:id; failed probes retry max_retries (default 3). kind=script runs bounded Starlark; digest/brief aggregate watches and tasks. Cron minimum 1 minute. quiet_from/quiet_to defer notifications in an HH:MM window; daily_limit (0..32) caps daily deliveries. notify_mode=agent escalates changes as untrusted evidence. Deterministic kinds never wake a model.",
	"routine_list":    "List owned routines and their current delivery state; the err field carries the real probe failure reason. Observe the same schedule ID after an uncertain create before proposing another routine.",
	"routine_update":  "Update an owned routine schedule or text. Use an absolute once:RFC3339 expression or cron; after: is creation-only.",
	"routine_snooze":  "Snooze one owned routine until expression (RFC3339); this updates the existing schedule ID.",
	"routine_pause":   "Pause an owned routine; already queued occurrences recheck its state before execution.",
	"routine_delete":  "Retire an owned routine; the same schedule ID cannot reactivate it. This does not recall messages already sent.",
	"watch_subscribe": "Subscribe to connector events via one projected read tool (args is a JSON object string). sink=notify sends one message per new event; sink=agent wakes the agent with the events bundled as untrusted evidence. delivery=stream keeps a bounded wait-call outstanding for near-instant batches instead of polling on the cron cadence — use it for wait-style tools like a connector's message wait. Dedup is durable; a connector auth failure marks the watch degraded.",
	"watch_list":      "List owned event subscriptions only — reminders, watchers and other routines are not included.",
	"watch_pause":     "Pause one owned event subscription by schedule id.",
	"watch_delete":    "Delete one owned event subscription by schedule id; this does not recall messages already sent.",
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
	if op == "watch_list" || op == "watch_pause" || op == "watch_delete" {
		// Watch ops are scoped to event-kind routines; pause/delete verify
		// the target kind before mutating (SPEC-0054 R5.5).
		items, err := t.routineItems(ctx, principal)
		if err != nil {
			return nil, err
		}
		var events []map[string]any
		isEvent := false
		for _, item := range items {
			if fmt.Sprint(item["job_kind"]) != "event" {
				continue
			}
			events = append(events, item)
			if fmt.Sprint(item["schedule_id"]) == r.ID {
				isEvent = true
			}
		}
		if op == "watch_list" {
			return map[string]any{"status": "ok", "result": events}, nil
		}
		if !isEvent {
			return nil, errors.New("not an event subscription")
		}
		if op == "watch_pause" {
			method, path = http.MethodPost, "/v1/routines/"+r.ID+"/pause"
		} else {
			method, path = http.MethodDelete, "/v1/routines/"+r.ID
		}
	}
	switch op {
	case "watch_list", "watch_pause", "watch_delete":
		// Resolved above: watch_list already returned, pause/delete paths set.
	case "routine_list":
		method, path = http.MethodGet, "/v1/routines"
	case "watch_subscribe":
		method, path = http.MethodPost, "/v1/routines"
		var args map[string]any
		if r.Args != "" {
			if err := json.Unmarshal([]byte(r.Args), &args); err != nil {
				return nil, errors.New("watch args must be a JSON object")
			}
		}
		create := map[string]any{"schedule_id": r.ID, "timezone": r.Timezone, "expression": r.Expression, "input": r.Text, "job_kind": "event", "probe": map[string]any{"kind": "mcp_tool", "tool": r.Tool}}
		if len(args) > 0 {
			create["probe"].(map[string]any)["args"] = args
		}
		if r.Sink != "" {
			create["sink"] = r.Sink
		}
		if r.Delivery != "" {
			create["delivery"] = r.Delivery
		}
		payload = create
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
			probe := map[string]any{"kind": r.ProbeKind, "url": r.URL, "regex": r.Regex, "tool": r.Tool}
			if r.Recipe != "" {
				probe["recipe"] = r.Recipe
			}
			if r.Extract != "" {
				var steps []map[string]any
				if err := json.Unmarshal([]byte(r.Extract), &steps); err != nil {
					return nil, errors.New("extract must be a JSON array of steps")
				}
				probe["extract"] = steps
			}
			if r.ProbeScript != "" {
				probe["script"] = r.ProbeScript
			}
			create["probe"] = probe
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

// routineItems lists the principal's routines for the watch tools' kind
// check; a non-2xx or undecodable answer is an error, never an empty list.
func (t *Tools) routineItems(ctx context.Context, principal string) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(t.CommunicationURL, "/")+"/v1/routines", nil)
	if err != nil {
		return nil, err
	}
	if t.CommunicationAuth != "" {
		req.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	}
	req.Header.Set("X-Hub-Principal", principal)
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
	var items []map[string]any
	if json.Unmarshal(raw, &items) != nil {
		return nil, errors.New("routine list undecodable")
	}
	return items, nil
}
