package agenttools

import (
	"context"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// SPEC-0093 R2.3/R3.1: every model-audience tool whose executor is the
// in-runtime handler must be reachable through the shared dispatch — a
// cataloged name that falls through to "unknown tool" is a parity break,
// regardless of which family it belongs to. Fail-closed errors (missing
// backend, missing arguments) satisfy the check; only an undispatched name
// fails.
func TestEveryCatalogedHandlerToolDispatches(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_SKILLS_EFFECTIVE", t.TempDir())
	t.Setenv("HUB_SKILLS_STORE", t.TempDir())
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Audience != api.AudienceModel || !strings.HasPrefix(spec.Executor, "handler:") {
			continue
		}
		if spec.Name == "skill_edit" {
			continue // raw-argument executor path; dispatched via ExecCall
		}
		_, err := v.Call(context.Background(), spec.Name, Input{})
		if err != nil && strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("%s cataloged but not dispatched: %v", spec.Name, err)
		}
	}
}
