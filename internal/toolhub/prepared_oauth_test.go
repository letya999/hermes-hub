package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
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
	testOAuthHandoffAndCallback(t, false)
}

func TestRecipeOAuthHandoffWithoutCatalog(t *testing.T) {
	testOAuthHandoffAndCallback(t, true)
}

func testOAuthHandoffAndCallback(t *testing.T, fromRecipe bool) {
	var noticed []map[string]any
	noticer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/prepare-outcome" {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			noticed = append(noticed, payload)
		}
		w.WriteHeader(200)
	}))
	defer noticer.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", noticer.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

	entry, matched, err := api.PreparedForRepository("https://github.com/nspady/google-calendar-mcp", "")
	if err != nil || !matched || entry.OAuth == nil {
		t.Fatal("reviewed OAuth entry missing", err)
	}
	d := statefulContainerDefinition()
	d.DefinitionID, d.Version = entry.ID, "1.0.0"
	d.Source.Repository, d.Source.CommitSHA = entry.Source.Repository, entry.Source.CommitSHA
	host, providerName := "accounts.google.com", "recipe"
	if fromRecipe {
		d.Source.Repository = "https://github.com/example/files-mcp"
		d.Source.CommitSHA = strings.Repeat("a", 40)
		host, providerName = "identity.example.com", "recipe"
		d.OAuth = &api.OAuthRecipe{AuthorizeURL: "https://identity.example.com/authorize", TokenURL: "https://identity.example.com/token", Scopes: entry.OAuth.Scopes, ClientInput: entry.OAuth.ClientInput, ClientSections: []string{"installed", "web"}, StateTarget: entry.StateTarget, TokenFile: entry.OAuth.TokenFile, TokenTemplate: map[string]any{"normal": map[string]any{"access_token": "$oauth.ACCESS_TOKEN", "refresh_token": "$oauth.REFRESH_TOKEN", "expiry_date": "$oauth.EXPIRES_AT_UNIX_MS", "token_type": "$oauth.TOKEN_TYPE", "scope": "$oauth.OAUTH_SCOPE"}}, ProbeTool: "list-calendars"}
	}
	if fromRecipe {
		if err := json.Unmarshal([]byte(`{"registered_redirect_sections":["web"]}`), d.OAuth); err != nil {
			t.Fatal(err)
		}
	}
	d.Credentials = []api.CredentialInput{{Name: entry.OAuth.ClientInput, Required: true}}
	d.CredentialContractID, d.CredentialContractRevision = entry.ContractID, entry.ContractRevision
	d.CredentialContractEnv = map[string]string{entry.OAuth.ClientInput: entry.OAuth.ClientInput}
	store := NewStore()
	if err := store.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: "cred_calendar", ConnectionID: "conn_calendar", Revision: 1, Backend: "credential-broker", Locator: "credential_calendar", Keys: []string{entry.OAuth.ClientInput}, BrokerGrantID: "grant_calendar", BrokerContractID: entry.ContractID, BrokerContractRevision: entry.ContractRevision, BrokerEnv: d.CredentialContractEnv, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: ref.ConnectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	on := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard_calendar", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm, DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Locator: ref.Locator, ConfirmationNonce: "nonce", ConfirmationExpires: time.Now().Add(time.Hour), Revision: 1, CreatedAt: time.Now()}
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
	c := &ControlPlane{Store: store, Secrets: secrets, FormOrigin: "http://127.0.0.1:8090", Injector: func(_ context.Context, _ api.EffectiveBinding) (api.CredentialInjection, error) {
		return api.CredentialInjection{Environment: map[string]string{entry.OAuth.ClientInput: "/run/mcp-secrets/google/client.json"}, Mounts: []api.Mount{{Source: clientFile, Target: "/run/mcp-secrets/google/client.json", ReadOnly: true}, {Source: stateDir, Target: entry.StateTarget}}, Checkpoint: func() error { checkpoints++; return nil }}, nil
	}}
	c.Ready = func(_ context.Context, _ api.EffectiveBinding) error {
		if _, err := os.Stat(filepath.Join(stateDir, entry.OAuth.TokenFile)); err != nil {
			return api.ErrUnauthorized
		}
		return nil
	}
	body, err := c.confirm(t.Context(), auth, map[string]any{"onboarding_id": on.OnboardingID, "nonce": on.ConfirmationNonce})
	if err != nil || body["phase"] != api.PhaseAwaitingOAuth {
		t.Fatalf("handoff: %v %v", body, err)
	}
	authURL, _ := url.Parse(providerAuthURLOf(t, store, on.OnboardingID))
	if authURL.Host != host || authURL.Query().Get("scope") != "https://www.googleapis.com/auth/calendar.readonly" || authURL.Query().Get("code_challenge") == "" {
		t.Fatal("unsafe or incorrect OAuth URL")
	}
	var link string
	for _, payload := range noticed {
		if u, _ := payload["form_url"].(string); strings.Contains(u, "/connect/"+on.OnboardingID+"?k=") {
			link = u
		}
	}
	if link == "" || strings.Contains(link, "accounts.google.com") || strings.Contains(link, "state=") {
		t.Fatalf("no wizard /connect/ link reached the channel: %v", noticed)
	}
	var concurrent sync.WaitGroup
	for range 4 {
		concurrent.Add(1)
		go func() {
			defer concurrent.Done()
			if _, err := c.ensurePreparedOAuth(t.Context(), auth, on, d, false); err != nil || providerAuthURLOf(t, store, on.OnboardingID) != authURL.String() {
				t.Errorf("concurrent handoff changed URL: %v", err)
			}
		}()
	}
	concurrent.Wait()
	if err := c.completePreparedOAuth(t.Context(), "wrong", "code"); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatal("unbound callback accepted", err)
	}
	c.oauthFlows.mu.Lock()
	clear(c.oauthFlows.flows) // simulate a ToolHub restart while the browser link is outstanding
	c.oauthFlows.mu.Unlock()
	resumed, err := c.status(context.Background(), auth, map[string]any{"onboarding_id": on.OnboardingID})
	newRaw := providerAuthURLOf(t, store, on.OnboardingID)
	if err != nil || resumed["phase"] != api.PhaseAwaitingOAuth || newRaw == "" || newRaw == authURL.String() {
		t.Fatal("status did not reissue lost OAuth link", err)
	}
	authURL, _ = url.Parse(newRaw)
	state := authURL.Query().Get("state")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.FormValue("code_verifier") == "" || r.FormValue("client_id") != "fixture.apps.googleusercontent.com" {
			t.Error("invalid OAuth exchange")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600,"token_type":"Bearer","scope":"https://www.googleapis.com/auth/calendar.readonly"}`))
	}))
	defer provider.Close()
	c.oauthFlows.mu.Lock()
	c.oauthFlows.flows[state].broker.HTTP = provider.Client()
	c.oauthFlows.flows[state].broker.Providers[providerName] = oauth.Provider{Name: providerName, AuthorizeURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token"}
	c.oauthFlows.mu.Unlock()
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
	// A live wizard nonce sends the user back onto the same stable link —
	// it now renders the waiting/enabled state instead of a dead-end page.
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "/connect/"+on.OnboardingID+"?k=") {
		t.Fatal("callback did not return to the wizard link", response.Code, response.Header().Get("Location"), response.Body.String())
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
	if err := c.completePreparedOAuth(t.Context(), state, "fixture-code"); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatal("replayed callback accepted", err)
	}
	stored, _ := store.onboarding(on.OnboardingID)
	if stored.Phase != api.PhaseEnabled || stored.ProviderAuthorizationURL != "" {
		t.Fatal("onboarding did not resume")
	}
	if _, err := c.ensurePreparedOAuth(t.Context(), auth, stored, d, false); err == nil || !strings.Contains(err.Error(), "existing token state") {
		t.Fatal("existing token was overwritten", err)
	}
	if err := os.Remove(filepath.Join(stateDir, entry.OAuth.TokenFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientFile, []byte(`{"web":{"client_id":"fixture","client_secret":"fixture","redirect_uris":["http://wrong.example/callback"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ensurePreparedOAuth(t.Context(), auth, stored, d, false); !errors.Is(err, api.ErrInvalid) {
		t.Fatal("unregistered web callback accepted", err)
	}
}

func TestPreparedOAuthRejectsUnreviewedAdapter(t *testing.T) {
	for _, mutate := range []func(*api.PreparedOAuth){
		func(a *api.PreparedOAuth) { a.Scopes = nil },
		func(a *api.PreparedOAuth) { a.TokenFile = "../other.json" },
		func(a *api.PreparedOAuth) { a.TokenTemplate = map[string]any{"token": "$exec.unreviewed"} },
	} {
		var entries []api.PreparedEntry
		if err := json.Unmarshal(api.PreparedCatalogJSON, &entries); err != nil {
			t.Fatal(err)
		}
		mutate(entries[1].OAuth)
		encoded, _ := json.Marshal(entries)
		if _, err := api.ParsePreparedCatalog(encoded); !errors.Is(err, api.ErrInvalid) {
			t.Fatal("unreviewed OAuth adapter accepted", err)
		}
	}
}
