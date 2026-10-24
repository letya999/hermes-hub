package toolhub

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	brokeridentity "github.com/letya999/credential-broker/identity"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestReconcilerUsesDeclaredTeamCredentialContextWithoutOnboarding(t *testing.T) {
	s, definition, team := teamBrokerFixture(t)
	_, ref, err := s.ShareTeamConnection(team.TeamID, definition.DefinitionID, definition.Version, "credential-broker", "credential_team", "team-context", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	keyPath := staleBrokerKey(t)
	private, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	verifier := brokeridentity.Verifier{Keys: map[string]brokeridentity.TrustedKey{"toolhub": {PublicKey: ed25519.PrivateKey(private).Public().(ed25519.PublicKey), Issuer: "hermes-toolhub", Audiences: []string{"broker:control"}}}}
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := verifier.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "broker:control", r.Method, r.URL.RequestURI(), nil)
		if err != nil || !actor.ContextManager || actor.ContextID != "team-context" || actor.PrincipalID != "toolhub-reconciler" {
			t.Error("team credential check was not signed under its declared owner context and service actor")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/credentials/credential_team" {
			t.Error("unexpected operation during team health read")
		}
		reads++
		_, _ = w.Write([]byte(`{"status":"active"}`))
	}))
	defer server.Close()
	c := &ControlPlane{Store: s, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}}
	alive, err := c.NewReconciler().CredAlive(t.Context(), ref.CredentialRefID)
	if err != nil || !alive || reads != 1 {
		t.Fatalf("team credential check: alive=%v reads=%d error=%v", alive, reads, err)
	}
}

func TestReconcilerDoesNotManageInactiveTeamCredentials(t *testing.T) {
	s, definition, team := teamBrokerFixture(t)
	_, ref, err := s.ShareTeamConnection(team.TeamID, definition.DefinitionID, definition.Version, "credential-broker", "credential_team", "team-context", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	team.Status, team.Revision = api.DisabledStatus, team.Revision+1
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inactive team caused a context-manager request")
		_, _ = w.Write([]byte(`{"status":"active"}`))
	}))
	defer server.Close()
	c := &ControlPlane{Store: s, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: staleBrokerKey(t), KeyID: "toolhub", Issuer: "hermes-toolhub"}}
	if alive, err := c.NewReconciler().CredAlive(t.Context(), ref.CredentialRefID); alive || err == nil {
		t.Fatal("inactive team acquired credential-management authority")
	}
}
