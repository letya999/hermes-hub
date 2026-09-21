package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

const CalendarReadScope = "https://www.googleapis.com/auth/calendar.events.readonly"
const CalendarWriteScope = "https://www.googleapis.com/auth/calendar.events"

// CalendarDefinition separates projection grants; OAuth scope checks are also
// performed at the data-plane boundary, never inferred from model arguments.
func CalendarDefinition(write bool) ToolDefinition {
	id, effect := "google-calendar-read", ReadEffect
	names := []string{"list", "get"}
	if write {
		id, effect, names = "google-calendar-write", WriteEffect, []string{"create", "update", "delete"}
	}
	d := ToolDefinition{Schema: SchemaVersion, DefinitionID: id, Version: "1.0.0", Transport: ProviderAPI,
		Source:      DefinitionSource{URL: "https://www.googleapis.com/calendar/v3", TLSMode: "required"},
		Credentials: []CredentialInput{{Name: "ACCESS_TOKEN", Required: true}, {Name: "OAUTH_SCOPE", Required: true}},
		Workload:    WorkloadPolicy{Class: PerUser, Rationale: "Owner-scoped Google Calendar HTTPS requests"},
		Execution:   ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 65536, CPUMillis: 100, MemoryMiB: 64, MaxPIDs: 1, Egress: []string{"www.googleapis.com"}},
		Health:      HealthProbe{Kind: "http", Value: "/", TimeoutSeconds: 5}}
	for _, name := range names {
		args := []CLIArgument{{Name: "calendar_id", Type: "string", Required: true}}
		if name == "get" || name == "update" || name == "delete" {
			args = append(args, CLIArgument{Name: "event_id", Type: "string", Required: true})
		}
		if name == "create" || name == "update" {
			args = append(args, CLIArgument{Name: "event_json", Type: "string", Required: true})
		}
		if name == "list" {
			args = append(args, CLIArgument{Name: "page_token", Type: "string"})
		}
		d.Tools = append(d.Tools, ToolSpec{Name: name, Effect: effect, Arguments: args})
	}
	return d
}

type CalendarBackend struct{ HTTP *http.Client }

func (b CalendarBackend) Call(ctx context.Context, e EffectiveBinding, t ToolSpec, args map[string]any) (BackendResult, error) {
	return b.CallEnv(ctx, e, t, args, nil)
}

func (b CalendarBackend) CallEnv(ctx context.Context, e EffectiveBinding, t ToolSpec, args map[string]any, env map[string]string) (BackendResult, error) {
	if err := RejectAuthorityArguments(args); err != nil {
		return BackendResult{}, err
	}
	write := t.Effect == WriteEffect
	d := CalendarDefinition(write)
	if !definitionsEqual(e.Definition, d) || e.Connection == nil || env["ACCESS_TOKEN"] == "" || e.Connection.Metadata["google_sub"] == "" {
		return BackendResult{}, ErrUnauthorized
	}
	scope := CalendarReadScope
	if write {
		scope = CalendarWriteScope
	}
	if !slices.Contains(strings.Fields(env["OAUTH_SCOPE"]), scope) {
		return BackendResult{}, ErrUnauthorized
	}
	known := false
	for _, tool := range d.Tools {
		if tool.Name == t.Name {
			known = true
		}
	}
	if !known {
		return BackendResult{}, ErrInvalid
	}
	for key := range args {
		allowed := false
		for _, a := range t.Arguments {
			if key == a.Name {
				allowed = true
			}
		}
		if !allowed {
			return BackendResult{}, ErrInvalid
		}
	}
	calendar, ok := args["calendar_id"].(string)
	if !ok || !calendarResource(calendar) {
		return BackendResult{}, ErrInvalid
	}
	path := d.Source.URL + "/calendars/" + url.PathEscape(calendar) + "/events"
	if t.Name == "get" || t.Name == "update" || t.Name == "delete" {
		id, ok := args["event_id"].(string)
		if !ok || !calendarResource(id) {
			return BackendResult{}, ErrInvalid
		}
		path += "/" + url.PathEscape(id)
	}
	method, body := http.MethodGet, ""
	switch t.Name {
	case "create", "update":
		body, ok = args["event_json"].(string)
		var event map[string]any
		if !ok || len(body) > 16384 || json.Unmarshal([]byte(body), &event) != nil || len(event) == 0 {
			return BackendResult{}, ErrInvalid
		}
		// PATCH replaces an attendees array, supporting explicit add/remove/change.
		method = http.MethodPost
		if t.Name == "update" {
			method = http.MethodPatch
		}
	case "delete":
		method = http.MethodDelete
	case "list":
		q := url.Values{"maxResults": {"100"}}
		if page, exists := args["page_token"]; exists {
			value, valid := page.(string)
			if !valid || len(value) > 2048 {
				return BackendResult{}, ErrInvalid
			}
			q.Set("pageToken", value)
		}
		path += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	if err != nil {
		return BackendResult{}, ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+env["ACCESS_TOKEN"])
	req.Header.Set("Content-Type", "application/json")
	client := b.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return BackendResult{}, ctx.Err()
		}
		return BackendResult{}, fmt.Errorf("calendar request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(d.Execution.OutputBytes)+1))
	if err != nil || len(raw) > d.Execution.OutputBytes {
		return BackendResult{}, ErrInvalid
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return BackendResult{}, fmt.Errorf("calendar HTTP %d", resp.StatusCode)
	}
	if t.Name == "delete" {
		if resp.StatusCode != http.StatusNoContent {
			return BackendResult{}, ErrInvalid
		}
		receipt := SafeReceipt(calendar + ":" + args["event_id"].(string))
		if receipt == "" {
			return BackendResult{}, ErrInvalid
		}
		return BackendResult{Structured: map[string]any{"deleted": true, "receipt": receipt}, Receipt: receipt}, nil
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		return BackendResult{}, ErrInvalid
	}
	if t.Name == "list" {
		if items, exists := payload["items"]; exists {
			if _, ok := items.([]any); !ok {
				return BackendResult{}, ErrInvalid
			}
		} else if payload["kind"] != "calendar#events" {
			return BackendResult{}, ErrInvalid
		}
	}
	if t.Name == "get" && payload["id"] != args["event_id"] {
		return BackendResult{}, ErrInvalid
	}
	receipt := ""
	if write {
		id, valid := payload["id"].(string)
		if !valid || !calendarResource(id) {
			return BackendResult{}, ErrInvalid
		}
		if t.Name == "update" && id != args["event_id"] {
			return BackendResult{}, ErrInvalid
		}
		receipt = SafeReceipt(calendar + ":" + id)
		if receipt == "" {
			return BackendResult{}, ErrInvalid
		}
		payload["receipt"] = receipt
	}
	return BackendResult{Structured: payload, Receipt: receipt}, nil
}

func calendarResource(value string) bool {
	return value != "" && len(value) <= 128 && value != "." && value != ".." && !strings.ContainsAny(value, "/\\?#\r\n\x00")
}
