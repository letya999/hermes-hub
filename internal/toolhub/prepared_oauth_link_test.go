package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The agent-facing status for an awaiting-oauth onboarding must never carry
// link material: the /connect/ hop embeds the OAuth state, and the gateway
// contract bans credential-bearing parameters from tool results. The link
// lives in the channel notice; the status only points the agent there.
func TestStatusBodyOAuthKeepsLinkInChannel(t *testing.T) {
	control := &ControlPlane{Store: NewStore(), FormOrigin: "http://127.0.0.1:8090"}
	on := api.Onboarding{OnboardingID: "onboard_oauth", Phase: api.PhaseAwaitingOAuth,
		ProviderAuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth?client_id=x.apps.googleusercontent.com&code_challenge=zz&state=st-1"}
	body := control.statusBody(on, false)
	for _, key := range []string{"oauth_url", "authorization_url", "form_url", "instructions"} {
		if _, present := body[key]; present {
			t.Fatalf("%s is link material or prose: %s", key, body)
		}
	}
	raw, _ := json.Marshal(body)
	for _, leak := range []string{"st-1", "/connect/", "accounts.google.com", "code_challenge", "?state="} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("link material %q leaked into agent result: %s", leak, raw)
		}
	}
}

// A wizard GET whose OAuth flow died (step TTL, toolhub restart) must start a
// fresh flow: rendering the stale provider URL sends the owner through Google
// consent only to land on "authorization failed" at the callback.
func TestWizardOAuthRestartsDeadFlow(t *testing.T) {
	noticer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer noticer.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", noticer.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	entry, matched, err := api.PreparedForRepository("https://github.com/nspady/google-calendar-mcp", "")
	if err != nil || !matched || entry.OAuth == nil {
		t.Fatal("reviewed OAuth entry missing", err)
	}
	d := statefulContainerDefinition()
	d.DefinitionID, d.Version = entry.ID, "1.0.0"
	d.Source.Repository, d.Source.CommitSHA = entry.Source.Repository, entry.Source.CommitSHA
	d.Credentials = []api.CredentialInput{{Name: entry.OAuth.ClientInput, Required: true}}
	d.CredentialContractID, d.CredentialContractRevision = entry.ContractID, entry.ContractRevision
	d.CredentialContractEnv = map[string]string{entry.OAuth.ClientInput: entry.OAuth.ClientInput}
	store := NewStore()
	if err := store.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: "cred_wiz_dead", ConnectionID: "conn_wiz_dead", Revision: 1, Backend: "credential-broker", Locator: "credential_wiz_dead", Keys: []string{entry.OAuth.ClientInput}, BrokerGrantID: "grant_wiz_dead", BrokerContractID: entry.ContractID, BrokerContractRevision: entry.ContractRevision, BrokerEnv: d.CredentialContractEnv, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: ref.ConnectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	stale := "https://accounts.google.com/o/oauth2/v2/auth?state=dead-flow"
	on := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard_wiz_dead", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingOAuth, DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Locator: ref.Locator, ProviderAuthorizationURL: stale, WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour), Revision: 1, CreatedAt: time.Now()}
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
	c := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now, FormOrigin: "http://127.0.0.1:8090", Injector: func(_ context.Context, _ api.EffectiveBinding) (api.CredentialInjection, error) {
		return api.CredentialInjection{Environment: map[string]string{entry.OAuth.ClientInput: "/run/mcp-secrets/google/client.json"}, Mounts: []api.Mount{{Source: clientFile, Target: "/run/mcp-secrets/google/client.json", ReadOnly: true}, {Source: stateDir, Target: entry.StateTarget}}}, nil
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onboard_wiz_dead?k=wiz-key", nil)
	req.Host = "127.0.0.1"
	(&Gateway{Control: c}).serveConnect(rec, req)
	fresh := providerAuthURLOf(t, store, on.OnboardingID)
	if rec.Code != http.StatusOK || fresh == stale || strings.Contains(rec.Body.String(), "dead-flow") || c.preparedOAuthOnboardingID(mustState(t, fresh)) != on.OnboardingID {
		t.Fatalf("wizard served a dead OAuth flow: code=%d url=%s", rec.Code, fresh)
	}
}

// A legacy ?state= hop whose flow died must hand the browser to the wizard
// link: re-rendering the interstitial there fronts a dead authorization URL
// that only fails at /oauth/callback.
func TestConnectStateLinkRedirectsDeadFlowToWizard(t *testing.T) {
	auth := aliceAuth()
	on := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard_dead_state", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingOAuth, ProviderAuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth?state=dead-state", WizardNonce: "wiz-key", WizardExpires: time.Now().Add(time.Hour), Revision: 1, CreatedAt: time.Now()}
	store := NewStore()
	if err := store.PutOnboarding(on); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onboard_dead_state?state=dead-state", nil)
	req.Host = "127.0.0.1"
	(&Gateway{Control: &ControlPlane{Store: store, Now: time.Now, FormOrigin: "http://127.0.0.1:8090"}}).serveConnect(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "http://127.0.0.1:8090/connect/onboard_dead_state?k=wiz-key" {
		t.Fatalf("dead-flow hop must redirect to the wizard link: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func mustState(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get("state")
}

// A stale error from an earlier failed step must not survive the transition
// into awaiting-oauth — the status page reports the record's error as
// current, so a dead controller failure misleads every later check.
func TestPreparedOAuthTransitionClearsStaleError(t *testing.T) {
	noticer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
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
	d.Credentials = []api.CredentialInput{{Name: entry.OAuth.ClientInput, Required: true}}
	d.CredentialContractID, d.CredentialContractRevision = entry.ContractID, entry.ContractRevision
	d.CredentialContractEnv = map[string]string{entry.OAuth.ClientInput: entry.OAuth.ClientInput}
	store := NewStore()
	if err := store.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: "cred_calendar2", ConnectionID: "conn_calendar2", Revision: 1, Backend: "credential-broker", Locator: "credential_calendar2", Keys: []string{entry.OAuth.ClientInput}, BrokerGrantID: "grant_calendar2", BrokerContractID: entry.ContractID, BrokerContractRevision: entry.ContractRevision, BrokerEnv: d.CredentialContractEnv, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: ref.ConnectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	on := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard_calendar2", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm, DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Locator: ref.Locator, ConfirmationNonce: "nonce", ConfirmationExpires: time.Now().Add(time.Hour), Revision: 1, CreatedAt: time.Now(), Error: "bounded CLI isolation is unavailable", ErrorCode: "isolation"}
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
	c := &ControlPlane{Store: store, Secrets: secrets, FormOrigin: "http://127.0.0.1:8090", Injector: func(_ context.Context, _ api.EffectiveBinding) (api.CredentialInjection, error) {
		return api.CredentialInjection{Environment: map[string]string{entry.OAuth.ClientInput: "/run/mcp-secrets/google/client.json"}, Mounts: []api.Mount{{Source: clientFile, Target: "/run/mcp-secrets/google/client.json", ReadOnly: true}, {Source: stateDir, Target: entry.StateTarget}}}, nil
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
	stored := mustOnboarding(t, store, on.OnboardingID)
	if stored.Error != "" || stored.ErrorCode != "" {
		t.Fatalf("stale error survived the OAuth transition: %q %q", stored.Error, stored.ErrorCode)
	}
	if _, present := body["oauth_url"]; present {
		t.Fatalf("oauth_url is link material: %s", body)
	}
	parsed, _ := url.Parse(providerAuthURLOf(t, store, on.OnboardingID))
	if parsed.Query().Get("state") == "" {
		t.Fatal("flow state missing — the channel notice would carry no /connect/ link")
	}
}
