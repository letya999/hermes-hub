package broker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/strictjson"
	"github.com/letya999/credential-broker/provider"
)

const (
	oauthAccess  = "$oauth_access_token"
	oauthRefresh = "$oauth_refresh_token"
	oauthExpiry  = "$oauth_expires_at"
)

// OAuthProvider is administrator-registered, not inferred from model-supplied URLs.
// The supplied Client must use the fixed-authority safenet transport in production.
type OAuthProvider struct {
	ID                  string
	AuthorizeURL        string
	TokenURL            string
	ClientID            string
	RedirectURL         string
	Scopes              []string
	AuthStyle           string // none, post, basic
	SecretSource        func(context.Context) (string, error)
	Client              *http.Client
	AuthorizeParameters map[string]string // allowlisted provider quirks only
}
type oauthTokens struct {
	Access, Refresh string
	ExpiresIn       int64
}

func (p *OAuthProvider) Validate() error {
	if p.Client == nil || p.ClientID == "" || len(p.ClientID) > 512 || len(p.Scopes) == 0 || len(p.Scopes) > 32 || !slices.Contains([]string{"none", "post", "basic"}, p.AuthStyle) {
		return ErrInvalid
	}
	for _, raw := range []string{p.AuthorizeURL, p.TokenURL, p.RedirectURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "https" {
			return ErrInvalid
		}
	}
	if p.AuthStyle != "none" && p.SecretSource == nil {
		return ErrInvalid
	}
	for _, s := range p.Scopes {
		if len(s) == 0 || len(s) > 200 || strings.ContainsAny(s, " \r\n\x00") {
			return ErrInvalid
		}
	}
	for k, v := range p.AuthorizeParameters {
		if !slices.Contains([]string{"access_type", "prompt", "response_mode"}, k) || strings.ContainsAny(v, "\r\n\x00") || len(v) > 80 {
			return ErrInvalid
		}
		if k == "response_mode" && v != "query" {
			return ErrInvalid
		}
	}
	return nil
}
func (p *OAuthProvider) authorization(state, verifier string) string {
	u, _ := url.Parse(p.AuthorizeURL)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.RedirectURL)
	q.Set("scope", strings.Join(p.Scopes, " "))
	q.Set("state", state)
	h := sha256.Sum256([]byte(verifier))
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(h[:]))
	q.Set("code_challenge_method", "S256")
	for k, v := range p.AuthorizeParameters {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func (p *OAuthProvider) tokens(ctx context.Context, form url.Values) (oauthTokens, error) {
	form.Set("client_id", p.ClientID)
	secret := ""
	if p.SecretSource != nil {
		var e error
		secret, e = p.SecretSource(ctx)
		if e != nil || secret == "" {
			return oauthTokens{}, ErrUnavailable
		}
	}
	if p.AuthStyle == "post" {
		form.Set("client_secret", secret)
	}
	r, e := http.NewRequestWithContext(ctx, "POST", p.TokenURL, strings.NewReader(form.Encode()))
	if e != nil {
		return oauthTokens{}, ErrUnavailable
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	if p.AuthStyle == "basic" {
		r.SetBasicAuth(url.QueryEscape(p.ClientID), url.QueryEscape(secret))
	}
	res, e := p.Client.Do(r)
	if e != nil {
		return oauthTokens{}, ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		if res.StatusCode == 400 || res.StatusCode == 401 {
			return oauthTokens{}, ErrReauthorize
		}
		return oauthTokens{}, ErrUnavailable
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || len(raw) > 65536 || !strictjson.Valid(raw) {
		return oauthTokens{}, ErrUnavailable
	}
	defer clear(raw)
	var doc struct {
		Access    string          `json:"access_token"`
		Refresh   string          `json:"refresh_token"`
		TokenType string          `json:"token_type"`
		Expires   json.RawMessage `json:"expires_in"`
		Scope     string          `json:"scope"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Access == "" || len(doc.Access) > 16384 || len(doc.Refresh) > 16384 || !strings.EqualFold(doc.TokenType, "bearer") || strings.ContainsAny(doc.Access+doc.Refresh, "\r\n\x00") {
		return oauthTokens{}, ErrUnavailable
	}
	n, e := strconv.ParseInt(strings.Trim(string(doc.Expires), `"`), 10, 64)
	if e != nil || n < 1 || n > 86400 {
		return oauthTokens{}, ErrUnavailable
	}
	if doc.Scope != "" {
		scopes := strings.Fields(doc.Scope)
		for _, want := range p.Scopes {
			if !slices.Contains(scopes, want) {
				return oauthTokens{}, ErrReauthorize
			}
		}
	}
	return oauthTokens{doc.Access, doc.Refresh, n}, nil
}
func (p *OAuthProvider) exchange(ctx context.Context, code, verifier string) (oauthTokens, error) {
	return p.tokens(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {p.RedirectURL}})
}
func (p *OAuthProvider) refresh(ctx context.Context, refresh string) (oauthTokens, error) {
	return p.tokens(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}
func mergeTokens(b provider.Bundle, t oauthTokens, now time.Time) {
	b[oauthAccess] = []byte(t.Access)
	b[oauthExpiry] = []byte(strconv.FormatInt(now.Add(time.Duration(t.ExpiresIn)*time.Second).Unix(), 10))
	if t.Refresh != "" {
		b[oauthRefresh] = []byte(t.Refresh)
	}
}
func (b *Broker) StartOAuth(ctx context.Context, id, raw, csrf string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.live() {
		return "", ErrUnavailable
	}
	s, r, e := b.validSession(raw, id, true)
	if e != nil {
		return "", e
	}
	if !seal.Equal(csrf, b.CSRF(raw, id)) || r.View.Status != "authorizing" {
		return "", ErrDenied
	}
	c := b.catalog[r.ContractKey]
	p := b.cfg.OAuth[c.OAuthProvider]
	if p == nil {
		return "", ErrInvalid
	}
	state := seal.Random()
	verifier := seal.Random()
	verifierRef, e := b.cfg.Providers.Put(ctx, c.Storage, provider.Bundle{"verifier": []byte(verifier)})
	if e != nil {
		return "", ErrUnavailable
	}
	o := oauthRecord{StateHash: seal.Digest(state), RequestID: id, SessionHash: s.Hash, VerifierRef: verifierRef, ExpiresAt: b.now().Add(5 * time.Minute)}
	r.OAuthStateHash = o.StateHash
	ev := b.audit("oauth.started", r.Actor)
	ev.RequestID = id
	if _, e = b.commit(mutation{Audit: ev, OAuth: &o, Request: &r}); e != nil {
		return "", e
	}
	return p.authorization(state, verifier), nil
}
func (b *Broker) CompleteOAuth(ctx context.Context, raw, state, code string) error {
	if len(state) != 43 || len(code) == 0 || len(code) > 4096 || strings.ContainsAny(code, "\r\n\x00") {
		return ErrDenied
	}
	b.mu.Lock()
	if !b.live() {
		b.mu.Unlock()
		return ErrUnavailable
	}
	o, ok := b.oauth[seal.Digest(state)]
	if !ok || o.Consumed || o.SessionHash != seal.Digest(raw) || !b.now().Before(o.ExpiresAt) {
		b.mu.Unlock()
		return ErrDenied
	}
	s, r, e := b.validSession(raw, o.RequestID, true)
	if e != nil || r.OAuthStateHash != o.StateHash || r.View.Status != "authorizing" {
		b.mu.Unlock()
		return ErrDenied
	}
	c := b.catalog[r.ContractKey]
	p := b.cfg.OAuth[c.OAuthProvider]
	if p == nil || r.DraftRef == nil {
		b.mu.Unlock()
		return ErrInvalid
	}
	o.Consumed = true
	r.View.Status = "exchanging"
	ev := b.audit("oauth.exchanging", r.Actor)
	ev.RequestID = r.View.ID
	if _, e = b.commit(mutation{Audit: ev, OAuth: &o, Request: &r}); e != nil {
		b.mu.Unlock()
		return e
	}
	b.mu.Unlock()
	values, e := b.cfg.Providers.Resolve(ctx, *r.DraftRef)
	if e == nil {
		var tokens oauthTokens
		var proof provider.Bundle
		proof, e = b.cfg.Providers.Resolve(ctx, o.VerifierRef)
		if e == nil {
			tokens, e = p.exchange(ctx, code, string(proof["verifier"]))
			proof.Wipe()
		}
		if writer := b.cfg.Providers[o.VerifierRef.Provider]; writer != nil {
			_ = writer.Delete(ctx, o.VerifierRef)
		}
		if e == nil {
			mergeTokens(values, tokens, b.now())
		}
	}
	if values != nil {
		defer values.Wipe()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.requests[r.View.ID]
	if current.View.Status != "exchanging" || current.OAuthStateHash != o.StateHash || !b.now().Before(current.View.ExpiresAt) {
		return ErrExpired
	}
	if e != nil {
		current.View.Status = "authorizing"
		ev = b.audit("oauth.failed", r.Actor)
		ev.RequestID = r.View.ID
		if _, err := b.commit(mutation{Audit: ev, Request: &current}); err != nil {
			return err
		}
		return e
	}
	ref, e := b.cfg.Providers.Put(ctx, c.Storage, values)
	if e != nil {
		// Exchanged codes are not retried, even if storage failed after provider success.
		current.View.Status = "authorizing"
		ev = b.audit("oauth.persistence_failed", r.Actor)
		ev.RequestID = r.View.ID
		_, _ = b.commit(mutation{Audit: ev, Request: &current})
		return ErrUnavailable
	}
	_, e = b.ready(&current, ref, true, &s)
	return e
}
func (b *Broker) AccessToken(ctx context.Context, u Use) (string, error) {
	if !u.Valid() {
		return "", ErrDenied
	}
	b.mu.Lock()
	lock := b.refreshLocks[u.CredentialID]
	if lock == nil {
		lock = &sync.Mutex{}
		b.refreshLocks[u.CredentialID] = lock
	}
	b.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	if !u.Valid() {
		return "", ErrDenied
	}
	b.mu.Lock()
	c := b.credentials[u.CredentialID]
	p := b.cfg.OAuth[b.catalog[c.ContractKey].OAuthProvider]
	b.mu.Unlock()
	if p == nil {
		return "", ErrInvalid
	}
	values, e := b.cfg.Providers.Resolve(ctx, c.Ref)
	if e != nil {
		return "", ErrUnavailable
	}
	defer values.Wipe()
	expiry, e := strconv.ParseInt(string(values[oauthExpiry]), 10, 64)
	if e == nil && b.now().Add(30*time.Second).Before(time.Unix(expiry, 0)) && len(values[oauthAccess]) > 0 {
		if !u.Valid() {
			return "", ErrDenied
		}
		return string(values[oauthAccess]), nil
	}
	if len(values[oauthRefresh]) == 0 {
		return "", ErrReauthorize
	}
	tokens, e := p.refresh(ctx, string(values[oauthRefresh]))
	if e != nil {
		return "", b.requireReauthorization(u.CredentialID, c.View.Revision)
	}
	mergeTokens(values, tokens, b.now())
	ref, e := b.cfg.Providers.Put(ctx, b.catalog[c.ContractKey].Storage, values)
	if e != nil {
		// A rotating refresh token might already be consumed at the provider.
		// Never retry with an old token after losing its replacement.
		return "", b.requireReauthorization(u.CredentialID, c.View.Revision)
	}
	b.mu.Lock()
	current := b.credentials[u.CredentialID]
	if !b.live() || current.View.Revision != c.View.Revision || current.View.Status != "active" || current.TokenRevision != c.TokenRevision {
		b.mu.Unlock()
		return "", ErrDenied
	}
	current.Ref = ref
	current.TokenRevision++
	current.OwnedRefs = append(current.OwnedRefs, ref)
	ev := b.audit("oauth.refreshed", identityFromCredential(current))
	ev.CredentialID = current.View.ID
	_, e = b.commit(mutation{Audit: ev, Credential: &current})
	b.mu.Unlock()
	if e != nil {
		return "", e
	}
	if !u.Valid() {
		return "", ErrDenied
	}
	return tokens.Access, nil
}

func identityFromCredential(c credentialRecord) identity.Actor {
	return identity.Actor{PrincipalID: c.View.PrincipalID, ContextID: c.View.ContextID}
}

func (b *Broker) requireReauthorization(id string, revision uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.credentials[id]
	if current.View.Revision == revision && current.View.Status == "active" {
		current.View.Status = "reauthorize_required"
		current.View.Revision++
		ev := b.audit("oauth.reauthorization_required", identityFromCredential(current))
		ev.CredentialID = current.View.ID
		if _, err := b.commit(mutation{Audit: ev, Credential: &current}); err != nil {
			return err
		}
		b.invalidateCredential(current.View.ID)
	}
	return ErrReauthorize
}
