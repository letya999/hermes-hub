package identity

import "testing"

func TestEnvelopeValidation(t *testing.T) {
	valid := TelegramEnvelope("alice", 11, "alice", "policy-1")
	if err := valid.Validate("alice", "alice", "alice", "policy-1"); err != nil {
		t.Fatal(err)
	}
	slack := SlackEnvelope("alice", "T08ARHT9KFH", "U08ARHT9KFH", "alice", "policy-1")
	if err := slack.Validate("alice", "alice", "alice", "policy-1"); err != nil {
		t.Fatal(err)
	}
	if slack.ExternalIdentityID != "slack-t08arht9kfh-u08arht9kfh" || slack.ConversationID != slack.DeliveryTargetID {
		t.Fatalf("slack identity: %+v", slack)
	}
	if SlackIdentity("T1", "U1") == SlackIdentity("T2", "U1") {
		t.Fatal("slack identities collided across workspaces")
	}
	for name, envelope := range map[string]Envelope{
		"malformed": func() Envelope { e := valid; e.ConversationID = "bad:id"; return e }(),
		"principal": func() Envelope { e := valid; e.PrincipalID = "bob"; return e }(),
		"context":   func() Envelope { e := valid; e.ContextID = "other"; return e }(),
		"runtime":   func() Envelope { e := valid; e.RuntimeID = "stale"; return e }(),
		"policy":    func() Envelope { e := valid; e.PolicyVersion = "policy-0"; return e }(),
	} {
		if envelope.Validate("alice", "alice", "alice", "policy-1") == nil {
			t.Fatalf("accepted invalid %s envelope", name)
		}
	}
}
