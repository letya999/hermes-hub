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
	"os"
	"path/filepath"
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
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("input-config")}, nil); e != nil {
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
	use.Finish()
	if e = f.b.Release(testCtx, a, l.ID, v1.RuntimeRelease{}); e != nil {
		t.Fatal(e)
	}
	if e = f.b.DeleteCredential(testCtx, alice, c.ID); e != nil {
		t.Fatal(e)
	}
}

// A rotating refresh writes a fresh provider object every cycle. The
// credential record must not grow an unbounded ref list — ownership moves
// to bounded object rows, so serialized metadata stays flat (SPEC-0087 R4.1).
func TestRefreshMetadataGrowthIsLinear(t *testing.T) {
	var refreshes atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/token" {
			w.WriteHeader(400)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "authorization_code" {
			fmt.Fprint(w, `{"access_token":"access-0","refresh_token":"refresh-0","expires_in":60,"token_type":"Bearer"}`)
			return
		}
		n := refreshes.Add(1)
		fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":"refresh-%d","expires_in":60,"token_type":"bearer"}`, n, n)
	}))
	defer up.Close()
	f := newFixture(t, oauthContract())
	f.cfg.OAuth["example"] = oauthProvider(up)
	f.start()
	r, cookie, q := oauthBegin(t, f)
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "one-code"); e != nil {
		t.Fatal(e)
	}
	rr, _ := f.b.GetRequest(alice, r.ID)
	c, _ := f.b.GetCredential(alice, rr.CredentialID)
	g, a := f.grant(alice, c, "oauth", "oauth_binding", "oauth_workload")
	l, e := f.b.Acquire(a, v1.AcquireLease{GrantID: g.ID, TTLSeconds: 300})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		f.clock.add(45 * time.Second)
		use, e := f.b.BeginUse(testCtx, a, l.ID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := f.b.AccessToken(testCtx, use); e != nil {
			use.Finish()
			t.Fatal(e)
		}
		use.Finish()
	}
	if refreshes.Load() != 3 {
		t.Fatalf("refreshes = %d, want 3", refreshes.Load())
	}
	if got := len(f.b.credentials[c.ID].OwnedRefs); got > 1 {
		t.Fatalf("credential metadata grew to %d refs after 3 refreshes", got)
	}
	// Ownership must outlive the bounded metadata: delete still removes
	// every object this credential ever wrote (activation + rotations).
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

// A crash mid-refresh — provider write done, activation commit never run —
// must not orphan the rotated object: the intent row persisted before the
// write still owns it after restart (SPEC-0087 R4.2).
func TestRefreshCrashKeepsObjectOwned(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fmt.Fprint(w, `{"access_token":"access-1","refresh_token":"refresh-1","expires_in":60,"token_type":"bearer"}`)
	}))
	defer up.Close()
	f := newFixture(t, oauthContract())
	f.cfg.OAuth["example"] = oauthProvider(up)
	f.start()
	w := &crashWriter{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	r, cookie, q := oauthBegin(t, f)
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "one-code"); e != nil {
		t.Fatal(e)
	}
	rr, _ := f.b.GetRequest(alice, r.ID)
	c, _ := f.b.GetCredential(alice, rr.CredentialID)
	g, a := f.grant(alice, c, "oauth", "oauth_binding", "oauth_workload")
	l := f.lease(alice, g)
	w.armed.Store(true)
	f.clock.add(45 * time.Second)
	use, e := f.b.BeginUse(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.b.AccessToken(testCtx, use); !errors.Is(e, ErrReauthorize) {
		use.Finish()
		t.Fatalf("refresh after injected crash = %v", e)
	}
	use.Finish()
	f.restart()
	var intents int
	for _, o := range f.b.objects {
		if o.State == "intent" && o.OwnerKind == "credential" && o.OwnerID == c.ID && o.Op == "oauth.refresh" {
			intents++
		}
	}
	if intents != 1 {
		t.Fatalf("crashed refresh left %d owned intent rows, want 1", intents)
	}
}

