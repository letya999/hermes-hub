// Package oauth implements a reusable authorization-code+PKCE and device-flow broker.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
)

var (
	ErrInvalid = errors.New("invalid oauth request")
	ErrDenied  = errors.New("oauth request denied")
	ErrExpired = errors.New("oauth state expired")
	ErrUsed    = errors.New("oauth state already used")
)

type Provider struct {
	Name          string
	AuthorizeURL  string
	TokenURL      string
	DeviceCodeURL string
	Audience      string
}

type Broker struct {
	Providers map[string]Provider
	Redirects []string
	Secrets   credstore.Backend
	HTTP      *http.Client
	Now       func() time.Time
	TTL       time.Duration

	mu      sync.Mutex
	states  map[string]pending
	devices map[string]pending
	refresh sync.Map
}

type pending struct {
	State      string
	Verifier   string
	Principal  string
	Context    string
	Connection string
	Provider   string
	Redirect   string
	DeviceCode string
	Expires    time.Time
	Used       bool
}

type StartResult struct {
	AuthorizeURL string
	State        string
	UserCode     string
	Verification string
	DeviceCode   string
}

type TokenMeta struct {
	Locator  string
	Provider string
	Status   string
}

func NewBroker(secrets credstore.Backend, redirects []string) *Broker {
	return &Broker{
		Providers: OfficialProviders(),
		Redirects: append([]string(nil), redirects...),
		Secrets:   secrets,
		HTTP:      &http.Client{Timeout: 10 * time.Second},
		Now:       time.Now,
		TTL:       10 * time.Minute,
		states:    map[string]pending{},
		devices:   map[string]pending{},
	}
}

func (b *Broker) StartAuthCode(principal, contextID, connection, provider, redirect string) (StartResult, error) {
	if err := b.ready(); err != nil {
		return StartResult{}, err
	}
	if !b.allowRedirect(redirect) {
		return StartResult{}, fmt.Errorf("%w: redirect", ErrDenied)
	}
	prov, err := b.provider(provider)
	if err != nil {
		return StartResult{}, err
	}
	state, err := randomToken(24)
	if err != nil {
		return StartResult{}, err
	}
	verifier, err := randomToken(32)
	if err != nil {
		return StartResult{}, err
	}
	now := b.Now().UTC()
	b.mu.Lock()
	b.states[state] = pending{State: state, Verifier: verifier, Principal: principal, Context: contextID, Connection: connection, Provider: provider, Redirect: redirect, Expires: now.Add(b.TTL)}
	b.mu.Unlock()
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("state", state)
	values.Set("redirect_uri", redirect)
	values.Set("code_challenge", pkceChallenge(verifier))
	values.Set("code_challenge_method", "S256")
	if prov.Audience != "" {
		values.Set("audience", prov.Audience)
	}
	authorize, err := url.Parse(prov.AuthorizeURL)
	if err != nil {
		return StartResult{}, err
	}
	q := authorize.Query()
	for key, vs := range values {
		q[key] = vs
	}
	authorize.RawQuery = q.Encode()
	return StartResult{AuthorizeURL: authorize.String(), State: state}, nil
}

