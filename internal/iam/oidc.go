// Package iam holds the optional corporate-identity slice (#53): a
// stdlib-only OIDC client for ZITADEL-style issuers plus the durable
// channel-account link store. Single-user mode never touches it — an
// unset issuer means the package is inert.
package iam

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ErrVerification is the closed denial class for every failed OIDC
// check — the caller must not learn which claim failed.
var ErrVerification = errors.New("oidc verification failed")

// Claims is the verified outcome of an authorization-code exchange:
// the issuer that vouched and its opaque subject. Nothing else is
// trusted — email, name and groups are never read.
type Claims struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

// Provider is one configured OIDC issuer. https issuers only outside
// loopback — a plaintext corporate issuer is a misconfiguration, not a
// fallback.
type Provider struct {
	Issuer      string
	ClientID    string
	RedirectURL string
	Client      *http.Client
	Now         func() time.Time

	mu       sync.Mutex
	authURL  string
	tokenURL string
	jwksURI  string
	jwks     map[string]*rsa.PublicKey
}

func loopbackURL(u *url.URL) bool {
	h := u.Hostname()
	// host.docker.internal is Docker Desktop's designated host alias and
	// *.localhost is RFC 6761 loopback — either lets a dev IdP live
	// behind one issuer name that containers and browsers resolve alike.
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "host.docker.internal" || strings.HasSuffix(h, ".localhost")
}

// NewProvider validates the issuer shape. Discovery is lazy — the first
// AuthURL/Exchange fills endpoints, so a hub boots even while the IdP
// is unreachable.
func NewProvider(issuer, clientID, redirectURL string) (*Provider, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	u, err := url.Parse(issuer)
	// Issuers may carry a path — Authentik applications
	// (/application/o/<slug>) and Keycloak realms (/realms/<name>) issue
	// them; discovery appends /.well-known/openid-configuration to the
	// full issuer per RFC 8414. Query/fragment still reject.
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" {
		return nil, fmt.Errorf("invalid OIDC issuer")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopbackURL(u)) {
		return nil, fmt.Errorf("OIDC issuer must be https (loopback http allowed)")
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(redirectURL) == "" {
		return nil, fmt.Errorf("OIDC client id and redirect url required")
	}
	return &Provider{Issuer: issuer, ClientID: clientID, RedirectURL: redirectURL, Client: &http.Client{Timeout: 10 * time.Second}, Now: time.Now, jwks: map[string]*rsa.PublicKey{}}, nil
}

func (p *Provider) httpClient() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Provider) getJSON(ctx context.Context, raw string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil) // #nosec G107 -- operator-configured issuer endpoints
	if err != nil {
		return err
	}
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// discover fills endpoints from the issuer's well-known document.
// Endpoint hosts are re-pinned to the configured issuer's scheme —
// a malicious document cannot redirect token exchange off-issuer.
func (p *Provider) discover(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.authURL != "" {
		return nil
	}
	var doc struct {
		AuthURL  string `json:"authorization_endpoint"`
		TokenURL string `json:"token_endpoint"`
		JWKSURI  string `json:"jwks_uri"`
	}
	if err := p.getJSON(ctx, p.Issuer+"/.well-known/openid-configuration", &doc); err != nil {
		return fmt.Errorf("oidc discovery: %w", err)
	}
	for _, raw := range []string{doc.AuthURL, doc.TokenURL, doc.JWKSURI} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && loopbackURL(u))) {
			return fmt.Errorf("%w: endpoint scheme", ErrVerification)
		}
	}
	p.authURL, p.tokenURL, p.jwksURI = doc.AuthURL, doc.TokenURL, doc.JWKSURI
	return nil
}

// AuthURL builds the authorization request: authorization_code +
// PKCE (S256) + nonce, state carried opaquely by the caller.
func (p *Provider) AuthURL(ctx context.Context, state, nonce, challenge string) (string, error) {
	if err := p.discover(ctx); err != nil {
		return "", err
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.ClientID},
		"redirect_uri":          {p.RedirectURL},
		"scope":                 {"openid"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return p.authURL + "?" + q.Encode(), nil
}

// PKCEChallenge returns the S256 challenge for a fresh verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomToken is a 128-bit url-safe nonce/state/verifier.
func RandomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

// ValidToken bounds tokens minted by RandomToken before they reach a store
// lookup or an interpolated page — anything else cannot be a live nonce.
func ValidToken(token string) bool { return tokenPattern.MatchString(token) }

// Exchange runs the code exchange and validates the id_token end to
// end: RS256 signature against JWKS (kid miss forces one refetch —
// that is the rotation path), issuer, audience, expiry, nonce.
func (p *Provider) Exchange(ctx context.Context, code, verifier, expectNonce string) (Claims, error) {
	if err := p.discover(ctx); err != nil {
		return Claims{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.RedirectURL},
		"client_id":     {p.ClientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Claims{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return Claims{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Claims{}, fmt.Errorf("%w: token endpoint", ErrVerification)
	}
	var tokens struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokens); err != nil || tokens.IDToken == "" {
		return Claims{}, fmt.Errorf("%w: token response", ErrVerification)
	}
	return p.verifyIDToken(ctx, tokens.IDToken, expectNonce)
}

func (p *Provider) verifyIDToken(ctx context.Context, raw, expectNonce string) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Claims{}, ErrVerification
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &header) != nil || header.Alg != "RS256" || header.Kid == "" {
		return Claims{}, ErrVerification
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrVerification
	}
	key, err := p.jwksKey(ctx, header.Kid)
	if err != nil {
		return Claims{}, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return Claims{}, ErrVerification
	}
	var claims struct {
		Iss   string `json:"iss"`
		Sub   string `json:"sub"`
		Aud   any    `json:"aud"`
		Exp   int64  `json:"exp"`
		Nonce string `json:"nonce"`
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(pb, &claims) != nil {
		return Claims{}, ErrVerification
	}
	audOK := false
	switch a := claims.Aud.(type) {
	case string:
		audOK = a == p.ClientID
	case []any:
		for _, v := range a {
			if v == p.ClientID {
				audOK = true
			}
		}
	}
	// Issuer comparison tolerates a trailing slash: the configured issuer
	// is canonicalized in NewProvider while some IdPs (Authentik) assert
	// iss with a trailing "/" — same origin+path either way.
	if strings.TrimRight(claims.Iss, "/") != p.Issuer || !audOK || claims.Sub == "" || claims.Nonce != expectNonce {
		return Claims{}, ErrVerification
	}
	if claims.Exp == 0 || p.now().Unix() >= claims.Exp {
		return Claims{}, ErrVerification
	}
	return Claims{Issuer: claims.Iss, Subject: claims.Sub}, nil
}

// jwksKey resolves a signing key; an unknown kid triggers exactly one
// refetch — that is the issuer-key-rotation path, bounded so a hostile
// kid stream cannot loop fetches.
func (p *Provider) jwksKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	if key, ok := p.jwks[kid]; ok {
		p.mu.Unlock()
		return key, nil
	}
	uri := p.jwksURI
	p.mu.Unlock()

	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := p.getJSON(ctx, uri, &set); err != nil {
		return nil, fmt.Errorf("%w: jwks", ErrVerification)
	}
	fresh := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || k.N == "" || k.E == "" {
			continue
		}
		nb, err1 := base64.RawURLEncoding.DecodeString(k.N)
		eb, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		fresh[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
	}
	p.mu.Lock()
	p.jwks = fresh
	key, ok := fresh[kid]
	p.mu.Unlock()
	if !ok {
		return nil, ErrVerification
	}
	return key, nil
}