// The activation write inside CompleteOAuth has the same crash window: the
// exchanged values reach disk but the credential commit never runs. The
// request-owned intent row must survive the restart (SPEC-0087 R4.2).
func TestActivationCrashKeepsObjectOwned(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fmt.Fprint(w, `{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600,"token_type":"bearer"}`)
	}))
	defer up.Close()
	f := newFixture(t, oauthContract())
	f.cfg.OAuth["example"] = oauthProvider(up)
	f.start()
	w := &crashWriter{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	r, cookie, q := oauthBegin(t, f)
	w.armed.Store(true)
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "one-code"); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("activation after injected crash = %v", e)
	}
	f.restart()
	var intents int
	for _, o := range f.b.objects {
		if o.State == "intent" && o.OwnerKind == "request" && o.OwnerID == r.ID && o.Op == "oauth.activate" {
			intents++
		}
	}
	if intents != 1 {
		t.Fatalf("crashed activation left %d owned intent rows, want 1", intents)
	}
}

// A crash writing the PKCE verifier object — same window, owned by the
// request that started the flow (SPEC-0087 R4.2).
func TestVerifierCrashKeepsObjectOwned(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	defer up.Close()
	f := newFixture(t, oauthContract())
	f.cfg.OAuth["example"] = oauthProvider(up)
	f.start()
	w := &crashWriter{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	r := f.request(alice, "oauth", "google", "verifier_crash")
	cookie := f.pair(alice, r)
	if e := f.b.Submit(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID), provider.Bundle{"token": []byte("input-config")}, nil); e != nil {
		t.Fatal(e)
	}
	w.armed.Store(true)
	if _, e := f.b.StartOAuth(testCtx, r.ID, cookie, f.b.CSRF(cookie, r.ID)); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("oauth start after injected crash = %v", e)
	}
	f.restart()
	var intents int
	for _, o := range f.b.objects {
		if o.State == "intent" && o.OwnerKind == "request" && o.OwnerID == r.ID && o.Op == "oauth.verifier" {
			intents++
		}
	}
	if intents != 1 {
		t.Fatalf("crashed verifier write left %d owned intent rows, want 1", intents)
	}
}

// A refresh during active use must not strand the in-use version: the
// replacement is activated first and the superseded version stays pinned —
// resolvable — until the use releases it (SPEC-0087 R4.2).
func TestRotatingOAuthReplacementNotLost(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fmt.Fprint(w, `{"access_token":"access-1","refresh_token":"refresh-1","expires_in":60,"token_type":"bearer"}`)
	}))
	defer up.Close()
	f := newFixture(t, oauthContract())
	f.cfg.OAuth["example"] = oauthProvider(up)
	f.start()
	r, cookie, q := oauthBegin(t, f)
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "one-code"); e != nil {
		t.Fatal(e)
	}
	rr, _ := f.b.GetRequest(alice, r.ID)
	c, _ := f.b.GetCredential(alice, rr.CredentialID)
	g, a := f.grant(alice, c, "oauth", "oauth_binding", "oauth_workload")
	l := f.lease(alice, g)
	inUse, e := f.b.BeginUse(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer inUse.Finish()
	oldRef := inUse.Ref
	f.clock.add(45 * time.Second)
	other, e := f.b.BeginUse(testCtx, a, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	token, e := f.b.AccessToken(testCtx, other)
	other.Finish()
	if e != nil || token != "access-1" {
		t.Fatal(token, e)
	}
	row, ok := f.b.objects[objectID(oldRef)]
	if !ok || row.State != "pinned" {
		t.Fatalf("in-use version after replacement = %+v (present=%v), want pinned", row, ok)
	}
	got, e := f.b.cfg.Providers.Resolve(testCtx, oldRef)
	if e != nil {
		t.Fatal("pinned version unresolvable:", e)
	}
	got.Wipe()
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
