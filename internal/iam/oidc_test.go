package iam

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"os"
	"testing"
	"time"
)

// fixtureIdP is a minimal OIDC issuer: discovery, JWKS and a token
// endpoint that signs an id_token with the current key.
type fixtureIdP struct {
	*httptest.Server
	key    atomic.Pointer[rsa.PrivateKey]
	kid    atomic.Value // string
	issuer string
	claims map[string]any
}

func newFixtureIdP(t *testing.T) *fixtureIdP {
	t.Helper()
	idp := &fixtureIdP{}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp.key.Store(key)
	idp.kid.Store("key-1")
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": idp.issuer + "/authorize",
			"token_endpoint":         idp.issuer + "/token",
			"jwks_uri":               idp.issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := idp.key.Load().PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": idp.kid.Load().(string),
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"` + idp.kid.Load().(string) + `"}`))
		payload, _ := json.Marshal(idp.claims)
		body := base64.RawURLEncoding.EncodeToString(payload)
		sum := sha256.Sum256([]byte(header + "." + body))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, idp.key.Load(), crypto.SHA256, sum[:])
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": header + "." + body + "." + base64.RawURLEncoding.EncodeToString(sig)})
	})
	idp.Server = httptest.NewServer(mux)
	idp.issuer = idp.URL
	idp.claims = map[string]any{"iss": idp.issuer, "sub": "corp-sub-1", "aud": "hub-client", "exp": time.Now().Add(time.Hour).Unix(), "nonce": "n-1"}
	return idp
}

func fixtureProvider(t *testing.T, idp *fixtureIdP) *Provider {
	t.Helper()
	p, err := NewProvider(idp.issuer, "hub-client", "https://hub.example.com/iam/oidc/callback")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOIDCExchangeHappyPath(t *testing.T) {
	idp := newFixtureIdP(t)
	defer idp.Close()
	p := fixtureProvider(t, idp)
	authURL, err := p.AuthURL(t.Context(), "st-1", "n-1", PKCEChallenge("ver"))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("nonce") != "n-1" || u.Query().Get("state") != "st-1" {
		t.Fatalf("auth url params: %s", authURL)
	}
	claims, err := p.Exchange(t.Context(), "code-1", "ver", "n-1")
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "corp-sub-1" || claims.Issuer != idp.issuer {
		t.Fatalf("claims: %+v", claims)
	}
}

func TestOIDCClaimRejections(t *testing.T) {
	for name, mutate := range map[string]func(*fixtureIdP){
		"bad-nonce":   func(i *fixtureIdP) { i.claims["nonce"] = "other" },
		"expired":     func(i *fixtureIdP) { i.claims["exp"] = time.Now().Add(-time.Hour).Unix() },
		"wrong-aud":   func(i *fixtureIdP) { i.claims["aud"] = "someone-else" },
		"wrong-iss":   func(i *fixtureIdP) { i.claims["iss"] = "https://evil.example.com" },
		"empty-sub":   func(i *fixtureIdP) { i.claims["sub"] = "" },
		"missing-sub": func(i *fixtureIdP) { delete(i.claims, "sub") },
	} {
		t.Run(name, func(t *testing.T) {
			idp := newFixtureIdP(t)
			defer idp.Close()
			mutate(idp)
			if _, err := fixtureProvider(t, idp).Exchange(t.Context(), "c", "v", "n-1"); err == nil {
				t.Fatal("invalid claims accepted")
			}
		})
	}
}

func TestOIDCKeyRotation(t *testing.T) {
	idp := newFixtureIdP(t)
	defer idp.Close()
	p := fixtureProvider(t, idp)
	if _, err := p.Exchange(t.Context(), "c", "v", "n-1"); err != nil {
		t.Fatal(err)
	}
	// Issuer rotates: new key, new kid. Cached JWKS misses once,
	// refetches, validates — never accepts the old key's token.
	newKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	idp.key.Store(newKey)
	idp.kid.Store("key-2")
	if _, err := p.Exchange(t.Context(), "c", "v", "n-1"); err != nil {
		t.Fatalf("post-rotation token rejected: %v", err)
	}
}

func TestOIDCIssuerValidation(t *testing.T) {
	for _, bad := range []string{"http://corp.example.com", "https://idp.example.com/path", "not-a-url", "https://idp.example.com?x=1"} {
		if _, err := NewProvider(bad, "c", "r"); err == nil {
			t.Fatalf("issuer %q accepted", bad)
		}
	}
	if _, err := NewProvider("http://localhost:9000", "c", "r"); err != nil {
		t.Fatalf("loopback http issuer denied: %v", err)
	}
}

func TestLinkStoreLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenLinkStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Link("https://idp", "sub-1", "alice", "telegram-11"); err != nil {
		t.Fatal(err)
	}
	if err := s.Link("https://idp", "sub-1", "alice", "slack-t-u"); err != nil {
		t.Fatal(err) // same verified account may hold a second channel
	}
	if err := s.Link("https://idp", "sub-1", "alice", "telegram-11"); err != nil {
		t.Fatal(err) // idempotent re-link
	}
	l, ok := s.Resolve("telegram-11")
	if !ok || l.Principal != "alice" || l.Subject != "sub-1" {
		t.Fatalf("resolve: %+v", l)
	}
	if err := s.Link("https://idp", "sub-2", "bob", "telegram-11"); err != ErrLinkConflict {
		t.Fatal("transport re-bound to another principal")
	}
	if err := s.Link("https://idp", "sub-1", "bob", "telegram-99"); err != ErrLinkConflict {
		t.Fatal("subject claimed a second principal")
	}
	if !s.Unlink("telegram-11") {
		t.Fatal("unlink failed")
	}
	if _, ok := s.Resolve("telegram-11"); ok {
		t.Fatal("unlinked transport still resolves")
	}
	if s.Unlink("telegram-11") {
		t.Fatal("double unlink reported success")
	}
	// Restart durability
	reopened, err := OpenLinkStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, ok := reopened.Resolve("slack-t-u"); !ok {
		t.Fatal("link lost across restart")
	}
	if got := len(reopened.LinksForPrincipal("alice")); got != 1 {
		t.Fatalf("links for principal: %d", got)
	}
}

func TestLinkStoreAuditHashedSubject(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenLinkStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.Link("https://idp", "secret-sub", "alice", "telegram-11")
	_ = s.Unlink("telegram-11")
	data, err := os.ReadFile(dir + "/iam-links-audit.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"event":"link"`) || !strings.Contains(text, `"event":"unlink"`) {
		t.Fatalf("audit missing events: %s", text)
	}
	if strings.Contains(text, "secret-sub") {
		t.Fatal("raw subject leaked into audit")
	}
}
