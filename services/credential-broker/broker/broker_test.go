package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/internal/seal"
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
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("SENTINEL_NOT_A_REAL_SECRET")}, nil); e != nil {
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
func (f *fixture) checkpoint(a identity.Actor, g v1.Grant, session string) {
	f.t.Helper()
	l := f.lease(a, g)
	m, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil {
		f.t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(m.Mounts[1].Source, "runtime_session.json"), []byte(session), 0600); e != nil {
		f.t.Fatal(e)
	}
	if e = f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{Checkpoint: true, Quiesced: true}); e != nil {
		f.t.Fatal(e)
	}
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
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}, nil); !errors.Is(e, ErrDenied) {
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
	if e = f.b.Submit(testCtx, r.ID, cookie, "bad", provider.Bundle{"token": []byte("x")}, nil); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"unknown": []byte("x")}, nil); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("secret-value")}, nil); e != nil {
		t.Fatal(e)
	}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("other")}, nil); !errors.Is(e, ErrExpired) {
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
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("new-value")}, nil); e != nil {
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

// Each quiesced checkpoint writes a fresh provider object. State metadata
// must stay flat across cycles — ownership lives in object rows — and delete
// removes every checkpoint version, not just the last (SPEC-0087 R4.1).
func TestCheckpointMetadataGrowthIsLinear(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	for i := 0; i < 3; i++ {
		l := f.lease(alice, g)
		m, e := f.b.Materialize(testCtx, a, l.ID)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(m.Mounts[1].Source, "runtime_session.json"), []byte(fmt.Sprintf(`{"session":"v%d"}`, i)), 0600); e != nil {
			t.Fatal(e)
		}
		if e = f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{Checkpoint: true, Quiesced: true}); e != nil {
			t.Fatal(e)
		}
	}
	var rec stateRecord
	for _, s := range f.b.states {
		rec = s
	}
	if len(rec.OwnedRefs) > 1 {
		t.Fatalf("state metadata grew to %d refs after 3 checkpoints", len(rec.OwnedRefs))
	}
	if e := f.b.DeleteCredential(testCtx, alice, c.ID); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(filepath.Join(f.root, "secrets"))
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 0 {
		t.Fatalf("%d provider objects leaked after delete", len(entries))
	}
}

// crashWriter simulates a crash immediately after the provider write: the
// object lands on disk but the caller observes failure. With reservable
// refs the intent row is already durable, so the object stays owned.
type crashWriter struct {
	provider.Provider
	armed atomic.Bool
}

func (w *crashWriter) WriteRef(ctx context.Context, ref provider.Ref, b provider.Bundle) error {
	rw, ok := w.Provider.(provider.RefWriter)
	if !ok {
		return provider.ErrUnavailable
	}
	if e := rw.WriteRef(ctx, ref, b); e != nil {
		return e
	}
	if w.armed.Load() {
		return provider.ErrUnavailable
	}
	return nil
}

// A crash between the provider write and the activation commit must not
// orphan the object: the intent row persisted before the write still owns
// it after restart — it is a known owned object, never an unknown remote
// (SPEC-0087 R4.2).
func TestLocalCreateCrashBeforeActivationOwned(t *testing.T) {
	f := newFixture(t)
	w := &crashWriter{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	w.armed.Store(true)
	r := f.request(alice, "pat", "github", "crash_write")
	cookie := f.pair(alice, r)
	e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}, nil)
	if !errors.Is(e, ErrUnavailable) {
		t.Fatalf("submit after injected crash = %v", e)
	}
	f.restart()
	var intents int
	for _, o := range f.b.objects {
		if o.State == "intent" && o.OwnerKind == "request" && o.OwnerID == r.ID {
			intents++
		}
	}
	if intents != 1 {
		t.Fatalf("crashed write left %d owned intent rows, want 1", intents)
	}
	entries, e := os.ReadDir(filepath.Join(f.root, "secrets"))
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 1 {
		t.Fatalf("crashed write produced %d objects, want 1", len(entries))
	}
}

// On the success path the intent row reserved before the provider write is
// completed by the same activation commit — no "intent" state lingers after
// enrollment (SPEC-0087 R4 — intents resolve, they do not accumulate).
func TestSuccessfulWriteLeavesNoIntent(t *testing.T) {
	f := newFixture(t)
	c := f.enroll(alice, "pat", "github")
	var intents int
	var currents int
	for _, o := range f.b.objects {
		if o.State == "intent" {
			intents++
		}
		if o.OwnerID == c.ID && o.State == "current" {
			currents++
		}
	}
	if intents != 0 || currents != 1 {
		t.Fatalf("enrollment left %d intent / %d current rows, want 0/1", intents, currents)
	}
}

