package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/broker"
	"github.com/letya999/credential-broker/internal/safenet"
)

func TestBrowserLangResolution(t *testing.T) {
	cases := []struct {
		query, header, want string
	}{
		{"", "", "en"},
		{"", "kk", "kk"},
		{"", "es-ES,es;q=0.9", "es"},
		{"", "fr,es;q=0.9,ru;q=0.5", "es"},
		{"", "es;q=0.1,ru;q=0.9", "ru"},
		{"", "zh-CN", "en"},
		{"kk", "es", "kk"},
		{"fr", "ru", "ru"},
		{"ru-RU", "", "ru"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/connect/x?lang="+tc.query, nil)
		r.Header.Set("Accept-Language", tc.header)
		if got := browserLang(r); got != tc.want {
			t.Errorf("lang=%q al=%q got %q want %q", tc.query, tc.header, got, tc.want)
		}
	}
}

// The /connect page renders in the negotiated language and carries ?lang=
// through its internal links and form actions so the session keeps the same
// language after approval and submit.
func TestBrowserFormLocalizedAndPropagatesLang(t *testing.T) {
	f := fixtureHTTP(t, apiContract(), safenet.Policy{}, nil)
	w := f.api("POST", "/v1/requests", "broker:control", actor, v1.CreateRequest{
		ContractID: "pat", ContractRevision: 1, ConnectionID: "gitlab", OnboardingID: "onboard1",
		IdempotencyKey: "es_form_key", OwnerKind: "user",
	})
	r := parseResponse[v1.Request](t, w, http.StatusCreated)

	// Unapproved pairing page in Spanish.
	req := f.request("GET", "/connect/"+r.ID+"?lang=es", "", actor, nil)
	w = f.serve(req)
	body := w.Body.String()
	for _, want := range []string{`lang="es"`, "Confirma este código", "/connect/" + r.ID + "?lang=es"} {
		if !strings.Contains(body, want) {
			t.Fatalf("pairing page misses %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Подтвердите") {
		t.Fatal("pairing page leaked Russian copy")
	}
	// Accept-Language negotiation on the post-approval form: pair approves
	// its own session cookie.
	cookie, _ := f.pair(r)
	req = f.request("GET", "/connect/"+r.ID, "", actor, nil)
	req.AddCookie(cookie)
	req.Header.Set("Accept-Language", "kk")
	w = f.serve(req)
	body = w.Body.String()
	for _, want := range []string{`lang="kk"`, "міндетті", "/submit?lang=kk"} {
		if !strings.Contains(body, want) {
			t.Fatalf("form page misses %q:\n%s", want, body)
		}
	}
}

// The authorizing and already-processing branches render localized too, and
// the oauth form action carries ?lang= forward.
func TestBrowserFormAuthorizingBranchesLocalized(t *testing.T) {
	for _, status := range []string{"authorizing", "exchanging"} {
		var buf strings.Builder
		err := formPage.Execute(&buf, struct {
			View v1.SessionView
			CSRF string
			Lang string
			T    webText
		}{v1.SessionView{RequestID: "r1", Approved: true, Status: status}, "c", "es", webTextFor("es")})
		if err != nil {
			t.Fatal(err)
		}
		body := buf.String()
		if strings.Contains(body, "Файлы приняты") || strings.Contains(body, "Авторизация обрабатывается") {
			t.Fatalf("status %s leaked Russian copy:\n%s", status, body)
		}
	}
	var buf strings.Builder
	if err := formPage.Execute(&buf, struct {
		View v1.SessionView
		CSRF string
		Lang string
		T    webText
	}{v1.SessionView{RequestID: "r1", Approved: true, Status: "authorizing"}, "c", "kk", webTextFor("kk")}); err != nil {
		t.Fatal(err)
	}
	if body := buf.String(); !strings.Contains(body, "/oauth?lang=kk") {
		t.Fatalf("oauth action misses ?lang=:\n%s", body)
	}
}

// The submit success page honors the ?lang= the form action carried.
func TestBrowserSubmitSuccessLocalized(t *testing.T) {
	f := fixtureHTTP(t, apiContract(), safenet.Policy{}, nil)
	w := f.api("POST", "/v1/requests", "broker:control", actor, v1.CreateRequest{
		ContractID: "pat", ContractRevision: 1, ConnectionID: "gitlab", OnboardingID: "onboard1",
		IdempotencyKey: "es_done_key", OwnerKind: "user",
	})
	r := parseResponse[v1.Request](t, w, http.StatusCreated)
	cookie, csrf := f.pair(r)

	raw, ct := formBytes(t, [][2]string{{"csrf", csrf}, {"token", "v"}})
	req := f.request("POST", "/connect/"+r.ID+"/submit?lang=es", "", actor, raw)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", f.s.origin)
	req.AddCookie(cookie)
	w = f.serve(req)
	body := w.Body.String()
	for _, want := range []string{`lang="es"`, "Credenciales recibidas"} {
		if !strings.Contains(body, want) {
			t.Fatalf("success page misses %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Учётные данные") {
		t.Fatal("success page leaked Russian copy")
	}

	// Kazakh catalog covers the success page too.
	w = f.api("POST", "/v1/requests", "broker:control", actor, v1.CreateRequest{
		ContractID: "pat", ContractRevision: 1, ConnectionID: "gitlab2", OnboardingID: "onboard1",
		IdempotencyKey: "kk_done_key", OwnerKind: "user",
	})
	r = parseResponse[v1.Request](t, w, http.StatusCreated)
	cookie, csrf = f.pair(r)
	raw, ct = formBytes(t, [][2]string{{"csrf", csrf}, {"token", "v"}})
	req = f.request("POST", "/connect/"+r.ID+"/submit?lang=kk", "", actor, raw)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", f.s.origin)
	req.AddCookie(cookie)
	w = f.serve(req)
	if body := w.Body.String(); !strings.Contains(body, `lang="kk"`) || !strings.Contains(body, "Тіркелгі деректері қабылданды") {
		t.Fatalf("kk success page wrong:\n%s", body)
	}
}

// An OAuth contract's submit lands on the authorizing redirect, which must
// carry ?lang= forward so the hop page keeps the negotiated language.
func TestBrowserAuthorizingRedirectKeepsLang(t *testing.T) {
	c := apiContract()
	c.Deliveries = nil
	c.OAuthProvider = "testoauth"
	op := &broker.OAuthProvider{ID: "testoauth", AuthorizeURL: "https://provider.example/authorize", TokenURL: "https://provider.example/token", RedirectURL: "https://credentials.example/oauth/callback", ClientID: "client", Scopes: []string{"read"}, AuthStyle: "none", Client: http.DefaultClient}
	f := fixtureHTTP(t, c, safenet.Policy{}, op)
	w := f.api("POST", "/v1/requests", "broker:control", actor, v1.CreateRequest{
		ContractID: c.ID, ContractRevision: c.Revision, ConnectionID: "gitlab", OnboardingID: "onboard1",
		IdempotencyKey: "es_auth_redir", OwnerKind: "user",
	})
	r := parseResponse[v1.Request](t, w, http.StatusCreated)
	cookie, csrf := f.pair(r)
	raw, ct := formBytes(t, [][2]string{{"csrf", csrf}, {"token", "v"}})
	req := f.request("POST", "/connect/"+r.ID+"/submit?lang=es", "", actor, raw)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", f.s.origin)
	req.AddCookie(cookie)
	w = f.serve(req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected authorizing redirect, got %d %s", w.Code, w.Body)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "lang=es") {
		t.Fatalf("authorizing redirect dropped lang: %s", loc)
	}
}
