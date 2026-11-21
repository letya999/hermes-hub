package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWebCallsUseBoundedUpstreamSidecar(t *testing.T) {
	old := webRunner
	t.Cleanup(func() { webRunner = old })
	var calls []map[string]any
	webRunner = func(_ context.Context, body []byte) ([]byte, error) {
		var call map[string]any
		if err := json.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
		return []byte(`{"ok":true,"result":{"success":true,"data":{"web":[{"title":"fixture"}]}}}`), nil
	}
	tools := &Tools{}
	if _, err := tools.webCall(t.Context(), "web_search", Input{Query: "fixture", Limit: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.webCall(t.Context(), "web_fetch", Input{URL: "https://example.org/article"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.webCall(t.Context(), "web_providers", Input{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.webCall(t.Context(), "web_search_multi", Input{Query: "fixture", Providers: []string{"all"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.webCall(t.Context(), "web_cache", Input{Action: "status"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 5 || calls[0]["op"] != "web_search" || calls[0]["limit"] != float64(3) || calls[1]["op"] != "web_fetch" || calls[2]["op"] != "web_providers" || calls[3]["op"] != "web_search_multi" || calls[4]["action"] != "status" {
		t.Fatalf("wrong upstream calls: %v", calls)
	}
	for _, input := range []Input{{URL: "http://127.0.0.1/secret"}, {URL: "https://user:pass@example.org/"}, {URL: "https://example.org/#fragment"}} {
		if _, err := tools.webCall(t.Context(), "web_fetch", input); err == nil {
			t.Fatal("unsafe URL reached upstream")
		}
	}
	if _, err := tools.webCall(t.Context(), "web_search", Input{Query: strings.Repeat("x", 1001)}); err == nil || len(calls) != 5 {
		t.Fatal("invalid query reached upstream")
	}
	if _, err := tools.webCall(t.Context(), "web_search_multi", Input{Query: "x", Providers: []string{"bad/name"}}); err == nil || len(calls) != 5 {
		t.Fatal("invalid provider reached upstream")
	}
	if _, err := tools.webCall(t.Context(), "web_cache", Input{Action: "delete_all"}); err == nil || len(calls) != 5 {
		t.Fatal("invalid cache action reached upstream")
	}
	webRunner = func(context.Context, []byte) ([]byte, error) { return nil, errors.New("secret process failure") }
	if _, err := tools.webCall(t.Context(), "web_search", Input{Query: "x"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("runner error leaked")
	}
	webRunner = func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"ok":true,"result":{"success":false,"error":"private upstream detail"}}`), nil
	}
	if _, err := tools.webCall(t.Context(), "web_search", Input{Query: "x"}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("provider failure must surface as a safe MCP error")
	}
}
