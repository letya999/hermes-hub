package communication

import (
	"crypto/subtle"
	"errors"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/iam"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
)

// iamState is the optional SSO enrolment/link surface (ADR-0100). nil on the
// gateway keeps the deployment channel-mapped only; when configured, a
// verified OIDC login either binds the presenting channel account to its
// already-mapped principal (/link nonce) or enrols a provisioned user by
// subject match (enrol nonce) — never by name or email.
type iamState struct {
	mu       sync.Mutex
	provider *iam.Provider
	links    *iam.LinkStore
	nonces   map[string]linkNonce
	pending  map[string]oidcPending
}

// linkNonce is a single-use challenge delivered into the channel. Principal
// set binds the verified login to that user (/link); empty principal means
// enrolment — the callback resolves the principal by OIDCSubject match.
type linkNonce struct {
	transportID string
	principal   string
	expires     time.Time
}

// oidcPending binds an in-flight authorize round trip: state → verifier,
// expected id_token nonce and the original link challenge.
type oidcPending struct {
	verifier    string
	oidcNonce   string
	transportID string
	principal   string
	expires     time.Time
}

const iamLinkTTL = 10 * time.Minute

func (g *Gateway) iamEnabled() bool { return g != nil && g.iam != nil }

// oidcProvider builds the provider lazily so FormOrigin — assigned after New
// on some deployments — lands in the redirect URL. Discovery runs once.
func (g *Gateway) oidcProvider() (*iam.Provider, error) {
	st := g.iam
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.provider != nil {
		return st.provider, nil
	}
	origin := strings.TrimSuffix(g.config.FormOrigin, "/")
	if origin == "" {
		return nil, errors.New("public form origin required for OIDC redirect")
	}
	provider, err := iam.NewProvider(g.config.OIDCIssuer, g.config.OIDCClientID, origin+"/iam/oidc/callback")
	if err != nil {
		return nil, err
	}
	st.provider = provider
	return provider, nil
}

// iamResolveTransport is the ingress fallback for channel accounts bound via
// a verified link instead of the static users.yaml mapping. The resolved user
// carries SSO provenance so every envelope it mints shows who vouched for it.
func (g *Gateway) iamResolveTransport(transportID string) (User, bool) {
	if !g.iamEnabled() || g.iam.links == nil {
		return User{}, false
	}
	link, ok := g.iam.links.Resolve(transportID)
	if !ok {
		return User{}, false
	}
	user := g.user(link.Principal)
	if !user.Enabled {
		return User{}, false
	}
	user.SSOIssuer, user.SSOSubject = link.Issuer, link.Subject
	return user, true
}

// iamPrincipalBySubject resolves the single enabled user provisioned with
// this corporate subject. Zero or ambiguous matches deny.
func (g *Gateway) iamPrincipalBySubject(subject string) (string, bool) {
	principal := ""
	for _, user := range g.config.Users {
		if !user.Enabled || user.OIDCSubject == "" || subtle.ConstantTimeCompare([]byte(user.OIDCSubject), []byte(subject)) != 1 {
			continue
		}
		if principal != "" {
			return "", false
		}
		principal = user.ID
	}
	return principal, principal != ""
}

// iamMintNonce issues a single-use link challenge for transportID; principal
// may be empty (enrolment) — the callback decides by subject match.
func (g *Gateway) iamMintNonce(transportID, principal string) string {
	st := g.iam
	token := iam.RandomToken()
	st.mu.Lock()
	g.iamSweepLocked()
	st.nonces[token] = linkNonce{transportID: transportID, principal: principal, expires: g.now().Add(iamLinkTTL)}
	st.mu.Unlock()
	return token
}

func (g *Gateway) iamTakeNonce(token string) (linkNonce, bool) {
	st := g.iam
	st.mu.Lock()
	defer st.mu.Unlock()
	g.iamSweepLocked()
	nonce, ok := st.nonces[token]
	if ok {
		delete(st.nonces, token)
	}
	return nonce, ok
}

func (g *Gateway) iamPeekNonce(token string) (linkNonce, bool) {
	st := g.iam
	st.mu.Lock()
	defer st.mu.Unlock()
	g.iamSweepLocked()
	nonce, ok := st.nonces[token]
	return nonce, ok
}

