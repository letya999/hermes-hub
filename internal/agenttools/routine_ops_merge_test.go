package agenttools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Routine projects watch/routine ops onto the communication control plane:
// watch_* ops are scoped to event-kind schedules, creates carry the shaped
// payload, and malformed input fails before the call.
func TestRoutineOpsProjection(t *testing.T) {
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	routines := []map[string]any{
		{"schedule_id": "evt-1", "job_kind": "event"},
		{"schedule_id": "rem-1", "job_kind": "reminder"},
	}
	var lastMethod, lastPath string
	var lastPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Hub-Principal") != "alice" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		lastMethod, lastPath = r.Method, r.URL.Path
		if r.Method == http.MethodGet && r.URL.Path == "/v1/routines" {
			_ = json.NewEncoder(w).Encode(routines)
			return
		}
		if r.Body != nil {
			lastPayload = map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&lastPayload)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	tools := &Tools{CommunicationURL: srv.URL, CommunicationAuth: "tok", HTTP: srv.Client()}

	listed, err := tools.Routine(t.Context(), "watch_list", Input{})
	if err != nil {
		t.Fatal(err)
	}
	events := listed["result"].([]map[string]any)
	if len(events) != 1 || events[0]["schedule_id"] != "evt-1" {
		t.Fatalf("watch_list filter: %v", listed)
	}
	if _, err := tools.Routine(t.Context(), "watch_pause", Input{ID: "rem-1"}); err == nil {
		t.Fatal("paused a non-event routine")
	}
	if _, err := tools.Routine(t.Context(), "watch_pause", Input{ID: "evt-1"}); err != nil {
		t.Fatal(err)
	}
	if lastMethod != http.MethodPost || lastPath != "/v1/routines/evt-1/pause" {
		t.Fatalf("watch_pause call: %s %s", lastMethod, lastPath)
	}
	if _, err := tools.Routine(t.Context(), "watch_delete", Input{ID: "evt-1"}); err != nil {
		t.Fatal(err)
	}
	if lastMethod != http.MethodDelete {
		t.Fatalf("watch_delete call: %s %s", lastMethod, lastPath)
	}
	if _, err := tools.Routine(t.Context(), "routine_list", Input{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Routine(t.Context(), "routine_create", Input{ID: "r1", Expression: "*/5 * * * *", Text: "do it"}); err != nil {
		t.Fatal(err)
	}
	if lastPayload["job_kind"] != "agent" || lastPayload["schedule_id"] != "r1" {
		t.Fatalf("routine_create payload: %v", lastPayload)
	}
	if _, err := tools.Routine(t.Context(), "routine_create", Input{ID: "w1", Kind: "check", Expression: "@hourly", Text: "probe", ProbeKind: "http", URL: "https://example.com", Expect: "ok"}); err != nil {
		t.Fatal(err)
	}
	if lastPayload["job_kind"] != "check" || lastPayload["expect"] != "ok" || lastPayload["probe"].(map[string]any)["kind"] != "http" {
		t.Fatalf("check payload: %v", lastPayload)
	}
	if _, err := tools.Routine(t.Context(), "routine_create", Input{Kind: "script", Text: "print(1)"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Routine(t.Context(), "routine_create", Input{Kind: "script", Text: "def broken("}); err == nil {
		t.Fatal("uncompilable script accepted")
	}
	if _, err := tools.Routine(t.Context(), "watch_subscribe", Input{ID: "sub1", Expression: "*/1 * * * *", Text: "watch", Tool: "poll", Args: `{"q":"x"}`, Sink: "notify", Delivery: "stream"}); err != nil {
		t.Fatal(err)
	}
	probe := lastPayload["probe"].(map[string]any)
	if probe["tool"] != "poll" || probe["args"].(map[string]any)["q"] != "x" || lastPayload["sink"] != "notify" || lastPayload["delivery"] != "stream" {
		t.Fatalf("watch_subscribe payload: %v", lastPayload)
	}
	if _, err := tools.Routine(t.Context(), "watch_subscribe", Input{ID: "sub2", Args: "not json"}); err == nil {
		t.Fatal("invalid watch args accepted")
	}
	if _, err := tools.Routine(t.Context(), "routine_update", Input{ID: "r1", Expression: "@daily", Text: "new"}); err != nil {
		t.Fatal(err)
	}
	if lastPayload["expression"] != "@daily" || lastPayload["input"] != "new" {
		t.Fatalf("routine_update payload: %v", lastPayload)
	}
	if _, err := tools.Routine(t.Context(), "routine_snooze", Input{ID: "r1", Expression: "+10m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Routine(t.Context(), "routine_pause", Input{ID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Routine(t.Context(), "routine_delete", Input{ID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Routine(t.Context(), "routine_delete", Input{ID: "bad/id"}); err == nil {
		t.Fatal("slashed id accepted")
	}
	if _, err := tools.Routine(t.Context(), "bogus_op", Input{}); err == nil {
		t.Fatal("unknown op accepted")
	}
	noHub := &Tools{}
	if _, err := noHub.Routine(t.Context(), "routine_list", Input{}); err == nil {
		t.Fatal("routine without hub url accepted")
	}
	noAuth := &Tools{CommunicationURL: srv.URL, CommunicationAuth: "${AUTH}"}
	if _, err := noAuth.Routine(t.Context(), "routine_list", Input{}); err == nil {
		t.Fatal("routine with unresolved auth accepted")
	}
	_ = strings.TrimSpace
}
