package oauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
)

func authorizeCode(t *testing.T, authorizeURL string) string {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := location.Query().Get("code")
	if code == "" {
		t.Fatalf("missing code in %s", resp.Header.Get("Location"))
	}
	return code
}

func testSecrets(t *testing.T) credstore.Backend {
	t.Helper()
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testBroker(t *testing.T, fixture *Fixture, secrets credstore.Backend) *Broker {
	t.Helper()
	redirect := "http://127.0.0.1/callback"
	broker := NewBroker(secrets, []string{redirect})
	broker.HTTP = fixture.Server.Client()
	broker.Providers["fixture"] = fixture.Provider()
	return broker
}

func TestOfficialProviderEndpoints(t *testing.T) {
	providers := OfficialProviders()
	if providers["google"].AuthorizeURL != GoogleAuthorizeURL || providers["google"].TokenURL != GoogleTokenURL || providers["google"].DeviceCodeURL != GoogleDeviceURL {
		t.Fatalf("google endpoints=%+v", providers["google"])
	}
	if GoogleAuthorizeURL != "https://accounts.google.com/o/oauth2/v2/auth" || GoogleTokenURL != "https://oauth2.googleapis.com/token" || GoogleDeviceURL != "https://oauth2.googleapis.com/device/code" {
		t.Fatal("google official URLs drifted")
	}
	if providers["slack"].AuthorizeURL != "https://slack.com/oauth/v2/authorize" || providers["slack"].TokenURL != "https://slack.com/api/oauth.v2.access" {
		t.Fatalf("slack endpoints=%+v", providers["slack"])
	}
	if providers["atlassian"].AuthorizeURL != "https://auth.atlassian.com/authorize" || providers["atlassian"].Audience != "api.atlassian.com" || providers["atlassian"].TokenURL != "https://auth.atlassian.com/oauth/token" {
		t.Fatalf("atlassian endpoints=%+v", providers["atlassian"])
	}
}

func TestAuthCodePKCEAndSingleUseState(t *testing.T) {
	fixture := NewFixture()
	defer fixture.Close()
	secrets := testSecrets(t)
	broker := testBroker(t, fixture, secrets)
	redirect := "http://127.0.0.1/callback"
	start, err := broker.StartAuthCode("alice", "alice", "google-work", "fixture", redirect)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(start.AuthorizeURL)
	if err != nil || parsed.Query().Get("code_challenge_method") != "S256" || parsed.Query().Get("state") != start.State {
		t.Fatalf("authorize url=%s err=%v", start.AuthorizeURL, err)
	}
	code := authorizeCode(t, start.AuthorizeURL)
	meta, err := broker.HandleCallback("alice", "alice", "google-work", start.State, code, redirect)
	if err != nil || meta.Locator == "" {
		t.Fatalf("callback=%+v err=%v", meta, err)
	}
	values, err := secrets.Get(meta.Locator, "alice")
	if err != nil || values["ACCESS_TOKEN"] == "" || values["REFRESH_TOKEN"] == "" {
		t.Fatalf("stored tokens=%v err=%v", values, err)
	}
	if _, err := broker.HandleCallback("alice", "alice", "google-work", start.State, code, redirect); !errors.Is(err, ErrUsed) {
		t.Fatalf("replayed state: %v", err)
	}
	if _, err := broker.StartAuthCode("alice", "alice", "google-work", "fixture", "https://evil.example/cb"); !errors.Is(err, ErrDenied) {
		t.Fatalf("non-allowlisted redirect: %v", err)
	}
	start2, err := broker.StartAuthCode("alice", "alice", "google-work", "fixture", redirect)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.HandleCallback("bob", "alice", "google-work", start2.State, "code", redirect); !errors.Is(err, ErrDenied) {
		t.Fatalf("mismatched principal: %v", err)
	}
}

func TestExpiredStateDenied(t *testing.T) {
	fixture := NewFixture()
	defer fixture.Close()
	broker := testBroker(t, fixture, testSecrets(t))
	now := time.Now().UTC()
	broker.Now = func() time.Time { return now }
	broker.TTL = time.Minute
	redirect := "http://127.0.0.1/callback"
	start, err := broker.StartAuthCode("alice", "alice", "google-work", "fixture", redirect)
	if err != nil {
		t.Fatal(err)
	}
	broker.Now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := broker.HandleCallback("alice", "alice", "google-work", start.State, "code", redirect); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired state: %v", err)
	}
}

