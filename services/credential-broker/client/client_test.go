package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/identity"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func signerActor(t *testing.T) (Signer, identity.Actor) {
	t.Helper()
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return Signer{KeyID: "key", Issuer: "hub", Audience: "broker:control", PrivateKey: key}, identity.Actor{PrincipalID: "alice", ContextID: "team", RuntimeID: "runtime1", PolicyVersion: "policy1"}
}
func TestSigningAndSDKMethods(t *testing.T) {
	sign, a := signerActor(t)
	verifier := identity.Verifier{Keys: map[string]identity.TrustedKey{"key": {PublicKey: sign.PrivateKey.Public().(ed25519.PublicKey), Issuer: "hub", Audiences: []string{"broker:control"}}}}
	h := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		if _, e = verifier.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "broker:control", r.Method, r.URL.RequestURI(), raw); e != nil {
			t.Error(e)
		}
		body := "{}"
		if r.URL.Path == "/v1/contracts" {
			body = "[]"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	c, e := New("https://broker.example", sign, a, h)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	checks := []func() error{func() error { _, e := c.CreateRequest(ctx, v1.CreateRequest{}); return e }, func() error { _, e := c.Request(ctx, "request1"); return e }, func() error { return c.Approve(ctx, "request1", "CODE") }, func() error { _, e := c.Grant(ctx, "cred1", v1.GrantRequest{}); return e }, func() error { _, e := c.Acquire(ctx, v1.AcquireLease{}); return e }, func() error { _, e := c.Events(ctx, 10); return e }, func() error { _, e := c.Contracts(ctx); return e }, func() error { return c.Revoke(ctx, "cred1") }, func() error { _, e := c.Materialize(ctx, "lease1"); return e }, func() error { return c.Release(ctx, "lease1", v1.RuntimeRelease{}) }, func() error { _, e := c.Renew(ctx, "lease1", 120); return e }}
	for _, check := range checks {
		if e := check(); e != nil {
			t.Fatal(e)
		}
	}
	if c.http == h || h.CheckRedirect != nil || c.http.Timeout == 0 {
		t.Fatal("caller HTTP client mutated")
	}
	if c.http.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects")
	}
}
func TestClientValidationAndSanitizedErrors(t *testing.T) {
	sign, a := signerActor(t)
	for _, base := range []string{"http://internet.example", "https://user:password@broker.example", "https://broker.example/path", "https://broker.example?q=x", "https://broker.example#fragment", ":bad", "file:///etc/passwd"} {
		if _, e := New(base, sign, a, nil); e == nil {
			t.Fatal(base)
		}
	}
	bad := sign
	bad.PrivateKey = nil
	if _, e := New("https://broker.example", bad, a, nil); e == nil {
		t.Fatal("key")
	}
	bad = sign
	bad.Audience = "other"
	if _, e := New("https://broker.example", bad, a, nil); e == nil {
		t.Fatal("audience")
	}
	c, e := New("http://127.0.0.1:8787", sign, a, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Do(context.Background(), "POST", "/v1/requests", make(chan int), nil); e == nil {
		t.Fatal("marshal")
	}
	for _, p := range []string{"//evil.example", "/v1/foo#x", "/v1/foo\\bar", "/v1/foo\n"} {
		if _, e = c.request(context.Background(), "GET", p, nil, ""); e == nil {
			t.Fatal(p)
		}
	}
	for _, tc := range []struct {
		status     int
		body, want string
	}{{403, `{"code":"denied"}`, "denied"}, {500, `{"code":"SECRET_TOKEN_IN_UPSTREAM_BODY"}`, "request_failed"}, {401, "not json", "request_failed"}, {200, "invalid JSON", "invalid broker response"}, {200, strings.Repeat("x", (1<<20)+1), "invalid broker response"}} {
		c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})
		var out any
		e = c.Do(context.Background(), "GET", "/v1/contracts", nil, &out)
		if e == nil || !strings.Contains(e.Error(), tc.want) || strings.Contains(e.Error(), "SECRET_TOKEN") {
			t.Fatal(e)
		}
	}
	c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("unsafe transport secret") })
	if e = c.Do(context.Background(), "GET", "/v1/contracts", nil, nil); e == nil || strings.Contains(e.Error(), "unsafe") {
		t.Fatal(e)
	}
	c.signer.PrivateKey = nil
	if _, e = c.request(context.Background(), "GET", "/v1/contracts", nil, ""); e == nil {
		t.Fatal("signing")
	}
}
func TestClientNeverFollowsRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect was followed") }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/v1/contracts")
		w.WriteHeader(307)
	}))
	defer source.Close()
	sign, a := signerActor(t)
	c, e := New(source.URL, sign, a, &http.Client{Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	var out json.RawMessage
	if e = c.Do(context.Background(), "GET", "/v1/contracts", nil, &out); e == nil {
		t.Fatal("redirect treated as success")
	}
}
