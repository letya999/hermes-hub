package broker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/provider"
)

func oauthContract() contract.Contract {
	c := plainContract()
	c.ID = "oauth"
	c.OAuthProvider = "example"
	c.Deliveries = nil
	c.Routes = []contract.Route{{ID: "api", BaseURL: "https://api.example.com", PathPrefixes: []string{"/v1"}, Methods: []string{"GET"}, Auth: "oauth2"}}
	return c
}
func oauthProvider(up *httptest.Server) *OAuthProvider {
	return &OAuthProvider{ID: "example", AuthorizeURL: up.URL + "/authorize", TokenURL: up.URL + "/token", ClientID: "test-client", RedirectURL: "https://credentials.example/oauth/callback", Scopes: []string{"calendar.read"}, AuthStyle: "none", Client: up.Client(), AuthorizeParameters: map[string]string{"access_type": "offline"}}
}
func oauthBegin(t *testing.T, f *fixture) (v1.Request, string, url.Values) {
	t.Helper()
	r := f.request(alice, "oauth", "google", "google_request")
	cookie := f.pair(alice, r)
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("input-config")}); e != nil {
		t.Fatal(e)
	}
	target, e := f.b.StartOAuth(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID))
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(target)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "https://credentials.example/oauth/callback" || q.Get("state") == "" {
		t.Fatal("oauth contract", q)
	}
	return r, cookie, q
}

func TestOAuthPKCERefreshAndConcurrentUse(t *testing.T) {
	var challenge atomic.Value
	challenge.Store("")
	var exchanges, refreshes atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/token" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Error("token request")
			w.WriteHeader(400)
			return
		}
		if e := r.ParseForm(); e != nil {
			t.Error(e)
		}
		if r.Form.Get("grant_type") == "authorization_code" {
			exchanges.Add(1)
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge.Load().(string) {
				t.Error("PKCE mismatch")
			}
			if r.Form.Get("code") != "one-code" {
				t.Error("code")
			}
			fmt.Fprint(w, `{"access_token":"access-v1","refresh_token":"refresh-v1","expires_in":40,"token_type":"Bearer","scope":"calendar.read"}`)
		} else {
			refreshes.Add(1)
			if r.Form.Get("refresh_token") != "refresh-v1" {
				t.Error("refresh")
			}
			fmt.Fprint(w, `{"access_token":"access-v2","refresh_token":"refresh-v2","expires_in":3600,"token_type":"bearer"}`)
		}
	}))
	defer up.Close()
	f := newFixture(t, oauthContract())
	f.cfg.OAuth["example"] = oauthProvider(up)
	f.start()
	r, cookie, q := oauthBegin(t, f)
	challenge.Store(q.Get("code_challenge"))
	if e := f.b.CompleteOAuth(testCtx, "different-browser", q.Get("state"), "one-code"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "one-code"); e != nil {
		t.Fatal(e)
	}
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "one-code"); !errors.Is(e, ErrDenied) {
		t.Fatal("code replay", e)
	}
	rr, _ := f.b.GetRequest(alice, r.ID)
	c, _ := f.b.GetCredential(alice, rr.CredentialID)
	g, a := f.grant(alice, c, "oauth", "oauth_binding", "oauth_workload")
	l := f.lease(alice, g)
	use, e := f.b.BeginUse(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	token, e := f.b.AccessToken(testCtx, use)
	if e != nil || token != "access-v1" {
		t.Fatal(token, e)
	}
	use.Finish()
	f.clock.add(15 * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, e := f.b.BeginUse(testCtx, a, l.ID)
			if e != nil {
				t.Error(e)
				return
			}
			defer u.Finish()
			token, e := f.b.AccessToken(testCtx, u)
			if e != nil || token != "access-v2" {
				t.Error(token, e)
			}
		}()
	}
	wg.Wait()
	if exchanges.Load() != 1 || refreshes.Load() != 1 {
		t.Fatal("refresh not serialized", exchanges.Load(), refreshes.Load())
	}
	view, _ := f.b.GetCredential(alice, c.ID)
	if view.Revision != 1 {
		t.Fatal("refresh should not revoke grant epoch")
	}
	f.restart()
	l = f.lease(alice, g)
	use, e = f.b.BeginUse(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer use.Finish()
	token, e = f.b.AccessToken(testCtx, use)
	if e != nil || token != "access-v2" {
		t.Fatal("durable refresh", e)
	}
	mat, e := f.b.Materialize(testCtx, a, l.ID)
	if e != nil || len(mat.Env) != 0 {
		t.Fatal("OAuth tokens must not escape as env", e)
	}
	if e = f.b.DeleteCredential(testCtx, alice, c.ID); e != nil {
		t.Fatal(e)
	}
}

