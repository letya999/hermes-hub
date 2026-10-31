package agenttools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRoutineRejectsUnresolvedConfiguration(t *testing.T) {
	for _, field := range []string{"url", "authorization"} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = w.Write([]byte(`[]`))
			}))
			defer server.Close()
			v := fixture(t)
			v.CommunicationURL, v.CommunicationAuth = server.URL, "token"
			want := "authorization is unavailable"
			if field == "url" {
				v.CommunicationURL = "${HUB_COMMUNICATION_CONTROL_URL}"
				want = "require HUB_COMMUNICATION_CONTROL_URL"
			} else {
				v.CommunicationAuth = "${HUB_RUNTIME_AUTH}"
			}
			_, err := v.Routine(context.Background(), "routine_list", Input{})
			if err == nil || !strings.Contains(err.Error(), want) || calls != 0 {
				t.Fatalf("unresolved %s: error=%v requests=%d", field, err, calls)
			}
		})
	}
}

func TestRoutineLargeReplyIsNotSilentlyLost(t *testing.T) {
	value := strings.Repeat("x", 74000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"input": value})
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	out, err := v.Routine(context.Background(), "routine_list", Input{})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := out["result"].(map[string]any)
	if !ok || result["input"] != value {
		t.Fatal("large success reply lost its result")
	}
}

