package compaction

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The default summarizer posts a bounded continuity prompt to the managed
// model relay and returns the assistant content — the same OpenAI surface
// the agent itself uses.
func TestRelaySummarizePostsBoundedPrompt(t *testing.T) {
	var payload map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "dense summary"}}},
		})
	}))
	defer srv.Close()
	t.Setenv("HUB_MANAGED_MODEL_URL", srv.URL+"/v1")
	t.Setenv("OPENAI_API_KEY", "relay-key")

	summary, err := RelaySummarize(context.Background(), "m1", []map[string]any{
		{"role": "user", "content": "task A"},
		{"role": "assistant", "content": "done A"},
	})
	if err != nil || summary != "dense summary" {
		t.Fatalf("summarize: %q %v", summary, err)
	}
	if payload["model"] != "m1" {
		t.Fatalf("model: %v", payload["model"])
	}
	if auth != "Bearer relay-key" {
		t.Fatalf("relay bearer missing: %q", auth)
	}
	msgs, _ := payload["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("prompt shape: %v", msgs)
	}
	user, _ := msgs[1].(map[string]any)
	content, _ := user["content"].(string)
	if !strings.Contains(content, "task A") || !strings.Contains(content, "done A") {
		t.Fatalf("transcript missing from prompt: %q", content)
	}
}

// A focus hint steers the summary prompt — the caller's exact words reach
// the relay as a literal instruction line, never as prompt structure.
func TestRelaySummarizeCarriesFocus(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "s"}}},
		})
	}))
	defer srv.Close()
	t.Setenv("HUB_MANAGED_MODEL_URL", srv.URL+"/v1")
	if _, err := RelaySummarizeFocus(context.Background(), "m1", []map[string]any{
		{"role": "user", "content": "task A"},
	}, "the deploy steps"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := payload["messages"].([]any)
	system, _ := msgs[0].(map[string]any)
	content, _ := system["content"].(string)
	if !strings.Contains(content, "the deploy steps") {
		t.Fatalf("focus missing from system prompt: %q", content)
	}
}

// A relay failure surfaces a bounded error — the provider's detail string
// (credentials hints, endpoints, trace ids) never crosses the boundary.
func TestRelaySummarizeErrorIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"provider exploded with secret detail"}}`))
	}))
	defer srv.Close()
	t.Setenv("HUB_MANAGED_MODEL_URL", srv.URL+"/v1")
	_, err := RelaySummarize(context.Background(), "m1", nil)
	if err == nil || strings.Contains(err.Error(), "secret detail") {
		t.Fatalf("error leaked upstream detail: %v", err)
	}
}