// iamSweepLocked expires stale challenges; caller holds st.mu.
func (g *Gateway) iamSweepLocked() {
	st := g.iam
	now := g.now()
	for key, nonce := range st.nonces {
		if now.After(nonce.expires) {
			delete(st.nonces, key)
		}
	}
	for key, pending := range st.pending {
		if now.After(pending.expires) {
			delete(st.pending, key)
		}
	}
}

func (g *Gateway) iamPutPending(p oidcPending) string {
	st := g.iam
	state := iam.RandomToken()
	st.mu.Lock()
	g.iamSweepLocked()
	st.pending[state] = p
	st.mu.Unlock()
	return state
}

func (g *Gateway) iamTakePending(state string) (oidcPending, bool) {
	st := g.iam
	st.mu.Lock()
	defer st.mu.Unlock()
	p, ok := st.pending[state]
	if ok && g.now().After(p.expires) {
		delete(st.pending, state)
		return oidcPending{}, false
	}
	if ok {
		delete(st.pending, state)
	}
	return p, ok
}

// registerIAM mounts the enrolment/link surface only when OIDC is configured.
func (g *Gateway) registerIAM(mux *http.ServeMux) {
	if !g.iamEnabled() {
		return
	}
	mux.HandleFunc("/iam/link/", g.serveIAMLink)
	mux.HandleFunc("/iam/oidc/start", g.serveOIDCStart)
	mux.HandleFunc("/iam/oidc/callback", g.serveOIDCCallback)
}

func (g *Gateway) iamPage(w http.ResponseWriter, lang locale.Lang, titleKey, bodyKey string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html lang="`+string(lang)+`"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+html.EscapeString(locale.T(lang, titleKey))+`</title><main><h1>`+html.EscapeString(locale.T(lang, titleKey))+`</h1><p>`+html.EscapeString(locale.T(lang, bodyKey))+`</p></main></html>`) // #nosec G705 -- lang is a constrained locale.Lang enum; every interpolated string is HTML-escaped
}

// serveIAMLink renders the landing page for a delivered link token. The token
// itself is the bearer secret — viewing costs nothing, spending it requires
// the POST form below which carries the token as its CSRF proof.
func (g *Gateway) serveIAMLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/iam/link/")
	lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
	if !iam.ValidToken(token) {
		g.iamPage(w, lang, "iam.gone", "iam.gone-body", http.StatusGone)
		return
	}
	if _, ok := g.iamPeekNonce(token); !ok {
		g.iamPage(w, lang, "iam.gone", "iam.gone-body", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html lang="`+string(lang)+`"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+html.EscapeString(locale.T(lang, "iam.link-title"))+`</title><main><h1>`+html.EscapeString(locale.T(lang, "iam.link-title"))+`</h1><p>`+html.EscapeString(locale.T(lang, "iam.link-body"))+`</p><form method="post" action="/iam/oidc/start"><input type="hidden" name="n" value="`+html.EscapeString(token)+`"><button>`+html.EscapeString(locale.T(lang, "iam.link-start"))+`</button></form></main></html>`) // #nosec G705 -- token is charset-validated by iam.ValidToken and escaped
}

