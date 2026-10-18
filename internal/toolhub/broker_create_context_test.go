package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// When the broker rejects CreateRequest (e.g. the pinned contract vanished
// from its catalog), the surfaced error must name the contract — a bare
// "invalid_request (400)" leaves owner and operator guessing what to restore.
func TestBrokerRequestErrorNamesContract(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(brokerv1.Error{Code: "invalid_request"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "NOTION_TOKEN", Required: true}}
	definition.CredentialContractID = "notion-token"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"NOTION_TOKEN": "NOTION_TOKEN"}
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-test", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingCreds, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version}

	err = control.ensureBrokerRequest(context.Background(), aliceAuth(), &onboarding, definition)
	if err == nil || !strings.Contains(err.Error(), "notion-token") {
		t.Fatalf("error hides the rejected contract: %v", err)
	}
}

// A shared workload's credential is context-owned: ToolHub must assert
// context_manager in the signed control claims or the broker rejects the
// owner_kind=context request with a bare 400. Per-user installs must not
// carry the assertion.
func TestBrokerRequestSharedCarriesContextManager(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var contextManager *bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/requests" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
			var claims struct {
				Actor struct {
					ContextManager bool `json:"context_manager"`
				} `json:"actor"`
			}
			if err := json.Unmarshal(payload, &claims); err != nil {
				t.Error(err)
			}
			cm := claims.Actor.ContextManager
			contextManager = &cm
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "r1", Status: "pending", AuthorizationURL: "https://broker/connect/r1", ContractID: "grafana-sat", ContractRevision: 1})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/requests/") {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "r1", Status: "pending", AuthorizationURL: "https://broker/connect/r1", ContractID: "grafana-sat", ContractRevision: 1})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := NewStore()
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	definition := remoteDefinition()
	definition.Workload.Class = api.Shared
	definition.Workload.Sharable = true
	definition.Credentials = []api.CredentialInput{{Name: "GRAFANA_SERVICE_ACCOUNT_TOKEN", Required: true}}
	definition.CredentialContractID = "grafana-sat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GRAFANA_SERVICE_ACCOUNT_TOKEN": "GRAFANA_SERVICE_ACCOUNT_TOKEN"}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	onboarding, err := control.newOnboarding(aliceAuth(), api.OnboardingCatalog, "shared-test", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := control.ensureBrokerRequest(t.Context(), aliceAuth(), &onboarding, definition); err != nil {
		t.Fatal(err)
	}
	if contextManager == nil || !*contextManager {
		t.Fatalf("shared credential request signed without context_manager: %v", contextManager)
	}
}

// A grant on a context-owned credential needs the same context_manager
// assertion as the request that minted it; without it the broker rejects the
// enable step after the owner already submitted the form.
func TestBrokerGrantSharedCarriesContextManager(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var contextManager *bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
			var claims struct {
				Actor struct {
					ContextManager bool `json:"context_manager"`
				} `json:"actor"`
			}
			if err := json.Unmarshal(payload, &claims); err != nil {
				t.Error(err)
			}
			cm := claims.Actor.ContextManager
			contextManager = &cm
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_1"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	definition := remoteDefinition()
	definition.CredentialContractID = "grafana-sat"
	definition.CredentialContractRevision = 1
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-shared", BrokerCredentialID: "credential_1"}
	binding := api.ToolBinding{Schema: api.SchemaVersion, ToolBindingID: "bind-1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, WorkloadClass: api.Shared, Status: api.ActiveStatus, PolicyVersion: "v1"}

	grantID, err := control.ensureBrokerGrant(t.Context(), aliceAuth(), definition, onboarding, binding)
	if err != nil {
		t.Fatal(err)
	}
	if grantID != "grant_1" {
		t.Fatalf("grant=%q", grantID)
	}
	if contextManager == nil || !*contextManager {
		t.Fatalf("shared credential grant signed without context_manager: %v", contextManager)
	}
}

// Polling a pending shared onboarding reads the minted credential, which is
// context-owned: without context_manager the broker reports it not_found and
// the heal path would reset a healthy request into an endless resubmit loop.
func TestBrokerRefreshSharedCredentialCheckCarriesContextManager(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var contextManager *bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_1" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
			var claims struct {
				Actor struct {
					ContextManager bool `json:"context_manager"`
				} `json:"actor"`
			}
			if err := json.Unmarshal(payload, &claims); err != nil {
				t.Error(err)
			}
			cm := claims.Actor.ContextManager
			contextManager = &cm
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_1", Status: "active", OwnerKind: "context"})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/requests/") {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "r1", Status: "pending", AuthorizationURL: "https://broker/connect/r1", ContractID: "grafana-sat", ContractRevision: 1})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := NewStore()
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	definition := remoteDefinition()
	definition.Workload.Class = api.Shared
	definition.Workload.Sharable = true
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	onboarding, err := control.newOnboarding(aliceAuth(), api.OnboardingCatalog, "shared-refresh", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "r1"
	onboarding.BrokerCredentialID = "credential_1"
	onboarding.Phase = api.PhaseAwaitingConfirm
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}

	if err := control.refreshBrokerRequest(t.Context(), aliceAuth(), &onboarding); err != nil {
		t.Fatal(err)
	}
	if contextManager == nil || !*contextManager {
		t.Fatalf("shared credential read signed without context_manager: %v", contextManager)
	}
	if onboarding.BrokerCredentialID == "" {
		t.Fatal("healthy shared credential was reset as missing")
	}
}

