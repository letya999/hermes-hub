package toolhub

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestConfirmDeliveryFailureDoesNotRerouteOAuth(t *testing.T) {
	// A broker bundle missing a required non-client input — the reviewed
	// Workspace contract delivers the account email alongside the OAuth
	// client — is an owner-fixable credential failure. Confirm must reopen
	// the credential step so a later status poll mints a fresh Broker form;
	// an OAuth handoff can mint tokens but never the missing email field.
	store := NewStore()
	definition := statefulContainerDefinition()
	definition.DefinitionID = "google-workspace-mcp"
	definition.Credentials = []api.CredentialInput{{Name: "GOOGLE_CLIENT_SECRET_PATH", Required: true}, {Name: "USER_GOOGLE_EMAIL", Required: true}}
	definition.OAuth = &api.OAuthRecipe{
		AccountInput:   "USER_GOOGLE_EMAIL",
		AuthorizeURL:   "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:       "https://oauth2.googleapis.com/token",
		Scopes:         []string{"https://www.googleapis.com/auth/calendar.readonly"},
		ClientInput:    "GOOGLE_CLIENT_SECRET_PATH",
		ClientSections: []string{"installed", "web"},
		StateTarget:    "/state",
		TokenFile:      "session.json",
		TokenTemplate:  map[string]any{"normal": map[string]any{"access_token": "$oauth.ACCESS_TOKEN", "refresh_token": "$oauth.REFRESH_TOKEN"}},
		ProbeTool:      "list_calendars",
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "refs.enc"), Key: bytes.Repeat([]byte("k"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	clientFile := filepath.Join(root, "client.json")
	if err := os.WriteFile(clientFile, []byte(`{"installed":{"client_id":"fixture.apps.googleusercontent.com","client_secret":"fixture-secret","redirect_uris":["http://localhost"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now,
		// The injector offers the OAuth mounts, so an ErrUnauthorized reroute
		// would mint a live provider link; a delivery gap must never reach it.
		Injector: func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
			return api.CredentialInjection{Environment: map[string]string{"GOOGLE_CLIENT_SECRET_PATH": "/run/mcp-secrets/google/client.json"}, Mounts: []api.Mount{{Source: clientFile, Target: "/run/mcp-secrets/google/client.json", ReadOnly: true}, {Source: stateDir, Target: "/state"}}}, nil
		},
		Ready: func(context.Context, api.EffectiveBinding) error {
			return fmt.Errorf("%w: %w: broker delivery does not satisfy %s; not delivered: %s", api.ErrIsolation, api.ErrCredentialDelivery, definition.DefinitionID, "USER_GOOGLE_EMAIL")
		},
	}
	grantTestControlOperations(t, store, auth.PrincipalID, "confirm")
	connectionID := api.DeterministicID("conn", auth.PrincipalID, definition.DefinitionID, "onboard-workspace-delivery")
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connectionID, 1), ConnectionID: connectionID, Revision: 1, Backend: "credential-broker", Locator: "broker-cred-1", Keys: []string{"GOOGLE_CLIENT_SECRET_PATH", "USER_GOOGLE_EMAIL"}, Status: api.ActiveStatus, BrokerGrantID: "grant-1", BrokerContractID: "google-workspace-mcp", BrokerContractRevision: 1, BrokerEnv: map[string]string{"GOOGLE_CLIENT_SECRET_PATH": "GOOGLE_CLIENT_SECRET_PATH", "USER_GOOGLE_EMAIL": "USER_GOOGLE_EMAIL"}}
	if err := store.PutCredentialReference(reference); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: connectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-workspace-delivery", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1, Mode: api.OnboardingSelfInstall, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, Phase: api.PhaseAwaitingConfirm, Locator: "broker-cred-1", BrokerCredentialID: "broker-cred-1", BrokerRequestID: "r-dead", BrokerAuthorizationURL: "https://broker/connect/r-dead", Required: []api.CredentialHint{{Name: "GOOGLE_CLIENT_SECRET_PATH"}, {Name: "USER_GOOGLE_EMAIL"}}, ConfirmationNonce: "nonce-1", ConfirmationExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(t.Context(), auth, "confirm", map[string]any{"onboarding_id": onboarding.OnboardingID, "nonce": "nonce-1"}); err == nil {
		t.Fatal("unsatisfiable delivery passed confirm")
	}
	after, err := store.onboarding(onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Phase != api.PhaseAwaitingCreds || after.BrokerRotateCredentialID != "broker-cred-1" || after.BrokerCredentialID != "" || after.Locator != "" || after.BrokerRequestID != "" || after.BrokerAuthorizationURL != "" || after.ConfirmationNonce != "" {
		t.Fatalf("delivery failure did not reopen the credential form: %+v", after)
	}
	if after.ProviderAuthorizationURL != "" || len(control.oauthFlows.flows) != 0 {
		t.Fatalf("delivery failure minted a provider OAuth link: %+v", after)
	}
	if after.Error == "" {
		t.Fatal("delivery failure dropped the failure record")
	}
}
