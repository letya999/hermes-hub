package toolhub

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestConnectorReauthRecovery(t *testing.T) {
	for _, phase := range []string{api.PhaseEnabled, api.PhaseAwaitingConfirm, api.PhaseDisabled, api.PhaseRevoked, api.PhaseFailed, api.PhaseVerifying, api.PhaseRetryScheduled} {
		t.Run(phase, func(t *testing.T) {
			store, auth, _, _ := managedStore(t)
			o := m13P3EnabledOnboarding(t, store, auth)
			o.OnboardingID = api.DeterministicID("onboard", auth.PrincipalID, "recovery", phase)
			o.Phase, o.BrokerCredentialID, o.Locator = phase, "credential-old", "credential-old"
			o.ConfirmationNonce, o.ConfirmationUsed = "spent-confirm", true
			o.WizardNonce, o.WizardExpires = "old-wizard", time.Now().Add(time.Hour)
			if err := store.PutOnboarding(o); err != nil {
				t.Fatal(err)
			}
			c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute}
			args := map[string]any{"target": o.OnboardingID}
			result, err := c.connectorReauth(t.Context(), auth, args)
			if err != nil || result.State != api.OpStateAwaitingUser {
				t.Fatalf("reauth state=%s err=%v", result.State, err)
			}
			fresh, err := store.OnboardingFor(auth, o.OnboardingID)
			if err != nil || fresh.Phase != api.PhaseAwaitingCreds || fresh.FormNonce == "" || fresh.WizardNonce == "old-wizard" || fresh.ConfirmationNonce != "" || fresh.ConfirmationUsed || fresh.BrokerRotateCredentialID != "credential-old" {
				t.Fatal("reauth did not replace the protected action")
			}
			if fresh.BindingID != o.BindingID || fresh.ConnectionID != o.ConnectionID {
				t.Fatal("reauth replaced the connection")
			}
			retry, err := c.connectorReauth(t.Context(), auth, args)
			again, _ := store.OnboardingFor(auth, o.OnboardingID)
			if err != nil || retry.OperationID != result.OperationID || again.FormNonce != fresh.FormNonce {
				t.Fatal("retry replaced live intake")
			}
			c.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
			if _, err := c.connectorReauth(t.Context(), auth, args); err != nil {
				t.Fatal(err)
			}
			renewed, _ := store.OnboardingFor(auth, o.OnboardingID)
			if renewed.FormNonce == fresh.FormNonce || !renewed.FormExpires.After(c.now()) {
				t.Fatal("expired form was not renewed")
			}
			fresh = renewed
			fresh.Phase, fresh.Revision = api.PhaseAwaitingConfirm, fresh.Revision+1
			if err := store.PutOnboarding(fresh); err != nil {
				t.Fatal(err)
			}
			fresh.Phase, fresh.Revision = api.PhaseEnabled, fresh.Revision+1
			if err := store.PutOnboarding(fresh); err != nil {
				t.Fatal(err)
			}
			done, err := c.connectorStatus(t.Context(), auth, "", result.OperationID)
			if err != nil || done.State != api.OpStateDone {
				t.Fatalf("completion=%s err=%v", done.State, err)
			}
			next, err := c.connectorReauth(t.Context(), auth, args)
			if err != nil || next.State != api.OpStateAwaitingUser || next.OperationID == result.OperationID {
				t.Fatal("completed operation blocked a later reauth")
			}
		})
	}
}

func TestConnectorReauthCannotResurrectRemoved(t *testing.T) {
	s, auth, _, _ := managedStore(t)
	o := m13P3EnabledOnboarding(t, s, auth)
	o.Phase, o.Revision = api.PhaseRemoved, o.Revision+1
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s}
	if _, err := c.connectorReauth(t.Context(), auth, map[string]any{"target": o.OnboardingID}); !errors.Is(err, api.ErrRevoked) && !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("removed reauth: %v", err)
	}
}

func TestExplicitBrokerReauthWithInvisibleCredential(t *testing.T) {
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_invisible":
			brokerNotFound(w)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			var request brokerv1.CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.RotateCredentialID != "" || request.ConnectionID == "" {
				t.Error("replacement must own a separate connection")
			}
			created++
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_replacement", Status: "pending", ContractID: "test-contract", ContractRevision: 1, AuthorizationURL: "https://broker.example/connect/replacement", ExpiresAt: time.Now().Add(time.Hour)})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_replacement":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_replacement", Status: "pending", ContractID: "test-contract", ContractRevision: 1, AuthorizationURL: "https://broker.example/connect/replacement", ExpiresAt: time.Now().Add(time.Hour)})
		default:
			t.Errorf("unexpected Broker operation %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := NewStore()
	d := remoteDefinition()
	d.CredentialContractID, d.CredentialContractRevision = "test-contract", 1
	d.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: time.Now, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: staleBrokerKey(t), KeyID: "toolhub", Issuer: "hermes-toolhub"}}
	o, err := c.newOnboarding(aliceAuth(), api.OnboardingCatalog, "replacement", d, "", "")
	if err != nil {
		t.Fatal(err)
	}
	o.Phase, o.BrokerCredentialID, o.Locator = api.PhaseAwaitingConfirm, "credential_invisible", "credential_invisible"
	o.Revision++
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	result, err := c.connectorReauth(t.Context(), aliceAuth(), map[string]any{"target": o.OnboardingID})
	if err != nil || result.State != api.OpStateAwaitingUser || created != 1 {
		t.Fatalf("replacement state=%s requests=%d err=%v", result.State, created, err)
	}
	fresh, _ := s.OnboardingFor(aliceAuth(), o.OnboardingID)
	if fresh.BrokerRotateCredentialID != "credential_invisible" || fresh.BrokerRequestID != "request_replacement" || fresh.FormNonce != "" {
		t.Fatal("replacement lost audit reference or used a local form")
	}
}

func TestConnectorRevokePendingRotation(t *testing.T) {
	var revoked int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/credentials/credential-old/revoke" {
			t.Errorf("unexpected revoke route %s %s", r.Method, r.URL.Path)
		}
		revoked++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	s, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	o := m13P3EnabledOnboarding(t, s, auth)
	o.Phase, o.BrokerRotateCredentialID, o.Revision = api.PhaseAwaitingCreds, "credential-old", o.Revision+1
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: time.Now, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: staleBrokerKey(t), KeyID: "toolhub", Issuer: "hermes-toolhub"}}
	foreign := identity.TelegramEnvelope("mallory", 8, "runtime", auth.PolicyVersion)
	if _, err := c.InvokeConnector(t.Context(), foreign, "connector_edit", map[string]any{"name": o.OnboardingID, "action": "revoke"}); err == nil || revoked != 0 {
		t.Fatal("foreign revocation reached Broker")
	}
	result, err := c.InvokeConnector(t.Context(), auth, "connector_edit", map[string]any{"name": o.OnboardingID, "action": "revoke"})
	if err != nil || result.State != api.OpStateDone || revoked != 1 {
		t.Fatalf("revoke state=%s count=%d err=%v", result.State, revoked, err)
	}
	got, _ := s.OnboardingFor(auth, o.OnboardingID)
	bound, _ := s.binding(o.BindingID)
	if got.Phase != api.PhaseRevoked || bound.Status != api.RevokedStatus {
		t.Fatal("revoke left local access active")
	}
}
