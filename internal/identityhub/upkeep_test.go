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

func TestRegistryStorePathSeparatesEnvironments(t *testing.T) {
	dir := t.TempDir()
	dev, err := StorePath(dir, "dev")
	if err != nil || dev != filepath.Join(dir, "identity.dev.sqlite") {
		t.Fatalf("dev path: %q %v", dev, err)
	}
	prod, err := StorePath(dir, "prod")
	if err != nil || prod == dev {
		t.Fatal("dev and prod share registry")
	}
	for _, query := range [][2]string{{"", "dev"}, {dir, ""}, {dir, "../prod"}} {
		if _, err := StorePath(query[0], query[1]); !errors.Is(err, contract.ErrInvalid) {
			t.Fatalf("invalid path accepted: %v", err)
		}
	}
}

func TestRegistryUpkeepExpiresOnlyPendingProofs(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{7}})
	ctx := context.Background()
	now := time.Now()
	r.now = func() time.Time { return now }
	channel := contract.ChannelIdentity{Channel: contract.Telegram, ID: "8"}
	expired, err := r.BeginLink(ctx, "alice", channel, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	active, err := r.BeginLink(ctx, "alice", contract.ChannelIdentity{Channel: contract.Telegram, ID: "9"}, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.ExpirePendingLinks(ctx, now.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	var proof []byte
	if err := r.db.QueryRow("SELECT state,proof_hash FROM links WHERE id=?", expired.ID).Scan(&state, &proof); err != nil || state != "gone" || len(proof) != 0 {
		t.Fatal("expired proof retained")
	}
	if err := r.VerifyLink(ctx, expired.ID, expired.Proof, channel); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("expired proof resurrected: %v", err)
	}
	if err := r.db.QueryRow("SELECT state FROM links WHERE id=?", active.ID).Scan(&state); err != nil || state != "pending" {
		t.Fatal("active proof expired")
	}
	if _, err := r.Resolve(ctx, contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}); err != nil {
		t.Fatal("verified link expired")
	}
	var count int
	if err := r.db.QueryRow("SELECT count(*) FROM audit WHERE action='link_expire'").Scan(&count); err != nil || count != 1 {
		t.Fatal("expiry was not audited exactly once")
	}
}
