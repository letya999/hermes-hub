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
	for _, phase := range []string{api.PhaseEnabled, api.PhaseAwaitingConfirm, "disabled", api.PhaseRevoked, api.PhaseFailed, "verifying", "retry-scheduled"} {
		t.Run(phase, func(t *testing.T) {
			store, auth, _, _ := managedStore(t)
			o := m13P3EnabledOnboarding(t, store, auth)
			o.OnboardingID = api.DeterministicID("onboard", auth.PrincipalID, "recovery", phase)
			applyOnboardingPhase(&o, phase)
			o.BrokerCredentialID, o.Locator = "credential-old", "credential-old"
			if o.Stage != api.StageGone {
				// A spent setup confirmation is the tombstone the reauth
				// replaces — but a gone record may not hold a setup gate.
				o.ArmGate(api.GateConfirmSetup, "spent-confirm", time.Time{})
				o.ConfirmationUsed = true
				o.WizardNonce, o.WizardExpires = "old-wizard", time.Now().Add(time.Hour)
			}
			if err := store.PutOnboarding(o); err != nil {
				t.Fatal(err)
			}
			c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute}
			args := map[string]any{"target": o.OnboardingID}
			result, err := c.connectorReauth(t.Context(), auth, args)
			if err != nil || result.State != api.ModelStateWaitingYou {
				t.Fatalf("reauth state=%s err=%v", result.State, err)
			}
			fresh, err := store.OnboardingFor(auth, o.OnboardingID)
			if err != nil || !fresh.Awaiting(api.GateCredentials) || fresh.GateNonce(api.GateCredentials) == "" || fresh.WizardNonce == "old-wizard" || fresh.GateNonce(api.GateConfirmSetup) != "" || fresh.ConfirmationUsed || fresh.BrokerRotateCredentialID != "credential-old" {
				t.Fatal("reauth did not replace the protected action")
			}
			if fresh.BindingID != o.BindingID || fresh.ConnectionID != o.ConnectionID {
				t.Fatal("reauth replaced the connection")
			}
			retry, err := c.connectorReauth(t.Context(), auth, args)
			again, _ := store.OnboardingFor(auth, o.OnboardingID)
			if err != nil || retry.OperationID != result.OperationID || again.GateNonce(api.GateCredentials) != fresh.GateNonce(api.GateCredentials) {
				t.Fatal("retry replaced live intake")
			}
			c.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
			if _, err := c.connectorReauth(t.Context(), auth, args); err != nil {
				t.Fatal(err)
			}
			renewed, _ := store.OnboardingFor(auth, o.OnboardingID)
			if renewed.GateNonce(api.GateCredentials) == fresh.GateNonce(api.GateCredentials) || !renewed.GateExpiry(api.GateCredentials).After(c.now()) {
				t.Fatal("expired form was not renewed")
			}
			fresh = renewed
			fresh.Revision = fresh.Revision + 1
			fresh.Stage = api.StagePending
			fresh.ArmGate(api.GateConfirmSetup, "", time.Time{})
			if err := store.PutOnboarding(fresh); err != nil {
				t.Fatal(err)
			}
			fresh.Revision = fresh.Revision + 1
			fresh.Stage = api.StageLive
			if err := store.PutOnboarding(fresh); err != nil {
				t.Fatal(err)
			}
			done, err := c.connectorStatus(t.Context(), auth, "", result.OperationID)
			if err != nil || done.State != api.ModelStateReady {
				t.Fatalf("completion=%s err=%v", done.State, err)
			}
			next, err := c.connectorReauth(t.Context(), auth, args)
			if err != nil || next.State != api.ModelStateWaitingYou || next.OperationID == result.OperationID {
				t.Fatal("completed operation blocked a later reauth")
			}
		})
	}
}

func TestConnectorReauthCannotResurrectRemoved(t *testing.T) {
	s, auth, _, _ := managedStore(t)
	o := m13P3EnabledOnboarding(t, s, auth)
	o.Revision = o.Revision + 1
	o.Retire(api.TombstoneRemoved)
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
	o, _, err := c.newOnboarding(aliceAuth(), api.OnboardingCatalog, "replacement", d, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	o.BrokerCredentialID, o.Locator = "credential_invisible", "credential_invisible"
	o.Stage = api.StagePending
	o.ArmGate(api.GateConfirmSetup, "", time.Time{})
	o.Revision++
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	result, err := c.connectorReauth(t.Context(), aliceAuth(), map[string]any{"target": o.OnboardingID})
	if err != nil || result.State != api.ModelStateWaitingYou || created != 1 {
		t.Fatalf("replacement state=%s requests=%d err=%v", result.State, created, err)
	}
	fresh, _ := s.OnboardingFor(aliceAuth(), o.OnboardingID)
	if fresh.BrokerRotateCredentialID != "credential_invisible" || fresh.BrokerRequestID != "request_replacement" || fresh.GateNonce(api.GateCredentials) != "" {
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
	o.BrokerRotateCredentialID, o.Revision = "credential-old", o.Revision+1
	o.Stage = api.StagePending
	o.ArmGate(api.GateCredentials, "", time.Time{})
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: time.Now, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: staleBrokerKey(t), KeyID: "toolhub", Issuer: "hermes-toolhub"}}
	foreign := identity.TelegramEnvelope("mallory", 8, "runtime", auth.PolicyVersion)
	if _, err := c.InvokeConnector(t.Context(), foreign, "connector_edit", map[string]any{"name": o.OnboardingID, "action": "revoke"}); err == nil || revoked != 0 {
		t.Fatal("foreign revocation reached Broker")
	}
	result, err := c.InvokeConnector(t.Context(), auth, "connector_edit", map[string]any{"name": o.OnboardingID, "action": "revoke"})
	if err != nil || result.State != api.ModelStateDone || revoked != 1 {
		t.Fatalf("revoke state=%s count=%d err=%v", result.State, revoked, err)
	}
	got, _ := s.OnboardingFor(auth, o.OnboardingID)
	bound, _ := s.binding(o.BindingID)
	if !got.Revoked() || bound.Status != api.RevokedStatus {
		t.Fatal("revoke left local access active")
	}
}
