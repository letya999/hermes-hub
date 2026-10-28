package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The tests in this file cover ToolHub ↔ Credential Broker state drift: the
// broker records changed while ToolHub still references them. Visible deleted
// credentials permit fresh requests; an ambiguous credential 404 preserves the
// reference for diagnosis. Request renewal has a separate lifecycle.

func staleBrokerKey(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	return keyPath
}

func brokerNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(brokerv1.Error{Code: "not_found"})
}

// The recorded previous grant may be gone at the broker (drift between the
// ToolHub store and the broker ledger). Revoking it must not abort rotation —
// a missing grant is already the desired post-revoke state.
func TestMaterializeBindingToleratesMissingOldGrant(t *testing.T) {
	keyPath := staleBrokerKey(t)
	var granted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_1/revoke":
			brokerNotFound(w)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_2/grants":
			granted = true
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_2", CredentialID: "credential_2", Active: true})
		default:
			brokerNotFound(w)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	old := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("conn-existing", 1), ConnectionID: "conn-existing", Revision: 1, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus, BrokerContractID: "github-pat", BrokerContractRevision: 1, BrokerGrantID: "grant_1"}
	if err := store.PutCredentialReference(old); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-existing", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: old.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	onboarding := api.Onboarding{OnboardingID: "onboard-drift", Locator: "credential_2", BrokerCredentialID: "credential_2", Required: []api.CredentialHint{{Name: "GOOGLE_TOKEN"}}}
	binding, err := control.materializeBinding(t.Context(), auth, onboarding, definition)
	if err != nil {
		t.Fatalf("rotation aborted on a missing old grant: %v", err)
	}
	if !granted {
		t.Fatal("rotation never minted the replacement grant")
	}
	if binding.ConnectionID != "conn-existing" || binding.CredentialRevision != 2 {
		t.Fatalf("binding did not reuse rotated connection: %+v", binding)
	}
}

func TestMaterializeBindingRotatesRevokedGrantWithoutNewSecret(t *testing.T) {
	keyPath := staleBrokerKey(t)
	lookups, grants := 0, 0
	failLookup := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/grants/grant_1":
			lookups++
			if failLookup {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(brokerv1.Error{Code: "unavailable"})
				return
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_1", Active: false})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_1/revoke":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			grants++
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_2", CredentialID: "credential_1", Active: true})
		default:
			brokerNotFound(w)
		}
	}))
	defer server.Close()
	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	refID := api.CredentialReferenceID("conn-existing", 1)
	bindID := api.DeterministicBindingID(auth.PrincipalID, auth.ContextID, auth.RuntimeID, definition.DefinitionID, definition.Version, "conn-existing", refID)
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: refID, ConnectionID: "conn-existing", Revision: 1, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, BrokerContractID: "github-pat", BrokerContractRevision: 1, BrokerEnv: map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}, BrokerGrantID: "grant_1", BrokerGrantPolicy: auth.PolicyVersion, BrokerGrantBinding: bindID, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-existing", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: refID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	store.bindings[bindID] = api.ToolBinding{Schema: api.SchemaVersion, ToolBindingID: bindID, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: "conn-existing", ConnectionRevision: 1, CredentialRefID: refID, CredentialRevision: 1, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	onboarding := api.Onboarding{OnboardingID: "onboard-reuse", Locator: "credential_1", BrokerCredentialID: "credential_1", Required: []api.CredentialHint{{Name: "GOOGLE_TOKEN"}}}
	if _, err := control.materializeBinding(t.Context(), auth, onboarding, definition); err == nil || grants != 0 {
		t.Fatalf("unavailable broker rotated live grant: grants=%d err=%v", grants, err)
	}
	failLookup = false
	if _, err := control.materializeBinding(t.Context(), auth, onboarding, definition); err != nil {
		t.Fatal(err)
	}
	_, current, err := store.OwnedConnection(auth, "conn-existing")
	if err != nil || current.BrokerGrantID != "grant_2" || current.Locator != "credential_1" || lookups != 2 || grants != 1 {
		t.Fatalf("grant recovery: lookup=%d grants=%d recovered=%t err=%v", lookups, grants, current.BrokerGrantID == "grant_2", err)
	}
}

