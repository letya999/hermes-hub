package toolhub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	brokerclient "github.com/letya999/credential-broker/client"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestRefreshBrokerRequestPreservesInvisibleCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/credentials/credential_invisible" {
			t.Error("ambiguous credential visibility triggered another operation")
		}
		brokerNotFound(w)
	}))
	defer server.Close()
	s := NewStore()
	definition := remoteDefinition()
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: time.Now, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: staleBrokerKey(t), KeyID: "toolhub", Issuer: "hermes-toolhub"}}
	o, err := c.newOnboarding(aliceAuth(), api.OnboardingCatalog, "visibility-check", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	o.Phase, o.BrokerCredentialID, o.Locator = api.PhaseAwaitingConfirm, "credential_invisible", "credential_invisible"
	o.BrokerRequestID, o.ConfirmationNonce = "request_existing", "existing-confirmation"
	o.Revision++
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	before := o
	if err := c.refreshBrokerRequest(t.Context(), aliceAuth(), &o); !brokerclient.IsNotFound(err) {
		t.Fatalf("ambiguous visibility error not preserved: %v", err)
	} else if api.ErrorCode(err) != "credential_visibility_unknown" {
		t.Fatalf("ambiguous credential visibility lacks a distinct machine-readable cause: %q", api.ErrorCode(err))
	}
	stored, err := s.onboarding(o.OnboardingID)
	if err != nil || !api.RecordsEqual(stored, before) || !api.RecordsEqual(o, before) {
		t.Fatal("ambiguous response changed saved setup, credential reference or confirmation")
	}
}

func TestBrokerReuseDoesNotReplaceInvisibleCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/credentials/credential_invisible" {
			t.Error("unknown visibility of reusable credential caused a replacement operation")
		}
		brokerNotFound(w)
	}))
	defer server.Close()
	s := NewStore()
	d := remoteDefinition()
	d.CredentialContractID, d.CredentialContractRevision = "github-pat", 1
	d.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GITHUB_PERSONAL_ACCESS_TOKEN"}
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("existing", 1), ConnectionID: "existing", Revision: 1, Backend: "credential-broker", Locator: "credential_invisible", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus, BrokerContractID: d.CredentialContractID, BrokerContractRevision: 1}
	if err := s.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := s.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "existing", DefinitionID: d.DefinitionID, CredentialRefID: ref.CredentialRefID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: time.Now, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: staleBrokerKey(t), KeyID: "toolhub", Issuer: "hermes-toolhub"}}
	o, err := c.newOnboarding(aliceAuth(), api.OnboardingCatalog, "reuse-check", d, "", "")
	if err != nil {
		t.Fatal(err)
	}
	before := o
	if err := c.ensureBrokerRequest(t.Context(), aliceAuth(), &o, d); !brokerclient.IsNotFound(err) {
		t.Fatalf("visibility error not propagated: %v", err)
	}
	if !api.RecordsEqual(o, before) {
		t.Fatal("ambiguous credential visibility changed new setup")
	}
}

func TestBrokerRotationPreservesInvisibleTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/credentials/credential_invisible" {
			t.Error("invisible rotation target caused a new credential request")
		}
		brokerNotFound(w)
	}))
	defer server.Close()
	s := NewStore()
	d := remoteDefinition()
	d.CredentialContractID, d.CredentialContractRevision = "github-pat", 1
	d.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GITHUB_PERSONAL_ACCESS_TOKEN"}
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: time.Now, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: staleBrokerKey(t), KeyID: "toolhub", Issuer: "hermes-toolhub"}}
	o, err := c.newOnboarding(aliceAuth(), api.OnboardingCatalog, "rotate-check", d, "", "")
	if err != nil {
		t.Fatal(err)
	}
	o.BrokerRotateCredentialID = "credential_invisible"
	before := o
	if err := c.ensureBrokerRequest(t.Context(), aliceAuth(), &o, d); !brokerclient.IsNotFound(err) {
		t.Fatalf("rotation visibility error not propagated: %v", err)
	} else if api.ErrorCode(err) != "credential_visibility_unknown" {
		t.Fatalf("rotation did not identify ambiguous credential visibility: %q", api.ErrorCode(err))
	}
	if !api.RecordsEqual(o, before) {
		t.Fatal("rotation discarded original target")
	}
}
