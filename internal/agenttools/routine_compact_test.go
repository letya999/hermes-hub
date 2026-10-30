package agenttools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestRoutinePublicSurfaceHasFiveTools(t *testing.T) {
	want := []string{"routine_create", "routine_delete", "routine_edit", "routine_get", "routine_list"}
	v := fixture(t)
	client, closeAll := connectHub(t, v)
	defer closeAll()
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var mcpNames, catalogNames []string
	for _, tool := range listed.Tools {
		if strings.HasPrefix(tool.Name, "routine_") {
			mcpNames = append(mcpNames, tool.Name)
		}
	}
	definition := HubToolsDefinition()
	if definition.Version != "5.9.0" {
		t.Errorf("new immutable contract version=%s", definition.Version)
	}
	arguments := map[string][]string{"routine_create": {"origin_job_id", "destination_task_id"}, "routine_list": {"query", "view"}, "routine_get": {"include", "history_limit", "history_cursor", "preview_limit"}, "routine_edit": {"action", "expected_revision", "patch", "until", "origin_job_id"}}
	for _, tool := range definition.Tools {
		if strings.HasPrefix(tool.Name, "routine_") {
			catalogNames = append(catalogNames, tool.Name)
		}
		for _, name := range arguments[tool.Name] {
			found := false
			for _, arg := range tool.Arguments {
				if arg.Name == name {
					found = true
				}
			}
			if !found {
				t.Errorf("%s missing %s", tool.Name, name)
			}
		}
	}
	slices.Sort(mcpNames)
	slices.Sort(catalogNames)
	if !slices.Equal(mcpNames, want) || !slices.Equal(catalogNames, want) {
		t.Fatalf("public routines MCP=%v catalog=%v want=%v", mcpNames, catalogNames, want)
	}
}

func TestRoutineEditDispatchesExplicitActions(t *testing.T) {
	for _, tc := range []struct{ action, path, extra string }{
		{"update", "/v1/routines/item", `,"patch":{"input":"changed"}`},
		{"pause", "/v1/routines/item/pause", ""},
		{"resume", "/v1/routines/item/resume", ""},
		{"snooze", "/v1/routines/item/snooze", `,"until":"2099-01-01T10:00:00Z"`},
	} {
		t.Run(tc.action, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.Method != "POST" || r.URL.Path != tc.path || body["expected_revision"] != float64(4) {
					t.Errorf("wrong edit: %s %s %+v", r.Method, r.URL.Path, body)
				}
				if tc.action == "update" && body["input"] != "changed" {
					t.Error("patch lost")
				}
				if tc.action == "snooze" && body["until"] != "2099-01-01T10:00:00Z" {
					t.Error("until lost")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"revision": 5})
			}))
			defer server.Close()
			v := fixture(t)
			v.CommunicationURL = server.URL
			var in Input
			if err := json.Unmarshal([]byte(`{"id":"item","expected_revision":4,"action":"`+tc.action+`"`+tc.extra+`}`), &in); err != nil {
				t.Fatal(err)
			}
			out, err := v.Call(context.Background(), "routine_edit", in)
			if err != nil || calls != 1 || out.(map[string]any)["result"].(map[string]any)["revision"] != float64(5) {
				t.Fatalf("edit=%v err=%v calls=%d", out, err, calls)
			}
		})
	}
}

func TestRoutineEditRejectsInvalidArgumentsBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _ = json.NewEncoder(w).Encode(map[string]any{}) }))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	for _, fields := range []string{
		`"action":"bogus"`, `"action":""`, `"action":"update"`, `"action":"update","patch":{}`,
		`"action":"update","patch":{"input":"x"},"until":"2099-01-01T00:00:00Z"`,
		`"action":"pause","patch":{}`, `"action":"resume","until":"2099-01-01T00:00:00Z"`,
		`"action":"snooze"`, `"action":"snooze","until":"2099-01-01T00:00:00Z","patch":{}`,
	} {
		var in Input
		if err := json.Unmarshal([]byte(`{"id":"item","expected_revision":4,`+fields+`}`), &in); err != nil {
			t.Fatal(err)
		}
		_, err := v.Call(context.Background(), "routine_edit", in)
		if err == nil || !strings.Contains(err.Error(), "routine_edit:") || calls != 0 {
			t.Errorf("invalid edit %s: err=%v calls=%d", fields, err, calls)
		}
	}
}