func TestEnsureBrokerGrantDoesNotReuseRevokedIdempotency(t *testing.T) {
	keyPath := staleBrokerKey(t)
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/v1/credentials/credential_1/grants" {
			brokerNotFound(w)
			return
		}
		var input brokerv1.GrantRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		keys = append(keys, input.IdempotencyKey)
		_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: []string{"grant_revoked", "grant_active"}[len(keys)-1], Active: len(keys) == 2})
	}))
	defer server.Close()
	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Broker: &cfg, Now: time.Now}
	binding := api.ToolBinding{Schema: api.SchemaVersion, ToolBindingID: "binding1", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: "conn1", WorkloadClass: definition.Workload.Class}
	onboarding := api.Onboarding{OnboardingID: "onboard1", BrokerCredentialID: "credential_1"}
	grantID, err := control.ensureBrokerGrant(t.Context(), auth, definition, onboarding, binding)
	if err != nil || grantID != "grant_active" || len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("revoked idempotency replay: active=%t attempts=%d distinct=%t err=%v", grantID == "grant_active", len(keys), len(keys) == 2 && keys[0] != keys[1], err)
	}
}

// cutAuthorization revokes the broker credential recorded on the onboarding.
// When the broker no longer holds that credential, revoke is a no-op outcome,
// not an error that blocks the user-facing revoke/remove flow.
func TestCutAuthorizationToleratesMissingBrokerCredential(t *testing.T) {
	keyPath := staleBrokerKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		brokerNotFound(w)
	}))
	defer server.Close()

	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: NewStore(), Broker: &cfg, Now: time.Now}
	onboarding := api.Onboarding{OnboardingID: "onboard-stale", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", BrokerCredentialID: "credential_gone"}
	if err := control.cutAuthorization(onboarding, false); err != nil {
		t.Fatalf("revoke aborted on an already-absent credential: %v", err)
	}
}

// A recorded broker request that no longer exists (ledger reset) must be
// replaced by a fresh request under a new idempotency key, the same way an
// expired or canceled request already is.
func TestRefreshBrokerRequestHealsDeadRequest(t *testing.T) {
	keyPath := staleBrokerKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_gone":
			brokerNotFound(w)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var c contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(c)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			var input brokerv1.CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: input.ContractID, ContractRevision: input.ContractRevision, ConnectionID: input.ConnectionID, Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_new"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_new":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_new"})
		default:
			brokerNotFound(w)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "dead-request", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_gone"
	onboarding.BrokerAuthorizationURL = "http://127.0.0.1/connect/request_gone"
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.refreshBrokerRequest(t.Context(), auth, &onboarding); err != nil {
		t.Fatalf("dead request aborted refresh: %v", err)
	}
	if onboarding.BrokerRequestID != "request_new" || onboarding.BrokerAttempts != 1 {
		t.Fatalf("dead request was not replaced: %+v", onboarding)
	}
}

// A visible deleted credential can reset setup to a fresh request. A masked
// 404 is not equivalent: it must preserve the old reference for diagnosis.
func TestRefreshBrokerRequestHealsDeadCredential(t *testing.T) {
	keyPath := staleBrokerKey(t)
	var fresh bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_1":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_1", ContractID: "github-pat", ContractRevision: 1, Status: "ready", CredentialID: "credential_dead"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_dead":
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_dead", Status: "deleted"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var c contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(c)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			var input brokerv1.CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			// The vanished credential cannot be a rotate target — the broker
			// denies rotation of an unknown credential — so the healed request
			// must go out as a fresh one.
			fresh = input.RotateCredentialID == ""
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: input.ContractID, ContractRevision: input.ContractRevision, ConnectionID: input.ConnectionID, Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_new"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_new":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", Status: "pending"})
		default:
			brokerNotFound(w)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "dead-credential", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_1"
	onboarding.BrokerAuthorizationURL = "http://127.0.0.1/connect/request_1"
	onboarding.BrokerCredentialID = "credential_dead"
	onboarding.Locator = "credential_dead"
	onboarding.Phase = api.PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = api.RandomNonce()
	onboarding.ConfirmationExpires = time.Now().Add(time.Hour)
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.refreshBrokerRequest(t.Context(), auth, &onboarding); err != nil {
		t.Fatalf("dead credential aborted refresh: %v", err)
	}
	if onboarding.Phase != api.PhaseAwaitingCreds || onboarding.BrokerCredentialID != "" || onboarding.Locator != "" {
		t.Fatalf("onboarding was not reset to awaiting-creds: %+v", onboarding)
	}
	if onboarding.BrokerRequestID != "request_new" {
		t.Fatalf("no fresh request was minted: %+v", onboarding)
	}
	if !fresh {
		t.Fatal("healed request kept the vanished credential as rotate target")
	}
}

