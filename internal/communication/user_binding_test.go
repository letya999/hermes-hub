package communication

import (
	"context"
	"testing"
)

func TestGatewayUsersDoNotInheritAnotherRuntimeIdentity(t *testing.T) {
	t.Setenv("HUB_RUNTIME_ID", "alice")
	t.Setenv("HUB_POLICY_VERSION", "global-policy")
	c := testConfig(t)
	c.RuntimeURL, c.RuntimeAuth = "http://localhost:1", "synthetic"
	c.Users[0].RuntimeID, c.Users[0].PolicyVersion = "alice-runtime", "alice-policy"
	c.Users = append(c.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, RuntimeID: "bob-runtime", PolicyVersion: "bob-policy"})
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	for i, chatID := range []int64{11, 22} {
		update := Update{UpdateID: 81000 + i, Message: &Message{MessageID: i + 1, From: &TGUser{ID: chatID}, Chat: TGChat{ID: chatID, Type: "private"}, Text: "task"}}
		if err := g.handleUpdate(context.Background(), update); err != nil {
			t.Fatal(err)
		}
		job, err := g.spool.ClaimJob()
		if err != nil || job == nil {
			t.Fatal(err)
		}
		user := c.Users[i]
		if job.UserID != user.ID || job.PrincipalID != user.ID || job.RuntimeID != user.RuntimeID || job.PolicyVersion != user.PolicyVersion {
			t.Fatalf("wrong user envelope: %+v", job.Envelope)
		}
		if err := g.spool.CompleteJob(job.ID); err != nil {
			t.Fatal(err)
		}
	}
	if envelope := (User{ID: "bob"}).envelope(22); envelope.RuntimeID != "bob" {
		t.Fatal("global runtime redirected Bob")
	}
}
