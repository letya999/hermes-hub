package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func actor() Actor {
	return Actor{PrincipalID: "alice", ContextID: "personal-alice", RuntimeID: "runtime-alice", PolicyVersion: "sha256-policy"}
}
func TestAssertions(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	v := Verifier{Keys: map[string]TrustedKey{"key": {PublicKey: pub, Issuer: "hub", Audiences: []string{"broker:control", "broker:runtime"}}}, Now: func() time.Time { return now }}
	c := Claims{KeyID: "key", Issuer: "hub", Audience: "broker:control", IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Method: "POST", RequestURI: "/v1/requests", BodySHA256: Hash([]byte(`{}`)), Actor: actor()}
	token, e := Sign(priv, c)
	if e != nil {
		t.Fatal(e)
	}
	got, e := v.Verify(token, "broker:control", "POST", "/v1/requests", []byte(`{}`))
	if e != nil || got.PrincipalID != "alice" {
		t.Fatal(e)
	}
	tests := []struct {
		name   string
		mutate func(*Claims)
	}{
		{"issuer", func(c *Claims) { c.Issuer = "other" }}, {"key", func(c *Claims) { c.KeyID = "unknown" }},
		{"audience", func(c *Claims) { c.Audience = "broker:runtime" }}, {"expired", func(c *Claims) { c.ExpiresAt = now.Unix() }},
		{"future", func(c *Claims) {
			c.IssuedAt = now.Add(time.Minute).Unix()
			c.ExpiresAt = now.Add(2 * time.Minute).Unix()
		}},
		{"long lifetime", func(c *Claims) { c.ExpiresAt = now.Add(10 * time.Minute).Unix() }},
		{"stale issued", func(c *Claims) { c.IssuedAt = now.Add(-3 * time.Minute).Unix() }},
		{"method", func(c *Claims) { c.Method = "GET" }}, {"URI", func(c *Claims) { c.RequestURI = "/v1/grants" }},
		{"body", func(c *Claims) { c.BodySHA256 = Hash([]byte("other")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := c
			tt.mutate(&cc)
			bad, _ := Sign(priv, cc)
			if _, e := v.Verify(bad, "broker:control", "POST", "/v1/requests", []byte(`{}`)); e == nil {
				t.Fatal("accepted")
			}
		})
	}
	for _, bad := range []string{"", token + "x", strings.Repeat("a", 5000), "bad.bad", "a.b.c", "!.x"} {
		if _, e := v.Verify(bad, "broker:control", "POST", "/v1/requests", []byte(`{}`)); e == nil {
			t.Fatal("bad token")
		}
	}
	c.Audience = "broker:runtime"
	token, _ = Sign(priv, c)
	if _, e = v.Verify(token, "broker:runtime", "POST", "/v1/requests", []byte(`{}`)); e == nil {
		t.Fatal("runtime missing binding")
	}
	c.Actor.BindingID = "binding"
	c.Actor.WorkloadID = "workload"
	token, _ = Sign(priv, c)
	if _, e = v.Verify(token, "broker:runtime", "POST", "/v1/requests", []byte(`{}`)); e != nil {
		t.Fatal(e)
	}
	// Signed duplicate fields are invalid despite being signed with a trusted key.
	data, _ := json.Marshal(c)
	data = append([]byte(`{"version":1,`), data[1:]...)
	p := base64.RawURLEncoding.EncodeToString(data)
	sig := ed25519.Sign(priv, []byte("hermes-credential-broker/assertion/v1."+p))
	if _, e = v.Verify(p+"."+base64.RawURLEncoding.EncodeToString(sig), "broker:runtime", "POST", "/v1/requests", []byte(`{}`)); e == nil {
		t.Fatal("duplicate key")
	}
	if _, e = Sign(nil, c); e == nil {
		t.Fatal("short key")
	}
	c.Actor.PrincipalID = "../alice"
	if _, e = Sign(priv, c); e == nil {
		t.Fatal("invalid actor")
	}
}
func TestActorIDs(t *testing.T) {
	for _, s := range []string{"A", "user:alice", "../x", strings.Repeat("a", 41), "", "é"} {
		if ValidID(s) {
			t.Fatal(s)
		}
	}
	a := actor()
	if !a.Valid() {
		t.Fatal("valid")
	}
	a.BindingID = "../../"
	if a.Valid() {
		t.Fatal("binding")
	}
	a = actor()
	a.PolicyVersion = ""
	if a.Valid() {
		t.Fatal("policy")
	}
}
func FuzzVerifier(f *testing.F) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	v := Verifier{Keys: map[string]TrustedKey{"key": {PublicKey: pub, Issuer: "hub", Audiences: []string{"broker:control"}}}}
	f.Add("bad.bad")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 8192 {
			return
		}
		if _, e := v.Verify(s, "broker:control", "GET", "/v1/contracts", nil); e == nil {
			t.Fatal("unsigned fuzz token accepted")
		}
	})
}
