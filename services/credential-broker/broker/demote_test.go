package broker

import (
	"errors"
	"testing"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/provider"
)

func pairContract() contract.Contract {
	c := plainContract()
	c.Fields = append(c.Fields, contract.Field{ID: "project", Kind: "string", Label: "Project", Required: true, MaxBytes: 1024})
	c.Deliveries = append(c.Deliveries, contract.Delivery{Type: "env", Field: "project", Target: "PROJECT_ID"})
	return c
}

func TestDemotedRequiredFieldSkipsAndMaterializes(t *testing.T) {
	f := newFixture(t, pairContract())
	r := f.request(alice, "pat", "gitlab", "demote_pair")
	cookie := f.pair(alice, r)
	values := provider.Bundle{"token": []byte("SENTINEL_NOT_A_REAL_SECRET")}
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), values, nil); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), values, map[string]bool{"project": true}); e != nil {
		t.Fatal("owner-marked optional field must be accepted", e)
	}
	rr, _ := f.b.GetRequest(alice, r.ID)
	c, e := f.b.GetCredential(alice, rr.CredentialID)
	if e != nil {
		t.Fatal(e)
	}
	g, a := f.grant(alice, c, "pat", "demote_bind", "demote_worker")
	l := f.lease(a, g)
	m, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil || m.Env["API_TOKEN"] != "SENTINEL_NOT_A_REAL_SECRET" {
		t.Fatal(m, e)
	}
	if _, ok := m.Env["PROJECT_ID"]; ok {
		t.Fatal("skipped field must not materialize")
	}
	f.restart()
	l = f.lease(a, g)
	m, e = f.b.Materialize(testCtx, a, l.ID)
	if e != nil {
		t.Fatal("demotion must survive restart", e)
	}
	if _, ok := m.Env["PROJECT_ID"]; ok {
		t.Fatal("demoted field must stay absent after restart")
	}
}
