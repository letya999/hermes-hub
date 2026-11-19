package identityhub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
)

func fixture(t *testing.T, users ...commodel.User) (*Registry, []Mapping) {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatalf("open fixture registry: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	mappings, err := r.ImportLegacy(context.Background(), "fixture", "personal", users)
	if err != nil {
		t.Fatalf("import fixture users: %v", err)
	}
	return r, mappings
}

func TestLegacySpaceMapsToOwnedAgent(t *testing.T) {
	r, mappings := fixture(t, commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{7}})
	if len(mappings) != 1 || mappings[0] != (Mapping{Space: "alice", Human: "alice", Agent: "agent:alice:default"}) {
		t.Fatalf("migration: %+v", mappings)
	}
	ctx := context.Background()
	human, err := r.Principal(ctx, "alice")
	if err != nil || human != (contract.Principal{ID: "alice", Kind: contract.Human}) {
		t.Fatalf("human: %+v %v", human, err)
	}
	agent, err := r.Principal(ctx, mappings[0].Agent)
	if err != nil || agent.Kind != contract.Agent || agent.OwnerID != human.ID {
		t.Fatalf("owned agent: %+v %v", agent, err)
	}
	resolved, err := r.Resolve(ctx, contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"})
	if err != nil || resolved != human {
		t.Fatalf("legacy channel: %+v %v", resolved, err)
	}
}

func TestAgentPrincipalCannotImpersonateHuman(t *testing.T) {
	r, mappings := fixture(t, commodel.User{ID: "alice", Enabled: true}, commodel.User{ID: "bob", Enabled: true})
	ctx := context.Background()
	agent := mappings[0].Agent
	if err := r.CanAct(ctx, "alice", agent, "alice"); err != nil {
		t.Fatalf("owner's agent: %v", err)
	}
	for _, claim := range [][3]string{{agent, agent, "alice"}, {"bob", agent, "bob"}, {"alice", agent, "bob"}, {"alice", "alice", "alice"}} {
		if err := r.CanAct(ctx, claim[0], claim[1], claim[2]); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("impersonation %v: %v", claim, err)
		}
	}
}

func TestChannelLinkRequiresProof(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true})
	ctx := context.Background()
	channel := contract.ChannelIdentity{Channel: contract.Telegram, ID: "77"}
	pending, err := r.BeginLink(ctx, "alice", channel, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, channel); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("unproven link resolved: %v", err)
	}
	if err := r.VerifyLink(ctx, pending.ID, "wrong-proof", channel); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("wrong proof: %v", err)
	}
	if err := r.VerifyLink(ctx, pending.ID, pending.Proof, channel); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("rejected proof resurrected: %v", err)
	}
	pending, err = r.BeginLink(ctx, "alice", channel, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.VerifyLink(ctx, pending.ID, pending.Proof, channel); err != nil {
		t.Fatal(err)
	}
	human, err := r.Resolve(ctx, channel)
	if err != nil || human.ID != "alice" {
		t.Fatalf("proven link: %+v %v", human, err)
	}
	if err := r.RevokeLink(ctx, pending.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, channel); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("revoked link resolved: %v", err)
	}
}

func TestSimilarNamesNeverMerge(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}, commodel.User{ID: "alice2", Enabled: true, TelegramIDs: []int64{8}})
	ctx := context.Background()
	for channel, want := range map[string]string{"7": "alice", "8": "alice2"} {
		got, err := r.Resolve(ctx, contract.ChannelIdentity{Channel: contract.Telegram, ID: channel})
		if err != nil || got.ID != want {
			t.Fatalf("exact link %s: %+v %v", channel, got, err)
		}
	}
	if _, err := r.Principal(ctx, "Alice"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("display name match: %v", err)
	}
}
