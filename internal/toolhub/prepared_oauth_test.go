package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/oauth"
)

func TestPreparedOAuthHandoffAndCallback(t *testing.T) {
	entry, matched, err := preparedForRepository("https://github.com/nspady/google-calendar-mcp", "")
	if err != nil || !matched || entry.OAuth == nil {
		t.Fatal("reviewed OAuth entry missing", err)
	}
	d := statefulContainerDefinition()
	d.DefinitionID, d.Version = entry.ID, "1.0.0"
	d.Source.Repository, d.Source.CommitSHA = entry.Source.Repository, entry.Source.CommitSHA
	d.Credentials = []CredentialInput{{Name: entry.OAuth.ClientInput, Required: true}}
	d.CredentialContractID, d.CredentialContractRevision = entry.ContractID, entry.ContractRevision
	d.CredentialContractEnv = map[string]string{entry.OAuth.ClientInput: entry.OAuth.ClientInput}
	store := NewStore()
	if err := store.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	ref := CredentialReference{Schema: SchemaVersion, CredentialRefID: "cred_calendar", ConnectionID: "conn_calendar", Revision: 1, Backend: "credential-broker", Locator: "credential_calendar", Keys: []string{entry.OAuth.ClientInput}, BrokerGrantID: "grant_calendar", BrokerContractID: entry.ContractID, BrokerContractRevision: entry.ContractRevision, BrokerEnv: d.CredentialContractEnv, Status: ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(Connection{Schema: SchemaVersion, ConnectionID: ref.ConnectionID, Owner: OwnerRef{Type: PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	on := Onboarding{Schema: SchemaVersion, OnboardingID: "onboard_calendar", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: OnboardingCatalog, Phase: PhaseAwaitingConfirm, DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Locator: ref.Locator, ConfirmationNonce: "nonce", ConfirmationExpires: time.Now().Add(time.Hour), Revision: 1, CreatedAt: time.Now()}
	if err := store.PutOnboarding(on); err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "tokens.enc"), Key: bytes.Repeat([]byte("k"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	clientFile := filepath.Join(root, "client.json")
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientFile, []byte(`{"installed":{"client_id":"fixture.apps.googleusercontent.com","client_secret":"fixture-secret","redirect_uris":["http://localhost"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	c := &ControlPlane{Store: store, Secrets: secrets, FormOrigin: "http://127.0.0.1:8090", Injector: func(_ context.Context, _ EffectiveBinding) (CredentialInjection, error) {
		return CredentialInjection{Environment: map[string]string{entry.OAuth.ClientInput: "/run/mcp-secrets/google/client.json"}, Mounts: []Mount{{Source: clientFile, Target: "/run/mcp-secrets/google/client.json", ReadOnly: true}, {Source: stateDir, Target: entry.StateTarget}}, Checkpoint: func() error { checkpoints++; return nil }}, nil
	}}
	c.Ready = func(_ context.Context, _ EffectiveBinding) error {
		if _, err := os.Stat(filepath.Join(stateDir, entry.OAuth.TokenFile)); err != nil {
			return ErrUnauthorized
		}
		return nil
	}
	body, err := c.confirm(t.Context(), auth, map[string]any{"onboarding_id": on.OnboardingID, "nonce": on.ConfirmationNonce})
	if err != nil || body["phase"] != PhaseAwaitingOAuth {
		t.Fatalf("handoff: %v %v", body, err)
	}
	authURL, _ := url.Parse(body["authorization_url"].(string))
	if authURL.Host != "accounts.google.com" || authURL.Query().Get("scope") != "https://www.googleapis.com/auth/calendar.readonly" || authURL.Query().Get("code_challenge") == "" {
		t.Fatal("unsafe or incorrect OAuth URL")
	}
	var concurrent sync.WaitGroup
	for range 4 {
		concurrent.Add(1)
		go func() {
			defer concurrent.Done()
			body, err := c.ensurePreparedOAuth(t.Context(), auth, on, d)
			if err != nil || body["authorization_url"] != authURL.String() {
				t.Errorf("concurrent handoff changed URL: %v", err)
			}
		}()
	}
	concurrent.Wait()
	if err := c.completePreparedOAuth(t.Context(), "wrong", "code"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unbound callback accepted", err)
	}
	c.oauthMu.Lock()
	clear(c.oauthFlows) // simulate a ToolHub restart while the browser link is outstanding
	c.oauthMu.Unlock()
	resumed, err := c.status(auth, map[string]any{"onboarding_id": on.OnboardingID})
	if err != nil || resumed["phase"] != PhaseAwaitingOAuth || resumed["authorization_url"] == authURL.String() {
		t.Fatal("status did not reissue lost OAuth link", err)
	}
	authURL, _ = url.Parse(resumed["authorization_url"].(string))
	state := authURL.Query().Get("state")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.FormValue("code_verifier") == "" || r.FormValue("client_id") != "fixture.apps.googleusercontent.com" {
			t.Error("invalid OAuth exchange")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600,"token_type":"Bearer","scope":"https://www.googleapis.com/auth/calendar.readonly"}`))
	}))
	defer provider.Close()
	c.oauthMu.Lock()
	c.oauthFlows[state].broker.Providers["google"] = oauth.Provider{Name: "google", AuthorizeURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token"}
	c.oauthMu.Unlock()
	c.OAuth = oauth.NewBroker(secrets, []string{c.origin() + "/oauth/callback"})
	gateway := &Gateway{Control: c}
	callback := "/oauth/callback?state=" + url.QueryEscape(state) + "&code=fixture-code"
	request := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.Host = "127.0.0.1:8090"
		return r
	}
	post := httptest.NewRecorder()
	gateway.serveOAuthCallback(post, request(http.MethodPost, callback))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatal("callback accepted POST", post.Code)
	}
	wrong := httptest.NewRecorder()
	gateway.serveOAuthCallback(wrong, request(http.MethodGet, "/oauth/callback?state=wrong&code=fixture-code"))
	if wrong.Code != http.StatusForbidden {
		t.Fatal("callback accepted wrong state", wrong.Code)
	}
	response := httptest.NewRecorder()
	gateway.serveOAuthCallback(response, request(http.MethodGet, callback))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("callback failed", response.Code, response.Body.String())
	}
	if checkpoints != 1 {
		t.Fatal("state was not checkpointed exactly once")
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, entry.OAuth.TokenFile))
	if err != nil {
		t.Fatal(err)
	}
	var tokens map[string]map[string]any
	if json.Unmarshal(raw, &tokens) != nil || tokens["normal"]["refresh_token"] != "fixture-refresh" {
		t.Fatal("upstream token format was not saved")
	}
	if err := c.completePreparedOAuth(t.Context(), state, "fixture-code"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("replayed callback accepted", err)
	}
	stored, _ := store.onboarding(on.OnboardingID)
	if stored.Phase != PhaseEnabled || stored.ProviderAuthorizationURL != "" {
		t.Fatal("onboarding did not resume")
	}
	if _, err := c.ensurePreparedOAuth(t.Context(), auth, stored, d); err == nil || !strings.Contains(err.Error(), "existing token state") {
		t.Fatal("existing token was overwritten", err)
	}
	if err := os.Remove(filepath.Join(stateDir, entry.OAuth.TokenFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientFile, []byte(`{"web":{"client_id":"fixture","client_secret":"fixture","redirect_uris":["http://wrong.example/callback"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ensurePreparedOAuth(t.Context(), auth, stored, d); !errors.Is(err, ErrInvalid) {
		t.Fatal("unregistered web callback accepted", err)
	}
}

func TestPreparedOAuthRejectsUnreviewedAdapter(t *testing.T) {
	for _, mutate := range []func(*PreparedOAuth){
		func(a *PreparedOAuth) { a.Scopes = []string{"https://www.googleapis.com/auth/calendar"} },
		func(a *PreparedOAuth) { a.TokenFile = "../other.json" },
		func(a *PreparedOAuth) { a.TokenFormat = "unreviewed" },
	} {
		var entries []PreparedEntry
		if err := json.Unmarshal(preparedCatalogJSON, &entries); err != nil {
			t.Fatal(err)
		}
		mutate(entries[1].OAuth)
		encoded, _ := json.Marshal(entries)
		if _, err := parsePreparedCatalog(encoded); !errors.Is(err, ErrInvalid) {
			t.Fatal("unreviewed OAuth adapter accepted", err)
		}
	}
}