// The end-to-end regression: an onboarding reached awaiting-confirm with a
// live broker credential while the previously recorded grant is gone. confirm
// must complete the rotation instead of dying on the revoke 404.
func TestConfirmSucceedsWhenOldGrantMissing(t *testing.T) {
	keyPath := staleBrokerKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_1":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_1", ContractID: "github-pat", ContractRevision: 1, Status: "ready", CredentialID: "credential_2"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_2":
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_2", Status: "active"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_1/revoke":
			brokerNotFound(w)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_2/grants":
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_2", CredentialID: "credential_2", Active: true})
		default:
			brokerNotFound(w)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	old := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("conn-existing", 1), ConnectionID: "conn-existing", Revision: 1, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus, BrokerContractID: "github-pat", BrokerContractRevision: 1, BrokerGrantID: "grant_1"}
	if err := store.PutCredentialReference(old); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-existing", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: old.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "confirm-drift", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_1"
	onboarding.BrokerCredentialID = "credential_2"
	onboarding.Locator = "credential_2"
	onboarding.Phase = api.PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = "nonce-x"
	onboarding.ConfirmationExpires = time.Now().Add(time.Hour)
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.confirm(t.Context(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID, "nonce": "nonce-x"}); err != nil {
		t.Fatalf("confirm aborted on a missing old grant: %v", err)
	}
	stored, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != api.PhaseConfirmed || stored.BindingID == "" {
		t.Fatalf("confirm did not bind: %+v", stored)
	}
}

// A user-initiated rotate whose previous credential is visibly deleted at the broker
// must mint a plain fresh request on the deterministic connection — sending
// the dead credential id as rotate target is denied by the broker.
func TestEnsureBrokerRequestDropsDeadRotateTarget(t *testing.T) {
	keyPath := staleBrokerKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_gone":
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_gone", Status: "deleted"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var c contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(c)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			var input brokerv1.CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.RotateCredentialID != "" {
				t.Errorf("dead credential %q kept as rotate target", input.RotateCredentialID)
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: input.ContractID, ContractRevision: input.ContractRevision, ConnectionID: input.ConnectionID, Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_new"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_new":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", Status: "pending"})
		default:
			brokerNotFound(w)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "rotate-drift", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRotateCredentialID = "credential_gone"
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.ensureBrokerRequest(t.Context(), auth, &onboarding, definition); err != nil {
		t.Fatalf("dead rotate target aborted the request: %v", err)
	}
	if onboarding.BrokerRequestID != "request_new" {
		t.Fatalf("no fresh request was minted: %+v", onboarding)
	}
}

// The regenerate path reaches the request lookup inside ensureBrokerRequest
// itself (awaiting-creds with a recorded request). A request the broker no
// longer holds must be replaced there too, not returned as a hard 404.
func TestEnsureBrokerRequestHealsDeadRecordedRequest(t *testing.T) {
	keyPath := staleBrokerKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_gone":
			brokerNotFound(w)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var c contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(c)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			var input brokerv1.CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: input.ContractID, ContractRevision: input.ContractRevision, ConnectionID: input.ConnectionID, Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_new"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_new":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", Status: "pending"})
		default:
			brokerNotFound(w)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "regen-dead", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_gone"
	onboarding.BrokerAuthorizationURL = "http://127.0.0.1/connect/request_gone"
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.ensureBrokerRequest(t.Context(), auth, &onboarding, definition); err != nil {
		t.Fatalf("dead request aborted regenerate: %v", err)
	}
	if onboarding.BrokerRequestID != "request_new" || onboarding.BrokerAttempts != 1 {
		t.Fatalf("dead request was not replaced: %+v", onboarding)
	}
}

// The admission probe revokes its temporary grant on cleanup. If the broker
// dropped that grant mid-admission, the cleanup must not poison the result —
// absent is the post-revoke state.
func TestAdmitWithBrokerCredentialToleratesMissingTempGrant(t *testing.T) {
	keyPath := staleBrokerKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_temp", CredentialID: "credential_1", Active: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_temp/revoke":
			brokerNotFound(w)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_temp", CredentialID: "credential_1", Revision: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_1", Env: map[string]string{"GOOGLE_TOKEN": "x"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			brokerNotFound(w)
		}
	}))
	defer server.Close()

	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: NewStore(), Broker: &cfg, BrokerRuntime: &cfg, Now: time.Now}
	control.AdmitWithCredentials = func(_ context.Context, d api.ToolDefinition, _ map[string]string) (api.ToolDefinition, error) {
		return d, nil
	}
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	onboarding := api.Onboarding{OnboardingID: "onboard-admission", PrincipalID: "alice"}
	if _, err := control.admitWithBrokerCredential(t.Context(), aliceAuth(), onboarding, definition, "credential_1"); err != nil {
		t.Fatalf("admission failed on a vanished temporary grant: %v", err)
	}
}
