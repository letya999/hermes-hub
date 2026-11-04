package agenttools

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// M31.11 S2: every model-facing member carries a real right — a cataloged
// tool without `requires` would admit by default, and `tools.select` is the
// r4 admission crutch r5 retired.
func TestEveryModelToolRequiresRight(t *testing.T) {
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Audience != api.AudienceModel {
			continue
		}
		if len(spec.Requires) == 0 {
			t.Errorf("%s: no requires — would admit by default", spec.Name)
			continue
		}
		for _, right := range spec.Requires {
			// tools.select/tools.list are real rights in the frozen
			// vocabulary, but they honestly describe only the platform
			// surface pair — any other member claiming them is the r4
			// admission crutch resurfacing.
			if right == "tools.select" && spec.Name != "tool_call" {
				t.Errorf("%s: falls back to tools.select — denied by design outside tool_call", spec.Name)
			}
			if right == "tools.list" && spec.Name != "tool_search" {
				t.Errorf("%s: falls back to tools.list — denied by design outside tool_search", spec.Name)
			}
		}
	}
}
