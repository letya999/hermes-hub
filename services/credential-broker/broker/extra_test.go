package broker

import (
	"strings"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/provider"
)

func extraField() *contract.Extra {
	return &contract.Extra{
		Fields:     []contract.Field{{ID: "gitlab_project_id", Kind: "string", Label: "GITLAB_PROJECT_ID", Required: false, MaxBytes: 1024}},
		Deliveries: []contract.Delivery{{Type: "env", Field: "gitlab_project_id", Target: "GITLAB_PROJECT_ID"}},
	}
}

func TestExtraFieldsCollectAndDeliver(t *testing.T) {
	f := newFixture(t)
	in := v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "gitlab", OnboardingID: "onboard_1", IdempotencyKey: "extra_ok", OwnerKind: "user", Extra: extraField()}
	r, e := f.b.CreateRequest(testCtx, alice, in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.b.CreateRequest(testCtx, alice, in); e != nil {
		t.Fatal("extra request must stay idempotent", e)
	}
	cookie := f.pair(alice, r)
	v, _, e := f.b.OpenSession(r.ID, cookie)
	if e != nil || len(v.Fields) != 1 || len(v.Extra) != 1 || v.Extra[0].ID != "gitlab_project_id" || v.Extra[0].Required {
		t.Fatal("session must expose contract fields plus optional extras", v, e)
	}
	if !v.AnyRequired {
		t.Fatal("required contract field must flag the view")
	}
	values := provider.Bundle{"token": []byte("SENTINEL_NOT_A_REAL_SECRET"), "gitlab_project_id": []byte("42")}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), values, nil); e != nil {
		t.Fatal(e)
	}
	rr, e := f.b.GetRequest(alice, r.ID)
	if e != nil || rr.Status != "ready" {
		t.Fatal(rr, e)
	}
	c, e := f.b.GetCredential(alice, rr.CredentialID)
	if e != nil {
		t.Fatal(e)
	}
	g, a := f.grant(alice, c, "pat", "extra_bind", "extra_worker")
	l := f.lease(a, g)
	m, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil || m.Env["API_TOKEN"] != "SENTINEL_NOT_A_REAL_SECRET" || m.Env["GITLAB_PROJECT_ID"] != "42" {
		t.Fatal(m, e)
	}
	f.restart()
	l = f.lease(a, g)
	m, e = f.b.Materialize(testCtx, a, l.ID)
	if e != nil || m.Env["GITLAB_PROJECT_ID"] != "42" {
		t.Fatal("extras must survive restart", m, e)
	}
}

// Noisy upstream metadata can declare far more optional settings than the
// 32-field catalog ceiling — a gitlab-class connector lists ~50. Extras carry
// their own bound and must not fail the request.
func TestExtraFieldsBeyondCatalogCeiling(t *testing.T) {
	f := newFixture(t)
	extra := &contract.Extra{}
	for i := 0; i < 40; i++ {
		id := "opt_" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		extra.Fields = append(extra.Fields, contract.Field{ID: id, Kind: "string", Label: "OPT_" + id, MaxBytes: 64})
		extra.Deliveries = append(extra.Deliveries, contract.Delivery{Type: "env", Field: id, Target: "EXTRA_" + strings.ToUpper(id)})
	}
	in := v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "noisy", OnboardingID: "onboard_1", IdempotencyKey: "extra_many_ok", OwnerKind: "user", Extra: extra}
	r, e := f.b.CreateRequest(testCtx, alice, in)
	if e != nil {
		t.Fatal(e)
	}
	cookie := f.pair(alice, r)
	v, _, e := f.b.OpenSession(r.ID, cookie)
	if e != nil || len(v.Extra) != 40 {
		t.Fatal("session must expose every extra", len(v.Extra), e)
	}
	values := provider.Bundle{"token": []byte("SENTINEL_NOT_A_REAL_SECRET"), "opt_aa": []byte("1")}
	if e = f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), values, nil); e != nil {
		t.Fatal(e)
	}
	rr, e := f.b.GetRequest(alice, r.ID)
	if e != nil || rr.Status != "ready" {
		t.Fatal(rr, e)
	}
	c, e := f.b.GetCredential(alice, rr.CredentialID)
	if e != nil {
		t.Fatal(e)
	}
	g, a := f.grant(alice, c, "pat", "extra_bind40", "extra_worker40")
	m, e := f.b.Materialize(testCtx, a, f.lease(a, g).ID)
	if e != nil || m.Env["EXTRA_OPT_AA"] != "1" {
		t.Fatal(m, e)
	}
}