func TestRoutineGetIncludesBoundedHistoryAndPreview(t *testing.T) {
	var paths []string
	historyLimit, historyCursor, previewLimit := "7", "run-a", "3"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/routines/item":
			_, _ = w.Write([]byte(`{"state":"paused","schedule":{"revision":4}}`))
		case "/v1/routines/item/history":
			if r.URL.Query().Get("limit") != historyLimit || r.URL.Query().Get("after") != historyCursor {
				t.Errorf("history options=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"items":[{"occurrence_id":"run-b","delivery_state":"delivered"}],"next_cursor":"run-b"}`))
		case "/v1/routines/item/preview":
			if r.URL.Query().Get("limit") != previewLimit {
				t.Errorf("preview options=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"state":"paused","timezone":"UTC","times":["2099-01-01T10:00:00Z"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	var in Input
	if err := json.Unmarshal([]byte(`{"id":"item","include":["history","next_runs"],"history_limit":7,"history_cursor":"run-a","preview_limit":3}`), &in); err != nil {
		t.Fatal(err)
	}
	out, err := v.Routine(context.Background(), "routine_get", in)
	if err != nil {
		t.Fatal(err)
	}
	card := out["result"].(map[string]any)
	if card["history"] == nil || card["next_runs"] == nil || card["state"] != "paused" || len(paths) != 3 {
		t.Fatalf("incomplete card=%v paths=%v", card, paths)
	}
	history := card["history"].(map[string]any)
	if history["next_cursor"] != "run-b" || history["items"].([]any)[0].(map[string]any)["delivery_state"] != "delivered" {
		t.Fatal("history lost", history)
	}
	paths = nil
	if _, err := v.Routine(context.Background(), "routine_get", Input{ID: "item"}); err != nil || len(paths) != 1 {
		t.Fatalf("default get fetched extras: %v %v", paths, err)
	}
	historyLimit, historyCursor, previewLimit = "10", "", "5"
	if err := json.Unmarshal([]byte(`{"id":"item","include":["history","next_runs"],"history_limit":0,"history_cursor":"","preview_limit":0}`), &in); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(context.Background(), "routine_get", in); err != nil {
		t.Fatal(err)
	}
}

func TestRoutineGetRejectsInvalidReadOptionsBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{}`)) }))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	for _, fields := range []string{`"include":["logs"]`, `"include":["history","history"]`, `"include":["history","next_runs","history"]`, `"history_limit":-1`, `"history_limit":101`, `"preview_limit":-1`, `"preview_limit":21`} {
		var in Input
		if err := json.Unmarshal([]byte(`{"id":"item",`+fields+`}`), &in); err != nil {
			t.Fatal(err)
		}
		_, err := v.Routine(context.Background(), "routine_get", in)
		if err == nil || calls != 0 {
			t.Errorf("invalid read %s err=%v calls=%d", fields, err, calls)
		}
	}
}

func TestRoutineGetDoesNotHideFailedComponents(t *testing.T) {
	for _, failed := range []string{"card-shape", "history", "preview"} {
		t.Run(failed, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/"+failed) {
					http.Error(w, "read unavailable", http.StatusServiceUnavailable)
					return
				}
				if failed == "card-shape" {
					_, _ = w.Write([]byte(`[]`))
					return
				}
				_, _ = w.Write([]byte(`{"state":"active"}`))
			}))
			defer server.Close()
			v := fixture(t)
			v.CommunicationURL = server.URL
			var in Input
			if err := json.Unmarshal([]byte(`{"id":"item","include":["history","next_runs"]}`), &in); err != nil {
				t.Fatal(err)
			}
			out, err := v.Routine(context.Background(), "routine_get", in)
			if err == nil || out != nil {
				t.Fatalf("incomplete get reported success: %v %v", out, err)
			}
		})
	}
}

func TestRoutineListTransportsSearchQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != "Пятница & 10:00" || r.URL.Query().Get("after") != "item-a" || r.URL.Query().Get("limit") != "2" {
			t.Errorf("lost search options: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL = server.URL
	if _, err := v.Routine(context.Background(), "routine_list", Input{Query: "Пятница & 10:00", Limit: 2, Cursor: "item-a"}); err != nil {
		t.Fatal(err)
	}
}

// TestAssignmentCatalogVersion: assignment tools joined the immutable
// catalog — the contract revision must advance so protected stores can
// admit the new selection set (SPEC-0080 R2.3).
func TestAssignmentCatalogVersion(t *testing.T) {
	definition := HubToolsDefinition()
	if definition.Version != "5.9.0" {
		t.Fatalf("catalog version = %s, want 5.9.0 with install_local skill import", definition.Version)
	}
}
