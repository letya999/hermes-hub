package communication

import (
	"context"
	"testing"
)

func TestGroupApprovalIsBoundToMemberAndAudience(t *testing.T) {
	g, err := New(groupConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "@helper act"}}); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatal(err)
	}
	event := streamEvent(*job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:one", "one"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := g.spool.RecordStreamEvent(*job, event); err != nil {
		t.Fatal(err)
	}
	delivery, err := g.spool.ClaimDelivery()
	if err != nil || delivery == nil || len(delivery.Buttons) == 0 {
		t.Fatalf("keyboard missing: %v %v", delivery, err)
	}
	tap := func(sender, chat int64) {
		t.Helper()
		if err := g.handleCallback(context.Background(), &CallbackQuery{ID: "cb", From: &TGUser{ID: sender}, Message: &Message{Chat: TGChat{ID: chat}}, Data: delivery.Buttons[0].Data}); err != nil {
			t.Fatal(err)
		}
	}
	tap(99, -100123)
	tap(11, -100999)
	tap(11, 11)
	mapping, _, _ := g.spool.Mapping(job.ID)
	if mapping.Control != nil {
		t.Fatal("foreign member/audience approved")
	}
	tap(11, -100123)
	mapping, _, _ = g.spool.Mapping(job.ID)
	if mapping.Control == nil || mapping.Control.Choice != "once" {
		t.Fatal("authorized group approval not accepted")
	}
}