// A crash between the checkpoint provider write and the state commit must
// not orphan the snapshot object: the intent row owns it across restart
// (SPEC-0087 R4.2).
func TestCheckpointCrashKeepsObjectOwned(t *testing.T) {
	f := newFixture(t, stateContract())
	w := &crashWriter{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	l := f.lease(alice, g)
	m, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(m.Mounts[1].Source, "runtime_session.json"), []byte(`{"session":"v1"}`), 0600); e != nil {
		t.Fatal(e)
	}
	w.armed.Store(true)
	if e = f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{Checkpoint: true, Quiesced: true}); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("checkpoint after injected crash = %v", e)
	}
	f.restart()
	var intents int
	for _, o := range f.b.objects {
		if o.State == "intent" && o.OwnerKind == "state" && o.Op == "state.checkpoint" {
			intents++
		}
	}
	if intents != 1 {
		t.Fatalf("crashed checkpoint left %d owned intent rows, want 1", intents)
	}
}

// Same crash window on the Telegram session submit path: the signed
// session bundle reaches disk but the activation commit never runs. The
// request-owned intent row keeps the object owned (SPEC-0087 R4.2).
func TestTelegramCrashKeepsObjectOwned(t *testing.T) {
	c := contract.Contract{ID: "telegram-session", Revision: 2, Title: "Telegram", Storage: "local", Fields: []contract.Field{
		{ID: "api_id", Label: "API ID", Kind: "string", Required: true, MaxBytes: 20},
		{ID: "api_hash", Label: "API hash", Kind: "secret", Required: true, MaxBytes: 128},
		{ID: "session", Label: "Session", Kind: "secret", Required: true, MaxBytes: 8192},
		{ID: "expected_user_id", Label: "Account ID", Kind: "string", Required: true, MaxBytes: 20},
	}, Deliveries: []contract.Delivery{{Type: "env", Field: "session", Target: "TELEGRAM_SESSION_STRING"}}}
	f := newFixture(t, c)
	w := &crashWriter{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	r, e := f.b.CreateRequest(testCtx, alice, v1.CreateRequest{ContractID: "telegram-session", ContractRevision: 2, ConnectionID: "tg", OnboardingID: "onboard_1", IdempotencyKey: "tg_crash", OwnerKind: "user"})
	if e != nil {
		t.Fatal(e)
	}
	w.armed.Store(true)
	values := provider.Bundle{"api_id": []byte("123"), "api_hash": []byte("h"), "session": []byte("s"), "expected_user_id": []byte("42")}
	if e := f.b.SubmitTelegram(testCtx, alice, r.ID, values); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("telegram submit after injected crash = %v", e)
	}
	f.restart()
	var intents int
	for _, o := range f.b.objects {
		if o.State == "intent" && o.OwnerKind == "request" && o.OwnerID == r.ID && o.Op == "submit.telegram" {
			intents++
		}
	}
	if intents != 1 {
		t.Fatalf("crashed telegram submit left %d owned intent rows, want 1", intents)
	}
}

// Deletion is by-known-ref only: a provider object the broker never owned
// is never touched by credential delete, even when it shares the store
// (SPEC-0087 R4.2 — unknown remote objects are conservative keeps).
func TestUnknownRemoteObjectNeverDeleted(t *testing.T) {
	f := newFixture(t)
	foreign, e := f.cfg.Providers.Put(testCtx, "local", provider.Bundle{"token": []byte("foreign")})
	if e != nil {
		t.Fatal(e)
	}
	c := f.enroll(alice, "pat", "github")
	if e := f.b.DeleteCredential(testCtx, alice, c.ID); e != nil {
		t.Fatal(e)
	}
	got, e := f.cfg.Providers.Resolve(testCtx, foreign)
	if e != nil {
		t.Fatal("unknown remote object deleted:", e)
	}
	got.Wipe()
}