func TestDeviceFlowAndSerializedRefresh(t *testing.T) {
	fixture := NewFixture()
	defer fixture.Close()
	secrets := testSecrets(t)
	broker := testBroker(t, fixture, secrets)
	start, err := broker.StartDevice("alice", "alice", "google-work", "fixture")
	if err != nil || start.UserCode == "" || start.DeviceCode == "" {
		t.Fatalf("device start=%+v err=%v", start, err)
	}
	meta, err := broker.PollDevice("alice", "alice", "google-work", "fixture", start.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	before, err := secrets.Get(meta.Locator, "alice")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := broker.Refresh("alice", "google-work", "fixture")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := secrets.Get(meta.Locator, "alice")
	if err != nil || after["ACCESS_TOKEN"] == "" || after["ACCESS_TOKEN"] == before["ACCESS_TOKEN"] {
		t.Fatalf("refresh did not rotate access token: before=%v after=%v err=%v", before, after, err)
	}
	if strings.Contains(meta.Locator, after["ACCESS_TOKEN"]) {
		t.Fatal("token leaked into locator")
	}
}

func TestRemoteMCPDiscoveryUsesWellKnown(t *testing.T) {
	fixture := NewFixture()
	defer fixture.Close()
	broker := testBroker(t, fixture, testSecrets(t))
	provider, err := broker.DiscoverRemoteMCP(fixture.Server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if provider.AuthorizeURL != fixture.Server.URL+"/authorize" || provider.TokenURL != fixture.Server.URL+"/token" {
		t.Fatalf("discovered=%+v", provider)
	}
	if !strings.HasSuffix(ProtectedResourceWellKnown, "oauth-protected-resource") || !strings.HasSuffix(AuthorizationServerWellKnown, "oauth-authorization-server") {
		t.Fatal("RFC well-known paths drifted")
	}
}

func TestBrokerDenyPaths(t *testing.T) {
	fixture := NewFixture()
	defer fixture.Close()
	broker := testBroker(t, fixture, testSecrets(t))
	if _, err := broker.StartDevice("alice", "alice", "slack-work", "slack"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("slack device: %v", err)
	}
	if _, err := broker.HandleCallback("alice", "alice", "google-work", "missing", "code", "http://127.0.0.1/callback"); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing state: %v", err)
	}
	if _, err := broker.DiscoverRemoteMCP(fixture.Server.URL + "/missing"); !errors.Is(err, ErrDenied) {
		t.Fatalf("bad discovery: %v", err)
	}
	if n := fixture.RefreshCount("none"); n != 0 {
		t.Fatal(n)
	}
	if _, err := broker.Refresh("alice", "missing-connection", "fixture"); !errors.Is(err, ErrDenied) {
		t.Fatalf("refresh without token: %v", err)
	}
	if err := (*Broker)(nil).ready(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil broker: %v", err)
	}
	start, err := broker.StartDevice("alice", "alice", "google-work", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.PollDevice("bob", "alice", "google-work", "fixture", start.DeviceCode); !errors.Is(err, ErrDenied) {
		t.Fatalf("device principal mismatch: %v", err)
	}
	broker.Now = func() time.Time { return time.Now().UTC().Add(24 * time.Hour) }
	if _, err := broker.PollDevice("alice", "alice", "google-work", "fixture", start.DeviceCode); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired device: %v", err)
	}
}

func TestTokensStayInEncryptedStoreNotHermesPayload(t *testing.T) {
	fixture := NewFixture()
	defer fixture.Close()
	secrets := testSecrets(t)
	broker := testBroker(t, fixture, secrets)
	redirect := "http://127.0.0.1/callback"
	start, err := broker.StartAuthCode("alice", "alice", "google-work", "fixture", redirect)
	if err != nil {
		t.Fatal(err)
	}
	code := authorizeCode(t, start.AuthorizeURL)
	meta, err := broker.HandleCallback("alice", "alice", "google-work", start.State, code, redirect)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := secrets.List("alice")
	if err != nil || len(listed) != 1 {
		t.Fatal(listed, err)
	}
	for _, name := range listed[0].Names {
		if strings.Contains(strings.ToLower(name), "hermes") {
			t.Fatal(name)
		}
	}
	if meta.Status != credstore.StatusActive {
		t.Fatal(meta)
	}
	payload := start.AuthorizeURL + meta.Locator + meta.Status
	values, _ := secrets.Get(meta.Locator, "alice")
	if strings.Contains(payload, values["ACCESS_TOKEN"]) || strings.Contains(payload, values["REFRESH_TOKEN"]) {
		t.Fatal("hermes-facing payload contained tokens")
	}
}

type putErrBackend struct{ credstore.Backend }

func (b putErrBackend) Put(string, string, map[string]string) error { return credstore.ErrInvalid }

func TestReadyDefaultsAudienceReplayAndDiscoveryErrors(t *testing.T) {
	fixture := NewFixture()
	defer fixture.Close()
	secrets := testSecrets(t)
	redirect := "http://127.0.0.1/callback"
	broker := &Broker{Secrets: secrets, Redirects: []string{redirect}, Providers: map[string]Provider{"fixture": fixture.Provider()}}
	start, err := broker.StartAuthCode("alice", "alice", "google-work", "fixture", redirect)
	if err != nil || start.State == "" {
		t.Fatalf("ready defaults: %+v err=%v", start, err)
	}
	if _, err := broker.StartAuthCode("alice", "alice", "google-work", "missing", redirect); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provider: %v", err)
	}
	broker.Providers["atlassian"] = Provider{Name: "atlassian", AuthorizeURL: fixture.Server.URL + "/authorize", TokenURL: fixture.Server.URL + "/token", Audience: "api.atlassian.com"}
	atlassian, err := broker.StartAuthCode("alice", "alice", "jira-work", "atlassian", redirect)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(atlassian.AuthorizeURL)
	if err != nil || parsed.Query().Get("audience") != "api.atlassian.com" {
		t.Fatalf("audience missing: %s err=%v", atlassian.AuthorizeURL, err)
	}
	code := authorizeCode(t, start.AuthorizeURL)
	if _, err := broker.HandleCallback("alice", "alice", "google-work", start.State, code, "https://evil.example/cb"); !errors.Is(err, ErrDenied) {
		t.Fatalf("callback redirect: %v", err)
	}
	meta, err := broker.HandleCallback("alice", "alice", "google-work", start.State, code, redirect)
	if err != nil || meta.Locator == "" {
		t.Fatalf("callback=%+v err=%v", meta, err)
	}
	device, err := broker.StartDevice("alice", "alice", "google-work", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.PollDevice("alice", "alice", "google-work", "fixture", device.DeviceCode); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.PollDevice("alice", "alice", "google-work", "fixture", device.DeviceCode); !errors.Is(err, ErrUsed) {
		t.Fatalf("replayed device: %v", err)
	}
	if _, err := broker.PollDevice("alice", "alice", "google-work", "fixture", "missing"); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing device: %v", err)
	}
	if _, err := broker.Refresh("alice", "google-work", "missing"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("refresh unknown provider: %v", err)
	}
	failing := NewBroker(putErrBackend{Backend: secrets}, []string{redirect})
	failing.HTTP = fixture.Server.Client()
	failing.Providers["fixture"] = fixture.Provider()
	startFail, err := failing.StartAuthCode("alice", "alice", "google-work", "fixture", redirect)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.HandleCallback("alice", "alice", "google-work", startFail.State, authorizeCode(t, startFail.AuthorizeURL), redirect); !errors.Is(err, credstore.ErrInvalid) {
		t.Fatalf("persist put: %v", err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "oauth-protected-resource") {
			_ = json.NewEncoder(w).Encode(map[string]any{"authorization_servers": []any{}})
			return
		}
		http.NotFound(w, r)
	}))
	defer empty.Close()
	if _, err := broker.DiscoverRemoteMCP(empty.URL); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty servers: %v", err)
	}
	issuerEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "oauth-protected-resource") {
			_ = json.NewEncoder(w).Encode(map[string]any{"authorization_servers": []any{""}})
			return
		}
		http.NotFound(w, r)
	}))
	defer issuerEmpty.Close()
	if _, err := broker.DiscoverRemoteMCP(issuerEmpty.URL); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty issuer: %v", err)
	}
	badJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer badJSON.Close()
	if _, err := broker.getJSON(badJSON.URL); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad discovery json: %v", err)
	}
	if _, err := broker.postForm(badJSON.URL, url.Values{"grant_type": {"refresh_token"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad token json: %v", err)
	}
	resp, err := http.Get(fixture.Server.URL + "/authorize")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("pkce missing status=%d", resp.StatusCode)
	}
	if itoa(0) != "0" || !containsName([]string{"REFRESH_TOKEN"}, "REFRESH_TOKEN") || containsName(nil, "REFRESH_TOKEN") {
		t.Fatal("helpers")
	}
}
