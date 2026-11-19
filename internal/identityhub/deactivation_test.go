package identityhub

import (
	"context"
	"errors"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestDeletingHumanRetiresProfilesAndChannelProofs(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}, commodel.User{ID: "admin", Enabled: true})
	ctx := context.Background()
	if err := r.BootstrapPlatformAdmin(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	agent, err := r.PutAgentProfile(ctx, "alice", "research", contract.Restrictions{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := r.BeginLink(ctx, "alice", contract.ChannelIdentity{Channel: contract.Telegram, ID: "8"}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DeactivateHuman(ctx, "alice", "admin"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("foreign human deleted: %v", err)
	}
	if err := r.DeactivateHuman(ctx, "alice", "alice"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alice", agent.ID, "agent:alice:default"} {
		if _, err := r.Principal(ctx, id); !errors.Is(err, contract.ErrNotFound) {
			t.Fatalf("deleted principal %s: %v", id, err)
		}
	}
	if _, err := r.Resolve(ctx, contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("deleted channel: %v", err)
	}
	if err := r.VerifyLink(ctx, pending.ID, pending.Proof, contract.ChannelIdentity{Channel: contract.Telegram, ID: "8"}); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("deleted owner proof reused: %v", err)
	}
	if err := r.CanAct(ctx, "alice", agent.ID, "alice"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("deleted agent still acts: %v", err)
	}
	if _, err := r.PutAgentProfile(ctx, "alice", "research", contract.Restrictions{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("deleted owner resurrected an agent: %v", err)
	}
	if err := r.DeactivateHuman(ctx, "admin", "alice"); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("inactive principal transition: %v", err)
	}
	var profiles int
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM profiles WHERE id LIKE 'agent:alice:%'").Scan(&profiles); err != nil || profiles != 0 {
		t.Fatalf("owned profiles survived: count=%d err=%v", profiles, err)
	}
}