func TestRoutineInvalidSuccessReplyFailsExplicitly(t *testing.T) {
	for _, body := range []string{`{"items":`, `{"input":"` + strings.Repeat("x", 256*1024) + `"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		v := fixture(t)
		v.CommunicationURL = server.URL
		_, err := v.Routine(context.Background(), "routine_list", Input{})
		server.Close()
		if err == nil {
			t.Errorf("invalid/beyond-bound response accepted (%d bytes)", len(body))
		}
	}
}

func TestRoutineLifecycleCatalogDeclaresArguments(t *testing.T) {
	if err := HubToolsDefinition().Validate(); err != nil {
		t.Errorf("invalid published routine catalog: %v", err)
	}
	want := map[string][]string{
		"routine_get": {"id", "include", "history_limit", "history_cursor", "preview_limit"}, "routine_delete": {"id", "expected_revision"},
		"routine_edit": {"id", "action", "expected_revision", "patch", "until"}, "routine_list": {"limit", "cursor", "kind", "state", "query"}, "routine_create": {"timing"},
	}
	found := map[string]bool{}
	for _, tool := range HubToolsDefinition().Tools {
		names, ok := want[tool.Name]
		if !ok {
			continue
		}
		found[tool.Name] = true
		for _, name := range names {
			present := false
			for _, arg := range tool.Arguments {
				if arg.Name == name {
					present = true
					if (name == "id" || name == "expected_revision" || name == "action") && !arg.Required {
						t.Errorf("%s %s must be required", tool.Name, name)
					}
				}
			}
			if !present {
				t.Errorf("%s missing %s", tool.Name, name)
			}
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("missing lifecycle tool %s", name)
		}
	}
}

func TestRoutinePublishedLifecycleDispatch(t *testing.T) {
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Method+" "+r.URL.Path] = true
		var body map[string]any
		if r.Method != http.MethodGet {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
		}
		if r.URL.Path == "/v1/routines" && r.Method == http.MethodGet {
			if r.URL.Query().Get("limit") != "7" || r.URL.Query().Get("after") != "cursor-a" || r.URL.Query().Get("kind") != "reminder" || r.URL.Query().Get("state") != "paused" {
				t.Errorf("lost list filters: %s", r.URL.RawQuery)
			}
		}
		if strings.HasSuffix(r.URL.Path, "/history") && r.URL.Query().Get("after") != "cursor-a" {
			t.Error("lost history cursor")
		}
		if r.Method != http.MethodGet && r.URL.Path != "/v1/routines" && body["expected_revision"] != float64(3) {
			t.Errorf("lost revision: %+v", body)
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/routines/item" && body["input"] != "edited" {
			t.Errorf("lost typed patch: %+v", body)
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/routines" && body["timing"] == nil {
			t.Error("lost timing")
		}
		_, _ = io.WriteString(w, `{"saved":true}`)
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	client, closeAll := connectHub(t, v)
	defer closeAll()
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := map[string]string{}
	for _, tool := range listed.Tools {
		descriptions[tool.Name] = tool.Description
	}
	for _, op := range []string{"create", "list", "get", "edit", "delete"} {
		name := "routine_" + op
		if descriptions[name] == "" {
			t.Errorf("missing documented MCP tool %s", name)
		}
		args := map[string]any{"id": "item", "action": "update", "expected_revision": 3, "limit": 7, "cursor": "cursor-a", "include": []string{"history", "next_runs"}, "history_cursor": "cursor-a", "kind": "reminder", "state": "paused", "timezone": "UTC", "expression": "2100-01-01T10:00:00Z", "text": "fixture", "patch": map[string]any{"input": "edited"}, "timing": map[string]any{"windows": []any{map[string]any{"from": "14:00", "to": "16:00"}}}}
		args["destination_task_id"] = "default"
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Errorf("published %s call failed: %+v %v", name, result, err)
		}
	}
	if len(seen) != 7 {
		t.Errorf("five public tools reached %d distinct HTTP routes, want 7", len(seen))
	}
}

func TestRoutineListRequestsBoundedDefaultPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "20" {
			t.Errorf("list must request default page: %s", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "routine_list", Input{}); err != nil {
		t.Fatal(err)
	}
}

func TestRoutineMutationRequiresObservedRevision(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = io.WriteString(w, `{}`) }))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	for _, op := range []string{"routine_update", "routine_pause", "routine_resume", "routine_snooze", "routine_delete"} {
		_, err := v.Routine(context.Background(), op, Input{ID: "fixture", Expression: "2100-01-01T10:00:00Z"})
		if err == nil {
			t.Errorf("%s accepted missing revision", op)
		}
	}
	if calls != 0 {
		t.Errorf("unfenced mutations reached server: %d", calls)
	}
}

func TestRoutineCreatePassesConnectorProbeArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid fixture body", http.StatusBadRequest)
			return
		}
		probe, _ := body["probe"].(map[string]any)
		args, _ := probe["args"].(map[string]any)
		if args["resource"] != "fixture" {
			t.Errorf("connector probe arguments lost: %+v", body)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "routine_create", Input{DestinationTaskID: "default", ID: "check-args", Kind: "check", ProbeKind: "mcp_tool", Tool: "read_resource", Args: `{"resource":"fixture"}`}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range HubToolsDefinition().Tools {
		if tool.Name == "routine_create" {
			for _, arg := range tool.Arguments {
				if arg.Name == "args" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("create does not advertise probe arguments")
	}
}

func TestRoutineConflictKeepsStatusAndReasonWithoutRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "routine revision changed; read current state", http.StatusConflict)
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	_, err := v.Routine(context.Background(), "routine_pause", Input{ID: "fixture", ExpectedRevision: 1})
	if err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), "revision changed") || calls != 1 {
		t.Fatalf("conflict lost or automatically retried: %v calls=%d", err, calls)
	}
}

func TestRoutineToolsCallCommunicationHub(t *testing.T) {
	v := fixture(t)
	if _, err := v.Routine(context.Background(), "routine_list", Input{}); err == nil {
		t.Fatal("routine without hub url succeeded")
	}
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-token" || r.Header.Get("X-Hub-Principal") != "alice" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		seen[r.Method+" "+r.URL.Path]++
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/routines":
			_ = json.NewEncoder(w).Encode([]map[string]string{})
		case "POST /v1/routines":
			if !json.Valid(body) {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"schedule_id":"morning"}`))
		case "POST /v1/routines/morning":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"schedule_id":"morning"}`))
		case "POST /v1/routines/morning/snooze":
			var request map[string]any
			if json.Unmarshal(body, &request) != nil || request["until"] != "2099-01-02T00:00:00Z" {
				t.Errorf("wrong snooze payload: %s", body)
			}
			w.WriteHeader(http.StatusOK)
		case "POST /v1/routines/morning/pause", "DELETE /v1/routines/morning":
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	v.CommunicationAuth = "control-token"
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	ctx := context.Background()
	if _, err := v.Routine(ctx, "routine_list", Input{}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "morning", Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Text: "brief"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_update", Input{ID: "morning", ExpectedRevision: 1, Expression: "* * * * *", Text: "later"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_pause", Input{ID: "morning", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_snooze", Input{ID: "morning", ExpectedRevision: 1, Expression: "2099-01-02T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_delete", Input{ID: "morning", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_delete", Input{ID: "bad/id", ExpectedRevision: 1}); err == nil {
		t.Fatal("slash id accepted")
	}
	if _, err := v.Routine(ctx, "unknown", Input{}); err == nil {
		t.Fatal("unknown op accepted")
	}
	if seen["GET /v1/routines"] != 1 || seen["POST /v1/routines"] != 1 || seen["DELETE /v1/routines/morning"] != 1 || seen["POST /v1/routines/morning/snooze"] != 1 {
		t.Fatalf("hub calls: %v", seen)
	}
}

// A reminder routine tells the hub not to wake an agent at fire time: the
// created schedule must carry job_kind=reminder.
func TestRoutineCreateReminderPassesJobKind(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/routines" {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"schedule_id":"call-bank"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "routine_create", Input{DestinationTaskID: "default", ID: "call-bank", Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Text: "Позвонить в банк", Kind: "reminder"}); err != nil {
		t.Fatal(err)
	}
	if request["job_kind"] != "reminder" {
		t.Fatalf("reminder kind lost: %v", request)
	}
}

// kind=script runs in the bounded Starlark runner at fire time. Create-time
// contract: valid Starlark is admitted; a syntax-broken script is rejected
// before it ever becomes a dead schedule.
func TestRoutineCreateScriptGate(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "routine_create", Input{DestinationTaskID: "default", ID: "nightly", Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Text: `emit("backup done")`, Kind: "script"}); err != nil {
		t.Fatalf("valid script routine rejected: %v", err)
	}
	if request["job_kind"] != "script" {
		t.Fatalf("script kind lost: %v", request)
	}
	request = nil
	if _, err := v.Routine(context.Background(), "routine_create", Input{DestinationTaskID: "default", ID: "broken", Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Text: `emit(`, Kind: "script"}); err == nil {
		t.Fatal("syntax-broken script accepted")
	}
	if request != nil {
		t.Fatal("broken script reached the hub")
	}
}

// Watcher and check routines carry a deterministic probe spec to the hub;
// digest needs no probe — the hub aggregates the owner's watch states.
func TestRoutineCreateProbeKinds(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	ctx := context.Background()
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "price", Timezone: "UTC", Expression: "*/15 * * * *", Text: "watch the price", Kind: "watcher", URL: "https://example.com/p", Regex: "price: (\\d+)"}); err != nil {
		t.Fatal(err)
	}
	probe, _ := request["probe"].(map[string]any)
	if request["job_kind"] != "watcher" || probe["url"] != "https://example.com/p" || probe["regex"] != "price: (\\d+)" {
		t.Fatalf("watcher probe lost: %v", request)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "pr-merged", Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Text: "remind me to close the tab", Kind: "check", URL: "https://example.com/pr", Regex: "state: (\\w+)", Expect: "changed"}); err != nil {
		t.Fatal(err)
	}
	if request["expect"] != "changed" {
		t.Fatalf("check expect lost: %v", request)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "dg", Timezone: "UTC", Expression: "0 8 * * *", Text: "morning digest", Kind: "digest"}); err != nil {
		t.Fatal(err)
	}
	if request["job_kind"] != "digest" {
		t.Fatalf("digest kind lost: %v", request)
	}
}

// The catalog declares kind as optional so callers can pick reminder without
// breaking existing routine_create invocations.
func TestRoutineCreateKindIsOptionalInCatalog(t *testing.T) {
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name != "routine_create" {
			continue
		}
		optional := map[string]bool{}
		for _, a := range spec.Arguments {
			optional[a.Name] = !a.Required
		}
		if !optional["kind"] || !optional["url"] || !optional["regex"] || !optional["expect"] {
			t.Fatalf("routine_create probe args missing or required: %v", optional)
		}
		if !strings.Contains(spec.Description, "reminder") {
			t.Fatal("routine_create description does not mention reminder")
		}
		return
	}
	t.Fatal("routine_create missing from catalog")
}

// Watcher policy fields ride the same create payload so an agent can set a
// quiet window, a daily cap and check retries without a second endpoint.
func TestRoutineCreateForwardsPolicyFields(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"schedule_id":"quiet-watch"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "routine_create", Input{DestinationTaskID: "default", ID: "quiet-watch", Timezone: "UTC", Expression: "0 * * * *", Text: "watch", Kind: "watcher", URL: "https://example.com/q", QuietFrom: "22:00", QuietTo: "07:00", DailyLimit: 4, MaxRetries: 5}); err != nil {
		t.Fatal(err)
	}
	if request["quiet_from"] != "22:00" || request["quiet_to"] != "07:00" {
		t.Fatalf("quiet window lost: %v", request)
	}
	if request["daily_limit"] != float64(4) || request["max_retries"] != float64(5) {
		t.Fatalf("limit fields lost: %v", request)
	}
}

// An mcp_tool probe names one projected read tool instead of a URL; the
// create payload must carry kind + tool so the hub can validate it.
func TestRoutineCreateForwardsMCPTool(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"schedule_id":"conn-watch"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "routine_create", Input{DestinationTaskID: "default", ID: "conn-watch", Timezone: "UTC", Expression: "0 * * * *", Text: "watch connector", Kind: "watcher", ProbeKind: "mcp_tool", Tool: "hub-google-work-search-abc"}); err != nil {
		t.Fatal(err)
	}
	probe, ok := request["probe"].(map[string]any)
	if !ok || probe["kind"] != "mcp_tool" || probe["tool"] != "hub-google-work-search-abc" {
		t.Fatalf("mcp_tool probe lost: %v", request)
	}
}

// The probe pipeline fields ride the same probe object so an agent can create
// recipe, extract-chain, script and command watchers without a second tool.
func TestRoutineCreateForwardsProbePipeline(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"schedule_id":"feed"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	ctx := context.Background()
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "feed", Timezone: "UTC", Expression: "*/30 * * * *", Text: "watch the feed", Kind: "watcher", URL: "https://example.com/rss", Recipe: "rss_feed"}); err != nil {
		t.Fatal(err)
	}
	probe, _ := request["probe"].(map[string]any)
	if probe["recipe"] != "rss_feed" {
		t.Fatalf("recipe lost: %v", request)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "price2", Timezone: "UTC", Expression: "*/15 * * * *", Text: "watch", Kind: "watcher", URL: "https://example.com/p", Extract: `[{"kind":"css","selector":"div.price"},{"kind":"strip"}]`}); err != nil {
		t.Fatal(err)
	}
	probe, _ = request["probe"].(map[string]any)
	steps, _ := probe["extract"].([]any)
	if len(steps) != 2 || steps[0].(map[string]any)["kind"] != "css" {
		t.Fatalf("extract lost: %v", request)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "dir", Timezone: "UTC", Expression: "10 * * * *", Text: "watch dir", Kind: "watcher", ProbeKind: "command", ProbeScript: `emit(hash_path("."))`}); err != nil {
		t.Fatal(err)
	}
	probe, _ = request["probe"].(map[string]any)
	if probe["kind"] != "command" || probe["script"] != `emit(hash_path("."))` {
		t.Fatalf("command probe lost: %v", request)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "bad", Timezone: "UTC", Expression: "10 * * * *", Text: "watch", Kind: "watcher", URL: "https://example.com", Extract: `[{"kind":`}); err == nil {
		t.Fatal("malformed extract accepted")
	}
}

// needs and deliver_mode ride the same create surface: a script probe gets
// hub-prefetched tool results via needs() and report modes ship the value
// every tick or as a staged file (SPEC-0059 R5.2/R6).
func TestRoutineCreateForwardsNeedsAndDeliver(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"schedule_id":"search-report"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	ctx := context.Background()
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "search-report", Timezone: "UTC", Expression: "0 9 * * *", Text: "morning search", Kind: "watcher", ProbeKind: "command", ProbeScript: `emit(needs()[0])`, Needs: `[{"tool":"web_search","args":{"q":"LLM news"}}]`, DeliverMode: "content"}); err != nil {
		t.Fatal(err)
	}
	probe, _ := request["probe"].(map[string]any)
	needs, _ := probe["needs"].([]any)
	if len(needs) != 1 || needs[0].(map[string]any)["tool"] != "web_search" {
		t.Fatalf("needs lost: %v", request)
	}
	if request["deliver_mode"] != "content" {
		t.Fatalf("deliver_mode lost: %v", request)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "file-report", Timezone: "UTC", Expression: "0 9 * * *", Text: "send file", Kind: "watcher", ProbeKind: "command", ProbeScript: `emit(read_file("r.md"))`, DeliverMode: "artifact", ArtifactName: "report.md"}); err != nil {
		t.Fatal(err)
	}
	if request["deliver_mode"] != "artifact" || request["artifact_name"] != "report.md" {
		t.Fatalf("artifact fields lost: %v", request)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "bad-needs", Timezone: "UTC", Expression: "0 9 * * *", Text: "x", Kind: "watcher", ProbeKind: "command", ProbeScript: `emit(1)`, Needs: `[{"tool":`}); err == nil {
		t.Fatal("malformed needs accepted")
	}
}

// render/service probes name an operator-registered service; browser steps
// travel as a JSON array for the render contract (SPEC-0059 R5.4/R5.5).
func TestRoutineCreateForwardsRenderAndService(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"schedule_id":"spa"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	ctx := context.Background()
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "spa", Timezone: "UTC", Expression: "*/30 * * * *", Text: "watch spa", Kind: "watcher", ProbeKind: "render", URL: "https://spa.example.com", Service: "browserless", Steps: `[{"action":"click","selector":"#go"}]`, WaitFor: "networkidle"}); err != nil {
		t.Fatal(err)
	}
	probe, _ := request["probe"].(map[string]any)
	steps, _ := probe["steps"].([]any)
	if probe["kind"] != "render" || probe["service"] != "browserless" || probe["wait_for"] != "networkidle" || len(steps) != 1 {
		t.Fatalf("render probe lost: %v", request)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "svc", Timezone: "UTC", Expression: "0 9 * * *", Text: "jobs", Kind: "watcher", ProbeKind: "service", Service: "jobftch", Args: `{"tenant":"alice"}`}); err != nil {
		t.Fatal(err)
	}
	probe, _ = request["probe"].(map[string]any)
	args, _ := probe["args"].(map[string]any)
	if probe["kind"] != "service" || probe["service"] != "jobftch" || args["tenant"] != "alice" {
		t.Fatalf("service probe lost: %v", request)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "bad-steps", Timezone: "UTC", Expression: "0 9 * * *", Text: "x", Kind: "watcher", ProbeKind: "render", URL: "https://x", Service: "s", Steps: `[{"action":`}); err == nil {
		t.Fatal("malformed steps accepted")
	}
	// The LLM-facing schema must expose the pipeline fields or the agent can
	// never pass them.
	declared := map[string]bool{}
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name != "routine_create" {
			continue
		}
		for _, a := range spec.Arguments {
			declared[a.Name] = true
		}
	}
	for _, want := range []string{"needs", "deliver_mode", "artifact_name", "service", "steps", "wait_for", "args", "recipe", "extract", "probe_script"} {
		if !declared[want] {
			t.Fatalf("routine_create schema missing %q", want)
		}
	}
}

// watch_subscribe is an event-subscription routine: the create payload must
// carry job_kind=event, an mcp_tool probe with parsed JSON args, and the
// requested sink — the agent never constructs the probe envelope itself.
func TestWatchSubscribeCreatesEventSchedule(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"schedule_id":"mail-watch"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "watch_subscribe", Input{ID: "mail-watch", Timezone: "UTC", Expression: "*/5 * * * *", Text: "new mail", Tool: "hub-mail-events-abc", Sink: "agent", Args: `{"folder":"INBOX"}`}); err != nil {
		t.Fatal(err)
	}
	probe, ok := request["probe"].(map[string]any)
	if request["job_kind"] != "event" || request["sink"] != "agent" || !ok || probe["kind"] != "mcp_tool" || probe["tool"] != "hub-mail-events-abc" {
		t.Fatalf("event subscription payload lost: %v", request)
	}
	args, _ := probe["args"].(map[string]any)
	if args["folder"] != "INBOX" {
		t.Fatalf("tool args not forwarded: %v", probe)
	}
	// A non-JSON args string must be rejected before reaching the hub.
	request = nil
	if _, err := v.Routine(context.Background(), "watch_subscribe", Input{ID: "mail-watch2", Timezone: "UTC", Expression: "*/5 * * * *", Text: "new mail", Tool: "hub-mail-events-abc", Args: `{oops`}); err == nil {
		t.Fatal("malformed args JSON accepted")
	}
	if request != nil {
		t.Fatal("malformed args reached the hub")
	}
}

// watch_subscribe forwards delivery=stream so the hub supervises a bounded
// wait-loop instead of polling on the cron cadence (CHG-0104).
func TestWatchSubscribeForwardsDelivery(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"schedule_id":"dm-watch"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "watch_subscribe", Input{ID: "dm-watch", Timezone: "UTC", Expression: "* * * * *", Text: "Daniil DMs", Tool: "hub-tg-wait_for_settled_message-a", Delivery: "stream", Sink: "agent", Args: `{"chat_id":"@daniil_x"}`}); err != nil {
		t.Fatal(err)
	}
	if request["delivery"] != "stream" {
		t.Fatalf("delivery mode lost: %v", request)
	}
}

// watch_subscribe forwards the deterministic gate and the source's cadence
// contract: filter goes to probe.filter and the declared interval to
// probe.min_interval_seconds so the hub can validate a poll-style stream.
func TestWatchSubscribeForwardsFilterAndMinInterval(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"schedule_id":"feed-watch"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	_, err := v.Routine(context.Background(), "watch_subscribe", Input{ID: "feed-watch", Timezone: "UTC", Expression: "* * * * *", Text: "interesting repos", Tool: "hub-feed-poll-abc", Delivery: "stream", Sink: "agent", Args: `{"scope":"all"}`, Filter: "print('keep' if 'github' in event['text'] else 'drop')", MinIntervalSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	probe, _ := request["probe"].(map[string]any)
	if probe["filter"] != "print('keep' if 'github' in event['text'] else 'drop')" || probe["min_interval_seconds"] != float64(30) {
		t.Fatalf("probe contract fields lost: %v", probe)
	}
	// The LLM-facing schema must expose every contract arg or the agent can
	// never declare a stream's delivery, gate, or cadence.
	declared := map[string]bool{}
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name != "watch_subscribe" {
			continue
		}
		for _, a := range spec.Arguments {
			declared[a.Name] = true
		}
	}
	for _, want := range []string{"delivery", "filter", "min_interval_seconds", "sources"} {
		if !declared[want] {
			t.Fatalf("watch_subscribe schema missing %q", want)
		}
	}
}

// watch_subscribe fans one subscription out over several acquisition
// inputs: the sources JSON array lands on probe.sources so the stream loop
// merges one wake per round instead of one watch per chat.
func TestWatchSubscribeForwardsSources(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"schedule_id":"groups-watch"}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	sources := `[{"name":"alpha","args":{"chat_id":1}},{"name":"beta","tool":"hub-tg-wait-settled-xyz","args":{"chat_id":2}}]`
	if _, err := v.Routine(context.Background(), "watch_subscribe", Input{ID: "groups-watch", Timezone: "UTC", Expression: "* * * * *", Text: "watch groups", Tool: "hub-tg-wait-abc", Delivery: "stream", Sink: "agent", Args: `{"timeout":20}`, Sources: sources}); err != nil {
		t.Fatal(err)
	}
	probe, _ := request["probe"].(map[string]any)
	list, ok := probe["sources"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("sources lost: %v", probe)
	}
	first, _ := list[0].(map[string]any)
	second, _ := list[1].(map[string]any)
	if first["name"] != "alpha" || second["tool"] != "hub-tg-wait-settled-xyz" {
		t.Fatalf("source fields mangled: %v", list)
	}
	if _, err := v.Routine(context.Background(), "watch_subscribe", Input{ID: "groups-watch2", Timezone: "UTC", Expression: "* * * * *", Text: "w", Tool: "hub-tg-wait-abc", Delivery: "stream", Sources: `{oops`}); err == nil {
		t.Fatal("malformed sources JSON accepted")
	}
}

// watch_sample POSTs to the schedule's /sample route, forwarding a
// candidate filter for dry-run verdicts — it is the agent's window into
// the connector's real event shape before it commits a gate.
func TestWatchSamplePostsToSampleRoute(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var gotPath, gotMethod string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if strings.HasSuffix(r.URL.Path, "/sample") {
			_, _ = w.Write([]byte(`{"schedule_id":"w1","sources":[{"source":"alpha","events":[{"id":"m1","raw":"{\"chat_id\":-1}"}],"verdicts":{"m1":"keep"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`[{"schedule_id":"w1","job_kind":"event"}]`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	out, err := v.Routine(context.Background(), "watch_sample", Input{ID: "w1", Filter: `emit("keep")`})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || !strings.HasSuffix(gotPath, "/v1/routines/w1/sample") {
		t.Fatalf("sample routed wrong: %s %s", gotMethod, gotPath)
	}
	if gotBody["filter"] != `emit("keep")` {
		t.Fatalf("candidate filter not forwarded: %v", gotBody)
	}
	res, _ := out["result"].(map[string]any)
	sources, _ := res["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sample response not surfaced: %v", out)
	}
	// A non-event routine must not be samplable through the watch surface.
	v.CommunicationURL = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"schedule_id":"rem1","job_kind":"reminder"}]`))
	})).URL
	if _, err := v.Routine(context.Background(), "watch_sample", Input{ID: "rem1"}); err == nil {
		t.Fatal("reminder accepted through watch_sample")
	}
}

// watch_list/pause/delete operate only on event-kind routines: a reminder id
// must not be pausable through the watch surface.
func TestWatchOpsFilterEventKind(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"schedule_id": "mail-watch", "job_kind": "event"},
				{"schedule_id": "standup", "job_kind": "reminder"},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	ctx := context.Background()
	out, err := v.Routine(ctx, "watch_list", Input{})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(out["result"])
	if !strings.Contains(string(result), "mail-watch") || strings.Contains(string(result), "standup") {
		t.Fatalf("watch_list not filtered: %s", result)
	}
	if _, err := v.Routine(ctx, "watch_pause", Input{ID: "standup"}); err == nil {
		t.Fatal("watch_pause accepted a non-event routine")
	}
	if _, err := v.Routine(ctx, "watch_delete", Input{ID: "mail-watch"}); err != nil {
		t.Fatalf("watch_delete on an event routine failed: %v", err)
	}
}

// An agent-kind routine carrying probe-shaped args is the cron-shaped
// misuse watch_subscribe exists to prevent — the tool must refuse it at
// the tool layer with a pointer to watch_subscribe, before the hub sees it.
func TestRoutineCreateRejectsProbeArgsOnAgentKind(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	reached := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	ctx := context.Background()
	// Agent-kind carrying a source probe tool — the exact misuse that burnt
	// LLM wakes polling a source that produces nothing new.
	_, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "poll-mon", Expression: "*/15 * * * *", Text: "", Kind: "agent", Tool: "hub-tg-wait_for_new_message-abc", Args: `{"chat_id":"@x"}`})
	if err == nil || !strings.Contains(err.Error(), "watch_subscribe") {
		t.Fatalf("probe-args agent routine not refused with watch hint: %v", err)
	}
	if reached {
		t.Fatal("misuse request reached the hub")
	}
	// Same shape through args only (no kind passed — agent is the default).
	_, err = v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "poll-mon2", Expression: "*/15 * * * *", Text: "", Args: `{"chat_id":"@x"}`, Tool: "hub-feed-abc"})
	if err == nil || !strings.Contains(err.Error(), "watch_subscribe") {
		t.Fatalf("default-kind probe routine not refused: %v", err)
	}
	// Probe-shaped args on any non-probe kind are equally meaningless — the
	// tool drops them silently, so every shape must be refused with the
	// same pointer, not only the agent default.
	for _, tc := range []Input{
		{ID: "p3", Expression: "*/15 * * * *", Tool: "hub-tg-wait-abc"},
		{ID: "p4", Kind: "reminder", Expression: "0 9 * * *", Tool: "hub-tg-wait-abc"},
		{ID: "p5", Kind: "digest", Expression: "0 9 * * *", Tool: "hub-feed-abc"},
		{ID: "p6", Expression: "*/15 * * * *", Args: `{"chat_id":"@x"}`},
	} {
		tc.DestinationTaskID = "default"
		if _, err := v.Routine(ctx, "routine_create", tc); err == nil || !strings.Contains(err.Error(), "watch_subscribe") {
			t.Fatalf("probe args on kind %q not refused: %v", tc.Kind, err)
		}
	}
	// Watcher kind is the legitimate carrier of probe fields — not blocked.
	reached = false
	if _, err := v.Routine(ctx, "routine_create", Input{DestinationTaskID: "default", ID: "w-legit", Timezone: "UTC", Expression: "0 9 * * *", Text: "page check", Kind: "watcher", ProbeKind: "http", URL: "https://example.com", Regex: "v(\\d+)"}); err != nil {
		t.Fatalf("legit watcher blocked: %v", err)
	}
	if !reached {
		t.Fatal("legit watcher did not reach the hub")
	}
}

// watch_resume re-arms a paused event subscription: it is scoped to
// event-kind routines and posts the resume edge, not a generic update.
func TestWatchResumeRoutesResume(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var lastMethod, lastPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastMethod, lastPath = r.Method, r.URL.Path
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"schedule_id": "mail-watch", "job_kind": "event"},
				{"schedule_id": "standup", "job_kind": "reminder"},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "watch_resume", Input{ID: "standup"}); err == nil {
		t.Fatal("watch_resume accepted a non-event routine")
	}
	if _, err := v.Routine(context.Background(), "watch_resume", Input{ID: "mail-watch"}); err != nil {
		t.Fatalf("watch_resume on an event routine failed: %v", err)
	}
	if lastMethod != http.MethodPost || lastPath != "/v1/routines/mail-watch/resume" {
		t.Fatalf("watch_resume call: %s %s", lastMethod, lastPath)
	}
}

// The call dispatcher routes watch_* names into the routine transport.
func TestWatchToolsDispatch(t *testing.T) {
	v := fixture(t)
	for _, name := range []string{"watch_subscribe", "watch_list", "watch_pause", "watch_resume", "watch_delete", "watch_sample"} {
		_, err := v.call(context.Background(), name, Input{}, nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "routines require HUB_COMMUNICATION_CONTROL_URL") {
			t.Fatalf("%s not dispatched to Routine: %v", name, err)
		}
	}
}

// The MCP server actually publishes the watch tools alongside routines.
func TestWatchToolsPublished(t *testing.T) {
	t.Setenv("HUB_HH_ENABLED", "false")
	client, closeAll := connectHub(t, fixture(t))
	defer closeAll()
	list, err := client.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"watch_subscribe", "watch_list", "watch_pause", "watch_resume", "watch_delete", "watch_sample"} {
		if !names[want] {
			t.Fatalf("%s not published on the MCP server", want)
		}
	}
}

// All four watch tools are declared in the capability catalog.
func TestWatchToolsInCatalog(t *testing.T) {
	names := map[string]bool{}
	for _, spec := range HubToolsDefinition().Tools {
		names[spec.Name] = true
	}
	for _, want := range []string{"watch_subscribe", "watch_list", "watch_pause", "watch_resume", "watch_delete", "watch_sample"} {
		if !names[want] {
			t.Fatalf("%s missing from catalog", want)
		}
	}
}

func TestCredentialFormCallCommunicationHub(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/credential-forms" || r.Header.Get("Authorization") != "Bearer control-token" || r.Header.Get("X-Hub-Principal") != "alice" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || request["service"] != "google" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"input":"protected-form","form_url":"http://127.0.0.1:8081/credentials/id?nonce=nonce","fields":["GOOGLE_OAUTH_CLIENT_SECRET"]}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	v.CommunicationAuth = "control-token"
	form, err := v.credentialForm(context.Background(), "google", []string{"GOOGLE_OAUTH_CLIENT_SECRET"})
	if err != nil || form["form_url"] != "http://127.0.0.1:8081/credentials/id?nonce=nonce" {
		t.Fatalf("form=%v err=%v", form, err)
	}
	if _, err := v.EnvUpdate(Input{Text: "GOOGLE_OAUTH_CLIENT_SECRET=must-not-be-stored"}); err == nil {
		t.Fatal("env_update accepted credentials with protected form configured")
	}
}
