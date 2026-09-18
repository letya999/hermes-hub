package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/broker"
	"github.com/letya999/credential-broker/client"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/internal/safenet"
	"github.com/letya999/credential-broker/materialize"
	"github.com/letya999/credential-broker/provider"
)

var actor = identity.Actor{PrincipalID: "alice", ContextID: "team", RuntimeID: "hermes-alice", PolicyVersion: "policy_v1"}

type httpFixture struct {
	s    *Server
	b    *broker.Broker
	key  ed25519.PrivateKey
	root string
	t    *testing.T
}

func apiContract() contract.Contract {
	return contract.Contract{ID: "pat", Revision: 1, Title: `API <script>alert(1)</script>`, Storage: "local", Fields: []contract.Field{{ID: "token", Kind: "secret", Label: "API token", Required: true, MaxBytes: 1024}}, Deliveries: []contract.Delivery{{Type: "env", Field: "token", Target: "TOKEN"}}}
}
func fixtureHTTP(t *testing.T, c contract.Contract, np safenet.Policy, op *broker.OAuthProvider) *httpFixture {
	t.Helper()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	p, e := provider.NewLocal("local", filepath.Join(root, "secrets"), bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	j, e := journal.Open(filepath.Join(root, "ledger"), bytes.Repeat([]byte{2}, 32), 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	m, e := materialize.New(filepath.Join(root, "runtime"), false)
	if e != nil {
		t.Fatal(e)
	}
	ops := map[string]*broker.OAuthProvider{}
	if op != nil {
		ops[op.ID] = op
	}
	b, e := broker.New(broker.Config{Contracts: []contract.Contract{c}, Providers: provider.Registry{"local": p}, Journal: j, Materializer: m, PublicOrigin: "https://credentials.example", SessionKey: bytes.Repeat([]byte{3}, 32), OAuth: ops})
	if e != nil {
		t.Fatal(e)
	}
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(Config{Broker: b, Verifier: identity.Verifier{Keys: map[string]identity.TrustedKey{"test": {PublicKey: pub, Issuer: "test-hub", Audiences: []string{"broker:control", "broker:approve", "broker:runtime"}}}}, NetworkPolicy: np})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = b.Close() })
	return &httpFixture{s, b, key, root, t}
}
func (f *httpFixture) request(method, path, audience string, a identity.Actor, raw []byte) *http.Request {
	f.t.Helper()
	r := httptest.NewRequest(method, "https://credentials.example"+path, bytes.NewReader(raw))
	r.TLS = &tls.ConnectionState{}
	r.RemoteAddr = "127.0.0.1:12345"
	if audience != "" {
		now := time.Now()
		token, e := identity.Sign(f.key, identity.Claims{KeyID: "test", Issuer: "test-hub", Audience: audience, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Method: method, RequestURI: r.URL.RequestURI(), BodySHA256: identity.Hash(raw), Actor: a})
		if e != nil {
			f.t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if len(raw) > 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}
func (f *httpFixture) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.s.ServeHTTP(w, r)
	return w
}
func (f *httpFixture) api(method, path, audience string, a identity.Actor, in any) *httptest.ResponseRecorder {
	f.t.Helper()
	var raw []byte
	if in != nil {
		var e error
		raw, e = json.Marshal(in)
		if e != nil {
			f.t.Fatal(e)
		}
	}
	return f.serve(f.request(method, path, audience, a, raw))
}
func parseResponse[T any](t *testing.T, w *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body)
	}
	var out T
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *httpFixture) create() v1.Request {
	w := f.api("POST", "/v1/requests", "broker:control", actor, v1.CreateRequest{ContractID: f.b.Catalog()[0].ID, ContractRevision: 1, ConnectionID: "connection1", OnboardingID: "onboard1", IdempotencyKey: "request_one", OwnerKind: "user"})
	return parseResponse[v1.Request](f.t, w, 201)
}
func formBytes(t *testing.T, fields [][2]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	m := multipart.NewWriter(&buf)
	for _, v := range fields {
		if e := m.WriteField(v[0], v[1]); e != nil {
			t.Fatal(e)
		}
	}
	if e := m.Close(); e != nil {
		t.Fatal(e)
	}
	return buf.Bytes(), m.FormDataContentType()
}
func (f *httpFixture) pair(r v1.Request) (*http.Cookie, string) {
	f.t.Helper()
	w := f.serve(f.request("GET", "/connect/"+r.ID, "", actor, nil))
	if w.Code != 200 {
		f.t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		f.t.Fatal("cookie flags")
	}
	if strings.Contains(w.Body.String(), "<script>") {
		f.t.Fatal("XSS")
	}
	code := regexp.MustCompile(`<p class="code">([A-Z2-7]{16})</p>`).FindStringSubmatch(w.Body.String())
	if len(code) != 2 {
		f.t.Fatal("pairing code")
	}
	wrong := actor
	wrong.PrincipalID = "bob"
	if w = f.api("POST", "/v1/requests/"+r.ID+"/approve", "broker:approve", wrong, v1.Approve{Code: code[1]}); w.Code != 404 {
		f.t.Fatal(w.Code)
	}
	if w = f.api("POST", "/v1/requests/"+r.ID+"/approve", "broker:control", actor, v1.Approve{Code: code[1]}); w.Code != 401 {
		f.t.Fatal("wrong audience")
	}
	if w = f.api("POST", "/v1/requests/"+r.ID+"/approve", "broker:approve", actor, v1.Approve{Code: code[1]}); w.Code != 200 {
		f.t.Fatal(w.Code, w.Body)
	}
	req := f.request("GET", "/connect/"+r.ID, "", actor, nil)
	req.AddCookie(cookies[0])
	w = f.serve(req)
	csrf := regexp.MustCompile(`name="csrf" value="([A-Z2-7]+)"`).FindStringSubmatch(w.Body.String())
	if len(csrf) != 2 {
		f.t.Fatal("form not rendered", w.Body)
	}
	return cookies[0], csrf[1]
}
func (f *httpFixture) submit(r v1.Request, cookie *http.Cookie, csrf string, values provider.Bundle) *httptest.ResponseRecorder {
	f.t.Helper()
	fields := [][2]string{{"csrf", csrf}}
	for k, v := range values {
		fields = append(fields, [2]string{k, string(v)})
	}
	raw, ct := formBytes(f.t, fields)
	req := f.request("POST", "/connect/"+r.ID+"/submit", "", actor, raw)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", f.s.origin)
	req.AddCookie(cookie)
	return f.serve(req)
}
func (f *httpFixture) enroll(values provider.Bundle) (v1.Request, v1.Grant, v1.Lease, identity.Actor) {
	f.t.Helper()
	r := f.create()
	cookie, csrf := f.pair(r)
	w := f.submit(r, cookie, csrf, values)
	if w.Code != 200 {
		f.t.Fatal(w.Code, w.Body)
	}
	r = parseResponse[v1.Request](f.t, f.api("GET", "/v1/requests/"+r.ID, "broker:control", actor, nil), 200)
	gi := v1.GrantRequest{ContractID: r.ContractID, ContractRevision: 1, PrincipalID: actor.PrincipalID, ContextID: actor.ContextID, RuntimeID: actor.RuntimeID, BindingID: "binding1", WorkloadID: "workload1", Execution: "dedicated", IdempotencyKey: "grant_first"}
	g := parseResponse[v1.Grant](f.t, f.api("POST", "/v1/credentials/"+r.CredentialID+"/grants", "broker:control", actor, gi), 201)
	l := parseResponse[v1.Lease](f.t, f.api("POST", "/v1/leases", "broker:control", actor, v1.AcquireLease{GrantID: g.ID}), 201)
	a := actor
	a.BindingID = g.BindingID
	a.WorkloadID = g.WorkloadID
	return r, g, l, a
}

