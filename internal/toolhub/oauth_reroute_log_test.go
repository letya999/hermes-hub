package toolhub

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// When confirm re-routes into a fresh OAuth handoff the materialization
// failure that caused it is swallowed — the onboarding error is cleared and
// nothing records why the user must authorize again. The service log must
// carry the underlying cause.
func TestOAuthRerouteLogsMaterializeError(t *testing.T) {
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
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: "cred_calendar3", ConnectionID: "conn_calendar3", Revision: 1, Backend: "credential-broker", Locator: "credential_calendar3", Keys: []string{entry.OAuth.ClientInput}, BrokerGrantID: "grant_calendar3", BrokerContractID: entry.ContractID, BrokerContractRevision: entry.ContractRevision, BrokerEnv: d.CredentialContractEnv, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: ref.ConnectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	on := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard_calendar3", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm, DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Locator: ref.Locator, ConfirmationNonce: "nonce", ConfirmationExpires: time.Now().Add(time.Hour), Revision: 1, CreatedAt: time.Now()}
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
		return api.ErrUnauthorized
	}
	var buf bytes.Buffer
	restore := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(restore)
	if _, err := c.confirm(t.Context(), auth, map[string]any{"onboarding_id": on.OnboardingID, "nonce": on.ConfirmationNonce}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !strings.Contains(buf.String(), "oauth re-route") || !strings.Contains(buf.String(), "materialize: toolhub authorization denied") {
		t.Fatalf("re-route swallowed the materialization error: %q", buf.String())
	}
}
