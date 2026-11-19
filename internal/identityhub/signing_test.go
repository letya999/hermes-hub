package identityhub

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestControlRunSigningKeySurvivesRestartAndRotationOverlap(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity.sqlite")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if r != nil {
			_ = r.Close()
		}
	})
	now := time.Unix(2_000_000_000, 0)
	r.now = func() time.Time { return now }
	if _, err := r.ImportLegacy(ctx, "fixture", "personal", []commodel.User{{ID: "alice", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	keys, err := r.SigningKeys(ctx)
	if err != nil || len(keys) != 4 {
		t.Fatalf("expected four purpose keys: count=%d err=%v", len(keys), err)
	}
	claims := contract.RunClaims{
		Audience: "tool-hub", Subject: "alice", Actor: "agent:alice:default",
		Organization: "personal", Space: "alice", Environment: "dev", Conversation: "telegram-7",
		Job: "job-7", Task: "default", Run: "run-7", Generation: "gen-2",
		PolicyRevision: contract.PolicyDigest([]byte("unpublished")), Origin: "telegram_bot", Mode: "ask-critical",
	}
	budget := time.Minute
	if _, err := r.IssueRun(ctx, claims, now.Add(budget), budget); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("token issued for an unpublished policy: %v", err)
	}
	staged, err := r.StagePassports(ctx, syntheticPolicyBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ActivatePassports(ctx, claims.Space, claims.Environment, "", staged.Digest); err != nil {
		t.Fatal(err)
	}
	signed, _, err := r.BindPassports(ctx, contract.TemplateRequest{Subject: claims.Subject, Actor: claims.Actor, Space: claims.Space}, claims.Environment)
	if err != nil {
		t.Fatal(err)
	}
	claims.PolicyRevision = signed.Digest
	first, err := r.IssueRun(ctx, claims, now.Add(2*budget), budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return now }
	if err := r.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(contract.Handler(r, "synthetic-control"))
	defer server.Close()
	reopened, err := contract.NewHTTPClient(server.URL, "synthetic-control", server.Client()).SigningKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := contract.VerifyRunToken(first, claims.Audience, reopened, now)
	if err != nil || verified.Expires != now.Add(budget).Unix() || verified.Run != claims.Run {
		t.Fatalf("reopen lost issuance or deadline: %v", err)
	}
	now = now.Add(10 * time.Second)
	if err := r.RotateSigningKeys(ctx, budget); err != nil {
		t.Fatal(err)
	}
	rotated, err := r.SigningKeys(ctx)
	if err != nil || len(rotated) != 8 {
		t.Fatalf("rotation overlap keys: count=%d err=%v", len(rotated), err)
	}
	if _, err := contract.VerifyRunToken(first, claims.Audience, rotated, now); err != nil {
		t.Fatal("old key refused during overlap")
	}
	second, err := r.IssueRun(ctx, claims, now.Add(20*time.Second), budget)
	if err != nil {
		t.Fatal(err)
	}
	verified, err = contract.VerifyRunToken(second, claims.Audience, rotated, now)
	if err != nil || verified.Expires != now.Add(20*time.Second).Unix() {
		t.Fatalf("rotation lost issuance or deadline: %v", err)
	}
	if _, err := contract.VerifyRunToken(second, claims.Audience, keys, now); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("new token accepted under the pre-rotation keys: %v", err)
	}
	var retiredSeedsRemoved bool
	if err := r.db.QueryRow("SELECT NOT EXISTS(SELECT 1 FROM signing_keys WHERE verify_until>0 AND seed IS NOT NULL)").Scan(&retiredSeedsRemoved); err != nil {
		t.Fatal(err)
	}
	if !retiredSeedsRemoved {
		t.Fatal("a retired key still has its signing seed in the current table")
	}
	now = now.Add(budget)
	visible, err := r.SigningKeys(ctx)
	if err != nil || len(visible) != 4 {
		t.Fatalf("expired overlap keys remain visible: count=%d err=%v", len(visible), err)
	}
}