func TestHTTPEnrollmentRuntimeAndAudit(t *testing.T) {
	f := fixtureHTTP(t, apiContract(), safenet.Policy{}, nil)
	r, g, l, a := f.enroll(provider.Bundle{"token": []byte("SENTINEL_TOKEN_NEVER_IN_CHAT")})
	if w := f.api("GET", "/v1/contracts", "broker:control", actor, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := f.api("GET", "/v1/credentials/"+r.CredentialID, "broker:control", actor, nil); w.Code != 200 || strings.Contains(w.Body.String(), "SENTINEL_TOKEN") {
		t.Fatal(w.Code)
	}
	m := parseResponse[v1.Materialized](t, f.api("POST", "/v1/runtime/leases/"+l.ID+"/materialize", "broker:runtime", a, nil), 200)
	if m.Env["TOKEN"] != "SENTINEL_TOKEN_NEVER_IN_CHAT" {
		t.Fatal(m)
	}
	if w := f.api("POST", "/v1/runtime/leases/"+l.ID+"/materialize", "broker:control", actor, nil); w.Code != 401 {
		t.Fatal("control secret extraction", w.Code)
	}
	if w := f.api("GET", "/v1/runtime/leases/"+l.ID, "broker:runtime", a, nil); w.Code != 200 || strings.Contains(w.Body.String(), "SENTINEL_TOKEN") {
		t.Fatal(w.Code)
	}
	if w := f.api("POST", "/v1/runtime/leases/"+l.ID+"/renew", "broker:runtime", a, map[string]int{"ttl_seconds": 120}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	ev := f.api("GET", "/v1/events?after=0", "broker:control", actor, nil)
	if ev.Code != 200 || strings.Contains(ev.Body.String(), "SENTINEL_TOKEN") {
		t.Fatal("audit secret", ev.Code)
	}
	if w := f.api("POST", "/v1/runtime/leases/"+l.ID+"/release", "broker:runtime", a, v1.RuntimeRelease{}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := f.api("POST", "/v1/grants/"+g.ID+"/revoke", "broker:control", actor, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := f.api("POST", "/v1/credentials/"+r.CredentialID+"/revoke", "broker:control", actor, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := f.api("DELETE", "/v1/credentials/"+r.CredentialID, "broker:control", actor, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := f.serve(f.request("GET", "/connect/"+r.ID, "", actor, nil)); w.Code != 410 {
		t.Fatal(w.Code)
	}
	if w := f.serve(f.request("GET", "/healthz", "", actor, nil)); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("headers")
	}
}

func TestHTTPBoundaryAndValidation(t *testing.T) {
	f := fixtureHTTP(t, apiContract(), safenet.Policy{}, nil)
	if _, e := New(Config{}); e == nil {
		t.Fatal("empty config")
	}
	if _, e := New(Config{Broker: f.b, Verifier: f.cfgVerifier(), DevHTTP: true}); e == nil {
		t.Fatal("dev origin mismatch")
	}
	for _, change := range []func(*http.Request){func(r *http.Request) { r.Host = "evil.example" }, func(r *http.Request) { r.TLS = nil }} {
		r := f.request("GET", "/healthz", "", actor, nil)
		change(r)
		if w := f.serve(r); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	for _, path := range []string{"/v1//requests", "/v1/%2e%2e/requests", "/v1/foo%5cbar"} {
		if w := f.serve(f.request("GET", path, "", actor, nil)); w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
	if w := f.serve(f.request("GET", "/other", "", actor, nil)); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := f.api("GET", "/v1/contracts", "", actor, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	dup := f.request("GET", "/v1/contracts", "broker:control", actor, nil)
	dup.Header.Add("Authorization", "Bearer copied")
	if w := f.serve(dup); w.Code != 401 {
		t.Fatal(w.Code)
	}
	wrong := f.request("GET", "/v1/contracts", "broker:control", actor, nil)
	wrong.Header.Set("Authorization", "Bearer invalid")
	if w := f.serve(wrong); w.Code != 401 {
		t.Fatal(w.Code)
	}
	raw := []byte(`{"owner_kind":"user","owner_kind":"context"}`)
	if w := f.serve(f.request("POST", "/v1/requests", "broker:control", actor, raw)); w.Code != 400 {
		t.Fatal(w.Code)
	}
	raw = []byte(`{"unknown":true}`)
	if w := f.serve(f.request("POST", "/v1/requests", "broker:control", actor, raw)); w.Code != 400 {
		t.Fatal(w.Code)
	}
	invalid := f.request("POST", "/v1/requests", "broker:control", actor, []byte("{}"))
	invalid.Header.Set("Content-Type", "text/plain")
	if w := f.serve(invalid); w.Code != 400 {
		t.Fatal(w.Code)
	}
	big := f.request("POST", "/v1/requests", "broker:control", actor, bytes.Repeat([]byte("x"), 65537))
	if w := f.serve(big); w.Code != 413 {
		t.Fatal(w.Code)
	}
	if w := f.api("GET", "/v1/events?after=notnumber", "broker:control", actor, nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := f.api("GET", "/v1/missing", "broker:control", actor, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	for i := 0; i < cap(f.s.slots); i++ {
		f.s.slots <- struct{}{}
	}
	if w := f.api("GET", "/healthz", "", actor, nil); w.Code != 503 {
		t.Fatal(w.Code)
	}
	for i := 0; i < cap(f.s.slots); i++ {
		<-f.s.slots
	}
	if e := f.b.Close(); e != nil {
		t.Fatal(e)
	}
	if w := f.api("GET", "/healthz", "", actor, nil); w.Code != 503 {
		t.Fatal(w.Code)
	}
}
func (f *httpFixture) cfgVerifier() identity.Verifier { return f.s.cfg.Verifier }

func TestBrowserCSRFMultipartAndExpiredLinks(t *testing.T) {
	f := fixtureHTTP(t, apiContract(), safenet.Policy{}, nil)
	r := f.create()
	cookie, csrf := f.pair(r)
	raw, ct := formBytes(t, [][2]string{{"csrf", csrf}, {"token", "x"}})
	req := f.request("POST", "/connect/"+r.ID+"/submit", "", actor, raw)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", "https://evil.example")
	if w := f.serve(req); w.Code != 403 {
		t.Fatal(w.Code)
	}
	req = f.request("POST", "/connect/"+r.ID+"/submit", "", actor, raw)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", f.s.origin)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if w := f.serve(req); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := f.submit(r, cookie, "wrong-csrf", provider.Bundle{"token": []byte("x")}); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := f.submit(r, cookie, csrf, provider.Bundle{"token": []byte("x"), "principal_id": []byte("bob")}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := f.api("POST", "/v1/requests/"+r.ID+"/cancel", "broker:control", actor, map[string]string{"bad": "field"}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := f.api("POST", "/v1/requests/"+r.ID+"/cancel", "broker:control", actor, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := f.submit(r, cookie, csrf, provider.Bundle{"token": []byte("x")}); w.Code != 410 {
		t.Fatal(w.Code)
	}
	if w := f.serve(f.request("GET", "/form.css", "", actor, nil)); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/css") {
		t.Fatal(w.Code)
	}
	for _, path := range []string{"/connect/none/bad", "/connect/none/x/y", "/oauth/callback?state=a&state=b&code=x", "/oauth/callback?state=a&code=x&error=denied", "/oauth/callback?state=a&code=x"} {
		w := f.serve(f.request("GET", path, "", actor, nil))
		if w.Code < 400 {
			t.Fatal(w.Code)
		}
	}
	for i := 0; i < 22; i++ {
		_ = f.serve(f.request("GET", "/form.css", "", actor, nil))
	}
	if w := f.serve(f.request("GET", "/form.css", "", actor, nil)); w.Code != 429 {
		t.Fatal("rate limit")
	}
}

func TestMultipartLimits(t *testing.T) {
	for _, fields := range [][][2]string{{{"csrf", "x"}, {"token", "x"}, {"token", "y"}}, {{"token", "x"}}, {{"csrf", strings.Repeat("a", 129)}}, {{"csrf", "x"}, {"token", strings.Repeat("a", 65537)}}, {{"csrf", "x"}, {"", "x"}}, {{"csrf", "x"}, {strings.Repeat("a", 81), "x"}}} {
		raw, ct := formBytes(t, fields)
		r := httptest.NewRequest("POST", "/", bytes.NewReader(raw))
		r.Header.Set("Content-Type", ct)
		v, _, e := readForm(r)
		v.Wipe()
		if e == nil {
			t.Fatal("bad multipart accepted")
		}
	}
	many := [][2]string{{"csrf", "x"}}
	for i := 0; i < 34; i++ {
		many = append(many, [2]string{fmt.Sprintf("f%d", i), "x"})
	}
	raw, ct := formBytes(t, many)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(raw))
	r.Header.Set("Content-Type", ct)
	if _, _, e := readForm(r); e == nil {
		t.Fatal("parts")
	}
	many = [][2]string{{"csrf", "x"}}
	for i := 0; i < 5; i++ {
		many = append(many, [2]string{fmt.Sprintf("f%d", i), strings.Repeat("a", 65536)})
	}
	raw, ct = formBytes(t, many)
	r = httptest.NewRequest("POST", "/", bytes.NewReader(raw))
	r.Header.Set("Content-Type", ct)
	if _, _, e := readForm(r); e == nil {
		t.Fatal("total")
	}
	for _, contentType := range []string{"text/plain", "multipart/form-data", "multipart/form-data; boundary=bad"} {
		r = httptest.NewRequest("POST", "/", strings.NewReader("invalid"))
		r.Header.Set("Content-Type", contentType)
		if _, _, e := readForm(r); e == nil {
			t.Fatal("format")
		}
	}
	// The untrusted original filename never becomes a materialization path.
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	_ = mw.WriteField("csrf", "x")
	part, _ := mw.CreateFormFile("config", "../../outside.json")
	_, _ = part.Write([]byte(`{}`))
	_ = mw.Close()
	r = httptest.NewRequest("POST", "/", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	values, token, e := readForm(r)
	if e != nil || token != "x" || string(values["config"]) != "{}" {
		t.Fatal(e)
	}
	values.Wipe()
}

func TestProxyFixedDestinationHeaderIsolationAndRevocation(t *testing.T) {
	var mode atomic.Int32
	entered := make(chan struct{}, 1)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer PROVIDER_SECRET_SENTINEL" {
			t.Error("credential not injected")
		}
		for _, h := range []string{"Cookie", "Proxy-Authorization", "X-Forwarded-For"} {
			if r.Header.Get(h) != "" {
				t.Error("header leaked", h)
			}
		}
		switch mode.Load() {
		case 1:
			w.Header().Set("Location", "https://evil.example")
			w.WriteHeader(302)
		case 2:
			fmt.Fprint(w, "PROVIDER_SECRET_SENTINEL")
		case 3:
			fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
		case 4:
			entered <- struct{}{}
			<-r.Context().Done()
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Set-Cookie", "credential=unsafe")
			fmt.Fprint(w, `{"ok":true}`)
		}
	}))
	defer up.Close()
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	np := safenet.Policy{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TLSConfig: &tls.Config{RootCAs: pool}}
	c := apiContract()
	c.Deliveries = nil
	c.Routes = []contract.Route{{ID: "api", BaseURL: up.URL, PathPrefixes: []string{"/v1"}, Methods: []string{"GET", "POST"}, Auth: "bearer", Field: "token"}}
	f := fixtureHTTP(t, c, np, nil)
	r, _, l, a := f.enroll(provider.Bundle{"token": []byte("PROVIDER_SECRET_SENTINEL")})
	path := "/v1/runtime/proxy/" + l.ID + "/api/v1/items?q=1"
	req := f.request("GET", path, "broker:runtime", a, nil)
	req.Header.Set("Cookie", "bad=value")
	req.Header.Set("Proxy-Authorization", "Basic BAD")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	w := f.serve(req)
	if w.Code != 200 || w.Header().Get("Set-Cookie") != "" || w.Body.String() != `{"ok":true}` {
		t.Fatal(w.Code, w.Body)
	}
	for _, suffix := range []string{"/api/v10", "/unknown/v1", "/api/v1/../outside", "/api/v1%2f..%2fprivate"} {
		w = f.api("GET", "/v1/runtime/proxy/"+l.ID+suffix, "broker:runtime", a, nil)
		if w.Code < 400 {
			t.Fatal("route escaped", suffix, w.Code)
		}
	}
	w = f.api("DELETE", path, "broker:runtime", a, nil)
	if w.Code != 403 {
		t.Fatal("method")
	}
	for _, m := range []int32{1, 2, 3} {
		mode.Store(m)
		w = f.api("GET", path, "broker:runtime", a, nil)
		if w.Code < 400 {
			t.Fatal("unsafe response", m)
		}
	}
	if w = f.api("GET", "/v1/runtime/proxy/bad", "broker:runtime", a, nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	mode.Store(4)
	done := make(chan *httptest.ResponseRecorder, 1)
	request := f.request("GET", path, "broker:runtime", a, nil)
	go func() { done <- f.serve(request) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no upstream request")
	}
	if e := f.b.RevokeCredential(actor, r.CredentialID); e != nil {
		t.Fatal(e)
	}
	select {
	case result := <-done:
		if result.Code < 400 {
			t.Fatal("inflight request survived revoke")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request not canceled")
	}
	if w = f.api("GET", path, "broker:runtime", a, nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestProxyHeaderBasicAndMTLS(t *testing.T) {
	for _, auth := range []string{"header", "basic", "mtls"} {
		t.Run(auth, func(t *testing.T) {
			values := provider.Bundle{"token": []byte("SYNTHETIC_USERNAME"), "password": []byte("SYNTHETIC_PASSWORD")}
			pool := x509.NewCertPool()
			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch auth {
				case "header":
					if r.Header.Get("X-API-Key") != "SYNTHETIC_USERNAME" {
						t.Error("api key")
					}
				case "basic":
					u, p, ok := r.BasicAuth()
					if !ok || u != "SYNTHETIC_USERNAME" || p != "SYNTHETIC_PASSWORD" {
						t.Error("basic")
					}
				case "mtls":
					if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
						t.Error("mutual TLS identity")
					}
				}
				fmt.Fprint(w, `{"ok":true}`)
			}))
			if auth == "mtls" {
				certPEM, keyPEM, cert := clientCertificate(t)
				values["token"] = certPEM
				values["password"] = keyPEM
				ca := x509.NewCertPool()
				ca.AddCert(cert)
				up.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca, MinVersion: tls.VersionTLS12}
			}
			up.StartTLS()
			defer up.Close()
			pool.AddCert(up.Certificate())
			np := safenet.Policy{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TLSConfig: &tls.Config{RootCAs: pool}}
			c := apiContract()
			c.Deliveries = nil
			c.Fields = append(c.Fields, contract.Field{ID: "password", Kind: "secret", Label: "Password", Required: true, MaxBytes: 10000})
			if auth == "mtls" {
				c.Fields[0].Kind = "pem"
				c.Fields[0].MaxBytes = 10000
				c.Fields[1].Kind = "pem"
			}
			route := contract.Route{ID: "api", BaseURL: up.URL, PathPrefixes: []string{"/v1"}, Methods: []string{"GET"}, Auth: auth, Field: "token"}
			if auth == "header" {
				route.Header = "X-API-Key"
			} else {
				route.PasswordField = "password"
			}
			route.StaticHeaders = map[string]string{"X-Client": "broker"}
			c.Routes = []contract.Route{route}
			f := fixtureHTTP(t, c, np, nil)
			_, _, l, a := f.enroll(values)
			w := f.api("GET", "/v1/runtime/proxy/"+l.ID+"/api/v1/test", "broker:runtime", a, nil)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
}
func clientCertificate(t *testing.T) ([]byte, []byte, *x509.Certificate) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "test-client"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	keyDer, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	cert, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDer}), cert
}

func TestBrowserOAuthAndProxy(t *testing.T) {
	var count atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			count.Add(1)
			fmt.Fprint(w, `{"access_token":"OAUTH_ACCESS_SENTINEL","refresh_token":"OAUTH_REFRESH_SENTINEL","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer OAUTH_ACCESS_SENTINEL" {
			t.Error("oauth header")
		}
		fmt.Fprint(w, `{"items":[]}`)
	}))
	defer up.Close()
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	np := safenet.Policy{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TLSConfig: &tls.Config{RootCAs: pool}}
	op := &broker.OAuthProvider{ID: "testoauth", AuthorizeURL: up.URL + "/authorize", TokenURL: up.URL + "/token", RedirectURL: "https://credentials.example/oauth/callback", ClientID: "client", Scopes: []string{"read"}, AuthStyle: "none", Client: up.Client()}
	c := apiContract()
	c.Deliveries = nil
	c.OAuthProvider = op.ID
	c.Routes = []contract.Route{{ID: "api", BaseURL: up.URL, PathPrefixes: []string{"/v1"}, Methods: []string{"GET"}, Auth: "oauth2"}}
	f := fixtureHTTP(t, c, np, op)
	r := f.create()
	cookie, csrf := f.pair(r)
	w := f.submit(r, cookie, csrf, provider.Bundle{"token": []byte("config-value")})
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body)
	}
	req := f.request("GET", "/connect/"+r.ID, "", actor, nil)
	req.AddCookie(cookie)
	w = f.serve(req)
	if !strings.Contains(w.Body.String(), "/oauth") {
		t.Fatal("auth state")
	}
	for _, raw := range []string{"csrf=x&csrf=y", "bad=x", strings.Repeat("x", 4097)} {
		req = f.request("POST", "/connect/"+r.ID+"/oauth", "", actor, []byte(raw))
		req.AddCookie(cookie)
		req.Header.Set("Origin", f.s.origin)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if result := f.serve(req); result.Code < 400 {
			t.Fatal(result.Code)
		}
	}
	req = f.request("POST", "/connect/"+r.ID+"/oauth", "", actor, []byte(url.Values{"csrf": {csrf}}.Encode()))
	req.AddCookie(cookie)
	req.Header.Set("Origin", f.s.origin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = f.serve(req)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body)
	}
	target, e := url.Parse(w.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	req = f.request("GET", "/oauth/callback?state="+target.Query().Get("state")+"&code=test_code", "", actor, nil)
	req.AddCookie(cookie)
	w = f.serve(req)
	if w.Code != 200 || count.Load() != 1 {
		t.Fatal(w.Code, w.Body)
	}
	r = parseResponse[v1.Request](t, f.api("GET", "/v1/requests/"+r.ID, "broker:control", actor, nil), 200)
	g, e := f.b.CreateGrant(actor, r.CredentialID, v1.GrantRequest{ContractID: c.ID, ContractRevision: 1, PrincipalID: actor.PrincipalID, ContextID: actor.ContextID, RuntimeID: actor.RuntimeID, BindingID: "binding1", WorkloadID: "workload1", Execution: "dedicated", IdempotencyKey: "oauth_grant"})
	if e != nil {
		t.Fatal(e)
	}
	l, e := f.b.Acquire(actor, v1.AcquireLease{GrantID: g.ID})
	if e != nil {
		t.Fatal(e)
	}
	a := actor
	a.BindingID = g.BindingID
	a.WorkloadID = g.WorkloadID
	w = f.api("GET", "/v1/runtime/proxy/"+l.ID+"/api/v1/items", "broker:runtime", a, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestLimiterCapacityAndRefill(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < 20; i++ {
		if !l.allow("1:42") {
			t.Fatal(i)
		}
	}
	if l.allow("1:43") {
		t.Fatal("port is not identity")
	}
	now = now.Add(time.Second)
	if !l.allow("1:42") {
		t.Fatal("refill")
	}
	for i := 0; i < 1023; i++ {
		if !l.allow(fmt.Sprintf("host%d:1", i)) {
			t.Fatal(i)
		}
	}
	if l.allow("overflow:1") {
		t.Fatal("unbounded map")
	}
	now = now.Add(2 * time.Minute)
	if !l.allow("overflow:1") {
		t.Fatal("eviction")
	}
}

func TestSDKOverHTTP(t *testing.T) {
	f := fixtureHTTP(t, apiContract(), safenet.Policy{}, nil)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.Host = f.s.host; f.s.ServeHTTP(w, r) }))
	defer srv.Close()
	c, e := client.New(srv.URL, client.Signer{KeyID: "test", Issuer: "test-hub", Audience: "broker:control", PrivateKey: f.key}, actor, srv.Client())
	if e != nil {
		t.Fatal(e)
	}
	contracts, e := c.Contracts(context.Background())
	if e != nil || len(contracts) != 1 {
		t.Fatal(e)
	}
	r, e := c.CreateRequest(context.Background(), v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "sdkconn", OnboardingID: "sdkjob", OwnerKind: "user", IdempotencyKey: "sdk_request"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Request(context.Background(), r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Events(context.Background(), 0); e != nil {
		t.Fatal(e)
	}
	if e = c.Revoke(context.Background(), "missing"); e == nil {
		t.Fatal("API error")
	}
}
