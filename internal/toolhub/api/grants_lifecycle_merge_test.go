package api

import (
	"errors"
	"testing"
	"time"
)

// Grant lifecycle: pending registration requires a confirmation digest,
// request rotates a nonce, acknowledge binds it, revoke cuts immediately.
func TestToolGrantLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	g := &Governance{}
	grant := NewToolGrant("grant-1", "alice", "alpha", "owner asked", "owner", now.Add(time.Hour), map[string]string{"tool": "send"})

	if err := g.PutToolGrant(grant, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unconfirmed grant must refuse: %v", err)
	}
	if err := Confirm(&grant, "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := g.PutToolGrant(grant, now); err != nil {
		t.Fatalf("confirmed pending grant: %v", err)
	}
	stale := grant
	stale.Revision = grant.Revision // same revision must not re-register
	if err := g.PutToolGrant(stale, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	revoked := grant
	revoked.Revision++
	revoked.Status = GrantRevoked
	revoked.Confirmation = nil
	if err := g.PutToolGrant(revoked, now); err != nil {
		t.Fatalf("revoked re-registration: %v", err)
	}
	if grants := g.PendingToolGrants("alice", "", now); len(grants) != 0 {
		t.Fatalf("revoked grant must not list: %v", grants)
	}

	pending := NewToolGrant("grant-2", "alice", "beta", "reason", "owner", now.Add(time.Hour), nil)
	if err := Confirm(&pending, "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := g.PutToolGrant(pending, now); err != nil {
		t.Fatal(err)
	}
	if grants := g.PendingToolGrants("alice", "beta", now); len(grants) != 1 {
		t.Fatalf("pending list: %v", grants)
	}
	requested, err := g.RequestToolGrant("alice", "grant-2", now)
	if err != nil || requested.RequestNonce == "" {
		t.Fatalf("request: %v", err)
	}
	if _, err := g.AcknowledgeToolGrant("alice", "grant-2", "wrong", now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad nonce: %v", err)
	}
	acked, err := g.AcknowledgeToolGrant("alice", "grant-2", requested.RequestNonce, now)
	if err != nil || acked.Status != GrantActive || !acked.active(now) {
		t.Fatalf("ack: %v %+v", err, acked)
	}
	if !acked.matches("alice", "beta", nil) || acked.matches("alice", "alpha", nil) || acked.matches("bob", "beta", nil) {
		t.Fatal("matches must bind principal and section")
	}
	if _, err := g.RequestToolGrant("alice", "grant-2", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("active grant cannot re-request: %v", err)
	}
	if err := g.RevokeToolGrant("grant-2", "model", "x", now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("model cannot revoke: %v", err)
	}
	if err := g.RevokeToolGrant("grant-2", "owner", "done", now); err != nil {
		t.Fatal(err)
	}
	if g.Grants[1].Status != GrantRevoked {
		t.Fatal("revoke must flip status")
	}
	if err := g.RevokeToolGrant("missing", "owner", "x", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing grant: %v", err)
	}
}

// Validate enforces identity, expiry, reason, status and match-pair shape.
func TestToolGrantValidateContract(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	base := func() ToolGrant {
		return NewToolGrant("g1", "alice", "alpha", "why", "owner", now.Add(time.Hour), nil)
	}
	bad := []func(*ToolGrant){
		func(g *ToolGrant) { g.Schema = 0 },
		func(g *ToolGrant) { g.GrantID = "!!" },
		func(g *ToolGrant) { g.Section = "!!" },
		func(g *ToolGrant) { g.GrantedBy = "model" },
		func(g *ToolGrant) { g.ExpiresAt = time.Time{} },
		func(g *ToolGrant) { g.Reason = "" },
		func(g *ToolGrant) { g.Status = "weird" },
		func(g *ToolGrant) { g.Revision = 0 },
		func(g *ToolGrant) { g.Match = map[string]string{"ok": "a\nb"} },
		func(g *ToolGrant) {
			g.Match = map[string]string{}
			for i := 0; i < 9; i++ {
				g.Match[string(rune('a'+i))] = "v"
			}
		},
	}
	for i, mutate := range bad {
		grant := base()
		mutate(&grant)
		if err := grant.Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}
}
