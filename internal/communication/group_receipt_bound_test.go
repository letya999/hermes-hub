package communication

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGroupReplayReceiptsAreBoundedAndDurable(t *testing.T) {
	g, err := New(groupConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	for id := 1; id <= 300; id++ {
		if err := g.handleUpdate(context.Background(), Update{UpdateID: id, Message: &Message{MessageID: id, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "passive"}}); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join(g.spool.root, "groups", "-100123.json"))
	if err != nil {
		t.Fatal(err)
	}
	var history struct{ Seen []int }
	if err := json.Unmarshal(body, &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Seen) != 256 || history.Seen[0] != 45 || history.Seen[len(history.Seen)-1] != 300 {
		t.Errorf("recent source-event receipts not bounded/durable: %v", history.Seen)
	}
}
