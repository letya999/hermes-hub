package communication

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGroupUpdateReplayPreservesOriginalAdmission(t *testing.T) {
	c := groupConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	send := func(id int, text string) {
		t.Helper()
		if err := g.handleUpdate(context.Background(), Update{UpdateID: id, Message: &Message{MessageID: id, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: text}}); err != nil {
			t.Error(err)
		}
	}
	send(1, "@helper first")
	send(2, "later passive")
	send(1, "@helper first")
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("job: %v %v", job, err)
	}
	if extra, err := g.spool.ClaimJob(); err != nil || extra != nil {
		t.Errorf("duplicate job: %v %v", extra, err)
	}
	file := filepath.Join(g.spool.root, "groups", "-100123.json")
	before, _ := os.ReadFile(file)
	send(2, "later passive")
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Error("passive replay changed transcript")
	}
}
