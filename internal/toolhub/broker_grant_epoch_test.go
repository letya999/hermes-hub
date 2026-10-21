package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// SPEC-0053 migrates active bindings to a new policy version on `up`, but the
// broker grant minted under the old epoch still pins it: Acquire matches the
// actor's policy against the grant's. A reused credential therefore needs the
// recorded grant's mint epoch — not the migrated binding record — to decide
// reusability; otherwise confirm dies on `credential broker: denied (403)`.
func TestBrokerGrantStalePolicyEpochRotatesOnConfirm(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var acquired []string
	revocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_1":
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_1", ConnectionID: "conn_1", ConsumerID: "github-work", ContextID: "alice", PrincipalID: "alice", OwnerKind: "user", Status: "active"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			var in brokerv1.GrantRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_2", CredentialID: "credential_1", PrincipalID: in.PrincipalID, ContextID: in.ContextID, RuntimeID: in.RuntimeID, BindingID: in.BindingID, WorkloadID: in.WorkloadID, Execution: "dedicated", ContractID: in.ContractID, ContractRevision: in.ContractRevision, PolicyVersion: "policy-1", Active: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_1/revoke":
			revocations++
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_2/revoke":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			var in brokerv1.AcquireLease
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			acquired = append(acquired, in.GrantID)
			if in.GrantID == "grant_1" {
				// Broker-side policy check: the stored grant pins the pre-
				// migration epoch, so lease acquisition is denied.
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(brokerv1.Error{Code: "denied"})
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_2", GrantID: in.GrantID, CredentialID: "credential_1", Revision: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_2/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_2", Env: map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "broker-secret"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_2/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.DefinitionID = "github-work"
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GITHUB_PERSONAL_ACCESS_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}

	// The footprint left by the pre-migration enable: connection + credential
	// reference naming grant_1, and a binding already moved to the current
	// policy epoch by MigrateBindings.
	refID := api.CredentialReferenceID("conn_1", 1)
	if err := store.PutCredentialReference(api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: refID, ConnectionID: "conn_1", Revision: 1, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, BrokerGrantID: "grant_1", BrokerContractID: "github-pat", BrokerContractRevision: 1, BrokerEnv: map[string]string{"GOOGLE_TOKEN": "GITHUB_PERSONAL_ACCESS_TOKEN"}, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn_1", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: refID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: "conn_1", ConnectionRevision: 1, CredentialRefID: refID, CredentialRevision: 1, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	store.bindings[binding.ToolBindingID] = binding

	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now}
	probeFailure := errors.New("provider awaits OAuth")
	probes := 0
	// Same wiring as the production gateway: the readiness callback injects
	// through the broker runtime, which acquires a lease on the stored grant.
	control.Ready = func(ctx context.Context, effective api.EffectiveBinding) error {
		injection, err := brokerRuntimeInjector(&cfg, &cfg)(ctx, effective)
		if err != nil {
			return err
		}
		defer injection.Cleanup()
		probes++
		if probes == 1 {
			return probeFailure
		}
		return nil
	}

	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "reinstall", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.Locator = "credential_1"
	onboarding.BrokerCredentialID = "credential_1"
	onboarding.BrokerContractID = "github-pat"
	onboarding.BrokerContractRevision = 1
	onboarding.Phase = api.PhaseAwaitingConfirm
	onboarding.FormNonce, onboarding.FormExpires = "", time.Time{}
	onboarding.ConfirmationNonce = api.RandomNonce()
	onboarding.ConfirmationExpires = time.Now().Add(time.Hour)
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}

	if _, err := control.materializeBinding(t.Context(), auth, onboarding, definition); !errors.Is(err, probeFailure) {
		t.Fatalf("initial readiness: %v", err)
	}
	_, pendingRef, err := store.OwnedConnection(auth, "conn_1")
	if err != nil || pendingRef.BrokerGrantID != "grant_2" {
		t.Fatalf("pending grant missing: %v", err)
	}
	if _, err := control.confirm(t.Context(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID, "nonce": onboarding.ConfirmationNonce}); err != nil {
		t.Fatalf("confirm failed on stale-policy grant instead of re-minting: %v", err)
	}
	if revocations != 1 {
		t.Fatalf("stale-epoch grant was not revoked before re-mint, revocations=%d", revocations)
	}
	for _, id := range acquired {
		if id == "grant_1" {
			t.Fatal("lease acquired on the stale-epoch grant")
		}
	}
	stored, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != api.PhaseConfirmed {
		t.Fatalf("phase=%q", stored.Phase)
	}
	_, ref, err := store.OwnedConnection(auth, "conn_1")
	if err != nil {
		t.Fatal(err)
	}
	if ref.BrokerGrantID != "grant_2" || ref.BrokerGrantPolicy != auth.PolicyVersion {
		t.Fatalf("grant was not re-minted under the current epoch: %+v", ref)
	}
	if ref.CredentialRefID != pendingRef.CredentialRefID || ref.Revision != pendingRef.Revision {
		t.Fatal("readiness retry rotated the credential and lost binding-scoped OAuth state")
	}
}

func TestBrokerGrantPendingBindingScope(t *testing.T) {
	auth := aliceAuth()
	definition := remoteDefinition()
	match := ownerConnectionMatch{
		connection: api.Connection{ConnectionID: "conn_1"},
		credential: api.CredentialReference{CredentialRefID: "cred_1", BrokerGrantID: "grant_1", BrokerGrantPolicy: auth.PolicyVersion},
	}
	id := api.DeterministicBindingID(auth.PrincipalID, auth.ContextID, auth.RuntimeID, definition.DefinitionID, definition.Version, "conn_1", "cred_1")
	match.credential.BrokerGrantBinding = id
	store := NewStore()
	if !store.brokerGrantReusable(auth, definition, match) {
		t.Fatal("pending grant cannot resume before a successful readiness probe")
	}
	for _, field := range []string{"binding", "policy", "grant", "principal", "context", "runtime", "version"} {
		t.Run(field, func(t *testing.T) {
			a, d, m := auth, definition, match
			switch field {
			case "binding":
				m.credential.BrokerGrantBinding = "other-binding"
			case "policy":
				a.PolicyVersion = "other-policy"
			case "grant":
				m.credential.BrokerGrantID = ""
			case "principal":
				a.PrincipalID = "bob"
			case "context":
				a.ContextID = "other-context"
			case "runtime":
				a.RuntimeID = "other-runtime"
			case "version":
				d.Version = "9.0.0"
			}
			if store.brokerGrantReusable(a, d, m) {
				t.Fatal("grant reused outside its minted scope")
			}
		})
	}
	store.bindings[id] = api.ToolBinding{Status: api.RevokedStatus, PolicyVersion: auth.PolicyVersion}
	if store.brokerGrantReusable(auth, definition, match) {
		t.Fatal("revoked binding reused")
	}
}
