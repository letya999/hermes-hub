package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/internal/safenet"
	"github.com/letya999/credential-broker/provider"
)

func TestBrowserFormMarksRequiredAndRendersExtras(t *testing.T) {
	f := fixtureHTTP(t, apiContract(), safenet.Policy{}, nil)
	w := f.api("POST", "/v1/requests", "broker:control", actor, v1.CreateRequest{
		ContractID: "pat", ContractRevision: 1, ConnectionID: "gitlab", OnboardingID: "onboard1",
		IdempotencyKey: "extra_form", OwnerKind: "user",
		Extra: &contract.Extra{
			Fields:     []contract.Field{{ID: "gitlab_project_id", Kind: "string", Label: "GITLAB_PROJECT_ID", MaxBytes: 1024}},
			Deliveries: []contract.Delivery{{Type: "env", Field: "gitlab_project_id", Target: "GITLAB_PROJECT_ID"}},
		},
	})
	r := parseResponse[v1.Request](t, w, http.StatusCreated)
	cookie, csrf := f.pair(r)
	req := f.request("GET", "/connect/"+r.ID+"?lang=ru", "", actor, nil)
	req.AddCookie(cookie)
	w = f.serve(req)
	body := w.Body.String()
	for _, want := range []string{"обязательное", "необязательное", `name="opt:token"`, "gitlab_project_id", "GITLAB_PROJECT_ID", "formnovalidate"} {
		if !strings.Contains(body, want) {
			t.Fatalf("form misses %q:\n%s", want, body)
		}
	}
	// The owner ticks "field is not required" and submits without the value.
	w = f.submit(r, cookie, csrf, provider.Bundle{"opt:token": []byte("1")})
	if w.Code != http.StatusOK {
		t.Fatalf("opt-out submit rejected: %d %s", w.Code, w.Body)
	}
}

// The opt-out checkbox only helps if the paired required input stops blocking
// the normal submit button. Inline script is CSP-forbidden, so the page must
// pull a same-origin /form.js that releases the required flag, and the CSP
// must list script-src 'self' to allow it (observed live: ticking the box
// still left the browser demanding the field).
func TestBrowserDemoteCheckboxReleasesRequired(t *testing.T) {
	f := fixtureHTTP(t, apiContract(), safenet.Policy{}, nil)
	w := f.api("POST", "/v1/requests", "broker:control", actor, v1.CreateRequest{
		ContractID: "pat", ContractRevision: 1, ConnectionID: "gitlab", OnboardingID: "onboard1",
		IdempotencyKey: "demote_js", OwnerKind: "user",
	})
	r := parseResponse[v1.Request](t, w, http.StatusCreated)
	cookie, _ := f.pair(r)
	req := f.request("GET", "/connect/"+r.ID, "", actor, nil)
	req.AddCookie(cookie)
	w = f.serve(req)
	if !strings.Contains(w.Body.String(), `src="/form.js"`) {
		t.Fatalf("form does not load the demote script:\n%s", w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP blocks the same-origin demote script: %s", csp)
	}
	w = f.serve(f.request("GET", "/form.js", "", actor, nil))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(w.Body.String(), "required") {
		t.Fatalf("demote script not served: %d %s", w.Code, w.Body)
	}
}

// A form carrying the full legitimate budget must submit: 32 contract fields,
// 32 opt-out controls, 64 request-scoped extras and the CSRF part — the old
// 33-part cap made a noisy upstream definition (gitlab-mcp shows ~30 extras)
// un-submittable with invalid_request on every attempt (observed live).
func TestBrowserFormSubmitsFullExtrasSet(t *testing.T) {
	c := apiContract()
	for i := 1; i < 32; i++ {
		id := fmt.Sprintf("field_%02d", i)
		c.Fields = append(c.Fields, contract.Field{ID: id, Kind: "string", Label: id, Required: true, MaxBytes: 64})
		c.Deliveries = append(c.Deliveries, contract.Delivery{Type: "env", Field: id, Target: "FIELD_" + fmt.Sprintf("%02d", i)})
	}
	f := fixtureHTTP(t, c, safenet.Policy{}, nil)
	extra := &contract.Extra{}
	values := provider.Bundle{"token": []byte("v"), "opt:token": []byte("1")}
	for i := 1; i < 32; i++ {
		id := fmt.Sprintf("field_%02d", i)
		values[id] = []byte("")
		values["opt:"+id] = []byte("1")
	}
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("extra_%02d", i)
		extra.Fields = append(extra.Fields, contract.Field{ID: id, Kind: "string", Label: id, MaxBytes: 64})
		extra.Deliveries = append(extra.Deliveries, contract.Delivery{Type: "env", Field: id, Target: "EXTRA_" + fmt.Sprintf("%02d", i)})
		values[id] = []byte("x")
	}
	w := f.api("POST", "/v1/requests", "broker:control", actor, v1.CreateRequest{
		ContractID: "pat", ContractRevision: 1, ConnectionID: "gitlab", OnboardingID: "onboard1",
		IdempotencyKey: "extra_full", OwnerKind: "user", Extra: extra,
	})
	r := parseResponse[v1.Request](t, w, http.StatusCreated)
	cookie, csrf := f.pair(r)
	if w := f.submit(r, cookie, csrf, values); w.Code != http.StatusOK {
		t.Fatalf("full extras submit rejected: %d %s", w.Code, w.Body)
	}
}
