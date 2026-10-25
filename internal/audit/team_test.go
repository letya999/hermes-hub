package audit

import (
	"path/filepath"
	"testing"
)

func TestTeamAttributionRejectsInvalidMetadata(t *testing.T) {
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"sender", "team", "binding"} {
		for _, value := range []string{"bad\nvalue", "bad\rvalue", "bad\x00value"} {
			event := NewEvent("tool-call", "alice", "allow")
			switch field {
			case "sender":
				event.SenderID = value
			case "team":
				event.TeamID = value
			case "binding":
				event.BindingID = value
			}
			if err := ledger.Append(event); err == nil {
				t.Errorf("accepted invalid %s attribution", field)
			}
		}
	}
}
