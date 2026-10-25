package agenttools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// goal_* tools ride POST /v1/goals on the communication hub: the principal
// comes from the runtime env (never from tool args), the conversation binds
// the call to the run's own task (SPEC-0074 R9).
func TestGoalToolPostsControlRequest(t *testing.T) {
	var gotPath, gotPrincipal, gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotPrincipal = r.Header.Get("X-Hub-Principal")
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "hub.goal", "output": "ok",
			"goal": map[string]any{"found": true, "status": "active", "text": "ship it", "engine": "hub", "turns_used": 3, "max_turns": 20},
		})
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL, v.CommunicationAuth = server.URL, "token"
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONVERSATION_ID", "telegram-11")
	out, err := v.Goal(context.Background(), "goal_set", Input{Text: "ship it"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/goals" || gotPrincipal != "alice" || gotAuth != "Bearer token" {
		t.Fatalf("request: %s principal=%s auth=%s", gotPath, gotPrincipal, gotAuth)
	}
	if gotBody["op"] != "set" || gotBody["text"] != "ship it" || gotBody["conversation_id"] != "telegram-11" {
		t.Fatalf("body: %v", gotBody)
	}
	goal, _ := out["goal"].(map[string]any)
	if goal["status"] != "active" || goal["turns_used"] != float64(3) {
		t.Fatalf("result: %v", out)
	}
}

// Unresolved control config must refuse before any HTTP call.
func TestGoalToolRejectsUnresolvedConfiguration(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL, v.CommunicationAuth = server.URL, "token"
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONVERSATION_ID", "telegram-11")
	v.CommunicationURL = "${HUB_COMMUNICATION_CONTROL_URL}"
	if _, err := v.Goal(context.Background(), "goal_status", Input{}); err == nil || !strings.Contains(err.Error(), "require HUB_COMMUNICATION_CONTROL_URL") || calls != 0 {
		t.Fatalf("unresolved url must refuse: %v calls=%d", err, calls)
	}
	v.CommunicationURL = server.URL
	v.CommunicationAuth = "${HUB_RUNTIME_AUTH}"
	if _, err := v.Goal(context.Background(), "goal_status", Input{}); err == nil || !strings.Contains(err.Error(), "authorization is unavailable") || calls != 0 {
		t.Fatalf("unresolved auth must refuse: %v calls=%d", err, calls)
	}
}

// A non-2xx hub reply surfaces a bounded refusal, never a panic or raw body.
func TestGoalToolSurfacesHubRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conversation does not belong to the caller", http.StatusNotFound)
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL, v.CommunicationAuth = server.URL, "token"
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONVERSATION_ID", "telegram-999")
	_, err := v.Goal(context.Background(), "goal_status", Input{})
	if err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "belong") {
		t.Fatalf("want bounded refusal naming the status: %v", err)
	}
}

// Every spec-named goal verb reaches the handler through the shared dispatch
// and carries a catalog spec + MCP entry — an unregistered name is a dead tool.
func TestGoalToolsDispatchAndCatalog(t *testing.T) {
	var lastOp string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		lastOp, _ = body["op"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"hub.goal","goal":{"status":"active"}}`))
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL, v.CommunicationAuth = server.URL, "token"
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONVERSATION_ID", "telegram-11")
	for _, name := range []string{"goal_status", "goal_pause", "goal_resume", "goal_clear"} {
		if _, err := v.call(context.Background(), name, Input{}, nil, nil, nil); err != nil {
			t.Fatalf("%s dispatch: %v", name, err)
		}
		if want := strings.TrimPrefix(name, "goal_"); lastOp != want {
			t.Fatalf("%s posted op %q", name, lastOp)
		}
	}
	for _, name := range []string{"goal_set", "goal_status", "goal_pause", "goal_clear", "goal_resume"} {
		found := false
		for _, spec := range HubToolsDefinition().Tools {
			if spec.Name == name {
				found = true
				if spec.CapabilityID == "" {
					t.Fatalf("%s missing capability id", name)
				}
			}
		}
		if !found {
			t.Fatalf("%s absent from the tool catalog", name)
		}
	}
	client, closeConn := connectHub(t, fixture(t))
	defer closeConn()
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"goal_set", "goal_status", "goal_pause", "goal_clear", "goal_resume"} {
		exposed := false
		for _, tool := range list.Tools {
			if tool.Name == name {
				exposed = true
			}
		}
		if !exposed {
			t.Fatalf("%s absent from the MCP surface", name)
		}
	}
}
