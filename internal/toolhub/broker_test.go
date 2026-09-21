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
	"github.com/letya999/hermes-hub/internal/credentialbroker"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			_ = json.NewEncoder(w).Encode(request)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_1":
			_ = json.NewEncoder(w).Encode(request)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_1", CredentialID: "credential_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", BindingID: "binding_1", WorkloadID: "work_1", Execution: "dedicated", ContractID: "github-pat", ContractRevision: 1, PolicyVersion: "policy-1", Active: true})
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
			if mode == "mount" {
				_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Mounts: []brokerv1.Mount{{Source: "/run/secret", Target: "/secret", ReadOnly: true}}})
			} else {
				_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Env: map[string]string{}})
			}
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
