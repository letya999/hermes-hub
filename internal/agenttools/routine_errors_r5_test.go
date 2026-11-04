package agenttools

import (
	"context"
	"strings"
	"testing"
)

// Model-facing validation errors must teach r5 names: a missing
// expected_revision points at routine_read (never routine_get), and
// probe-shaped routine_create args point at watch_create (never
// watch_subscribe). Compat dispatch keeps r4 op names internally — user-
// visible text does not.
func TestRoutineErrorsTeachR5Names(t *testing.T) {
	v := fixture(t)
	v.CommunicationURL, v.CommunicationAuth = "http://127.0.0.1:1", "token"
	if _, err := v.Routine(context.Background(), "routine_update", Input{ID: "item"}); err == nil ||
		!strings.Contains(err.Error(), "routine_read") || strings.Contains(err.Error(), "routine_get") {
		t.Fatalf("missing expected_revision must point at routine_read: %v", err)
	}
	if _, err := v.Routine(context.Background(), "routine_create", Input{ID: "item", Timezone: "UTC", Expression: "every:15m", Text: "x", Kind: "agent", Tool: "inbox_fetch", OriginJobID: "run-a"}); err == nil ||
		!strings.Contains(err.Error(), "watch_create") || strings.Contains(err.Error(), "watch_subscribe") {
		t.Fatalf("probe-shaped create args must point at watch_create: %v", err)
	}
}
