package communication

import (
	"context"
	"testing"
)

func TestGroupAcceptsUnseenUpdateWithoutNumericOrdering(t *testing.T) {
	g, err := New(groupConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	for _, id := range []int{100, 10} {
		text := "passive"
		if id == 10 {
			text = "@helper unseen request"
		}
		if err := g.handleUpdate(context.Background(), Update{UpdateID: id, Message: &Message{MessageID: id, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: text}}); err != nil {
			t.Fatal(err)
		}
	}
	if job, err := g.spool.ClaimJob(); err != nil || job == nil {
		t.Fatalf("unseen update discarded by high-water marker: %v %v", job, err)
	}
}