// A checkpoint version pinned by an active restore must survive any delete
// path: the pinned version stays resolvable and the credential is denied,
// never silently destroyed under active use (SPEC-0087 R4.2/R4.5).
func TestPinnedCheckpointVersionSurvivesGC(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	l := f.lease(alice, g)
	m, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(m.Mounts[1].Source, "runtime_session.json"), []byte(`{"session":"v1"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{Checkpoint: true, Quiesced: true}); e != nil {
		t.Fatal(e)
	}
	pinned := f.b.states[seal.Digest(c.ID+"/"+a.ContextID+"/"+g.BindingID)].Ref
	// A new lease restores from v1 — that version is pinned for the lease.
	l = f.lease(alice, g)
	if _, e = f.b.Materialize(testCtx, a, l.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.b.DeleteCredential(testCtx, alice, c.ID); !errors.Is(e, ErrConflict) {
		t.Fatalf("delete under pinned object = %v, want ErrConflict", e)
	}
	got, e := f.b.cfg.Providers.Resolve(testCtx, pinned)
	if e != nil {
		t.Fatal("pinned checkpoint version deleted:", e)
	}
	got.Wipe()
}

// Dropping a lease must release its uses' pins even if Finish is never
// called — otherwise a stale pin would deny credential deletion forever.
func TestDropLeaseReleasesUsePins(t *testing.T) {
	f := newFixture(t)
	c := f.enroll(alice, "pat", "github")
	g, a := f.grant(alice, c, "pat", "binding1", "workload1")
	l := f.lease(alice, g)
	if _, e := f.b.BeginUse(testCtx, a, l.ID); e != nil {
		t.Fatal(e)
	}
	if e := f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{}); e != nil {
		t.Fatal(e)
	}
	if e := f.b.DeleteCredential(testCtx, alice, c.ID); e != nil {
		t.Fatalf("stale use pin denied delete: %v", e)
	}
}

// An unchanged quiesced checkpoint must not mint a new provider object:
// digest-equal snapshots keep the same ref and no extra object row appears
// (SPEC-0087 R4 — versions only on real content change).
func TestUnchangedQuiescedCheckpointDoesNotCreateVersion(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	f.checkpoint(a, g, `{"session":"same"}`)
	first := f.b.states[seal.Digest(c.ID+"/"+a.ContextID+"/"+g.BindingID)]
	f.checkpoint(a, g, `{"session":"same"}`)
	second := f.b.states[seal.Digest(c.ID+"/"+a.ContextID+"/"+g.BindingID)]
	if first.Ref != second.Ref {
		t.Fatalf("unchanged checkpoint created new version %v, want %v", second.Ref, first.Ref)
	}
	stateObjs := 0
	for _, o := range f.b.objects {
		if o.OwnerKind == "state" {
			stateObjs++
		}
	}
	if stateObjs != 1 {
		t.Fatalf("unchanged checkpoint left %d state object rows, want 1", stateObjs)
	}
}

// A changed checkpoint activates the replacement first and only then retires
// the superseded version — the old row flips to retired, never vanishing
// before the new ref is durable (SPEC-0087 R4 — activate before retire).
func TestChangedCheckpointRetiresPreviousVersion(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	checkpoint := func(session string) { f.checkpoint(a, g, session) }
	key := seal.Digest(c.ID + "/" + a.ContextID + "/" + g.BindingID)
	checkpoint(`{"session":"one"}`)
	firstID := objectID(f.b.states[key].Ref)
	checkpoint(`{"session":"two"}`)
	secondID := objectID(f.b.states[key].Ref)
	if firstID == secondID {
		t.Fatal("changed checkpoint must create a new version")
	}
	if f.b.objects[firstID].State != "retired" {
		t.Fatalf("superseded version state = %q, want retired", f.b.objects[firstID].State)
	}
	if f.b.objects[secondID].State != "current" {
		t.Fatalf("new version state = %q, want current", f.b.objects[secondID].State)
	}
}

// Superseding a version whose own restore pin is still held must not
// physically delete it mid-checkpoint: after release the row rests at
// "retired" and the provider object stays resolvable until the cleanup
// worker owns deletion (SPEC-0087 R4 — retirement is a ledger label).
func TestSupersededCheckpointStaysResolvable(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	checkpoint := func(session string) { f.checkpoint(a, g, session) }
	key := seal.Digest(c.ID + "/" + a.ContextID + "/" + g.BindingID)
	checkpoint(`{"session":"one"}`)
	first := f.b.states[key].Ref
	checkpoint(`{"session":"two"}`)
	if f.b.objects[objectID(first)].State != "retired" {
		t.Fatalf("superseded version state = %q, want retired", f.b.objects[objectID(first)].State)
	}
	if _, e := os.Stat(filepath.Join(f.root, "secrets", first.Locator)); e != nil {
		t.Fatalf("retired version physically deleted before cleanup: %v", e)
	}
}

// failDelete refuses provider deletions until released — the sweep must
// keep the object durably pending-delete, never silently unowned.
type failDelete struct {
	provider.Provider
	fail atomic.Bool
}

func (w *failDelete) Delete(ctx context.Context, r provider.Ref) error {
	if w.fail.Load() {
		return provider.ErrUnavailable
	}
	return w.Provider.Delete(ctx, r)
}

// A failed physical delete leaves the row pending-delete and durable: the
// sweep reports denial, the object stays owned, and a retry under backoff
// is skipped until the clock passes (SPEC-0087 R4.6).
func TestDeleteFailureRemainsPendingAndDenied(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	checkpoint := func(session string) { f.checkpoint(a, g, session) }
	key := seal.Digest(c.ID + "/" + a.ContextID + "/" + g.BindingID)
	checkpoint(`{"session":"one"}`)
	retiredID := objectID(f.b.states[key].Ref)
	checkpoint(`{"session":"two"}`)
	w := &failDelete{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	w.fail.Store(true)
	if _, e := f.b.SweepObjects(testCtx, a, 10); e == nil {
		t.Fatal("sweep over failing delete must report denial")
	}
	f.restart()
	if f.b.objects[retiredID].State != "pending-delete" {
		t.Fatalf("failed delete left %q, want durable pending-delete", f.b.objects[retiredID].State)
	}
	// Backoff: an immediate retry does not touch the object again.
	if n, _ := f.b.SweepObjects(testCtx, a, 10); n != 0 {
		t.Fatalf("backoff-less retry re-attempted %d objects", n)
	}
	// After the backoff window the retry succeeds and resolves the row.
	f.b.cfg.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	w.fail.Store(false)
	if _, e := f.b.SweepObjects(testCtx, a, 10); e != nil {
		t.Fatalf("retry after backoff: %v", e)
	}
	if f.b.objects[retiredID].State != "deleted" {
		t.Fatalf("retried delete left %q, want deleted", f.b.objects[retiredID].State)
	}
}

// The deleted receipt is only committed after the provider has removed
// every copy: primary and recovery files are gone before the object row
// flips to "deleted" (SPEC-0087 R4.6 — cleanup both copies + sync first).
func TestCleanupBothCopiesBeforeReceipt(t *testing.T) {
	f := newFixture(t, stateContract())
	rec, e := provider.NewRecoverableLocal("local", filepath.Join(f.root, "secrets"), filepath.Join(f.root, "recovery"), bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{9}, 32))
	if e != nil {
		t.Fatal(e)
	}
	f.cfg.Providers["local"] = rec
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	checkpoint := func(session string) { f.checkpoint(a, g, session) }
	key := seal.Digest(c.ID + "/" + a.ContextID + "/" + g.BindingID)
	checkpoint(`{"session":"one"}`)
	retired := f.b.states[key].Ref
	checkpoint(`{"session":"two"}`)
	if _, e := os.Stat(filepath.Join(f.root, "secrets", retired.Locator)); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(f.root, "recovery", retired.Locator)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.b.SweepObjects(testCtx, a, 10); e != nil {
		t.Fatal(e)
	}
	if f.b.objects[objectID(retired)].State != "deleted" {
		t.Fatalf("object state = %q, want deleted", f.b.objects[objectID(retired)].State)
	}
	for _, dir := range []string{"secrets", "recovery"} {
		if _, e := os.Stat(filepath.Join(f.root, dir, retired.Locator)); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("%s copy survived receipt: %v", dir, e)
		}
	}
}

// The sweep deletes only exact owned refs in its queue: objects owned by
// another credential — and the still-current version of the same owner —
// are never touched (SPEC-0087 R4.6 — exact known refs only).
func TestAliasAndSharedCredentialUntouched(t *testing.T) {
	f := newFixture(t, stateContract())
	ca := f.enroll(alice, "session", "service")
	cb := f.enroll(alice, "session", "service-b")
	ga, a := f.grant(alice, ca, "session", "state_binding", "state_workload")
	gb, ab := f.grant(alice, cb, "session", "state_binding_b", "state_workload_b")
	keyA := seal.Digest(ca.ID + "/" + a.ContextID + "/" + ga.BindingID)
	f.checkpoint(a, ga, `{"session":"one"}`)
	retiredA := f.b.states[keyA].Ref
	f.checkpoint(a, ga, `{"session":"two"}`)
	currentA := f.b.states[keyA].Ref
	f.checkpoint(ab, gb, `{"session":"bee"}`)
	currentB := f.b.states[seal.Digest(cb.ID+"/"+ab.ContextID+"/"+gb.BindingID)].Ref
	if _, e := f.b.SweepObjects(testCtx, a, 10); e != nil {
		t.Fatal(e)
	}
	for _, keep := range []provider.Ref{currentA, currentB} {
		if f.b.objects[objectID(keep)].State != "current" {
			t.Fatalf("unrelated object state = %q, want current", f.b.objects[objectID(keep)].State)
		}
		if _, e := os.Stat(filepath.Join(f.root, "secrets", keep.Locator)); e != nil {
			t.Fatalf("unrelated object deleted: %v", e)
		}
	}
	if f.b.objects[objectID(retiredA)].State != "deleted" {
		t.Fatalf("queued object state = %q, want deleted", f.b.objects[objectID(retiredA)].State)
	}
}

// An abandoned owned intent is freed only through its ownership row:
// once the owning request is terminal, the sweep collects the crash-owned
// object by its exact ref — never by directory scanning (SPEC-0087 R4.6).
func TestAbandonedIntentFreedByOwnership(t *testing.T) {
	f := newFixture(t)
	w := &crashWriter{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	w.armed.Store(true)
	r := f.request(alice, "pat", "github", "abandoned_intent")
	cookie := f.pair(alice, r)
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}, nil); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	w.armed.Store(false)
	if e := f.b.CancelRequest(alice, r.ID); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(filepath.Join(f.root, "secrets"))
	if e != nil || len(entries) != 1 {
		t.Fatalf("abandoned object missing: %v %d", e, len(entries))
	}
	if _, e := f.b.SweepObjects(testCtx, alice, 10); e != nil {
		t.Fatal(e)
	}
	entries, e = os.ReadDir(filepath.Join(f.root, "secrets"))
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 0 {
		t.Fatalf("%d abandoned objects survived sweep", len(entries))
	}
	var intents int
	for _, o := range f.b.objects {
		if o.State != "deleted" {
			intents++
		}
	}
	if intents != 0 {
		t.Fatalf("%d non-deleted object rows after sweep", intents)
	}
}

// A draft owned by an expired — not canceled — request is likewise an
// abandoned owned object: expiry also frees it through ownership
// (SPEC-0087 R4.6 — expired drafts/verifiers via ownership only).
func TestExpiredDraftFreedByOwnership(t *testing.T) {
	f := newFixture(t)
	w := &crashWriter{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	w.armed.Store(true)
	r := f.request(alice, "pat", "github", "expired_draft")
	cookie := f.pair(alice, r)
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}, nil); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	w.armed.Store(false)
	f.clock.at = f.clock.at.Add(48 * time.Hour)
	if _, e := f.b.SweepObjects(testCtx, alice, 10); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(filepath.Join(f.root, "secrets"))
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 0 {
		t.Fatalf("%d expired-draft objects survived sweep", len(entries))
	}
}

// The periodic maintenance sweep — not only an explicit call — collects
// retired objects within its bounded batch (SPEC-0087 R4.6: the worker
// runs inside the existing maintenance tick).
func TestMaintenanceSweepCollectsObjects(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	checkpoint := func(session string) { f.checkpoint(a, g, session) }
	key := seal.Digest(c.ID + "/" + a.ContextID + "/" + g.BindingID)
	checkpoint(`{"session":"one"}`)
	retired := f.b.states[key].Ref
	checkpoint(`{"session":"two"}`)
	f.b.Sweep()
	if f.b.objects[objectID(retired)].State != "deleted" {
		t.Fatalf("maintenance sweep left %q, want deleted", f.b.objects[objectID(retired)].State)
	}
}

// The worker is bounded: a sweep processes at most `limit` queued
// objects per pass, so a backlog drains incrementally rather than in one
// unbounded burst (SPEC-0087 R4.6).
func TestSweepHonorsBatchLimit(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "state_binding", "state_workload")
	f.checkpoint(a, g, `{"session":"one"}`)
	f.checkpoint(a, g, `{"session":"two"}`)
	f.checkpoint(a, g, `{"session":"three"}`)
	if n, e := f.b.SweepObjects(testCtx, a, 1); e != nil || n != 1 {
		t.Fatalf("sweep limit 1 processed %d, err %v", n, e)
	}
	var deleted, remaining int
	for _, o := range f.b.objects {
		switch o.State {
		case "deleted":
			deleted++
		case "pending-delete", "retired":
			remaining++
		}
	}
	if deleted != 1 || remaining != 1 {
		t.Fatalf("after bounded sweep deleted=%d pending/retired=%d, want 1/1", deleted, remaining)
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
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}, nil); !errors.Is(e, ErrExpired) {
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
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("org-pat")}, nil); e != nil {
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
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}, nil); e != nil {
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
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("stale")}, nil); !errors.Is(e, ErrConflict) {
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
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("SENTINEL_NOT_A_REAL_SECRET")}, nil); e != nil {
		t.Fatal(e)
	}
	rr, e := f.b.GetRequest(alice, r.ID)
	if e != nil || rr.Status != "ready" || rr.CredentialID == "" {
		t.Fatal(rr, e)
	}
}

func TestConsumerIDDiscriminator(t *testing.T) {
	f := newFixture(t)
	create := func(consumer, idem string) (v1.Request, error) {
		return f.b.CreateRequest(testCtx, alice, v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "ssh", OnboardingID: "onboard_1", IdempotencyKey: idem, OwnerKind: "user", ConsumerID: consumer})
	}
	enrollReq := func(r v1.Request) v1.Credential {
		f.t.Helper()
		cookie := f.pair(alice, r)
		if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("k")}, nil); e != nil {
			f.t.Fatal(e)
		}
		rr, e := f.b.GetRequest(alice, r.ID)
		if e != nil || rr.Status != "ready" {
			f.t.Fatal(e)
		}
		c, e := f.b.GetCredential(alice, rr.CredentialID)
		if e != nil {
			f.t.Fatal(e)
		}
		return c
	}
	r1, e := create("ssh-box", "consumer_a")
	if e != nil {
		t.Fatal(e)
	}
	box := enrollReq(r1)
	if box.ConsumerID != "ssh-box" {
		t.Fatal(box.ConsumerID)
	}
	r2, e := create("ssh-git", "consumer_b")
	if e != nil {
		t.Fatal(e)
	}
	git := enrollReq(r2)
	if git.ConsumerID != "ssh-git" || git.ID == box.ID {
		t.Fatal("second consumer credential not distinct", git, box)
	}
	if _, e := create("ssh-box", "consumer_c"); !errors.Is(e, ErrConflict) {
		t.Fatal("same consumer must conflict", e)
	}
	r3, e := create("", "consumer_d")
	if e != nil {
		t.Fatal(e)
	}
	def := enrollReq(r3)
	if def.ConsumerID != "" {
		t.Fatal(def.ConsumerID)
	}
	if _, e := create("", "consumer_e"); !errors.Is(e, ErrConflict) {
		t.Fatal("second unlabeled credential must conflict", e)
	}
	if _, e := f.b.CreateRequest(testCtx, alice, v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "ssh", OnboardingID: "onboard_1", IdempotencyKey: "consumer_bad", OwnerKind: "user", ConsumerID: "Bad ID!"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("consumer must pass canonical ID", e)
	}
	if _, e := f.b.CreateRequest(testCtx, alice, v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "ssh", OnboardingID: "onboard_1", IdempotencyKey: "consumer_rot", OwnerKind: "user", ConsumerID: "ssh-git", RotateCredentialID: box.ID}); !errors.Is(e, ErrDenied) {
		t.Fatal("rotate must not retarget a different consumer", e)
	}
	r, e := f.b.CreateRequest(testCtx, alice, v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "ssh", OnboardingID: "onboard_1", IdempotencyKey: "consumer_rot2", OwnerKind: "user", ConsumerID: "ssh-box", RotateCredentialID: box.ID})
	if e != nil {
		t.Fatal("rotate with matching consumer must pass", e)
	}
	if r.ConsumerID != "ssh-box" {
		t.Fatal(r.ConsumerID)
	}
}
