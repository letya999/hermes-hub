package identityhub

import (
	"context"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestChannelBindingsExcludePendingAndRevoked(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{7}})
	ctx := context.Background()
	challenge, err := r.BeginLink(ctx, "alice", contract.ChannelIdentity{Channel: contract.Telegram, ID: "8"}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := r.SigningKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	link, err := contract.VerifyLinkToken(challenge.Proof, keys, time.Now())
	if err != nil || link.Challenge != challenge.ID || link.Subject != "alice" {
		t.Fatalf("link challenge lacks signed channel scope: %v", err)
	}
	if _, err := contract.VerifyRunToken(challenge.Proof, contract.ToolHubAudience, keys, time.Now()); err == nil {
		t.Fatal("link token authorized a run")
	}
	bindings, err := r.Bindings(ctx, "alice")
	if err != nil || len(bindings) != 1 || bindings[0].Channel.ID != "7" {
		t.Fatalf("pending binding leaked: %+v %v", bindings, err)
	}
	if err := r.RevokeLink(ctx, bindings[0].ID, "alice"); err != nil {
		t.Fatal(err)
	}
	bindings, err = r.Bindings(ctx, "alice")
	if err != nil || len(bindings) != 0 {
		t.Fatal("revoked binding visible")
	}
	if err := r.VerifyLink(ctx, challenge.ID, challenge.Proof, contract.ChannelIdentity{Channel: contract.Telegram, ID: "8"}); err != nil {
		t.Fatal(err)
	}
	bindings, err = r.Bindings(ctx, "alice")
	if err != nil || len(bindings) != 1 || bindings[0].Channel.ID != "8" {
		t.Fatal("new proof invisible")
	}
}
