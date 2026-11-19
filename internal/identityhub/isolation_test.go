package identityhub

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/contracts/identity/v1/conformance"
	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{7}, SlackIDs: []commodel.SlackLink{{TeamID: "T1", UserID: "U1"}}})
	for _, channel := range []contract.ChannelIdentity{{Channel: contract.Telegram, ID: "7"}, {Channel: contract.Slack, ID: "t1/u1"}} {
		conformance.Both(t, r, func(token string) http.Handler { return contract.Handler(r, token) }, channel, contract.Principal{ID: "alice", Kind: contract.Human})
	}
}

func TestLegacyMigrationIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity.sqlite")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	users := []commodel.User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}, {ID: "bob", Enabled: false, TelegramIDs: []int64{8}}}
	first, err := r.ImportLegacy(ctx, "one", "personal", users)
	if err != nil {
		t.Fatal(err)
	}
	users[0].ConfiguredEnv = map[string]bool{"private-credential-key-marker": true}
	users[0].WorkspaceDir = "private-path-marker"
	again, err := r.ImportLegacy(ctx, "one", "personal", []commodel.User{users[1], users[0]})
	if err != nil || !reflect.DeepEqual(again, first) {
		t.Fatalf("idempotent import: %+v %v", again, err)
	}
	users[0].TelegramIDs = []int64{9}
	if _, err := r.ImportLegacy(ctx, "one", "personal", users); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("changed import widened access: %v", err)
	}
	if _, err := r.ImportLegacy(ctx, "two", "personal", []commodel.User{{ID: "carol", Enabled: true, TelegramIDs: []int64{7}}}); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("foreign channel import: %v", err)
	}
	if _, err := r.Principal(ctx, "carol"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("partial import survived rollback: %v", err)
	}
	if _, err := r.Resolve(ctx, contract.ChannelIdentity{Channel: contract.Telegram, ID: "8"}); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("disabled user resolved: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve(ctx, contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"})
	if err != nil || got.ID != "alice" {
		t.Fatalf("reopened mapping: %+v %v", got, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"private-credential-key-marker", "private-path-marker"} {
		if bytes.Contains(data, []byte(marker)) {
			t.Fatal("non-identity import field reached the database")
		}
	}
}

func TestConcurrentChannelProofNeverSharesAnAccount(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true}, commodel.User{ID: "bob", Enabled: true})
	ctx := context.Background()
	channel := contract.ChannelIdentity{Channel: contract.Telegram, ID: "77"}
	challenges := make([]Challenge, 2)
	for i, owner := range []string{"alice", "bob"} {
		var err error
		challenges[i], err = r.BeginLink(ctx, owner, channel, time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i := range challenges {
		wg.Add(1)
		go func() { defer wg.Done(); c := challenges[i]; results[i] = r.VerifyLink(ctx, c.ID, c.Proof, channel) }()
	}
	wg.Wait()
	winner := -1
	for i, err := range results {
		if err == nil {
			if winner != -1 {
				t.Fatal("channel acquired by both humans")
			}
			winner = i
		} else if !errors.Is(err, contract.ErrConflict) {
			t.Fatal(err)
		}
	}
	if winner < 0 {
		t.Fatal("no proof succeeded")
	}
	got, err := r.Resolve(ctx, channel)
	if err != nil || got.ID != []string{"alice", "bob"}[winner] {
		t.Fatalf("winner: %+v %v", got, err)
	}
	if _, err := r.BeginLink(ctx, []string{"bob", "alice"}[winner], channel, time.Now().Add(time.Minute)); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("duplicate channel takeover: %v", err)
	}
}

func TestExpiredAndForeignChannelProofsDeny(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true})
	ctx := context.Background()
	now := time.Now()
	r.now = func() time.Time { return now }
	channel := contract.ChannelIdentity{Channel: contract.Telegram, ID: "77"}
	c, err := r.BeginLink(ctx, "alice", channel, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.VerifyLink(ctx, c.ID, c.Proof, contract.ChannelIdentity{Channel: contract.Telegram, ID: "78"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("foreign observation: %v", err)
	}
	c, err = r.BeginLink(ctx, "alice", channel, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := r.VerifyLink(ctx, c.ID, c.Proof, channel); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("expired proof: %v", err)
	}
	if _, err := r.Resolve(ctx, channel); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("expired channel resolved: %v", err)
	}
}

func TestForbiddenChannelTransitionsDoNotWrite(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true})
	ctx := context.Background()
	c, err := r.BeginLink(ctx, "alice", contract.ChannelIdentity{Channel: contract.Telegram, ID: "77"}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"pending", "revoke"}, {"pending", "delete"}, {"verified", "verify"}, {"verified", "expire"}, {"revoked", "verify"}, {"gone", "verify"}, {"gone", "revoke"}, {"pending", "unknown"}} {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE links SET state=? WHERE id=?", pair[0], c.ID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := transitionLink(ctx, tx, c.ID, pair[1]); !errors.Is(err, contract.ErrConflict) {
			_ = tx.Rollback()
			t.Fatalf("forbidden transition %v: %v", pair, err)
		}
		var got string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM links WHERE id=?", c.ID).Scan(&got); err != nil || got != pair[0] {
			_ = tx.Rollback()
			t.Fatal("refused transition changed the link")
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}
