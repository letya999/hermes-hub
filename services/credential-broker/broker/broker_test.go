package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/materialize"
	"github.com/letya999/credential-broker/provider"
)

var testCtx = context.Background()
var alice = identity.Actor{PrincipalID: "alice", ContextID: "team", RuntimeID: "hermes-alice", ExternalIdentityID: "tg-alice", ConversationID: "dm-alice", PolicyVersion: "policy_v1"}
var bob = identity.Actor{PrincipalID: "bob", ContextID: "team", RuntimeID: "hermes-bob", PolicyVersion: "policy_v1"}

type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *testClock) add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.at = c.at.Add(d) }

type fixture struct {
	b     *Broker
	cfg   Config
	root  string
	clock *testClock
	t     *testing.T
}

func plainContract() contract.Contract {
	return contract.Contract{ID: "pat", Revision: 1, Title: "PAT", Storage: "local", Fields: []contract.Field{{ID: "token", Kind: "secret", Label: "API token", Required: true, MaxBytes: 1024}}, Deliveries: []contract.Delivery{{Type: "env", Field: "token", Target: "API_TOKEN"}}}
}
func stateContract() contract.Contract {
	c := plainContract()
	c.ID = "session"
	c.Deliveries = []contract.Delivery{{Type: "file", Field: "token", Target: "/run/secrets/token", EnvName: "TOKEN_FILE"}}
	c.State = &contract.State{Target: "/home/mcp/.config/service", Files: []contract.StateFile{{Name: "runtime_session.json", JSON: true, MaxBytes: 1024}}}
	return c
}
func newFixture(t *testing.T, cs ...contract.Contract) *fixture {
	t.Helper()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	if len(cs) == 0 {
		cs = []contract.Contract{plainContract()}
	}
	p, e := provider.NewLocal("local", filepath.Join(root, "secrets"), bytes.Repeat([]byte{7}, 32))
	if e != nil {
		t.Fatal(e)
	}
	j, e := journal.Open(filepath.Join(root, "ledger"), bytes.Repeat([]byte{9}, 32), 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	m, e := materialize.New(filepath.Join(root, "runtime"), false)
	if e != nil {
		t.Fatal(e)
	}
	clock := &testClock{at: time.Now().UTC()}
	cfg := Config{Contracts: cs, Providers: provider.Registry{"local": p}, Journal: j, Materializer: m, PublicOrigin: "https://credentials.example", SessionKey: bytes.Repeat([]byte{5}, 32), Now: clock.now, OAuth: map[string]*OAuthProvider{}, ExternalAliases: map[string]ExternalAlias{}}
	f := &fixture{cfg: cfg, root: root, clock: clock, t: t}
	// OAuth fixtures fill their provider before calling start.
	if cs[0].OAuthProvider == "" {
		f.start()
	}
	t.Cleanup(func() {
		if f.b != nil {
			_ = f.b.Close()
		} else {
			_ = j.Close()
			_ = m.Close()
		}
	})
	return f
}
func (f *fixture) start() {
	f.t.Helper()
	b, e := New(f.cfg)
	if e != nil {
		f.t.Fatal(e)
	}
	f.b = b
}
func (f *fixture) restart() {
	f.t.Helper()
	if e := f.b.Close(); e != nil {
		f.t.Fatal(e)
	}
	j, e := journal.Open(filepath.Join(f.root, "ledger"), bytes.Repeat([]byte{9}, 32), 8<<20)
	if e != nil {
		f.t.Fatal(e)
	}
	m, e := materialize.New(filepath.Join(f.root, "runtime"), false)
	if e != nil {
		f.t.Fatal(e)
	}
	f.cfg.Journal = j
	f.cfg.Materializer = m
	f.start()
}
func (f *fixture) request(a identity.Actor, cid, conn, idem string) v1.Request {
	f.t.Helper()
	r, e := f.b.CreateRequest(testCtx, a, v1.CreateRequest{ContractID: cid, ContractRevision: 1, ConnectionID: conn, OnboardingID: "onboard_1", IdempotencyKey: idem, OwnerKind: "user"})
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *fixture) pair(a identity.Actor, r v1.Request) string {
	f.t.Helper()
	v, cookie, e := f.b.OpenSession(r.ID, "")
	if e != nil || v.Approved || len(v.Fields) != 0 {
		f.t.Fatal("unapproved browser", e)
	}
	if e = f.b.Approve(a, r.ID, v.Code); e != nil {
		f.t.Fatal(e)
	}
	return cookie
}
func (f *fixture) enroll(a identity.Actor, cid, conn string) v1.Credential {
	f.t.Helper()
	r := f.request(a, cid, conn, "create__"+conn)
	cookie := f.pair(a, r)
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("SENTINEL_NOT_A_REAL_SECRET")}); e != nil {
		f.t.Fatal(e)
	}
	rr, e := f.b.GetRequest(a, r.ID)
	if e != nil || rr.Status != "ready" {
		f.t.Fatal(e)
	}
	c, e := f.b.GetCredential(a, rr.CredentialID)
	if e != nil {
		f.t.Fatal(e)
	}
	return c
}
func grantInput(a identity.Actor, cid, binding, workload string) v1.GrantRequest {
	return v1.GrantRequest{ContractID: cid, ContractRevision: 1, PrincipalID: a.PrincipalID, ContextID: a.ContextID, RuntimeID: a.RuntimeID, BindingID: binding, WorkloadID: workload, Execution: "dedicated", IdempotencyKey: "grant__" + binding}
}
func (f *fixture) grant(a identity.Actor, c v1.Credential, cid, binding, workload string) (v1.Grant, identity.Actor) {
	f.t.Helper()
	g, e := f.b.CreateGrant(a, c.ID, grantInput(a, cid, binding, workload))
	if e != nil {
		f.t.Fatal(e)
	}
	a.BindingID = binding
	a.WorkloadID = workload
	return g, a
}
func (f *fixture) lease(a identity.Actor, g v1.Grant) v1.Lease {
	f.t.Helper()
	l, e := f.b.Acquire(a, v1.AcquireLease{GrantID: g.ID})
	if e != nil {
		f.t.Fatal(e)
	}
	return l
}

