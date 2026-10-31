package runtime

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/locale"
)

// toolProgressText renders run progress in the workspace language resolved
// from HUB_WORKSPACE preferences.
func TestToolProgressTextLocalized(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event nativeRunEvent
		key   string
	}{
		{"tool-error", nativeRunEvent{Event: "tool.end", Tool: "any", Error: true}, "progress.tool-error"},
		{"step1-accepted", nativeRunEvent{Event: "tool.end", Tool: "mcp__hub__toolhub_prepare_source", Output: `{"phase":"preparing"}`}, "mcp.step1-accepted"},
		{"step1-failed", nativeRunEvent{Event: "tool.end", Tool: "mcp__hub__toolhub_prepare_source", Output: `{"phase":"failed"}`}, "mcp.step1-failed"},
		{"step1-awaiting", nativeRunEvent{Event: "tool.end", Tool: "mcp__hub__toolhub_prepare_source", Output: `{"phase":"awaiting-credentials"}`}, "mcp.step1-awaiting"},
		{"step1-answered", nativeRunEvent{Event: "tool.end", Tool: "mcp__hub__toolhub_prepare_source"}, "mcp.step1-answered"},
		{"step1-started", nativeRunEvent{Event: "tool.start", Tool: "mcp__hub__toolhub_prepare_source"}, "mcp.step1-started"},
		{"step2-done", nativeRunEvent{Event: "tool.end", Tool: "mcp__hub__toolhub_required_credentials"}, "mcp.step2-done"},
		{"step2-started", nativeRunEvent{Event: "tool.start", Tool: "mcp__hub__toolhub_required_credentials"}, "mcp.step2-started"},
		{"step3-done", nativeRunEvent{Event: "tool.end", Tool: "mcp__hub__toolhub_confirm"}, "mcp.step3-done"},
		{"step3-started", nativeRunEvent{Event: "tool.start", Tool: "mcp__hub__toolhub_confirm"}, "mcp.step3-started"},
		{"step4-done", nativeRunEvent{Event: "tool.end", Tool: "mcp__hub__toolhub_enable"}, "mcp.step4-done"},
		{"step4-started", nativeRunEvent{Event: "tool.start", Tool: "mcp__hub__toolhub_enable"}, "mcp.step4-started"},
		{"invoke", nativeRunEvent{Event: "tool.start", Tool: "mcp__hub__toolhub_invoke"}, "mcp.invoke-check"},
		{"done", nativeRunEvent{Event: "tool.end", Tool: "file_list"}, "progress.tool-done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := locale.Lookup(locale.LangES, tc.key); !ok {
				t.Fatalf("catalog missing es %q", tc.key)
			}
			if got := toolProgressText(tc.event, locale.LangES); got != locale.T(locale.LangES, tc.key) {
				t.Fatalf("got %q", got)
			}
		})
	}
}
