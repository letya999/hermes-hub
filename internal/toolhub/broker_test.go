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
	"reflect"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
)

func TestCredentialBrokerOnboardingCreatesGrantAndOpaqueReference(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	request := brokerv1.Request{ID: "request_1", ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "ready", CredentialID: "credential_1", Revision: 1, AuthorizationURL: "http://127.0.0.1/connect/request_1"}
	requests := 0
	grantRevocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			requests++
			var input brokerv1.CreateRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil || requests > 1 && (input.RotateCredentialID != "credential_1" || input.ConnectionID != "connection_1") {
				t.Error("rotation did not select original Broker credential/connection")
			}
			_ = json.NewEncoder(w).Encode(request)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_1":
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_1", ConnectionID: "connection_1", Status: "active"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_1":
			_ = json.NewEncoder(w).Encode(request)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			if requests > 1 && grantRevocations == 0 {
				http.Error(w, "old workload grant remains active", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_1", CredentialID: "credential_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", BindingID: "binding_1", WorkloadID: "work_1", Execution: "dedicated", ContractID: "github-pat", ContractRevision: 1, PolicyVersion: "policy-1", Active: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_1/revoke":
			grantRevocations++
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_1", CredentialID: "credential_1", Revision: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_1", Env: map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "broker-secret"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/release":
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
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, OnboardingCatalog, "broker-test", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.ensureBrokerRequest(t.Context(), auth, &onboarding, definition); err != nil {
		t.Fatal(err)
	}
	stored, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != PhaseAwaitingConfirm || stored.Locator != "credential_1" {
		t.Fatalf("broker request did not become ready: %+v", stored)
	}
	if _, err := control.confirm(t.Context(), auth, map[string]any{"onboarding_id": stored.OnboardingID, "nonce": stored.ConfirmationNonce}); err != nil {
		t.Fatal(err)
	}
	stored, err = store.OnboardingFor(auth, stored.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := store.Resolve(auth, stored.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Credential == nil || effective.Credential.Backend != "credential-broker" || effective.Credential.Locator != "credential_1" || effective.Credential.BrokerGrantID != "grant_1" {
		t.Fatalf("unexpected broker reference: %+v", effective.Credential)
	}
	injector := brokerRuntimeInjector(&cfg, &cfg)
	injection, err := injector(t.Context(), effective)
	if err != nil || injection.Environment["GOOGLE_TOKEN"] != "broker-secret" {
		t.Fatalf("broker runtime injection=%v env=%v", err, injection.Environment)
	}
	if err := injection.Cleanup(); err != nil {
		t.Fatal(err)
	}
	reinstall, err := control.newOnboarding(auth, OnboardingCatalog, "reinstall", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.ensureBrokerRequest(t.Context(), auth, &reinstall, definition); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || reinstall.BrokerCredentialID != "credential_1" || reinstall.Phase != PhaseAwaitingConfirm {
		t.Fatal("reinstall created new enrollment")
	}
	if _, err := control.confirm(t.Context(), auth, map[string]any{"onboarding_id": reinstall.OnboardingID, "nonce": reinstall.ConfirmationNonce}); err != nil {
		t.Fatal(err)
	}
	reinstalled, _ := store.OnboardingFor(auth, reinstall.OnboardingID)
	if reinstalled.BindingID != stored.BindingID || len(store.connections) != 1 {
		t.Fatal("reinstall duplicated binding/connection")
	}
	rotated, err := control.Rotate(auth, map[string]any{"onboarding_id": reinstall.OnboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatal("rotation did not request Broker form")
	}
	if _, err := control.confirm(t.Context(), auth, map[string]any{"onboarding_id": reinstall.OnboardingID, "nonce": rotated["nonce"]}); err != nil {
		t.Fatal(err)
	}
	reinstalled, _ = store.OnboardingFor(auth, reinstall.OnboardingID)
	if grantRevocations != 1 || len(store.connections) != 1 || reinstalled.ConnectionID != stored.ConnectionID || reinstalled.CredentialRefID == stored.CredentialRefID {
		t.Fatal("rotation did not replace same connection revision")
	}
	if err := injection.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialBrokerInjectorAndModeSelection(t *testing.T) {
	noop := brokerRuntimeInjector(nil, nil)
	injection, err := noop(t.Context(), EffectiveBinding{})
	if err != nil || injection.Environment == nil || injection.Cleanup != nil {
		t.Fatalf("non-broker binding: injection=%+v err=%v", injection, err)
	}
	effective := EffectiveBinding{Credential: &CredentialReference{Backend: "credential-broker", BrokerGrantID: "grant_1"}, WorkloadID: "workload_1"}
	for _, pair := range [][2]*credentialbroker.Config{{nil, nil}, {{}, nil}, {nil, {}}} {
		if _, err := brokerRuntimeInjector(pair[0], pair[1])(t.Context(), effective); err == nil {
			t.Fatal("incomplete broker runtime configuration accepted")
		}
	}
	if _, err := brokerRuntimeInjector(&credentialbroker.Config{}, &credentialbroker.Config{})(t.Context(), effective); err == nil {
		t.Fatal("disabled broker runtime accepted")
	}
	local := func(context.Context, EffectiveBinding) (CredentialInjection, error) {
		return CredentialInjection{Environment: map[string]string{"TOKEN": "local"}}, nil
	}
	broker := func(context.Context, EffectiveBinding) (CredentialInjection, error) {
		return CredentialInjection{Environment: map[string]string{"TOKEN": "broker"}}, nil
	}
	localBinding := EffectiveBinding{Credential: &CredentialReference{Backend: "local"}}
	brokerBinding := EffectiveBinding{Credential: &CredentialReference{Backend: "credential-broker"}}
	credentialFreeBinding := EffectiveBinding{Definition: ToolDefinition{Credentials: nil}}
	for _, test := range []struct {
		name string
		fn   CredentialInjector
		b    EffectiveBinding
		want string
	}{
		{"both-local", mergeCredentialInjectors(local, broker, false), localBinding, "local"},
		{"both-broker", mergeCredentialInjectors(local, broker, false), brokerBinding, "broker"},
		{"local-only", mergeCredentialInjectors(local, nil, false), localBinding, "local"},
		{"broker-only", mergeCredentialInjectors(nil, broker, false), brokerBinding, "broker"},
		{"broker-mode", mergeCredentialInjectors(local, broker, true), brokerBinding, "broker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			injection, err := test.fn(t.Context(), test.b)
			if err != nil || injection.Environment["TOKEN"] != test.want {
				t.Fatalf("injection=%+v err=%v", injection, err)
			}
		})
	}
	if _, err := mergeCredentialInjectors(local, broker, true)(t.Context(), localBinding); err == nil {
		t.Fatal("broker-only mode accepted local reference")
	}
	injection, err = mergeCredentialInjectors(local, broker, true)(t.Context(), credentialFreeBinding)
	if err != nil || len(injection.Environment) != 0 || injection.Cleanup != nil {
		t.Fatalf("broker-only mode rejected a credential-free definition: injection=%+v err=%v", injection, err)
	}
	credentialRequiredWithoutRef := EffectiveBinding{Definition: ToolDefinition{Credentials: []CredentialInput{{Name: "TOKEN", Required: true}}}}
	if _, err := mergeCredentialInjectors(local, broker, true)(t.Context(), credentialRequiredWithoutRef); err == nil {
		t.Fatal("broker-only mode accepted a credential-free binding for a credentialed definition")
	}
	if mergeCredentialInjectors(nil, nil, false) != nil {
		t.Fatal("empty injector unexpectedly returned a function")
	}
	if _, err := (&ControlPlane{Broker: &credentialbroker.Config{URL: "https://broker.example"}}).Rotate(aliceAuth(), nil); err == nil {
		t.Fatal("broker rotation fell through to the local backend")
	}
}

func TestCredentialBrokerRuntimeFailsClosedOnDeliveryProblems(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	mode := "mount"
	checkpointed := false
	quiesced := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/leases":
			if mode == "acquire-fail" {
				http.Error(w, `{"code":"denied"}`, http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_1", CredentialID: "credential_1"})
		case "/v1/runtime/leases/lease_1/materialize":
			if mode == "materialize-fail" {
				http.Error(w, `{"code":"unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			if mode == "state" {
				_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Env: map[string]string{"TARGET": "/state"}, Mounts: []brokerv1.Mount{{Source: "/run/lease/state", Target: "/state", ReadOnly: false}}})
			} else if mode == "mount" {
				_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Env: map[string]string{"TARGET": "/secret"}, Mounts: []brokerv1.Mount{{Source: "/run/secret", Target: "/secret", ReadOnly: true}}})
			} else if mode == "missing-mount" {
				_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Mounts: []brokerv1.Mount{{Source: "/run/secret", Target: "/secret", ReadOnly: true}}})
			} else {
				_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Env: map[string]string{}})
			}
		case "/v1/runtime/leases/lease_1/release":
			var release brokerv1.RuntimeRelease
			if json.NewDecoder(r.Body).Decode(&release) != nil {
				t.Error("invalid release")
			}
			checkpointed = release.Checkpoint && release.Quiesced
			quiesced = release.Quiesced
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	effective := EffectiveBinding{
		Binding:    ToolBinding{ToolBindingID: "binding_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1"},
		Credential: &CredentialReference{Backend: "credential-broker", BrokerGrantID: "grant_1", Keys: []string{"TOKEN"}, BrokerEnv: map[string]string{"TOKEN": "TARGET"}},
		WorkloadID: "workload_1",
	}
	injection, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective)
	if err != nil || len(injection.Mounts) != 1 || injection.Mounts[0].Target != "/secret" {
		t.Fatalf("file delivery was not returned as a mount: %+v err=%v", injection, err)
	}
	if injection.Checkpoint != nil {
		t.Fatal("readonly credentials got a state checkpoint")
	}
	mode = "state"
	stateInjection, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective)
	if err != nil || stateInjection.Checkpoint == nil {
		t.Fatal("state checkpoint unavailable", err)
	}
	if err := stateInjection.Checkpoint(); err != nil || !checkpointed {
		t.Fatal("state not checkpointed after quiescence", err)
	}
	if err := stateInjection.Cleanup(); err != nil {
		t.Fatal(err)
	}
	failedInjection, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective)
	if err != nil {
		t.Fatal(err)
	}
	if err := failedInjection.Cleanup(); err != nil || checkpointed || quiesced {
		t.Fatal("failed call claimed quiescence or checkpointed state", err)
	}
	mode = "missing-mount"
	if _, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective); err == nil {
		t.Fatal("mount without required environment delivery was accepted")
	}
	mode = "missing"
	if _, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective); err == nil {
		t.Fatal("missing broker environment was accepted")
	}
	if _, err := brokerRuntimeInjector(&cfg, &credentialbroker.Config{URL: server.URL, KeyFile: filepath.Join(t.TempDir(), "missing.key"), KeyID: "toolhub", Issuer: "hermes-toolhub"})(t.Context(), effective); err == nil {
		t.Fatal("runtime signing key failure was ignored")
	}
	mode = "acquire-fail"
	if _, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective); err == nil {
		t.Fatal("broker lease denial was ignored")
	}
	mode = "materialize-fail"
	if _, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective); err == nil {
		t.Fatal("broker materialization failure was ignored")
	}
	effective.WorkloadID = ""
	if _, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective); err == nil {
		t.Fatal("missing workload identity was accepted")
	}
}

func TestCredentialBrokerControlRequestGuardsAndStatus(t *testing.T) {
	auth := aliceAuth()
	definition := remoteDefinition()
	if err := (&ControlPlane{}).ensureBrokerRequest(t.Context(), auth, &Onboarding{}, definition); err != nil {
		t.Fatal(err)
	}
	if err := (&ControlPlane{}).refreshBrokerRequest(t.Context(), auth, nil); err != nil {
		t.Fatal(err)
	}
	definition.CredentialContractID = ""
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: "https://broker.example"}}
	if err := control.ensureBrokerRequest(t.Context(), auth, &Onboarding{}, definition); err == nil {
		t.Fatal("missing reviewed contract accepted")
	}
	store := NewStore()
	stored := remoteDefinition()
	stored.DefinitionID = "github-work"
	if err := store.RegisterDefinition(stored); err != nil {
		t.Fatal(err)
	}
	onboarding := Onboarding{
		Schema: SchemaVersion, OnboardingID: "onboard_broker_status", PrincipalID: auth.PrincipalID,
		ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: OnboardingCatalog, DefinitionID: "github-work", DefinitionVersion: "1.0.0",
		Phase: PhaseAwaitingCreds, Required: []CredentialHint{{Name: "TOKEN", Type: "token"}},
		BrokerRequestID: "request_1", BrokerAuthorizationURL: "https://broker.example/connect/request_1",
		Revision: 1, CreatedAt: time.Now(),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	control = &ControlPlane{Store: store, Now: time.Now}
	body, err := control.requiredCredentials(auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil || body["input"] != "credential-broker" || body["broker_request_id"] != onboarding.BrokerRequestID {
		t.Fatalf("broker credential status: body=%v err=%v", body, err)
	}
	if _, err := control.status(auth, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ControlPlane{}).ensureBrokerGrant(t.Context(), auth, definition, onboarding, ToolBinding{}); err == nil {
		t.Fatal("missing broker configuration accepted")
	}
	onboarding.BrokerCredentialID = "credential_1"
	control.Broker = &credentialbroker.Config{URL: "https://broker.example"}
	if _, err := control.ensureBrokerGrant(t.Context(), auth, definition, onboarding, ToolBinding{WorkloadClass: PerJob}); err == nil {
		t.Fatal("per-job broker grant accepted")
	}
}

func TestCredentialBrokerContractValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*ToolDefinition)
	}{
		{"revision-without-contract", func(d *ToolDefinition) { d.CredentialContractRevision = 1 }},
		{"invalid-contract-id", func(d *ToolDefinition) { d.CredentialContractID = "bad/id"; d.CredentialContractRevision = 1 }},
		{"invalid-input-name", func(d *ToolDefinition) {
			d.CredentialContractID = "github-pat"
			d.CredentialContractRevision = 1
			d.CredentialContractEnv = map[string]string{"BAD-NAME": "TARGET"}
		}},
		{"unknown-input-name", func(d *ToolDefinition) {
			d.CredentialContractID = "github-pat"
			d.CredentialContractRevision = 1
			d.CredentialContractEnv = map[string]string{"OTHER_TOKEN": "TARGET"}
		}},
		{"invalid-target-name", func(d *ToolDefinition) {
			d.CredentialContractID = "github-pat"
			d.CredentialContractRevision = 1
			d.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "BAD-NAME"}
		}},
		{"missing-required-mapping", func(d *ToolDefinition) {
			d.CredentialContractID = "github-pat"
			d.CredentialContractRevision = 1
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			definition := remoteDefinition()
			test.edit(&definition)
			if err := definition.Validate(); err == nil {
				t.Fatal("invalid credential broker contract accepted")
			}
		})
	}
}

func TestCredentialBrokerExpiredRequestRegenerates(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var idempotencyKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_old":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_old", ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "expired", AuthorizationURL: "https://broker.example/connect/request_old"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			idempotencyKeys = append(idempotencyKeys, in["idempotency_key"].(string))
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "pending", AuthorizationURL: "https://broker.example/connect/request_new"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_new":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "pending", AuthorizationURL: "https://broker.example/connect/request_new"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GITHUB_PERSONAL_ACCESS_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, OnboardingCatalog, "broker-regen", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_old"
	onboarding.BrokerAuthorizationURL = "https://broker.example/connect/request_old"
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	body, err := control.requiredCredentials(auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if body["broker_request_id"] != "request_new" || body["authorization_url"] != "https://broker.example/connect/request_new" {
		t.Fatalf("dead link was not replaced: %v", body)
	}
	stored, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BrokerAttempts != 1 || stored.BrokerRequestID != "request_new" {
		t.Fatalf("unexpected regeneration state: %+v", stored)
	}
	if len(idempotencyKeys) != 1 || idempotencyKeys[0] == stored.OnboardingID {
		t.Fatalf("retry must use a fresh idempotency key: %v", idempotencyKeys)
	}
}

// An optional contract delivery for a declared credential input must reach the
// workload env mapping; only required names gate contract selection.
func TestBindReviewedContractKeepsOptionalDeliveries(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	catalog := []contract.Contract{{
		ID: "gitlab-pat", Revision: 1, Storage: "local",
		Fields: []contract.Field{
			{ID: "token", Kind: "secret", Required: true, MaxBytes: 4096},
			{ID: "api_url", Kind: "string", Required: false, MaxBytes: 2048},
		},
		Deliveries: []contract.Delivery{
			{Type: "env", Field: "token", Target: "GITLAB_PERSONAL_ACCESS_TOKEN"},
			{Type: "env", Field: "token", Target: "GITLAB_TOKEN"},
			{Type: "env", Field: "api_url", Target: "GITLAB_API_URL"},
		},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/contracts" {
			_ = json.NewEncoder(w).Encode(catalog)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.Credentials = []CredentialInput{
		{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true},
		{Name: "GITLAB_TOKEN", Required: true},
		{Name: "GITLAB_API_URL"},
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if definition.CredentialContractID != "gitlab-pat" || definition.CredentialContractRevision != 1 {
		t.Fatalf("contract not bound: %+v", definition)
	}
	want := map[string]string{"GITLAB_PERSONAL_ACCESS_TOKEN": "GITLAB_PERSONAL_ACCESS_TOKEN", "GITLAB_TOKEN": "GITLAB_TOKEN", "GITLAB_API_URL": "GITLAB_API_URL"}
	if !reflect.DeepEqual(definition.CredentialContractEnv, want) {
		t.Fatalf("optional delivery dropped from env mapping: %v", definition.CredentialContractEnv)
	}

	bound := remoteDefinition()
	bound.Credentials = []CredentialInput{
		{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true},
		{Name: "GITLAB_TOKEN", Required: true},
		{Name: "GITLAB_API_URL"},
	}
	bound.CredentialContractID = "gitlab-pat"
	bound.CredentialContractRevision = 1
	bound.CredentialContractEnv = map[string]string{"GITLAB_PERSONAL_ACCESS_TOKEN": "GITLAB_PERSONAL_ACCESS_TOKEN", "GITLAB_TOKEN": "GITLAB_TOKEN"}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &bound); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bound.CredentialContractEnv, want) {
		t.Fatalf("bound definition was not backfilled with optional delivery: %v", bound.CredentialContractEnv)
	}

	// A stored mapping is not proof that a newer Broker revision still delivers it.
	catalog[0].Revision = 2
	catalog[0].Deliveries = catalog[0].Deliveries[:1]
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &bound); err != nil {
		t.Fatal(err)
	}
	if bound.CredentialContractRevision != 1 {
		t.Fatal("selected contract revision without a required delivery")
	}
}

func TestMaterializeBindingRotatesBrokerConnection(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	revoked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_1/revoke" {
			revoked = true
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_2/grants" {
			if !revoked {
				t.Error("replacement grant preceded old grant revocation")
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_2", CredentialID: "credential_2", Active: true})
			return
		}
		http.NotFound(w, r)
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
	old := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID("conn-existing", 1), ConnectionID: "conn-existing", Revision: 1, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, Status: ActiveStatus, BrokerContractID: "github-pat", BrokerContractRevision: 1, BrokerGrantID: "grant_1"}
	if err := store.PutCredentialReference(old); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(Connection{Schema: SchemaVersion, ConnectionID: "conn-existing", Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: old.CredentialRefID, Revision: 1, Status: ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now, Ready: func(context.Context, EffectiveBinding) error { return nil }}
	onboarding := Onboarding{OnboardingID: "onboard-new", Locator: "credential_2", BrokerCredentialID: "credential_2", Required: []CredentialHint{{Name: "GOOGLE_TOKEN"}}}
	binding, err := control.materializeBinding(t.Context(), auth, onboarding, definition)
	if err != nil {
		t.Fatalf("broker rotation rejected: %v", err)
	}
	if binding.ConnectionID != "conn-existing" || binding.CredentialRevision != 2 {
		t.Fatalf("binding did not reuse rotated connection: %+v", binding)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.connections) != 1 {
		t.Fatalf("rotation created a second owner connection")
	}
	reference := store.credentials[binding.CredentialRefID]
	if reference.Backend != "credential-broker" || reference.BrokerGrantID != "grant_2" || reference.BrokerEnv["GOOGLE_TOKEN"] != "GOOGLE_TOKEN" {
		t.Fatalf("rotated reference lost broker fields: %+v", reference)
	}
	if store.credentials[old.CredentialRefID].Status != RevokedStatus {
		t.Fatal("previous credential reference was not revoked")
	}
}

func TestEnableAfterRevokeAllowsNewConnection(t *testing.T) {
	store := NewStore()
	auth := aliceAuth()
	definition := remoteDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	old := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID("conn-old", 1), ConnectionID: "conn-old", Revision: 1, Backend: credstore.BackendLocal, Locator: "old", Keys: []string{"GOOGLE_TOKEN"}, Status: RevokedStatus}
	if err := store.PutCredentialReference(old); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(Connection{Schema: SchemaVersion, ConnectionID: "conn-old", Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: old.CredentialRefID, Revision: 1, Status: RevokedStatus}); err != nil {
		t.Fatal(err)
	}
	tombstone := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: "conn-old", ConnectionRevision: 1, CredentialRefID: old.CredentialRefID, CredentialRevision: 1, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: RevokedStatus, Revision: 2}
	tombstone.ToolBindingID = DeterministicBindingID(tombstone.PrincipalID, tombstone.ContextID, tombstone.RuntimeID, tombstone.DefinitionID, tombstone.DefinitionVersion, tombstone.ConnectionID, tombstone.CredentialRefID)
	store.mu.Lock()
	store.bindings[tombstone.ToolBindingID] = tombstone
	store.mu.Unlock()

	next := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID("conn-new", 1), ConnectionID: "conn-new", Revision: 1, Backend: credstore.BackendLocal, Locator: "new", Keys: []string{"GOOGLE_TOKEN"}, Status: ActiveStatus}
	if err := store.PutCredentialReference(next); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(Connection{Schema: SchemaVersion, ConnectionID: "conn-new", Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: next.CredentialRefID, Revision: 1, Status: ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	binding, err := store.Enable(auth, definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatalf("enable blocked by unrelated revoked binding: %v", err)
	}
	if binding.ToolBindingID == tombstone.ToolBindingID || binding.ConnectionID != "conn-new" {
		t.Fatalf("enable resurrected the tombstone instead of binding the new connection: %+v", binding)
	}
}

func TestCredentialEgressHosts(t *testing.T) {
	env := map[string]string{
		"GITLAB_API_URL": "https://gitlab.example.com/api/v4",
		"CUSTOM_API_URL": "https://self.host:8443/api",
		"SECRET_URL":     "https://secret.example.com",
		"USERINFO_URL":   "https://user:password@userinfo.example.com",
		"QUERY_URL":      "https://query.example.com/?token=x",
		"TOKEN":          "https://token.example.com",
		"INSECURE":       "http://plain.example.com",
		"BROKEN":         "://bad",
	}
	hosts := credentialEgressHosts(env)
	want := map[string]bool{"gitlab.example.com": true, "self.host:8443": true}
	if len(hosts) != len(want) {
		t.Fatalf("credentialEgressHosts=%v", hosts)
	}
	for _, host := range hosts {
		if !want[host] {
			t.Fatalf("unexpected egress host %q", host)
		}
	}
	merged := mergeEgressHosts([]string{"gitlab.com", "GITLAB.EXAMPLE.COM"}, hosts)
	if len(merged) != 3 {
		t.Fatalf("mergeEgressHosts did not dedupe: %v", merged)
	}
}
