package identityhub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestSignedPolicyBundleRejectsUnsignedMismatchAndForeignSpace(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true})
	ctx := t.Context()
	if err := r.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	bundle := syntheticPolicyBundle()
	published, err := r.StagePassports(ctx, bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest, signed := published.Digest, published.JWS
	keys, err := r.SigningKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := contract.VerifyPolicyBundle(signed, digest, "alice", "dev", keys, time.Now())
	if err != nil || got.Space != "alice" || got.PassportRevision != "r-1" {
		t.Fatalf("valid bundle lost its policy owner or revision: %v", err)
	}
	bundle.Passports["grants"] = json.RawMessage(`{ "metadata":{"revision":"r-1","space":"alice","environment":"dev"}, "fixture": true, "kind": "grants" }`)
	same, err := r.StagePassports(ctx, bundle)
	if err != nil || same.Digest != digest {
		t.Fatalf("JSON object order changed the digest: %v", err)
	}
	bundle.Passports["grants"] = json.RawMessage(`{"fixture":false,"kind":"grants","metadata":{"space":"alice","environment":"dev","revision":"r-1"}}`)
	changed, err := r.StagePassports(ctx, bundle)
	if err != nil || changed.Digest == digest {
		t.Fatalf("a changed grant document did not change the digest: %v", err)
	}
	for _, tc := range []struct{ token, digest, space, env string }{
		{`{"space":"alice"}`, digest, "alice", "dev"},
		{signed, changed.Digest, "alice", "dev"},
		{signed, digest, "bob", "dev"},
		{signed, digest, "alice", "prod"},
	} {
		if _, err := contract.VerifyPolicyBundle(tc.token, tc.digest, tc.space, tc.env, keys, time.Now()); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("invalid policy was accepted: %v", err)
		}
	}
	if err := r.ActivatePassports(ctx, "alice", "prod", "", digest); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("foreign environment activated: %v", err)
	}
	if err := r.ActivatePassports(ctx, "alice", "dev", "", digest); err != nil {
		t.Fatal(err)
	}
	if err := r.ActivatePassports(ctx, "alice", "dev", "", changed.Digest); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("a stale publisher could replace active policy: %v", err)
	}
	_, derived, err := r.BindPassports(ctx, contract.TemplateRequest{Subject: "alice", Actor: "agent:alice:default", Space: "alice"}, "dev")
	if err != nil || derived.Identity == nil || derived.Identity.Restrictions.Mode != "ask-critical" || derived.PassportDigest != digest {
		t.Fatalf("active policy did not bind the current template: %v", err)
	}
	derived.Identity.Restrictions.Mode = "full-access"
	if _, err := r.StagePassports(ctx, derived); !errors.Is(err, contract.ErrInvalid) {
		t.Fatalf("publisher staged a supplied agent template: %v", err)
	}
	bundle.Passports["grants"] = json.RawMessage(`{"kind":"grants","metadata":{"space":"alice","environment":"dev","revision":"foreign"}}`)
	if _, err := r.StagePassports(ctx, bundle); !errors.Is(err, contract.ErrInvalid) {
		t.Fatalf("mixed-version policy staged: %v", err)
	}
}

func syntheticPolicyBundle() contract.PolicyBundle {
	bundle := contract.PolicyBundle{Space: "alice", Environment: "dev", PassportRevision: "r-1", Passports: map[string]json.RawMessage{}}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		bundle.Passports[name] = json.RawMessage(fmt.Sprintf(`{"kind":%q,"fixture":true,"metadata":{"space":"alice","environment":"dev","revision":"r-1"}}`, name))
	}
	return bundle
}

func TestPolicyDigestReadSurvivesSigningKeyRetirement(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true})
	ctx := t.Context()
	now := time.Now()
	r.now = func() time.Time { return now }
	if err := r.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	staged, err := r.StagePassports(ctx, syntheticPolicyBundle())
	if err != nil {
		t.Fatal(err)
	}
	digest, first := staged.Digest, staged.JWS
	server := httptest.NewServer(contract.Handler(r, "synthetic-policy-read"))
	defer server.Close()
	client := contract.NewHTTPClient(server.URL, "synthetic-policy-read", server.Client())
	if err := r.RotateSigningKeys(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	got, err := client.Policy(ctx, digest)
	if err != nil || got.Digest != digest || got.JWS == first {
		t.Fatalf("immutable bundle unavailable under active key: %v", err)
	}
	keys, err := client.SigningKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contract.VerifyPolicyBundle(got.JWS, digest, "alice", "dev", keys, now); err != nil {
		t.Fatal(err)
	}
	if _, err := contract.VerifyPolicyBundle(first, digest, "alice", "dev", keys, now); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("retired signature stayed valid: %v", err)
	}
	if _, err := client.Policy(ctx, contract.PolicyDigest([]byte("unpublished"))); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("unpublished policy was readable: %v", err)
	}
}
