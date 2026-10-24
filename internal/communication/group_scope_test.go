package communication

import (
	"context"
	"testing"
)

func TestUnconfiguredGroupCannotUsePersonalRuntime(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	message := Update{UpdateID: 9001, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "read my private memory"}}
	if err := g.handleUpdate(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatalf("group admitted personal authority: principal=%s context=%s scope=%s", job.PrincipalID, job.ContextID, job.ScopeID)
	}
}