func TestEnrollmentIdentityReplayAndIdempotency(t *testing.T) {
	f := newFixture(t)
	r := f.request(alice, "pat", "github", "one_request")
	same := f.request(alice, "pat", "github", "one_request")
	if same.ID != r.ID {
		t.Fatal("idempotency")
	}
	in := v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "other", OnboardingID: "onboard_1", IdempotencyKey: "one_request", OwnerKind: "user"}
	if _, e := f.b.CreateRequest(testCtx, alice, in); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	in.IdempotencyKey = "another_key"
	in.ConnectionID = "github"
	if _, e := f.b.CreateRequest(testCtx, alice, in); !errors.Is(e, ErrConflict) {
		t.Fatal("reserved connection", e)
	}
	if _, e := f.b.GetRequest(bob, r.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	v, cookie, e := f.b.OpenSession(r.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}); !errors.Is(e, ErrDenied) {
		t.Fatal("link possession is not identity", e)
	}
	if e = f.b.Approve(bob, r.ID, v.Code); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e = f.b.Approve(alice, r.ID, "bad"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e = f.b.Approve(alice, r.ID, strings.Repeat("A", 16)); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e = f.b.Approve(alice, r.ID, strings.ToLower(v.Code)); e != nil {
		t.Fatal(e)
	}
	other, _, e := f.b.OpenSession(r.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.b.Approve(alice, r.ID, other.Code); !errors.Is(e, ErrConflict) {
		t.Fatal("two browsers", e)
	}
	seen, again, e := f.b.OpenSession(r.ID, cookie)
	if e != nil || again != cookie || !seen.Approved || len(seen.Fields) != 1 {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, "bad", provider.Bundle{"token": []byte("x")}); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"unknown": []byte("x")}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("secret-value")}); e != nil {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("other")}); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	if _, _, e = f.b.OpenSession(r.ID, cookie); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	rr, _ := f.b.GetRequest(alice, r.ID)
	if rr.Status != "ready" || rr.Revision != 1 {
		t.Fatal(rr)
	}
	if e = f.b.CancelRequest(alice, r.ID); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if _, e = f.b.GetCredential(bob, rr.CredentialID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	events := f.b.Events(alice, 0, 100)
	raw, _ := json.Marshal(events)
	if bytes.Contains(raw, []byte("secret-value")) || bytes.Contains(raw, []byte("provider_ref")) {
		t.Fatal("event disclosure")
	}
	for _, e := range f.b.Events(bob, 0, 100).Events {
		if e.PrincipalID == "alice" {
			t.Fatal("cross-user ledger")
		}
	}
	data, _ := os.ReadFile(filepath.Join(f.root, "ledger", "ledger.bin"))
	if bytes.Contains(data, []byte("secret-value")) {
		t.Fatal("ledger plaintext")
	}
	cat := f.b.Catalog()
	cat[0].Fields[0].Label = "mutated"
	if f.b.Catalog()[0].Fields[0].Label == "mutated" {
		t.Fatal("catalog alias")
	}
	if f.b.PublicOrigin() != "https://credentials.example" || !f.b.Healthy() {
		t.Fatal("health")
	}
}