func (b *Broker) HandleCallback(principal, contextID, connection, state, code, redirect string) (TokenMeta, error) {
	if err := b.ready(); err != nil {
		return TokenMeta{}, err
	}
	if !b.allowRedirect(redirect) {
		return TokenMeta{}, fmt.Errorf("%w: redirect", ErrDenied)
	}
	b.mu.Lock()
	pendingState, ok := b.states[state]
	if !ok {
		b.mu.Unlock()
		return TokenMeta{}, fmt.Errorf("%w: state", ErrDenied)
	}
	if pendingState.Used {
		b.mu.Unlock()
		return TokenMeta{}, fmt.Errorf("%w: state", ErrUsed)
	}
	if b.Now().UTC().After(pendingState.Expires) {
		b.mu.Unlock()
		return TokenMeta{}, fmt.Errorf("%w: state", ErrExpired)
	}
	if pendingState.Principal != principal || pendingState.Context != contextID || pendingState.Connection != connection || pendingState.Redirect != redirect {
		b.mu.Unlock()
		return TokenMeta{}, fmt.Errorf("%w: state binding", ErrDenied)
	}
	pendingState.Used = true
	b.states[state] = pendingState
	b.mu.Unlock()
	prov, err := b.provider(pendingState.Provider)
	if err != nil {
		return TokenMeta{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("code_verifier", pendingState.Verifier)
	tokens, err := b.exchange(prov.TokenURL, form)
	if err != nil {
		return TokenMeta{}, err
	}
	return b.persist(principal, connection, pendingState.Provider, tokens)
}

func (b *Broker) StartDevice(principal, contextID, connection, provider string) (StartResult, error) {
	if err := b.ready(); err != nil {
		return StartResult{}, err
	}
	prov, err := b.provider(provider)
	if err != nil {
		return StartResult{}, err
	}
	if prov.DeviceCodeURL == "" {
		return StartResult{}, fmt.Errorf("%w: device flow", ErrInvalid)
	}
	resp, err := b.postForm(prov.DeviceCodeURL, url.Values{"client_id": {"hermes-hub-fixture"}})
	if err != nil {
		return StartResult{}, err
	}
	deviceCode, _ := resp["device_code"].(string)
	userCode, _ := resp["user_code"].(string)
	verification, _ := resp["verification_uri"].(string)
	if deviceCode == "" || userCode == "" {
		return StartResult{}, fmt.Errorf("%w: device response", ErrInvalid)
	}
	now := b.Now().UTC()
	b.mu.Lock()
	b.devices[deviceCode] = pending{Principal: principal, Context: contextID, Connection: connection, Provider: provider, DeviceCode: deviceCode, Expires: now.Add(b.TTL)}
	b.mu.Unlock()
	return StartResult{UserCode: userCode, Verification: verification, DeviceCode: deviceCode}, nil
}

func (b *Broker) PollDevice(principal, contextID, connection, provider, deviceCode string) (TokenMeta, error) {
	if err := b.ready(); err != nil {
		return TokenMeta{}, err
	}
	b.mu.Lock()
	pendingState, ok := b.devices[deviceCode]
	if !ok {
		b.mu.Unlock()
		return TokenMeta{}, fmt.Errorf("%w: device", ErrDenied)
	}
	if pendingState.Used {
		b.mu.Unlock()
		return TokenMeta{}, fmt.Errorf("%w: device", ErrUsed)
	}
	if b.Now().UTC().After(pendingState.Expires) {
		b.mu.Unlock()
		return TokenMeta{}, fmt.Errorf("%w: device", ErrExpired)
	}
	if pendingState.Principal != principal || pendingState.Context != contextID || pendingState.Connection != connection || pendingState.Provider != provider {
		b.mu.Unlock()
		return TokenMeta{}, fmt.Errorf("%w: device binding", ErrDenied)
	}
	b.mu.Unlock()
	prov, err := b.provider(provider)
	if err != nil {
		return TokenMeta{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", deviceCode)
	tokens, err := b.exchange(prov.TokenURL, form)
	if err != nil {
		return TokenMeta{}, err
	}
	b.mu.Lock()
	pendingState.Used = true
	b.devices[deviceCode] = pendingState
	b.mu.Unlock()
	return b.persist(principal, connection, provider, tokens)
}

func (b *Broker) Refresh(principal, connection, provider string) (TokenMeta, error) {
	if err := b.ready(); err != nil {
		return TokenMeta{}, err
	}
	unlock := b.lockRefresh(connection)
	defer unlock()
	listed, err := b.Secrets.List(principal)
	if err != nil {
		return TokenMeta{}, err
	}
	var locator string
	for _, info := range listed {
		if info.Status != credstore.StatusActive {
			continue
		}
		if containsName(info.Names, "REFRESH_TOKEN") {
			locator = info.Locator
			break
		}
	}
	if locator == "" {
		return TokenMeta{}, fmt.Errorf("%w: refresh token", ErrDenied)
	}
	values, err := b.Secrets.Get(locator, principal)
	if err != nil {
		return TokenMeta{}, err
	}
	refresh := values["REFRESH_TOKEN"]
	if refresh == "" {
		return TokenMeta{}, fmt.Errorf("%w: refresh token", ErrDenied)
	}
	prov, err := b.provider(provider)
	if err != nil {
		return TokenMeta{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refresh)
	tokens, err := b.exchange(prov.TokenURL, form)
	if err != nil {
		return TokenMeta{}, err
	}
	if tokens["refresh_token"] == "" {
		tokens["refresh_token"] = refresh
	}
	if err := b.Secrets.Put(locator, principal, tokenValues(tokens)); err != nil {
		return TokenMeta{}, err
	}
	return TokenMeta{Locator: locator, Provider: provider, Status: credstore.StatusActive}, nil
}

func (b *Broker) DiscoverRemoteMCP(resourceURL string) (Provider, error) {
	if err := b.ready(); err != nil {
		return Provider{}, err
	}
	resource, err := b.getJSON(strings.TrimRight(resourceURL, "/") + ProtectedResourceWellKnown)
	if err != nil {
		return Provider{}, err
	}
	servers, _ := resource["authorization_servers"].([]any)
	if len(servers) == 0 {
		return Provider{}, fmt.Errorf("%w: authorization_servers", ErrInvalid)
	}
	issuer, _ := servers[0].(string)
	if issuer == "" {
		return Provider{}, fmt.Errorf("%w: issuer", ErrInvalid)
	}
	meta, err := b.getJSON(strings.TrimRight(issuer, "/") + AuthorizationServerWellKnown)
	if err != nil {
		return Provider{}, err
	}
	authorize, _ := meta["authorization_endpoint"].(string)
	token, _ := meta["token_endpoint"].(string)
	device, _ := meta["device_authorization_endpoint"].(string)
	if authorize == "" || token == "" {
		return Provider{}, fmt.Errorf("%w: discovered endpoints", ErrInvalid)
	}
	provider := Provider{Name: "remote-mcp", AuthorizeURL: authorize, TokenURL: token, DeviceCodeURL: device}
	b.mu.Lock()
	if b.Providers == nil {
		b.Providers = map[string]Provider{}
	}
	b.Providers["remote-mcp"] = provider
	b.mu.Unlock()
	return provider, nil
}

func (b *Broker) persist(principal, connection, provider string, tokens map[string]string) (TokenMeta, error) {
	locator, err := b.Secrets.NewLocator()
	if err != nil {
		return TokenMeta{}, err
	}
	values := tokenValues(tokens)
	if err := b.Secrets.Put(locator, principal, values); err != nil {
		return TokenMeta{}, err
	}
	return TokenMeta{Locator: locator, Provider: provider, Status: credstore.StatusActive}, nil
}

func tokenValues(tokens map[string]string) map[string]string {
	values := map[string]string{}
	if tokens["access_token"] != "" {
		values["ACCESS_TOKEN"] = tokens["access_token"]
	}
	if tokens["refresh_token"] != "" {
		values["REFRESH_TOKEN"] = tokens["refresh_token"]
	}
	if tokens["token_type"] != "" {
		values["TOKEN_TYPE"] = tokens["token_type"]
	}
	if tokens["expires_in"] != "" {
		values["EXPIRES_IN"] = tokens["expires_in"]
	}
	return values
}

func (b *Broker) exchange(tokenURL string, form url.Values) (map[string]string, error) {
	resp, err := b.postForm(tokenURL, form)
	if err != nil {
		return nil, err
	}
	tokens := map[string]string{}
	for _, key := range []string{"access_token", "refresh_token", "token_type", "expires_in"} {
		switch v := resp[key].(type) {
		case string:
			tokens[key] = v
		case float64:
			tokens[key] = fmt.Sprint(int(v))
		}
	}
	if tokens["access_token"] == "" {
		return nil, fmt.Errorf("%w: token response", ErrDenied)
	}
	return tokens, nil
}

func (b *Broker) postForm(endpoint string, form url.Values) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 64*1024 {
		return nil, fmt.Errorf("%w: token response too large", ErrInvalid)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: token endpoint %s", ErrDenied, resp.Status)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: token JSON", ErrInvalid)
	}
	return payload, nil
}

func (b *Broker) getJSON(endpoint string) (map[string]any, error) {
	resp, err := b.HTTP.Get(endpoint) // #nosec G107 -- callers pass fixture or discovered HTTPS URLs.
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: discovery %s", ErrDenied, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 64*1024 {
		return nil, fmt.Errorf("%w: discovery too large", ErrInvalid)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: discovery JSON", ErrInvalid)
	}
	return payload, nil
}

func (b *Broker) provider(name string) (Provider, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	prov, ok := b.Providers[name]
	if !ok || prov.AuthorizeURL == "" || prov.TokenURL == "" {
		return Provider{}, fmt.Errorf("%w: provider", ErrInvalid)
	}
	return prov, nil
}

func (b *Broker) allowRedirect(redirect string) bool {
	for _, allowed := range b.Redirects {
		if allowed == redirect {
			return true
		}
	}
	return false
}

func (b *Broker) ready() error {
	if b == nil || b.Secrets == nil {
		return fmt.Errorf("%w: broker", ErrInvalid)
	}
	if b.Now == nil {
		b.Now = time.Now
	}
	if b.TTL <= 0 {
		b.TTL = 10 * time.Minute
	}
	if b.HTTP == nil {
		b.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if b.states == nil {
		b.states = map[string]pending{}
	}
	if b.devices == nil {
		b.devices = map[string]pending{}
	}
	return nil
}

func (b *Broker) lockRefresh(connection string) func() {
	v, _ := b.refresh.LoadOrStore(connection, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
