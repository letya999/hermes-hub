package routines

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestHookEventsPreserveIDsAndIgnoreIdlePayloads(t *testing.T) {
	probe := &commodel.WatchProbe{Kind: "hook"}
	for _, body := range []string{`{"events":[]}`, `{"event":false}`} {
		events, err := ExtractEvents(probe, []byte(body))
		if err != nil || len(events) != 0 {
			t.Fatalf("idle hook produced an event: count=%d err=%v", len(events), err)
		}
	}
	events, err := ExtractEvents(probe, []byte(`{"events":[{"id":"source-1","text":"one"},{"id":"source-2","text":"two"}]}`))
	if err != nil || len(events) != 2 || events[0].ID != "source-1" || events[1].ID != "source-2" || events[0].Text != "one" {
		t.Fatalf("hook lost source event identities: count=%d err=%v", len(events), err)
	}
}
