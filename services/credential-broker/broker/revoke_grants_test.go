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
