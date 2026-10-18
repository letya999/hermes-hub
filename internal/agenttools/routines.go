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

var routineDescriptions = map[string]string{
	"routine_edit":    "Modify an owned routine: action=update|pause|resume|snooze, id, expected_revision from get/list. update requires nonempty patch and no until; pause/resume accept neither; snooze requires until RFC3339 and no patch. To move future results, use update patch={destination_task_id:<ID>} after routine_list view=destinations; use destination_task_id=current with origin_job_id from current run instructions. Move keeps timing, pause, snooze and prior history; old admitted work keeps its audience. Other patch fields: input, expression, timezone, job_kind, probe, expect, max_retries, notify_mode, quiet_from, quiet_to, daily_limit, source_ids, sink, delivery, catch_up_window_ns, timing. Text edits preserve due/pause/snooze; timing edits clear snooze/reset anchor. Conflict requires rereading. Delete is separate.",
	"routine_get":     "Read full owner/context routine card: revision, configuration, state, saved destination name/chat/topic and active|archived|unavailable status, plus probe errors. Known retired IDs remain readable. Optional include=[history,next_runs]. history_limit 1..100 default 10, history_cursor for next page; preview_limit 1..20 default 5. History separates occurrence, execution and delivery (including partial/uncertain), normally retained 14 days; delivered does not mean read. next_runs previews dates without effects; paused dates are hypothetical, completed/deleted have none. Stream health is event_state. Extra reads are not an atomic snapshot; a failed component fails the call.",
	"routine_resume":  "Resume an owned routine using expected_revision from get/list. Expired once schedules need a new due time first. This does not replay past occurrences.",
	"routine_history": "Read a page of retained routine runs with separate occurrence, verified agent execution and delivery outcomes, including partial or uncertain multipart delivery. Pass next_cursor as cursor. Detail is retained 48h; summaries follow spool retention (normally 14d). Delivery is not a read receipt. Stream subscriptions expose current event_state in routine_get instead of occurrences.",
	"routine_preview": "Read planned slots without running a probe, model or delivery. Includes next snoozed due then original cadence. limit is 1..20. State=paused means hypothetical dates until resume; completed/deleted have no future slots.",
	"routine_create":  "Create owner/context routine. Pass origin_job_id from run routing reference or destination_task_id from routine_list view=destinations; current requires origin_job_id. Never guess chat/topic IDs. Default kind=agent; reminder sends exact text; deterministic kinds never wake a model. after:180s resolves once; once:RFC3339, cron or every:15m (1m..8760h). timing: windows, weekdays, at, start_at, end_at. watcher probe_kind: http url (regex capture or extract steps css/html2text/grep/drop_regex/sort_lines/json_path/rss, recipe=rss_feed), mcp_tool|tool (tool+args), script (probe_script via input()), command (owner watch/ files via read_file('name')), render|service (registered name); needs {tool,args} prefetch; deliver_mode=content|artifact(+artifact_name); baseline silent; changes show old/new. check: expect changed|unchanged|equals:v on URL/task:<id>/tool. quiet/daily_limit bound all notifications incl. agent escalation; notify_mode=agent escalates; dead probes alert once per error (last_error in get/list).",
	"routine_list":    "Find compact owner/context routine cards in stable ID pages. view=destinations instead lists live owned session/task names with task_id, chat_id, topic_id and conversation_id for routing. Optional case-insensitive query searches ID/text (routine view) or task name/ID (destination view) before paging; kind/state filters apply to routine view. limit 1..100 default 20; pass next_cursor as cursor. Use routine_get for full text, destination availability, outcomes and errors. Inspect the same ID after uncertain creation before proposing a replacement.",
	"routine_update":  "Edit patch using expected_revision from get/list; conflict means read and reconsider. Fields: input, expression, timezone, job_kind, probe, expect, max_retries, notify_mode, quiet_from, quiet_to, daily_limit, source_ids, sink, delivery, catch_up_window_ns, timing. Text retains due/pause/snooze; timing clears snooze and resets anchor, preserving pause. Kind change clears old action fields; supply complete target configuration. Clear optional probe/timing with null. once:RFC3339, five-field cron or every:15m; after: is creation-only. timing: windows [{from:HH:MM,to:HH:MM}], weekdays [0..6], at [HH:MM], start_at/end_at RFC3339. Opening inclusive, close exclusive, overnight allowed; extras form union. Identity/routing immutable.",
	"routine_snooze":  "Snooze one owned routine until expression (RFC3339); this updates the existing schedule ID.",
	"routine_pause":   "Pause an owned routine; already queued occurrences recheck its state before execution.",
	"routine_delete":  "Retire an owned routine; the same schedule ID cannot reactivate it. This does not recall messages already sent.",
	"watch_subscribe": "Subscribe to connector events via one projected read tool (args is a JSON object string). sink=notify sends one message per new event; sink=agent wakes the agent with the events bundled as untrusted evidence. delivery=stream keeps a bounded wait-call outstanding for near-instant batches instead of polling on the cron cadence — use it for wait-style tools like a connector's message wait. Dedup is durable; a connector auth failure marks the watch degraded.",
	"watch_list":      "List owned event subscriptions only — reminders, watchers and other routines are not included.",
	"watch_pause":     "Pause one owned event subscription by schedule id.",
	"watch_delete":    "Delete one owned event subscription by schedule id; this does not recall messages already sent.",
}

func (t *Tools) Routine(ctx context.Context, op string, r Input) (map[string]any, error) {
	if op == "routine_create" && r.OriginJobID == "" && r.DestinationTaskID == "" {
		return nil, errors.New("routine_create requires origin_job_id from current run instructions or explicit destination_task_id; use routine_list view=destinations")
	}
	if op == "routine_list" && r.View != "" && r.View != "routines" && r.View != "destinations" {
		return nil, errors.New("routine_list view must be routines or destinations")
	}
	if op == "routine_get" {
		if r.HistoryLimit < 0 || r.HistoryLimit > 100 || r.PreviewLimit < 0 || r.PreviewLimit > 20 {
			return nil, errors.New("routine_get: history_limit must be 1..100 and preview_limit 1..20; zero selects defaults")
		}
		seen := map[string]bool{}
		for _, include := range r.Include {
			if (include != "history" && include != "next_runs") || seen[include] {
				return nil, errors.New("routine_get: include accepts unique history or next_runs entries")
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
				return nil, errors.New("routine_get: invalid card response")
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
			return nil, errors.New("expected_revision from routine_get or routine_list is required")
		}
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
			if r.Needs != "" {
				var needs []map[string]any
				if err := json.Unmarshal([]byte(r.Needs), &needs); err != nil {
					return nil, errors.New("needs must be a JSON array of {tool,args}")
				}
				probe["needs"] = needs
			}
			if r.Service != "" {
				probe["service"] = r.Service
			}
			if r.Steps != "" {
				var steps []map[string]any
				if err := json.Unmarshal([]byte(r.Steps), &steps); err != nil {
					return nil, errors.New("steps must be a JSON array of browser actions")
				}
				probe["steps"] = steps
			}
			if r.WaitFor != "" {
				probe["wait_for"] = r.WaitFor
			}
			if r.Args != "" {
				var args map[string]any
				if err := json.Unmarshal([]byte(r.Args), &args); err != nil {
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
