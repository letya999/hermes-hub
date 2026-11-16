package identity

import (
	"strings"
	"testing"
)

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
		"malformed":        func() Envelope { e := valid; e.ConversationID = "bad:id"; return e }(),
		"principal":        func() Envelope { e := valid; e.PrincipalID = "bob"; return e }(),
		"context":          func() Envelope { e := valid; e.ContextID = "other"; return e }(),
		"runtime":          func() Envelope { e := valid; e.RuntimeID = "stale"; return e }(),
		"policy":           func() Envelope { e := valid; e.PolicyVersion = "policy-0"; return e }(),
		"profile-only":     func() Envelope { e := valid; e.CapabilityProfile = "default"; return e }(),
		"environment-only": func() Envelope { e := valid; e.Environment = "prod"; return e }(),
		"generation-only":  func() Envelope { e := valid; e.Generation = 1; return e }(),
		"bad-environment": func() Envelope {
			e := valid
			e.CapabilityProfile = "default"
			e.Environment = "any"
			e.Generation = 1
			return e
		}(),
		"issuer-only":  func() Envelope { e := valid; e.Issuer = "https://idp.example.com"; return e }(),
		"subject-only": func() Envelope { e := valid; e.Subject = "auth0|123"; return e }(),
		"issuer-oversize": func() Envelope {
			e := valid
			e.Issuer = strings.Repeat("a", 300)
			e.Subject = "s"
			return e
		}(),
	} {
		if envelope.Validate("alice", "alice", "alice", "policy-1") == nil {
			t.Fatalf("accepted invalid %s envelope", name)
		}
	}
	valid.CapabilityProfile, valid.Environment, valid.Generation = "default", "dev", 1
	if err := valid.Validate("alice", "alice", "alice", "policy-1"); err != nil {
		t.Fatal(err)
	}
	// SSO provenance (#53): issuer+subject travel as a pair and are
	// accepted as audit metadata.
	sso := TelegramEnvelope("alice", 11, "alice", "policy-1")
	sso.Issuer, sso.Subject = "https://idp.example.com", "auth0|abc123"
	if err := sso.Validate("alice", "alice", "alice", "policy-1"); err != nil {
		t.Fatalf("sso provenance rejected: %v", err)
	}
}
