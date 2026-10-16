package httpapi

import (
	"net/http"
	"testing"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/internal/safenet"
)

func TestHTTPDynamicContractRegistration(t *testing.T) {
	f := fixtureHTTP(t, apiContract(), safenet.Policy{}, nil)
	in := contract.Contract{ID: "dyn-test", Revision: 1, Title: "notion credentials", Fields: []contract.Field{{ID: "token", Kind: "secret", Label: "NOTION_TOKEN", Required: true, MaxBytes: 4096}}, Deliveries: []contract.Delivery{{Type: "env", Field: "token", Target: "NOTION_TOKEN"}}}
	if w := f.api("POST", "/v1/contracts", "broker:approve", actor, in); w.Code != http.StatusUnauthorized {
		t.Fatalf("approve audience must not register contracts: %d", w.Code)
	}
	if w := f.api("POST", "/v1/contracts", "broker:runtime", actor, in); w.Code != http.StatusUnauthorized {
		t.Fatalf("runtime audience must not register contracts: %d", w.Code)
	}
	w := f.api("POST", "/v1/contracts", "broker:control", actor, in)
	stored := parseResponse[contract.Contract](t, w, http.StatusCreated)
	if stored.Storage != "local" || stored.ID != "dyn-test" || stored.Revision != 1 {
		t.Fatalf("registered contract: %+v", stored)
	}
	w = f.api("POST", "/v1/contracts", "broker:control", actor, in)
	if w.Code != http.StatusCreated {
		t.Fatalf("idempotent re-registration: %d", w.Code)
	}
	other := in
	other.Title = "drifted"
	if w = f.api("POST", "/v1/contracts", "broker:control", actor, other); w.Code != http.StatusConflict {
		t.Fatalf("digest drift must conflict: %d", w.Code)
	}
	denied := in
	denied.ID = "dyn-denied"
	denied.AllowShared = true
	if w = f.api("POST", "/v1/contracts", "broker:control", actor, denied); w.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope contract must be denied: %d", w.Code)
	}
	bad := in
	bad.ID = "dyn-bad"
	bad.Deliveries = []contract.Delivery{{Type: "argv", Field: "token", Target: "--token"}}
	if w = f.api("POST", "/v1/contracts", "broker:control", actor, bad); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid delivery must fail: %d", w.Code)
	}
	w = f.api("GET", "/v1/contracts", "broker:control", actor, nil)
	list := parseResponse[[]contract.Contract](t, w, http.StatusOK)
	found := false
	for _, c := range list {
		if c.ID == "dyn-test" && c.Revision == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("dynamic contract missing from catalog listing")
	}
}
