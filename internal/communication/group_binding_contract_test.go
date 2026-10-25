package communication

import (
	"context"
	"testing"
)

func TestQueuedGroupBindingMustStillMatchItsAuthority(t *testing.T) {
	g, err := New(groupConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "@helper test"}}); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatal(err)
	}
	base := mappingFromJob(*job, g.now())
	if err := g.authorizeGroupMapping(base); err != nil {
		t.Fatal("current group denied", err)
	}
	for _, field := range []string{"actor", "scope", "context", "audience", "runtime", "policy", "principal"} {
		t.Run(field, func(t *testing.T) {
			m := base
			switch field {
			case "actor":
				m.ExternalIdentityID = "invalid"
			case "scope":
				m.ScopeID = "group:bob"
			case "context":
				m.ContextID = "bob"
			case "audience":
				m.DeliveryTargetID = "telegram-11"
			case "runtime":
				m.RuntimeID = "bob"
			case "policy":
				m.PolicyVersion = "changed"
			case "principal":
				m.PrincipalID = "bob"
			}
			if g.authorizeGroupMapping(m) == nil {
				t.Error("queued group authority mismatch accepted")
			}
		})
	}
}