func TestOAuthFailureRequiresNewFlowAndRevocation(t *testing.T) {
	var mode atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() != 1 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant","secret":"not-in-errors"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"access","refresh_token":"refresh","expires_in":1,"token_type":"Bearer"}`)
	}))
	defer up.Close()
	f := newFixture(t, oauthContract())
	f.cfg.OAuth["example"] = oauthProvider(up)
	f.start()
	r, cookie, q := oauthBegin(t, f)
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "code"); !errors.Is(e, ErrReauthorize) {
		t.Fatal(e)
	}
	rr, _ := f.b.GetRequest(alice, r.ID)
	if rr.Status != "authorizing" {
		t.Fatal(rr)
	}
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "code"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	target, e := f.b.StartOAuth(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID))
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(target)
	mode.Store(1)
	if e = f.b.CompleteOAuth(testCtx, cookie, u.Query().Get("state"), "code2"); e != nil {
		t.Fatal(e)
	}
	rr, _ = f.b.GetRequest(alice, r.ID)
	c, _ := f.b.GetCredential(alice, rr.CredentialID)
	g, a := f.grant(alice, c, "oauth", "binding1", "workload1")
	l := f.lease(alice, g)
	use, e := f.b.BeginUse(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer use.Finish()
	mode.Store(2)
	if _, e = f.b.AccessToken(testCtx, use); !errors.Is(e, ErrReauthorize) {
		t.Fatal(e)
	}
	view, _ := f.b.GetCredential(alice, c.ID)
	if view.Status != "reauthorize_required" || use.Valid() {
		t.Fatal("fail closed")
	}
}

func TestOAuthProviderValidationAndResponses(t *testing.T) {
	var payload atomic.Value
	payload.Store(`{"access_token":"a","expires_in":3600,"token_type":"Bearer"}`)
	var status atomic.Int32
	status.Store(200)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		fmt.Fprint(w, payload.Load().(string))
	}))
	defer up.Close()
	p := oauthProvider(up)
	if p.Validate() != nil {
		t.Fatal("valid")
	}
	changes := []func(*OAuthProvider){func(p *OAuthProvider) { p.Client = nil }, func(p *OAuthProvider) { p.Scopes = nil }, func(p *OAuthProvider) { p.AuthorizeURL = "http://example.com" }, func(p *OAuthProvider) { p.RedirectURL = "https://example.com/cb?q=x" }, func(p *OAuthProvider) { p.AuthStyle = "post" }, func(p *OAuthProvider) { p.Scopes = []string{"bad scope"} }, func(p *OAuthProvider) {
		p.AuthorizeParameters = map[string]string{"redirect_uri": "https://evil.example"}
	}, func(p *OAuthProvider) { p.AuthorizeParameters = map[string]string{"response_mode": "form_post"} }}
	for _, change := range changes {
		copy := *p
		change(&copy)
		if copy.Validate() == nil {
			t.Fatal("bad provider")
		}
	}
	for _, style := range []string{"none", "post", "basic"} {
		p.AuthStyle = style
		p.SecretSource = func(context.Context) (string, error) { return "client-secret", nil }
		if _, e := p.exchange(testCtx, "code", "verifier"); e != nil {
			t.Fatal(style, e)
		}
	}
	for _, bad := range []string{`{`, `{"access_token":"a","access_token":"b"}`, `{"access_token":"a","expires_in":0,"token_type":"Bearer"}`, `{"access_token":"a","expires_in":3600,"token_type":"mac"}`, `{"access_token":"a\n","expires_in":3600,"token_type":"Bearer"}`, `{"access_token":"a","expires_in":3600,"token_type":"Bearer","scope":"other"}`, strings.Repeat(" ", 65537)} {
		payload.Store(bad)
		if _, e := p.refresh(testCtx, "refresh"); e == nil {
			t.Fatal("invalid token response")
		}
	}
	payload.Store(`{"access_token":"a","expires_in":"3600","token_type":"Bearer"}`)
	if _, e := p.refresh(testCtx, "refresh"); e != nil {
		t.Fatal(e)
	}
	for _, n := range []int32{400, 401, 500} {
		status.Store(n)
		if _, e := p.exchange(testCtx, "code", "verifier"); e == nil {
			t.Fatal(n)
		}
	}
	p.SecretSource = func(context.Context) (string, error) { return "", errors.New("source unavailable") }
	if _, e := p.exchange(testCtx, "code", "verifier"); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	p.SecretSource = nil
	p.TokenURL = ":invalid"
	if _, e := p.exchange(testCtx, "code", "verifier"); e == nil {
		t.Fatal("bad token URL")
	}
}

func TestOAuthExpiredOrReplacedState(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("must not reach token endpoint") }))
	defer up.Close()
	f := newFixture(t, oauthContract())
	f.cfg.OAuth["example"] = oauthProvider(up)
	f.start()
	r, cookie, q := oauthBegin(t, f)
	if _, e := f.b.StartOAuth(testCtx, r.ID, cookie, "bad"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e := f.b.CompleteOAuth(testCtx, cookie, "bad", "code"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if _, e := f.b.StartOAuth(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID)); e != nil {
		t.Fatal(e)
	}
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "code"); !errors.Is(e, ErrDenied) {
		t.Fatal("replaced state", e)
	}
	f.clock.add(6 * time.Minute)
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "code"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e := f.b.CancelRequest(alice, r.ID); e != nil {
		t.Fatal(e)
	}
}
