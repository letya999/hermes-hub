package broker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/provider"
)

// R7.1/A7: the reviewed model contracts admit bounded OAuth session files and
// rotatable API keys — two session principals plus one key rotate and revoke
// independently, and their bytes never land in views, events or the ledger.
func TestModelSessionLeaseOwnerRotationAndRevocation(t *testing.T) {
	session := contract.Contract{ID: "cliproxy-session", Revision: 1, Title: "CLIProxy account session", Storage: "local",
		Fields:     []contract.Field{{ID: "session_json", Kind: "json", Label: "Account session JSON", Required: true, MaxBytes: 65536}},
		Deliveries: []contract.Delivery{{Type: "file", Field: "session_json", Target: "/cliproxy/auths/session.json", EnvName: "CLIPROXY_SESSION_FILE"}}}
	key := contract.Contract{ID: "model-api-key", Revision: 1, Title: "Model provider API key", Storage: "local",
		Fields:     []contract.Field{{ID: "api_key", Kind: "secret", Label: "Provider API key", Required: true, MaxBytes: 4096}},
		Deliveries: []contract.Delivery{{Type: "env", Field: "api_key", Target: "MODEL_API_KEY"}}}
	f := newFixture(t, session, key)

	enrollWith := func(a identity.Actor, cid, conn string, values provider.Bundle) v1.Credential {
		t.Helper()
		r := f.request(a, cid, conn, "create__"+conn)
		cookie := f.pair(a, r)
		if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), values, nil); e != nil {
			t.Fatal(e)
		}
		rr, e := f.b.GetRequest(a, r.ID)
		if e != nil || rr.Status != "ready" {
			t.Fatal(e)
		}
		c, e := f.b.GetCredential(a, rr.CredentialID)
		if e != nil {
			t.Fatal(e)
		}
		return c
	}

	// Two separate session principals and one provider key.
	sessA := enrollWith(alice, "cliproxy-session", "alice-account", provider.Bundle{"session_json": []byte(`{"token":"SENTINEL_SESSION_A_V1"}`)})
	sessB := enrollWith(bob, "cliproxy-session", "bob-account", provider.Bundle{"session_json": []byte(`{"token":"SENTINEL_SESSION_B_V1"}`)})
	keyCred := enrollWith(alice, "model-api-key", "provider", provider.Bundle{"api_key": []byte("SENTINEL_API_KEY_V1")})

	gA, actorA := f.grant(alice, sessA, "cliproxy-session", "model-relay-alice", "cliproxy-alice")
	gB, actorB := f.grant(bob, sessB, "cliproxy-session", "model-relay-bob", "cliproxy-bob")
	gK, actorK := f.grant(alice, keyCred, "model-api-key", "model-relay", "model-relay")

	// A bounded session file materializes only inside the pinned workload's
	// lease mount — the owner path, never a shared or agent-visible one.
	leaseA := f.lease(actorA, gA)
	mA, e := f.b.Materialize(testCtx, actorA, leaseA.ID)
	if e != nil || len(mA.Mounts) == 0 {
		t.Fatalf("session materialize: %v %+v", e, mA)
	}
	raw, e := os.ReadFile(mA.Mounts[0].Source)
	if e != nil || !strings.Contains(string(raw), "SENTINEL_SESSION_A_V1") {
		t.Fatalf("session file: %v %q", e, raw)
	}

	// The API key lands only as env on its own workload lease.
	leaseK := f.lease(actorK, gK)
	mK, e := f.b.Materialize(testCtx, actorK, leaseK.ID)
	if e != nil || mK.Env["MODEL_API_KEY"] != "SENTINEL_API_KEY_V1" {
		t.Fatalf("key materialize: %v %+v", e, mK)
	}

	// A session principal cannot touch another's grant or credential.
	if _, e := f.b.Acquire(actorB, v1.AcquireLease{GrantID: gA.ID}); e == nil {
		t.Fatal("cross-principal lease admitted")
	}
	if _, e := f.b.GetCredential(bob, sessA.ID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("cross-principal view: %v", e)
	}

	// Rotation changes only that principal's bytes; the sibling session and
	// the key keep serving their current revision.
	r3, e := f.b.CreateRequest(testCtx, alice, v1.CreateRequest{ContractID: "cliproxy-session", ContractRevision: 1, ConnectionID: "alice-account", OnboardingID: "onboard_1", IdempotencyKey: "rotate__alice-account", OwnerKind: "user", RotateCredentialID: sessA.ID})
	if e != nil {
		t.Fatal(e)
	}
	cookie := f.pair(alice, r3)
	if e := f.b.Submit(testCtx, r3.ID, cookie, f.b.CSRF(cookie, r3.ID), provider.Bundle{"session_json": []byte(`{"token":"SENTINEL_SESSION_A_V2"}`)}, nil); e != nil {
		t.Fatal(e)
	}
	rotated, e := f.b.GetCredential(alice, sessA.ID)
	if e != nil || rotated.Revision != sessA.Revision+1 {
		t.Fatalf("rotation: %v %+v", e, rotated)
	}
	leaseA2 := f.lease(actorA, gA)
	mA2, e := f.b.Materialize(testCtx, actorA, leaseA2.ID)
	if e != nil || len(mA2.Mounts) == 0 {
		t.Fatalf("rotated materialize: %v %+v", e, mA2)
	}
	raw2, _ := os.ReadFile(mA2.Mounts[0].Source)
	if !strings.Contains(string(raw2), "SENTINEL_SESSION_A_V2") {
		t.Fatalf("rotated session file: %q", raw2)
	}
	mK2, e := f.b.Materialize(testCtx, actorK, f.lease(actorK, gK).ID)
	if e != nil || mK2.Env["MODEL_API_KEY"] != "SENTINEL_API_KEY_V1" {
		t.Fatalf("rotation disturbed the key: %v", e)
	}
	mB, e := f.b.Materialize(testCtx, actorB, f.lease(actorB, gB).ID)
	if e != nil || len(mB.Mounts) == 0 {
		t.Fatalf("rotation disturbed the sibling session: %v", e)
	}

	// Revoking one session credential kills its leases; the other principal
	// and the key are unaffected.
	if e := f.b.RevokeCredential(bob, sessB.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.b.Acquire(actorB, v1.AcquireLease{GrantID: gB.ID}); e == nil {
		t.Fatal("revoked session still leases")
	}
	if _, e := f.b.Materialize(testCtx, actorA, leaseA2.ID); e != nil {
		t.Fatalf("revoke disturbed the sibling session: %v", e)
	}
	if _, e := f.b.Acquire(actorK, v1.AcquireLease{GrantID: gK.ID}); e != nil {
		t.Fatalf("revoke disturbed the key: %v", e)
	}

	// The ledger and every DTO view carry references, never secret bytes.
	var view []byte
	for _, v := range []any{sessA, sessB, keyCred, rotated, leaseA, leaseA2, leaseK} {
		raw, _ := json.Marshal(v)
		view = append(view, raw...)
	}
	events := f.b.Events(alice, 0, 1000)
	raw, _ = json.Marshal(events)
	view = append(view, raw...)
	for _, sentinel := range []string{"SENTINEL_SESSION_A", "SENTINEL_SESSION_B", "SENTINEL_API_KEY"} {
		if strings.Contains(string(view), sentinel) {
			t.Fatalf("secret bytes in views/events: %s", sentinel)
		}
	}
	// The tmpfs lease mounts are the delivery mechanism, not persistence:
	// only the ledger and provider store must stay byte-clean.
	e = filepath.Walk(f.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "runtime" {
				return filepath.SkipDir
			}
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, sentinel := range []string{"SENTINEL_SESSION_A", "SENTINEL_SESSION_B", "SENTINEL_API_KEY"} {
			if strings.Contains(string(body), sentinel) {
				t.Fatalf("plaintext secret persisted in %s", path)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
