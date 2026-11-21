package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func (t *Tools) Routine(ctx context.Context, op string, r Input) (map[string]any, error) {
	if op == "watch_sample" && r.ID == "" {
		return nil, errors.New("watch_sample requires an existing watch ID; before watch_create, call the projected source read tool directly")
	}
	if op == "routine_create" && r.OriginJobID == "" && r.DestinationTaskID == "" {
		return nil, errors.New("routine_create requires origin_job_id from current run instructions or explicit destination_task_id; use routine_list view=destinations")
	}
	if op == "routine_list" && r.View != "" && r.View != "routines" && r.View != "destinations" {
		return nil, errors.New("routine_list view must be routines or destinations")
	}
	if op == "routine_get" {
		if r.HistoryLimit < 0 || r.HistoryLimit > 100 || r.PreviewLimit < 0 || r.PreviewLimit > 20 {
			return nil, errors.New("routine_read: history_limit must be 1..100 and preview_limit 1..20; zero selects defaults")
		}
		seen := map[string]bool{}
		for _, include := range r.Include {
			if (include != "history" && include != "next_runs") || seen[include] {
				return nil, errors.New("routine_read: include accepts unique history or next_runs entries")
			}
			seen[include] = true
		}
		if len(r.Include) > 0 {
			includes := r.Include
			r.Include = nil
			out, err := t.Routine(ctx, op, r)
			if err != nil {
				return nil, err
			}
			card, ok := out["result"].(map[string]any)
			if !ok {
				return nil, errors.New("routine_read: invalid card response")
			}
			for _, include := range includes {
				operation := "routine_history"
				options := Input{ID: r.ID, Limit: r.HistoryLimit, Cursor: r.HistoryCursor}
				if options.Limit == 0 {
					options.Limit = 10
				}
				if include == "next_runs" {
					operation = "routine_preview"
					options.Limit, options.Cursor = r.PreviewLimit, ""
					if options.Limit == 0 {
						options.Limit = 5
					}
				}
				part, err := t.Routine(ctx, operation, options)
				if err != nil {
					return nil, err
				}
				card[include] = part["result"]
			}
			return out, nil
		}
	}
	if op == "routine_edit" {
		switch r.Action {
		case "update":
			if len(r.Patch) == 0 || r.Until != "" {
				return nil, errors.New("routine_edit: update requires nonempty patch and no until")
			}
		case "pause", "resume":
			if r.Patch != nil || r.Until != "" {
				return nil, errors.New("routine_edit: pause/resume accept neither patch nor until")
			}
		case "snooze":
			if r.Until == "" || r.Patch != nil {
				return nil, errors.New("routine_edit: snooze requires until and no patch")
			}
		default:
			return nil, errors.New("routine_edit: action must be update, pause, resume or snooze")
		}
		r.Expression = r.Until
		return t.Routine(ctx, "routine_"+r.Action, r)
	}
	if t.CommunicationURL == "" || strings.Contains(t.CommunicationURL, "${") {
		return nil, errors.New("routines require HUB_COMMUNICATION_CONTROL_URL")
	}
	if strings.Contains(t.CommunicationAuth, "${") {
		return nil, errors.New("routine control authorization is unavailable")
	}
	switch op {
	case "routine_update", "routine_pause", "routine_resume", "routine_snooze", "routine_delete":
		if r.ExpectedRevision == 0 {
			return nil, errors.New("expected_revision from routine_read or routine_list is required")
		}
	}
	principal := os.Getenv("HUB_PRINCIPAL_ID")
	if principal == "" {
		principal = os.Getenv("HUB_USER_ID")
	}
	var method, path string
	var payload any
	if op == "watch_list" || op == "watch_pause" || op == "watch_resume" || op == "watch_delete" || op == "watch_sample" {
		// Watch ops are scoped to event-kind routines; pause/resume/delete
		// verify the target kind before mutating (SPEC-0054 R5.5).
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
		} else if op == "watch_resume" {
			method, path = http.MethodPost, "/v1/routines/"+r.ID+"/resume"
			payload = map[string]any{}
		} else if op == "watch_sample" {
			method, path = http.MethodPost, "/v1/routines/"+r.ID+"/sample"
			if r.Filter != "" {
				payload = map[string]any{"filter": r.Filter}
			}
		} else {
			method, path = http.MethodDelete, "/v1/routines/"+r.ID
		}
	}
	switch op {
	case "watch_list", "watch_pause", "watch_resume", "watch_delete", "watch_sample":
		// Resolved above: watch_list already returned; pause/resume/delete
		// paths set.
	case "routine_list":
		method, path = http.MethodGet, "/v1/routines"
	case "routine_get":
		method, path = http.MethodGet, "/v1/routines/"+r.ID
	case "routine_history":
		method, path = http.MethodGet, "/v1/routines/"+r.ID+"/history"
	case "routine_preview":
		method, path = http.MethodGet, "/v1/routines/"+r.ID+"/preview"
	case "routine_resume":
		method, path = http.MethodPost, "/v1/routines/"+r.ID+"/resume"
	case "watch_subscribe":
		method, path = http.MethodPost, "/v1/routines"
		var args map[string]any
		if hasParam(r.Args) {
			if err := jsonParam(r.Args, &args); err != nil {
				return nil, errors.New("watch args must be a JSON object")
			}
		}
		create := map[string]any{"schedule_id": r.ID, "timezone": r.Timezone, "expression": r.Expression, "input": r.Text, "job_kind": "event", "probe": map[string]any{"kind": "mcp_tool", "tool": r.Tool}}
		if len(args) > 0 {
			create["probe"].(map[string]any)["args"] = args
		}
		if r.Filter != "" {
			create["probe"].(map[string]any)["filter"] = r.Filter
		}
		if r.MinIntervalSeconds > 0 {
			create["probe"].(map[string]any)["min_interval_seconds"] = r.MinIntervalSeconds
		}
		if r.CursorArg != "" {
			create["probe"].(map[string]any)["cursor_arg"] = r.CursorArg
		}
		if hasParam(r.Sources) {
			var sources []any
			if err := jsonParam(r.Sources, &sources); err != nil {
				return nil, errors.New("watch sources must be a JSON array of {name?,tool?,args}")
			}
			create["probe"].(map[string]any)["sources"] = sources
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
		// Probe-shaped args (a source tool or its args) on a non-probe kind
		// are silently dropped here and meaningless at the hub — refuse with
		// a pointer to watch_create, the cron-shaped misuse it prevents.
		if kind != "watcher" && kind != "check" && (r.Tool != "" || hasParam(r.Args)) {
			return nil, errors.New("probe-shaped args (tool/args) only apply to kind=watcher|check; for monitoring a source's events use watch_create instead — kind=agent wakes the model on a timer with no source probe")
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
		if r.OriginJobID != "" {
			create["origin_job_id"] = r.OriginJobID
		}
		if r.DestinationTaskID != "" {
			create["destination_task_id"] = r.DestinationTaskID
		}
		if r.Timing != nil {
			create["timing"] = r.Timing
		}
		if kind == "watcher" || kind == "check" {
			probe := map[string]any{"kind": r.ProbeKind, "url": r.URL, "regex": r.Regex, "tool": r.Tool}
			if r.Recipe != "" {
				probe["recipe"] = r.Recipe
			}
			if hasParam(r.Extract) {
				var steps []map[string]any
				if err := jsonParam(r.Extract, &steps); err != nil {
					return nil, errors.New("extract must be a JSON array of steps")
				}
				probe["extract"] = steps
			}
			if r.ProbeScript != "" {
				probe["script"] = r.ProbeScript
			}
			if hasParam(r.Needs) {
				var needs []map[string]any
				if err := jsonParam(r.Needs, &needs); err != nil {
					return nil, errors.New("needs must be a JSON array of {tool,args}")
				}
				probe["needs"] = needs
			}
			if r.Service != "" {
				probe["service"] = r.Service
			}
			if hasParam(r.Steps) {
				var steps []map[string]any
				if err := jsonParam(r.Steps, &steps); err != nil {
					return nil, errors.New("steps must be a JSON array of browser actions")
				}
				probe["steps"] = steps
			}
			if r.WaitFor != "" {
				probe["wait_for"] = r.WaitFor
			}
			if hasParam(r.Args) {
				var args map[string]any
				if err := jsonParam(r.Args, &args); err != nil {
					return nil, errors.New("args must be a JSON object")
				}
				probe["args"] = args
			}
			create["probe"] = probe
			if r.DeliverMode != "" {
				create["deliver_mode"] = r.DeliverMode
			}
			if r.ArtifactName != "" {
				create["artifact_name"] = r.ArtifactName
			}
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
		if r.Patch != nil {
			patch := map[string]any{}
			for key, value := range r.Patch {
				patch[key] = value
			}
			if r.OriginJobID != "" {
				patch["origin_job_id"] = r.OriginJobID
			}
			payload = patch
		}
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
	if op == "routine_list" || op == "routine_history" || op == "routine_preview" {
		limit := r.Limit
		if limit == 0 {
			limit = 20
		}
		q := url.Values{"limit": []string{fmt.Sprint(limit)}}
		if r.Cursor != "" {
			q.Set("after", r.Cursor)
		}
		if op == "routine_list" {
			if r.View != "" {
				q.Set("view", r.View)
			}
			if r.Query != "" {
				q.Set("query", r.Query)
			}
			if r.Kind != "" {
				q.Set("kind", r.Kind)
			}
			if r.State != "" {
				q.Set("state", r.State)
			}
		}
		path += "?" + q.Encode()
	}
	if r.ExpectedRevision > 0 && strings.HasPrefix(op, "routine_") && method != http.MethodGet && op != "routine_create" {
		if payload == nil {
			payload = map[string]any{}
		}
		payload.(map[string]any)["expected_revision"] = r.ExpectedRevision
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
	t.communicationAuthority(req, principal)
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("routine hub rejected the request (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 1024)])))
	}
	if len(raw) > 256*1024 {
		return nil, errors.New("routine reply exceeds 256 KiB; request a smaller page")
	}
	if len(raw) > 0 && !json.Valid(raw) {
		return nil, errors.New("routine reply is not valid JSON")
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
	t.communicationAuthority(req, principal)
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