// serveOIDCStart consumes the link token and opens the authorize round trip.
// The pending record — not the browser — carries the verifier and challenge.
func (g *Gateway) serveOIDCStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
	if r.ParseForm() != nil {
		g.iamPage(w, lang, "iam.denied", "iam.denied-body", http.StatusForbidden)
		return
	}
	token := r.PostForm.Get("n")
	nonce, ok := g.iamTakeNonce(token)
	if !ok || !iam.ValidToken(token) {
		g.iamPage(w, lang, "iam.gone", "iam.gone-body", http.StatusGone)
		return
	}
	provider, err := g.oidcProvider()
	if err != nil {
		g.iamPage(w, lang, "iam.denied", "iam.denied-body", http.StatusServiceUnavailable)
		return
	}
	verifier, oidcNonce := iam.RandomToken(), iam.RandomToken()
	state := g.iamPutPending(oidcPending{verifier: verifier, oidcNonce: oidcNonce, transportID: nonce.transportID, principal: nonce.principal, expires: g.now().Add(iamLinkTTL)})
	authURL, err := provider.AuthURL(r.Context(), state, oidcNonce, iam.PKCEChallenge(verifier))
	if err != nil {
		g.iamTakePending(state)
		g.iamPage(w, lang, "iam.denied", "iam.denied-body", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// serveOIDCCallback completes enrolment/linking: exchange binds code→claims,
// then the challenge decides the principal — never user input.
func (g *Gateway) serveOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
	deny := func(bodyKey string, status int) { g.iamPage(w, lang, "iam.denied", bodyKey, status) }
	pending, ok := g.iamTakePending(r.URL.Query().Get("state"))
	if !ok {
		deny("iam.gone-body", http.StatusGone)
		return
	}
	code := r.URL.Query().Get("code")
	provider, err := g.oidcProvider()
	if err != nil || code == "" {
		deny("iam.denied-body", http.StatusForbidden)
		return
	}
	claims, err := provider.Exchange(r.Context(), code, pending.verifier, pending.oidcNonce)
	if err != nil {
		deny("iam.denied-body", http.StatusForbidden)
		return
	}
	principal := pending.principal
	if principal == "" {
		resolved, found := g.iamPrincipalBySubject(claims.Subject)
		if !found {
			deny("iam.unprovisioned-body", http.StatusForbidden)
			return
		}
		principal = resolved
	}
	if err := g.iam.links.Link(claims.Issuer, claims.Subject, principal, pending.transportID); err != nil {
		deny("iam.conflict-body", http.StatusConflict)
		return
	}
	g.iamPage(w, lang, "iam.done", "iam.done-body", http.StatusOK)
}

// linkCommand mints the /link challenge for the verified channel sender and
// answers with the one-time URL. IAM-disabled replies stay honest.
func (g *Gateway) linkCommand(lang locale.Lang, route commandRoute) string {
	if !g.iamEnabled() {
		return locale.T(lang, "chat.link-disabled")
	}
	origin := strings.TrimSuffix(g.config.FormOrigin, "/")
	if origin == "" || route.transportID == "" {
		return locale.T(lang, "chat.link-unavailable")
	}
	token := g.iamMintNonce(route.transportID, route.principal)
	return locale.T(lang, "chat.link-url", origin+"/iam/link/"+token)
}

// unlinkCommand drops this channel account's verified binding. Unmapped
// accounts simply report there is nothing to drop.
func (g *Gateway) unlinkCommand(lang locale.Lang, route commandRoute) string {
	if !g.iamEnabled() {
		return locale.T(lang, "chat.link-disabled")
	}
	if route.transportID == "" || !g.iam.links.Unlink(route.transportID) {
		return locale.T(lang, "chat.unlink-none")
	}
	return locale.T(lang, "chat.unlinked")
}

// iamEnrolHint mints an enrolment challenge for an unmapped telegram sender
// when OIDC is configured; the returned URL lets them prove a provisioned
// corporate subject instead of being silently denied.
func (g *Gateway) iamEnrolHint(sender int64) string {
	if !g.iamEnabled() || strings.TrimSpace(g.config.FormOrigin) == "" {
		return ""
	}
	token := g.iamMintNonce("telegram-"+strconv.FormatInt(sender, 10), "")
	return strings.TrimSuffix(g.config.FormOrigin, "/") + "/iam/link/" + token
}

// slackEnrolHint is the slack counterpart of iamEnrolHint.
//
// get an answer delivery like telegram's chat.no-access-enrol reply.
//
//lint:ignore U1000 staged for the slack deny path once its unmapped senders
func (g *Gateway) slackEnrolHint(teamID, userID string) string {
	if !g.iamEnabled() || strings.TrimSpace(g.config.FormOrigin) == "" {
		return ""
	}
	token := g.iamMintNonce(identity.SlackIdentity(teamID, userID), "")
	return strings.TrimSuffix(g.config.FormOrigin, "/") + "/iam/link/" + token
}