func TestLeaseMaterializationRevokeAndRotation(t *testing.T) {
	f := newFixture(t)
	c := f.enroll(alice, "pat", "github")
	g, a := f.grant(alice, c, "pat", "binding1", "workload1")
	l := f.lease(alice, g)
	l.Deliveries[0].Target = "MUTATED"
	ins, e := f.b.InspectLease(a, l.ID)
	if e != nil || ins.Deliveries[0].Target != "API_TOKEN" {
		t.Fatal("descriptor alias", e)
	}
	mat, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil || mat.Env["API_TOKEN"] != "SENTINEL_NOT_A_REAL_SECRET" {
		t.Fatal(e)
	}
	mat.Env["API_TOKEN"] = "overwritten"
	again, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil || again.Env["API_TOKEN"] == "overwritten" {
		t.Fatal("cache alias", e)
	}
	if _, e = f.b.Materialize(testCtx, alice, l.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("runtime binding required", e)
	}
	wrong := a
	wrong.PrincipalID = "bob"
	if _, e = f.b.Materialize(testCtx, wrong, l.ID); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	wrong = a
	wrong.PolicyVersion = "policy_v2"
	if _, e = f.b.Acquire(wrong, v1.AcquireLease{GrantID: g.ID}); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if _, e = f.b.Renew(a, l.ID, 10); !errors.Is(e, ErrInvalid) {
		t.Fatal("shorter renewal", e)
	}
	if _, e = f.b.Renew(a, l.ID, 301); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, e = f.b.Renew(a, l.ID, 120); e != nil {
		t.Fatal(e)
	}
	use, e := f.b.BeginUse(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer use.Finish()
	use.Contract.Fields[0].Label = "evil"
	if f.b.Catalog()[0].Fields[0].Label == "evil" {
		t.Fatal("use aliases catalog")
	}
	r, e := f.b.CreateRequest(testCtx, alice, v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "github", OnboardingID: "rotate_job", IdempotencyKey: "rotate_one", OwnerKind: "user", RotateCredentialID: c.ID})
	if e != nil {
		t.Fatal(e)
	}
	cookie := f.pair(alice, r)
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("new-value")}); e != nil {
		t.Fatal(e)
	}
	if use.Valid() {
		t.Fatal("stale active request survives rotation")
	}
	select {
	case <-use.Context.Done():
	default:
		t.Fatal("not canceled")
	}
	if _, e = f.b.InspectLease(a, l.ID); e == nil {
		t.Fatal("old lease")
	}
	l = f.lease(alice, g)
	if l.Revision != 2 {
		t.Fatal(l)
	}
	if e = f.b.RevokeGrant(bob, g.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e = f.b.RevokeGrant(alice, g.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.b.RevokeGrant(alice, g.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.b.Acquire(alice, v1.AcquireLease{GrantID: g.ID}); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e = f.b.DeleteCredential(testCtx, alice, c.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.b.DeleteCredential(testCtx, alice, c.ID); e != nil {
		t.Fatal(e)
	}
	info, _ := f.b.GetCredential(alice, c.ID)
	if info.Status != "deleted" {
		t.Fatal(info)
	}
	for _, ref := range f.b.credentials[c.ID].OwnedRefs {
		if _, e = f.cfg.Providers.Resolve(testCtx, ref); e == nil {
			t.Fatal("old managed version remains")
		}
	}
}

func TestStateCheckpointsExclusiveLeaseAndRestart(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	l := f.lease(alice, g)
	if _, e := f.b.Acquire(alice, v1.AcquireLease{GrantID: g.ID}); !errors.Is(e, ErrConflict) {
		t.Fatal("concurrent state writers", e)
	}
	if e := f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{Checkpoint: true, Quiesced: true}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	m, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	statePath := filepath.Join(m.Mounts[1].Source, "runtime_session.json")
	if e = os.WriteFile(statePath, []byte(`{"session":"SENTINEL_STATE"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{Checkpoint: true}); !errors.Is(e, ErrInvalid) {
		t.Fatal("must be quiesced", e)
	}
	if e = f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{Checkpoint: true, Quiesced: true}); e != nil {
		t.Fatal(e)
	}
	l = f.lease(alice, g)
	m, e = f.b.Materialize(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(m.Mounts[1].Source, "runtime_session.json"))
	if e != nil || !bytes.Contains(data, []byte("SENTINEL_STATE")) {
		t.Fatal("restore", e)
	}
	f.restart()
	if _, e = f.b.InspectLease(a, l.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("restart resurrected lease", e)
	}
	if _, e = os.Stat(m.Mounts[0].Source); !os.IsNotExist(e) {
		t.Fatal("old plaintext survives restart", e)
	}
	l = f.lease(alice, g)
	m, e = f.b.Materialize(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	data, e = os.ReadFile(filepath.Join(m.Mounts[1].Source, "runtime_session.json"))
	if e != nil || !bytes.Contains(data, []byte("SENTINEL_STATE")) {
		t.Fatal(e)
	}
	if e = f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{}); e != nil {
		t.Fatal(e)
	}
	if e = f.b.DeleteCredential(testCtx, alice, c.ID); e != nil {
		t.Fatal(e)
	}
}

func TestExpirationCancellationAndBrowserLimits(t *testing.T) {
	f := newFixture(t)
	r := f.request(alice, "pat", "pending", "pending_request")
	for i := 0; i < 8; i++ {
		if _, _, e := f.b.OpenSession(r.ID, ""); e != nil {
			t.Fatal(e)
		}
	}
	if _, _, e := f.b.OpenSession(r.ID, ""); !errors.Is(e, ErrConflict) {
		t.Fatal("browser limit", e)
	}
	if e := f.b.CancelRequest(bob, r.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e := f.b.CancelRequest(alice, r.ID); e != nil {
		t.Fatal(e)
	}
	if e := f.b.CancelRequest(alice, r.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.b.OpenSession(r.ID, ""); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	r = f.request(alice, "pat", "expiring", "expiring_request")
	cookie := f.pair(alice, r)
	f.clock.add(16 * time.Minute)
	rr, _ := f.b.GetRequest(alice, r.ID)
	if rr.Status != "expired" {
		t.Fatal(rr)
	}
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	c := f.enroll(alice, "pat", "newconn")
	g, a := f.grant(alice, c, "pat", "bind1", "work1")
	l := f.lease(alice, g)
	if _, e := f.b.Acquire(alice, v1.AcquireLease{GrantID: g.ID, TTLSeconds: 301}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	f.clock.add(61 * time.Second)
	if _, e := f.b.InspectLease(a, l.ID); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	f.b.Sweep()
	if _, e := f.b.InspectLease(a, l.ID); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
}

func TestSharedOwnershipAndSeveralBindings(t *testing.T) {
	c := plainContract()
	c.AllowShared = true
	f := newFixture(t, c)
	admin := alice
	admin.ContextManager = true
	req := v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "orgconn", OnboardingID: "orgjob", IdempotencyKey: "organization", OwnerKind: "context"}
	if _, e := f.b.CreateRequest(testCtx, alice, req); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	r, e := f.b.CreateRequest(testCtx, admin, req)
	if e != nil {
		t.Fatal(e)
	}
	cookie := f.pair(admin, r)
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("org-pat")}); e != nil {
		t.Fatal(e)
	}
	r, _ = f.b.GetRequest(admin, r.ID)
	for i, a := range []identity.Actor{alice, bob} {
		in := grantInput(a, "pat", []string{"bind_a", "bind_b"}[i], "shared_worker")
		in.Execution = "shared"
		g, e := f.b.CreateGrant(admin, r.CredentialID, in)
		if e != nil {
			t.Fatal(e)
		}
		dup, e := f.b.CreateGrant(admin, r.CredentialID, in)
		if e != nil || dup.ID != g.ID {
			t.Fatal(e)
		}
		l := f.lease(a, g)
		ra := a
		ra.BindingID = in.BindingID
		ra.WorkloadID = in.WorkloadID
		m, e := f.b.Materialize(testCtx, ra, l.ID)
		if e != nil || m.Env["API_TOKEN"] != "org-pat" {
			t.Fatal(e)
		}
	}
	own := f.enroll(alice, "pat", "personal")
	in := grantInput(bob, "pat", "bob_personal", "newworker")
	if _, e = f.b.CreateGrant(alice, own.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("user shared credential", e)
	}
	in = grantInput(alice, "pat", "alice_other", "shared_worker")
	in.Execution = "shared"
	if _, e = f.b.CreateGrant(alice, own.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("mixed process credentials", e)
	}
	g, _ := f.grant(alice, own, "pat", "personal_one", "worker_one")
	_ = f.lease(alice, g)
	g, _ = f.grant(alice, own, "pat", "personal_two", "worker_two")
	_ = f.lease(alice, g)
	if _, e = f.b.GetCredential(bob, r.CredentialID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	managerBob := bob
	managerBob.ContextManager = true
	if _, e = f.b.GetCredential(managerBob, r.CredentialID); e != nil {
		t.Fatal(e)
	}
}

func TestRestartKeepsEnrollmentAndRejectsContractDrift(t *testing.T) {
	f := newFixture(t)
	r := f.request(alice, "pat", "pending", "persist_request")
	cookie := f.pair(alice, r)
	f.restart()
	view, again, e := f.b.OpenSession(r.ID, cookie)
	if e != nil || !view.Approved || again != cookie {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}); e != nil {
		t.Fatal(e)
	}
	cfg := f.cfg
	cfg.Contracts = []contract.Contract{plainContract()}
	cfg.Contracts[0].Deliveries[0].Target = "OTHER_TOKEN"
	if _, e = New(cfg); !errors.Is(e, ErrConflict) {
		t.Fatal("mutable contract revision", e)
	}
	if e = f.b.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.b.Close(); e != nil {
		t.Fatal(e)
	}
	if f.b.Healthy() {
		t.Fatal("closed health")
	}
	if _, e = f.b.GetRequest(alice, r.ID); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if _, _, e = f.b.OpenSession(r.ID, ""); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}

func TestRotationCannotResurrectRevokedCredential(t *testing.T) {
	f := newFixture(t)
	c := f.enroll(alice, "pat", "rotate")
	r, e := f.b.CreateRequest(testCtx, alice, v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "rotate", OnboardingID: "rotatejob", IdempotencyKey: "rotate_again", OwnerKind: "user", RotateCredentialID: c.ID})
	if e != nil {
		t.Fatal(e)
	}
	cookie := f.pair(alice, r)
	if e = f.b.RevokeCredential(alice, c.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("stale")}); !errors.Is(e, ErrConflict) {
		t.Fatal("stale form resurrected", e)
	}
	current, _ := f.b.GetCredential(alice, c.ID)
	if current.Status != "revoked" {
		t.Fatal(current)
	}
}

func TestDirectFormSkipsChannelApproval(t *testing.T) {
	f := newFixture(t)
	f.cfg.DirectForm = true
	f.restart()
	r := f.request(alice, "pat", "conn_direct", "create__direct")
	v, cookie, e := f.b.OpenSession(r.ID, "")
	if e != nil || !v.Approved || len(v.Fields) != 1 {
		t.Fatal("direct form must open pre-approved", v, e)
	}
	if e = f.b.Approve(alice, r.ID, v.Code); !errors.Is(e, ErrDenied) {
		t.Fatal("pre-approved session accepted a second approval", e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("SENTINEL_NOT_A_REAL_SECRET")}); e != nil {
		t.Fatal(e)
	}
	rr, e := f.b.GetRequest(alice, r.ID)
	if e != nil || rr.Status != "ready" || rr.CredentialID == "" {
		t.Fatal(rr, e)
	}
}
