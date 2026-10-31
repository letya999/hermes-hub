package broker

import (
	"errors"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
)

// Acquire denials carry a reason so operators can tell a grant/identity
// mismatch from an inactive credential without decoding the ledger.
func TestAcquireDenialReasons(t *testing.T) {
	f := newFixture(t)
	c := f.enroll(alice, "pat", "service")
	g, a := f.grant(alice, c, "pat", "binding1", "workload1")
	if _, e := f.b.Acquire(a, v1.AcquireLease{GrantID: "missing"}); !errors.Is(e, ErrDenied) || DenyReason(e) != "grant_missing" {
		t.Fatalf("missing grant: %v %q", e, DenyReason(e))
	}
	other := a
	other.PolicyVersion = "other"
	if _, e := f.b.Acquire(other, v1.AcquireLease{GrantID: g.ID}); !errors.Is(e, ErrDenied) || DenyReason(e) != "identity_mismatch" {
		t.Fatalf("identity: %v %q", e, DenyReason(e))
	}
	f.b.mu.Lock()
	cr := f.b.credentials[c.ID]
	cr.View.Status = "reauthorize_required"
	f.b.credentials[c.ID] = cr
	f.b.mu.Unlock()
	if _, e := f.b.Acquire(a, v1.AcquireLease{GrantID: g.ID}); !errors.Is(e, ErrDenied) || DenyReason(e) != "credential_inactive" {
		t.Fatalf("credential: %v %q", e, DenyReason(e))
	}
}

// Revoking a credential must also deactivate its grants: a stale active grant
// keeps blocking CreateGrant's conflict check for the same binding/workload.
func TestRevokeCredentialDeactivatesGrants(t *testing.T) {
	f := newFixture(t)
	c := f.enroll(alice, "pat", "service")
	g, _ := f.grant(alice, c, "pat", "binding1", "workload1")
	if e := f.b.RevokeCredential(alice, c.ID); e != nil {
		t.Fatal(e)
	}
	f.b.mu.Lock()
	active := f.b.grants[g.ID].View.Active
	f.b.mu.Unlock()
	if active {
		t.Fatal("revoked credential left its grant active")
	}
}

func TestGetGrantReportsRevocationOnlyToCredentialOwner(t *testing.T) {
	f := newFixture(t)
	c := f.enroll(alice, "pat", "service")
	g, _ := f.grant(alice, c, "pat", "binding1", "workload1")
	if _, err := f.b.GetGrant(bob, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner grant visible: %v", err)
	}
	if err := f.b.RevokeGrant(alice, g.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.b.GetGrant(alice, g.ID)
	if err != nil || got.ID != g.ID || got.Active {
		t.Fatalf("revoked grant visibility: active=%t err=%v", got.Active, err)
	}
	if err := f.b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.GetGrant(alice, g.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed broker grant lookup: %v", err)
	}
}

func TestCreateGrantReusesExactActiveBindingAfterControlRetry(t *testing.T) {
	f := newFixture(t)
	c := f.enroll(alice, "pat", "service")
	first, _ := f.grant(alice, c, "pat", "binding1", "workload1")
	retry := grantInput(alice, "pat", "binding1", "workload1")
	retry.IdempotencyKey = "new_onboarding_retry"
	got, err := f.b.CreateGrant(alice, c.ID, retry)
	if err != nil || got.ID != first.ID {
		t.Fatalf("exact active retry did not reuse grant: same=%t err=%v", got.ID == first.ID, err)
	}
	otherPolicy := alice
	otherPolicy.PolicyVersion = "policy_v2"
	if got, err := f.b.CreateGrant(otherPolicy, c.ID, retry); err == nil || got.ID == first.ID {
		t.Fatalf("policy change reused stale grant: same=%t err=%v", got.ID == first.ID, err)
	}
}

func TestCreateGrantReplacesStaleBindingForSameDedicatedWorkload(t *testing.T) {
	f := newFixture(t)
	c := f.enroll(alice, "pat", "service")
	old, oldActor := f.grant(alice, c, "pat", "binding1", "workload1")
	next := grantInput(alice, "pat", "binding2", "workload1")
	got, err := f.b.CreateGrant(alice, c.ID, next)
	if err != nil || !got.Active || got.ID == old.ID {
		t.Fatalf("same-workload rotation failed: new=%t err=%v", got.Active && got.ID != old.ID, err)
	}
	oldView, err := f.b.GetGrant(alice, old.ID)
	if err != nil || oldView.Active {
		t.Fatalf("old binding still active: active=%t err=%v", oldView.Active, err)
	}
	if _, err := f.b.Acquire(oldActor, v1.AcquireLease{GrantID: old.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("old binding acquired after rotation: %v", err)
	}
}