// The tools/list probe grants on the context-owned credential too: a shared
// admission signed as a plain user is rejected before the probe even runs.
func TestBrokerAdmissionSharedCarriesContextManager(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var contextManager *bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
			var claims struct {
				Actor struct {
					ContextManager bool `json:"context_manager"`
				} `json:"actor"`
			}
			if err := json.Unmarshal(payload, &claims); err != nil {
				t.Error(err)
			}
			cm := claims.Actor.ContextManager
			contextManager = &cm
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_temp", CredentialID: "credential_1", Active: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_temp/revoke":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_temp", CredentialID: "credential_1", Revision: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_1", Env: map[string]string{"GRAFANA_SERVICE_ACCOUNT_TOKEN": "x"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: NewStore(), Broker: &cfg, BrokerRuntime: &cfg, Now: time.Now}
	control.AdmitWithCredentials = func(_ context.Context, d api.ToolDefinition, _ map[string]string) (api.ToolDefinition, error) {
		return d, nil
	}
	definition := remoteDefinition()
	definition.Workload.Class = api.Shared
	definition.Credentials = []api.CredentialInput{{Name: "GRAFANA_SERVICE_ACCOUNT_TOKEN", Required: true}}
	definition.CredentialContractID = "grafana-sat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GRAFANA_SERVICE_ACCOUNT_TOKEN": "GRAFANA_SERVICE_ACCOUNT_TOKEN"}
	onboarding := api.Onboarding{OnboardingID: "onboard-shared-admission", PrincipalID: "alice"}
	if _, err := control.admitWithBrokerCredential(t.Context(), aliceAuth(), onboarding, definition, "credential_1"); err != nil {
		t.Fatal(err)
	}
	if contextManager == nil || !*contextManager {
		t.Fatalf("shared admission grant signed without context_manager: %v", contextManager)
	}
}

// Removing a shared install revokes the context-owned credential; a revoke
// signed as the plain owner gets not_found and leaks the credential active.
func TestBrokerRevokeSharedCarriesContextManager(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var contextManager *bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/revoke" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
			var claims struct {
				Actor struct {
					ContextManager bool `json:"context_manager"`
				} `json:"actor"`
			}
			if err := json.Unmarshal(payload, &claims); err != nil {
				t.Error(err)
			}
			cm := claims.Actor.ContextManager
			contextManager = &cm
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := NewStore()
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	definition := remoteDefinition()
	definition.Workload.Class = api.Shared
	definition.Workload.Sharable = true
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	onboarding, err := control.newOnboarding(aliceAuth(), api.OnboardingCatalog, "shared-remove", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerCredentialID = "credential_1"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}

	if err := control.cutAuthorization(onboarding); err != nil {
		t.Fatal(err)
	}
	if contextManager == nil || !*contextManager {
		t.Fatalf("shared credential revoke signed without context_manager: %v", contextManager)
	}
}
