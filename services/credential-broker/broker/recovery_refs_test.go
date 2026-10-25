package broker

import (
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/provider"
)

func TestRecoveryRefsIncludeOnlyLiveManagedCredential(t *testing.T) {
	f := newFixture(t)
	credential := f.enroll(alice, "pat", "connection-one")
	refs := f.b.RecoveryRefs()
	if len(refs) != 1 || refs[0] != f.b.credentials[credential.ID].Ref {
		t.Fatal("active credential ref missing from migration set")
	}
	if err := f.b.RevokeCredential(alice, credential.ID); err != nil {
		t.Fatal(err)
	}
	if refs := f.b.RecoveryRefs(); len(refs) != 0 {
		t.Fatal("revoked provider object included in migration set")
	}
}

func TestRecoveryRefsIncludePendingDraft(t *testing.T) {
	f := newFixture(t)
	ref, err := f.cfg.Providers.Put(testCtx, "local", provider.Bundle{"token": []byte("synthetic-draft")})
	if err != nil {
		t.Fatal(err)
	}
	f.b.requests["request-one"] = requestRecord{View: v1.Request{ID: "request-one", Status: "authorizing"}, DraftRef: &ref}
	refs := f.b.RecoveryRefs()
	if len(refs) != 1 || refs[0] != ref {
		t.Fatal("pending draft ref missing from migration set")
	}
}

func TestRecoveryRefsIncludeCurrentCheckpointForActiveGrant(t *testing.T) {
	f := newFixture(t)
	credential := f.enroll(alice, "pat", "connection-one")
	_, _ = f.grant(alice, credential, "pat", "binding-one", "workload-one")
	ref, err := f.cfg.Providers.Put(testCtx, "local", provider.Bundle{"snapshot": []byte("synthetic-checkpoint")})
	if err != nil {
		t.Fatal(err)
	}
	stateKey := seal.Digest(credential.ID + "/" + alice.ContextID + "/binding-one")
	f.b.states[stateKey] = stateRecord{Key: stateKey, Ref: ref, Revision: 1}
	refs := f.b.RecoveryRefs()
	if len(refs) != 2 || refs[1] != ref && refs[0] != ref {
		t.Fatal("current state checkpoint missing from migration set")
	}
}
