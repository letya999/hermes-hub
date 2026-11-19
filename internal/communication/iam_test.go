package communication

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
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/iam"
)

// iamFixtureIdP is a compact OIDC issuer for the gateway link flow:
// discovery + JWKS + a token endpoint signing whatever claims the test
// stages (the nonce is planted from the parsed authorize redirect).
type iamFixtureIdP struct {
	*httptest.Server
	key    *rsa.PrivateKey
	claims map[string]any
}

func newIAMFixtureIdP(t *testing.T) *iamFixtureIdP {
	t.Helper()
	idp := &iamFixtureIdP{}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp.key = key
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": idp.URL + "/authorize",
			"token_endpoint":         idp.URL + "/token",
			"jwks_uri":               idp.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := idp.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "key-1",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"key-1"}`))
		payload, _ := json.Marshal(idp.claims)
		body := base64.RawURLEncoding.EncodeToString(payload)
		sum := sha256.Sum256([]byte(header + "." + body))
		sig, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, sum[:])
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": header + "." + body + "." + base64.RawURLEncoding.EncodeToString(sig)})
	})
	idp.Server = httptest.NewServer(mux)
	idp.claims = map[string]any{"iss": idp.URL, "sub": "corp-sub-1", "aud": "hub-client", "exp": time.Now().Add(time.Hour).Unix()}
	return idp
}

// newIAMGateway builds a real gateway with OIDC pointed at the fixture issuer.
func newIAMGateway(t *testing.T, idp *iamFixtureIdP) *Gateway {
	t.Helper()
	config := baseTestConfig(t)
	config.OIDCIssuer = idp.URL
	config.OIDCClientID = "hub-client"
	config.FormOrigin = "http://127.0.0.1:8081"
	g, err := newTestGateway(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if g.iam == nil {
		t.Fatal("iam state not configured")
	}
	return g
}

// iamRunFlow drives nonce → landing → authorize redirect → callback and
// returns the callback response code. The fixture's staged nonce is synced
// from the authorize URL the way a real IdP echoes it into the id_token.
func iamRunFlow(t *testing.T, g *Gateway, idp *iamFixtureIdP, token string) int {
	t.Helper()
	handler := g.Handler()
	start := httptest.NewRequest(http.MethodPost, "/iam/oidc/start", strings.NewReader(url.Values{"n": {token}}.Encode()))
	start.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	startRec := httptest.NewRecorder()
	handler.ServeHTTP(startRec, start)
	if startRec.Code != http.StatusSeeOther {
		return startRec.Code
	}
	authorize, err := url.Parse(startRec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := authorize.Query()
	idp.claims["nonce"] = q.Get("nonce")
	callback := httptest.NewRequest(http.MethodGet, "/iam/oidc/callback?code=fixture-code&state="+url.QueryEscape(q.Get("state")), nil)
	callbackRec := httptest.NewRecorder()
	handler.ServeHTTP(callbackRec, callback)
	return callbackRec.Code
}

func TestIAMLinkBindsVerifiedSubject(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)

	token := g.iamMintNonce("telegram-11", "alice")
	page := httptest.NewRecorder()
	g.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/iam/link/"+token, nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "iam/oidc/start") {
		t.Fatalf("landing page = %d", page.Code)
	}
	if code := iamRunFlow(t, g, idp, token); code != http.StatusOK {
		t.Fatalf("callback = %d", code)
	}
	link, ok := fixtureBinding(g, "alice", "telegram-11")
	if !ok || link.PrincipalID != "alice" || link.Subject != "corp-sub-1" || link.Issuer != idp.URL {
		t.Fatalf("link = %+v ok=%v", link, ok)
	}
}

func TestIAMLinkedTransportResolvesWithProvenance(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	if err := fixtureOIDCLink(t, g, "alice", "telegram-77", "corp-sub-1"); err != nil {
		t.Fatal(err)
	}
	user, ok := g.telegramUserFor(77, 77)
	if !ok || user.ID != "alice" {
		t.Fatalf("resolved = %+v ok=%v", user, ok)
	}
	env := user.Envelope(77)
	if env.Issuer != idp.URL || env.Subject != "corp-sub-1" {
		t.Fatalf("envelope provenance = %q/%q", env.Issuer, env.Subject)
	}
	if err := env.Validate("alice", "alice", env.RuntimeID, env.PolicyVersion); err != nil {
		t.Fatalf("stamped envelope invalid: %v", err)
	}
}

func TestIAMLinkTokenSingleUse(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	token := g.iamMintNonce("telegram-11", "alice")
	if code := iamRunFlow(t, g, idp, token); code != http.StatusOK {
		t.Fatalf("first flow = %d", code)
	}
	// Replay: the consumed nonce must be gone, not merely re-rejected.
	if code := iamRunFlow(t, g, idp, token); code != http.StatusGone {
		t.Fatalf("replay = %d", code)
	}
}

func TestIAMCallbackRejectsForeignState(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/iam/oidc/callback?code=x&state=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("foreign state = %d", rec.Code)
	}
}

func TestIAMEnrolResolvesProvisionedSubject(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	config := baseTestConfig(t)
	config.OIDCIssuer = idp.URL
	config.OIDCClientID = "hub-client"
	config.FormOrigin = "http://127.0.0.1:8081"
	config.Users[0].OIDCSubject = "corp-sub-1"
	g, err := newTestGateway(t, config)
	if err != nil {
		t.Fatal(err)
	}
	token := g.iamMintNonce("telegram-55", "")
	if code := iamRunFlow(t, g, idp, token); code != http.StatusOK {
		t.Fatalf("enrol flow = %d", code)
	}
	user, ok := g.telegramUserFor(55, 55)
	if !ok || user.ID != "alice" || user.SSOSubject != "corp-sub-1" {
		t.Fatalf("enrolled = %+v ok=%v", user, ok)
	}
}

func TestIAMEnrolUnprovisionedDenied(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	token := g.iamMintNonce("telegram-56", "")
	if code := iamRunFlow(t, g, idp, token); code != http.StatusForbidden {
		t.Fatalf("unprovisioned enrol = %d", code)
	}
	if _, ok := fixtureBinding(g, "alice", "telegram-56"); ok {
		t.Fatal("unprovisioned subject created a link")
	}
}

func TestIAMConflictDenied(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	config := baseTestConfig(t)
	config.OIDCIssuer, config.OIDCClientID, config.FormOrigin = idp.URL, "hub-client", "http://127.0.0.1:8081"
	config.Users = append(config.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{44}, StateDir: config.Users[0].StateDir + "-bob", WorkspaceDir: config.Users[0].WorkspaceDir + "-bob"})
	g, err := newTestGateway(t, config)
	if err != nil {
		t.Fatal(err)
	}
	// Subject already bound to a different principal → ambiguous identity.
	if err := fixtureOIDCLink(t, g, "bob", "telegram-44", "corp-sub-1"); err != nil {
		t.Fatal(err)
	}
	token := g.iamMintNonce("telegram-11", "alice")
	if code := iamRunFlow(t, g, idp, token); code != http.StatusConflict {
		t.Fatalf("conflict = %d", code)
	}
}

func TestIAMRoutesAbsentWithoutOIDC(t *testing.T) {
	g, err := newTestGateway(t, baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/iam/link/" + iam.RandomToken(), "/iam/oidc/start", "/iam/oidc/callback"} {
		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, rec.Code)
		}
	}
}

func TestIAMNonceExpiry(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	token := g.iamMintNonce("telegram-66", "alice")
	expired := g.now().Add(2 * iamLinkTTL)
	g.now = func() time.Time { return expired }
	page := httptest.NewRecorder()
	g.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/iam/link/"+token, nil))
	if page.Code != http.StatusGone {
		t.Fatalf("expired link = %d", page.Code)
	}
}

func TestIAMUnlinkDropsBinding(t *testing.T) {
	idp := newIAMFixtureIdP(t)
	defer idp.Close()
	g := newIAMGateway(t, idp)
	if err := fixtureOIDCLink(t, g, "alice", "telegram-88", "corp-sub-1"); err != nil {
		t.Fatal(err)
	}
	route := commandRoute{channel: "telegram_bot", transportID: "telegram-88"}
	if answer := g.unlinkCommand("en", route); !strings.Contains(answer, "removed") {
		t.Fatalf("unlink answer = %q", answer)
	}
	if _, ok := fixtureBinding(g, "alice", "telegram-88"); ok {
		t.Fatal("link survived unlink")
	}
	if _, ok := g.telegramUserFor(88, 88); ok {
		t.Fatal("unlinked transport still resolves")
	}
}
