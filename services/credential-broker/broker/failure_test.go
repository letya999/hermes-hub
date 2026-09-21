package broker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/provider"
)

type faultProvider struct {
	provider.Provider
	readFail, writeFail, deleteFail bool
}

func (p *faultProvider) Read(ctx context.Context, r provider.Ref) (provider.Bundle, error) {
	if p.readFail {
		return nil, provider.ErrUnavailable
	}
	return p.Provider.Read(ctx, r)
}
func (p *faultProvider) Write(ctx context.Context, v provider.Bundle) (provider.Ref, error) {
	if p.writeFail {
		return provider.Ref{}, provider.ErrUnavailable
	}
	return p.Provider.Write(ctx, v)
}
func (p *faultProvider) Delete(ctx context.Context, r provider.Ref) error {
	if p.deleteFail {
		return provider.ErrUnavailable
	}
	return p.Provider.Delete(ctx, r)
}
func TestExternalAliasAndBackendFailures(t *testing.T) {
	f := newFixture(t)
	p := &faultProvider{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = p
	ref, e := p.Write(testCtx, provider.Bundle{"token": []byte("external")})
	if e != nil {
		t.Fatal(e)
	}
	f.cfg.ExternalAliases["preexisting"] = ExternalAlias{Ref: ref, PrincipalID: "alice", ContextID: "team", ContractKey: "pat@1"}
	in := v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "imported", OnboardingID: "importjob", IdempotencyKey: "imported_request", OwnerKind: "user", ExternalAlias: "preexisting"}
	if _, e = f.b.CreateRequest(testCtx, bob, in); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	p.readFail = true
	if _, e = f.b.CreateRequest(testCtx, alice, in); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	p.readFail = false
	r, e := f.b.CreateRequest(testCtx, alice, in)
	if e != nil || r.Status != "ready" {
		t.Fatal(e)
	}
	if e = f.b.DeleteCredential(testCtx, alice, r.CredentialID); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Read(testCtx, ref); e != nil {
		t.Fatal("deletion touched external secret", e)
	}
	r = f.request(alice, "pat", "input", "input_request")
	cookie := f.pair(alice, r)
	p.writeFail = true
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	p.writeFail = false
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("x")}); e != nil {
		t.Fatal(e)
	}
	r, _ = f.b.GetRequest(alice, r.ID)
	c, _ := f.b.GetCredential(alice, r.CredentialID)
	g, a := f.grant(alice, c, "pat", "binding1", "workload1")
	l := f.lease(alice, g)
	p.readFail = true
	if _, e = f.b.Materialize(testCtx, a, l.ID); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	p.readFail = false
	p.deleteFail = true
	if e = f.b.DeleteCredential(testCtx, alice, c.ID); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	v, _ := f.b.GetCredential(alice, c.ID)
	if v.Status != "revoked" {
		t.Fatal("failed deletion not fenced")
	}
	p.deleteFail = false
	if e = f.b.DeleteCredential(testCtx, alice, c.ID); e != nil {
		t.Fatal(e)
	}
}
func TestInvalidInputsAndMissingObjects(t *testing.T) {
	f := newFixture(t)
	input := v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "conn", OnboardingID: "job", IdempotencyKey: "some_request", OwnerKind: "user"}
	changes := []func(*v1.CreateRequest){func(i *v1.CreateRequest) { i.ConnectionID = "../x" }, func(i *v1.CreateRequest) { i.OnboardingID = "" }, func(i *v1.CreateRequest) { i.ContractID = "missing" }, func(i *v1.CreateRequest) { i.IdempotencyKey = "short" }, func(i *v1.CreateRequest) { i.OwnerKind = "global" }, func(i *v1.CreateRequest) { i.RotateCredentialID = "nonexistent" }, func(i *v1.CreateRequest) { i.ExternalAlias = "none" }}
	for _, change := range changes {
		x := input
		change(&x)
		if _, e := f.b.CreateRequest(testCtx, alice, x); e == nil {
			t.Fatal(x)
		}
	}
	if _, _, e := f.b.OpenSession("missing", ""); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e := f.b.CancelRequest(alice, "missing"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e := f.b.Approve(alice, "missing", "x"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e := f.b.RevokeGrant(alice, "missing"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e := f.b.RevokeCredential(alice, "missing"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if _, e := f.b.CreateGrant(alice, "missing", v1.GrantRequest{}); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	c := f.enroll(alice, "pat", "service")
	gi := grantInput(alice, "pat", "binding1", "workload1")
	for _, change := range []func(*v1.GrantRequest){func(g *v1.GrantRequest) { g.BindingID = "../x" }, func(g *v1.GrantRequest) { g.ContextID = "another" }, func(g *v1.GrantRequest) { g.Execution = "global" }, func(g *v1.GrantRequest) { g.ContractID = "missing" }, func(g *v1.GrantRequest) { g.Execution = "shared" }, func(g *v1.GrantRequest) { g.IdempotencyKey = "short" }} {
		x := gi
		change(&x)
		if _, e := f.b.CreateGrant(alice, c.ID, x); e == nil {
			t.Fatal(x)
		}
	}
	g, a := f.grant(alice, c, "pat", "binding1", "workload1")
	gi.WorkloadID = "workload2"
	if _, e := f.b.CreateGrant(alice, c.ID, gi); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	gi.IdempotencyKey = "another_grant"
	if _, e := f.b.CreateGrant(alice, c.ID, gi); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	l := f.lease(alice, g)
	uses := make([]Use, 0, 16)
	for i := 0; i < 16; i++ {
		u, e := f.b.BeginUse(testCtx, a, l.ID)
		if e != nil {
			t.Fatal(e)
		}
		uses = append(uses, u)
	}
	if _, e := f.b.BeginUse(testCtx, a, l.ID); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	for _, u := range uses {
		u.Finish()
	}
	if _, e := f.b.Renew(a, "missing", 120); e == nil {
		t.Fatal("missing renew")
	}
	if e := f.b.Release(testCtx, a, "missing", v1.RuntimeRelease{}); e == nil {
		t.Fatal("missing release")
	}
	if e := f.b.RevokeCredential(alice, c.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.b.CreateGrant(alice, c.ID, gi); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	ev := f.b.Events(alice, 0, 1)
	if len(ev.Events) != 1 {
		t.Fatal(ev)
	}
	ev = f.b.Events(alice, ev.NextCursor, 10000)
	if len(ev.Events) == 0 {
		t.Fatal(ev)
	}
}
func TestNewValidationAndCorruptLedger(t *testing.T) {
	f := newFixture(t)
	for _, change := range []func(*Config){func(c *Config) { c.PublicOrigin = "http://internet.example" }, func(c *Config) { c.PublicOrigin = "https://x/?q=x" }, func(c *Config) { c.SessionKey = nil }, func(c *Config) { c.Contracts = nil }, func(c *Config) { c.Contracts = []contract.Contract{plainContract(), plainContract()} }, func(c *Config) { c.Contracts = []contract.Contract{oauthContract()} }} {
		cfg := f.cfg
		change(&cfg)
		if _, e := New(cfg); e == nil {
			t.Fatal("bad configuration")
		}
	}
	if _, e := f.cfg.Journal.Append("other", map[string]string{"x": "y"}); e != nil {
		t.Fatal(e)
	}
	if _, e := New(f.cfg); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}
func TestFailClosedJournalAndStateFilesystem(t *testing.T) {
	f := newFixture(t, stateContract())
	c := f.enroll(alice, "session", "service")
	g, a := f.grant(alice, c, "session", "binding1", "workload1")
	l := f.lease(alice, g)
	m, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(m.Mounts[1].Source, "runtime_session.json")
	if e = os.Symlink("/etc/passwd", p); e != nil {
		t.Fatal(e)
	}
	if e = f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{Checkpoint: true, Quiesced: true}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e = f.cfg.Journal.Close(); e != nil {
		t.Fatal(e)
	}
	if f.b.Healthy() {
		t.Fatal("closed journal")
	}
	if _, e = f.b.Acquire(alice, v1.AcquireLease{GrantID: g.ID}); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = f.b.CreateRequest(testCtx, alice, v1.CreateRequest{}); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = f.b.CreateGrant(alice, c.ID, v1.GrantRequest{}); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = f.b.GetCredential(alice, c.ID); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if e = f.b.RevokeCredential(alice, c.ID); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if e = f.b.Approve(alice, f.b.requests[firstRequest(f)].View.ID, "anything"); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	f.clock.add(2 * time.Minute)
	f.b.Sweep()
}
func firstRequest(f *fixture) string {
	for id := range f.b.requests {
		return id
	}
	return ""
}
