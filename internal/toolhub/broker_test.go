package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var c contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(c)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			requests++
			var input brokerv1.CreateRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.ConsumerID != "github-work" || requests > 1 && (input.RotateCredentialID != "credential_1" || input.ConnectionID != "connection_1") {
				t.Error("rotation did not select original Broker credential/connection/consumer")
			}
			_ = json.NewEncoder(w).Encode(request)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_1":
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_1", ConnectionID: "connection_1", ConsumerID: "github-work", Status: "active"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_1":
			_ = json.NewEncoder(w).Encode(request)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			if requests > 1 && grantRevocations == 0 {
				http.Error(w, "old workload grant remains active", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_1", CredentialID: "credential_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", BindingID: "binding_1", WorkloadID: "work_1", Execution: "dedicated", ContractID: "github-pat", ContractRevision: 1, PolicyVersion: "policy-1", Active: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/grants/grant_1":
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_1", Active: true})
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
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "broker-test", definition, "", "")
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
	if stored.Phase != api.PhaseAwaitingConfirm || stored.Locator != "credential_1" {
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
	reinstall, err := control.newOnboarding(auth, api.OnboardingCatalog, "reinstall", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.ensureBrokerRequest(t.Context(), auth, &reinstall, definition); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || reinstall.BrokerCredentialID != "credential_1" || reinstall.Phase != api.PhaseAwaitingConfirm {
		t.Fatal("reinstall created new enrollment")
	}
	if _, err := control.confirm(t.Context(), auth, map[string]any{"onboarding_id": reinstall.OnboardingID, "nonce": reinstall.ConfirmationNonce}); err != nil {
		t.Fatal(err)
	}
	reinstalled, _ := store.OnboardingFor(auth, reinstall.OnboardingID)
	if reinstalled.BindingID != stored.BindingID || len(store.connections) != 1 {
		t.Fatal("reinstall duplicated binding/connection")
	}
	if _, err := control.Rotate(auth, map[string]any{"onboarding_id": reinstall.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatal("rotation did not request Broker form")
	}
	if _, err := control.confirm(t.Context(), auth, map[string]any{"onboarding_id": reinstall.OnboardingID, "nonce": confirmNonceOf(t, store, reinstall.OnboardingID)}); err != nil {
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
	injection, err := noop(t.Context(), api.EffectiveBinding{})
	if err != nil || injection.Environment == nil || injection.Cleanup != nil {
		t.Fatalf("non-broker binding: injection=%+v err=%v", injection, err)
	}
	effective := api.EffectiveBinding{Credential: &api.CredentialReference{Backend: "credential-broker", BrokerGrantID: "grant_1"}, WorkloadID: "workload_1"}
	for _, pair := range [][2]*credentialbroker.Config{{nil, nil}, {{}, nil}, {nil, {}}} {
		if _, err := brokerRuntimeInjector(pair[0], pair[1])(t.Context(), effective); err == nil {
			t.Fatal("incomplete broker runtime configuration accepted")
		}
	}
	if _, err := brokerRuntimeInjector(&credentialbroker.Config{}, &credentialbroker.Config{})(t.Context(), effective); err == nil {
		t.Fatal("disabled broker runtime accepted")
	}
	local := func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
		return api.CredentialInjection{Environment: map[string]string{"TOKEN": "local"}}, nil
	}
	broker := func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
		return api.CredentialInjection{Environment: map[string]string{"TOKEN": "broker"}}, nil
	}
	localBinding := api.EffectiveBinding{Credential: &api.CredentialReference{Backend: "local"}}
	brokerBinding := api.EffectiveBinding{Credential: &api.CredentialReference{Backend: "credential-broker"}}
	credentialFreeBinding := api.EffectiveBinding{Definition: api.ToolDefinition{Credentials: nil}}
	for _, test := range []struct {
		name string
		fn   api.CredentialInjector
		b    api.EffectiveBinding
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
	contractedLocal := api.EffectiveBinding{
		Definition: api.ToolDefinition{CredentialContractID: "github-pat", Credentials: []api.CredentialInput{{Name: "TOKEN", Required: true}}},
		Credential: &api.CredentialReference{Backend: "local"},
	}
	if _, err := mergeCredentialInjectors(local, broker, true)(t.Context(), contractedLocal); err == nil {
		t.Fatal("broker-only mode accepted a local reference for a contracted definition")
	}
	injection, err = mergeCredentialInjectors(local, broker, true)(t.Context(), localBinding)
	if err != nil || injection.Environment["TOKEN"] != "local" {
		t.Fatalf("credential-gate local reference: injection=%+v err=%v", injection, err)
	}
	injection, err = mergeCredentialInjectors(local, broker, true)(t.Context(), credentialFreeBinding)
	if err != nil || len(injection.Environment) != 0 || injection.Cleanup != nil {
		t.Fatalf("broker-only mode rejected a credential-free definition: injection=%+v err=%v", injection, err)
	}
	credentialRequiredWithoutRef := api.EffectiveBinding{Definition: api.ToolDefinition{Credentials: []api.CredentialInput{{Name: "TOKEN", Required: true}}}}
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
	effective := api.EffectiveBinding{
		Binding:    api.ToolBinding{ToolBindingID: "binding_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1"},
		Credential: &api.CredentialReference{Backend: "credential-broker", BrokerGrantID: "grant_1", Keys: []string{"TOKEN"}, BrokerEnv: map[string]string{"TOKEN": "TARGET"}},
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
	if _, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective); err == nil || !errors.Is(err, api.ErrCredentialDelivery) {
		t.Fatalf("missing broker environment was accepted or misclassified: %v", err)
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

func TestCredentialBrokerInjectorSatisfiesAlternativeGroups(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	mode := "jira"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_1", CredentialID: "credential_1"})
		case "/v1/runtime/leases/lease_1/materialize":
			env := map[string]string{}
			switch mode {
			case "jira":
				env = map[string]string{"JIRA_URL": "x", "JIRA_USERNAME": "x", "JIRA_API_TOKEN": "x"}
			case "confluence":
				env = map[string]string{"CONFLUENCE_URL": "x", "CONFLUENCE_USERNAME": "x", "CONFLUENCE_API_TOKEN": "x"}
			case "partial":
				env = map[string]string{"JIRA_URL": "x"}
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Env: env})
		case "/v1/runtime/leases/lease_1/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	names := []string{"JIRA_URL", "JIRA_USERNAME", "JIRA_API_TOKEN", "CONFLUENCE_URL", "CONFLUENCE_USERNAME", "CONFLUENCE_API_TOKEN"}
	credentials := make([]api.CredentialInput, len(names))
	brokerEnv := map[string]string{}
	for index, name := range names {
		credentials[index] = api.CredentialInput{Name: name, Required: true}
		brokerEnv[name] = name
	}
	effective := api.EffectiveBinding{
		Binding: api.ToolBinding{ToolBindingID: "binding_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1"},
		Definition: api.ToolDefinition{DefinitionID: "atlassian", Credentials: credentials,
			CredentialGroups: [][]string{{"JIRA_URL", "JIRA_USERNAME", "JIRA_API_TOKEN"}, {"CONFLUENCE_URL", "CONFLUENCE_USERNAME", "CONFLUENCE_API_TOKEN"}}},
		Credential: &api.CredentialReference{Backend: "credential-broker", BrokerGrantID: "grant_1", Keys: names, BrokerEnv: brokerEnv},
		WorkloadID: "workload_1",
	}
	injection, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective)
	if err != nil || len(injection.Environment) != 3 {
		t.Fatalf("complete jira block rejected: env=%v err=%v", injection.Environment, err)
	}
	mode = "confluence"
	injection, err = brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective)
	if err != nil || len(injection.Environment) != 3 {
		t.Fatalf("complete confluence block rejected: env=%v err=%v", injection.Environment, err)
	}
	for _, incomplete := range []string{"partial", "empty"} {
		mode = incomplete
		if _, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective); !errors.Is(err, api.ErrIsolation) || !errors.Is(err, api.ErrCredentialDelivery) {
			t.Fatalf("incomplete alternative groups accepted (%s): %v", incomplete, err)
		} else if !strings.Contains(err.Error(), "JIRA_API_TOKEN") {
			t.Fatalf("missing input names absent from delivery diagnosis: %v", err)
		}
	}
}

func TestCredentialBrokerControlRequestGuardsAndStatus(t *testing.T) {
	auth := aliceAuth()
	definition := remoteDefinition()
	if err := (&ControlPlane{}).ensureBrokerRequest(t.Context(), auth, &api.Onboarding{}, definition); err != nil {
		t.Fatal(err)
	}
	if err := (&ControlPlane{}).refreshBrokerRequest(t.Context(), auth, nil); err != nil {
		t.Fatal(err)
	}
	definition.CredentialContractID = ""
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: "https://broker.example"}}
	if err := control.ensureBrokerRequest(t.Context(), auth, &api.Onboarding{}, definition); err == nil {
		t.Fatal("missing reviewed contract accepted")
	}
	store := NewStore()
	stored := remoteDefinition()
	stored.DefinitionID = "github-work"
	if err := store.RegisterDefinition(stored); err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onboard_broker_status", PrincipalID: auth.PrincipalID,
		ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingCatalog, DefinitionID: "github-work", DefinitionVersion: "1.0.0",
		Phase: api.PhaseAwaitingCreds, Required: []api.CredentialHint{{Name: "TOKEN", Type: "token"}},
		BrokerRequestID: "request_1", BrokerAuthorizationURL: "https://broker.example/connect/request_1",
		Revision: 1, CreatedAt: time.Now(),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	control = &ControlPlane{Store: store, Now: time.Now}
	body, err := control.requiredCredentials(context.Background(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil || body["input"] != "credential-broker" || body["broker_request_id"] != onboarding.BrokerRequestID {
		t.Fatalf("broker credential status: body=%v err=%v", body, err)
	}
	if _, err := control.status(context.Background(), auth, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ControlPlane{}).ensureBrokerGrant(t.Context(), auth, definition, onboarding, api.ToolBinding{}); err == nil {
		t.Fatal("missing broker configuration accepted")
	}
	onboarding.BrokerCredentialID = "credential_1"
	control.Broker = &credentialbroker.Config{URL: "https://broker.example"}
	if _, err := control.ensureBrokerGrant(t.Context(), auth, definition, onboarding, api.ToolBinding{WorkloadClass: api.PerJob}); err == nil {
		t.Fatal("per-job broker grant accepted")
	}
}

func TestCredentialBrokerContractValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*api.ToolDefinition)
	}{
		{"revision-without-contract", func(d *api.ToolDefinition) { d.CredentialContractRevision = 1 }},
		{"invalid-contract-id", func(d *api.ToolDefinition) { d.CredentialContractID = "bad/id"; d.CredentialContractRevision = 1 }},
		{"invalid-input-name", func(d *api.ToolDefinition) {
			d.CredentialContractID = "github-pat"
			d.CredentialContractRevision = 1
			d.CredentialContractEnv = map[string]string{"BAD-NAME": "TARGET"}
		}},
		{"unknown-input-name", func(d *api.ToolDefinition) {
			d.CredentialContractID = "github-pat"
			d.CredentialContractRevision = 1
			d.CredentialContractEnv = map[string]string{"OTHER_TOKEN": "TARGET"}
		}},
		{"invalid-target-name", func(d *api.ToolDefinition) {
			d.CredentialContractID = "github-pat"
			d.CredentialContractRevision = 1
			d.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "BAD-NAME"}
		}},
		{"missing-required-mapping", func(d *api.ToolDefinition) {
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var c contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(c)
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

	// A re-minted request is dead to the owner unless the channel is told:
	// the QR invite or form link must be delivered again, not just stored.
	var notice map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&notice)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"login_url": "http://localhost:8081/telegram-auth/remint"})
	}))
	defer gw.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", gw.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

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
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "broker-regen", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_old"
	onboarding.BrokerAuthorizationURL = "https://broker.example/connect/request_old"
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	body, err := control.requiredCredentials(context.Background(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if body["broker_request_id"] != "request_new" || body["signin_url"] != "http://localhost:8081/telegram-auth/remint" ||
		mustOnboarding(t, store, onboarding.OnboardingID).BrokerAuthorizationURL != "https://broker.example/connect/request_new" {
		t.Fatalf("dead link was not replaced: %v", body)
	}
	if notice["broker_request_id"] != "request_new" {
		t.Fatalf("owner was not told about the re-minted request: %v", notice)
	}
	stored, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BrokerAttempts != 1 || stored.BrokerRequestID != "request_new" {
		t.Fatalf("unexpected regeneration state: %+v", stored)
	}
	if stored.ChannelAuthURL != "http://localhost:8081/telegram-auth/remint" {
		t.Fatalf("channel sign-in link not recorded: %+v", stored)
	}
	if len(idempotencyKeys) != 1 || idempotencyKeys[0] == stored.OnboardingID {
		t.Fatalf("retry must use a fresh idempotency key: %v", idempotencyKeys)
	}
}

// A request the broker no longer has (ledger reset, volume drift) is re-minted
// on demand — and the owner must be told, or the QR invite is never issued.
func TestCredentialBrokerMissingRequestRegeneratesAndNotifies(t *testing.T) {
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var c contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(c)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "pending", AuthorizationURL: "https://broker.example/connect/request_new"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_new":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "pending", AuthorizationURL: "https://broker.example/connect/request_new"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var notice map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&notice)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gw.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", gw.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

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
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "broker-missing", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_gone"
	onboarding.BrokerAuthorizationURL = "https://broker.example/connect/request_gone"
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.requiredCredentials(context.Background(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	if notice["broker_request_id"] != "request_new" {
		t.Fatalf("owner was not told about the re-minted request: %v", notice)
	}
}

// A still-pending request also deserves a re-notice: the QR invite it produced
// has a shorter TTL than the request itself, so asking again must re-deliver
// the link instead of leaving the owner with the dead page.
func TestCredentialBrokerPendingRequestReNotifies(t *testing.T) {
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
		if r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_live" {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_live", ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "pending", AuthorizationURL: "https://broker.example/connect/request_live"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	var notice map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&notice)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"login_url": "http://localhost:8081/telegram-auth/tok123"})
	}))
	defer gw.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", gw.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

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
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "broker-pending", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_live"
	onboarding.BrokerAuthorizationURL = "https://broker.example/connect/request_live"
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	body, err := control.requiredCredentials(context.Background(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if notice["broker_request_id"] != "request_live" {
		t.Fatalf("owner was not re-notified about the live request: %v", notice)
	}
	if body["signin_url"] != "http://localhost:8081/telegram-auth/tok123" {
		t.Fatalf("channel sign-in link missing from the tool result: %v", body["signin_url"])
	}
}

// A pending request stays usable until actual expiry: Broker refuses a
// second pending request for the same connection, so the live link is
// re-delivered instead of reminted. Truly expired requests are reminted —
// that path is covered by broker_stale_expired_test.go.
func TestCredentialBrokerPendingRequestRemainsUsable(t *testing.T) {
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
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_dying":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_dying", ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "pending", AuthorizationURL: "https://broker.example/connect/request_dying", ExpiresAt: time.Now().Add(5 * time.Minute)})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var c contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(c)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			t.Error("created a second pending request for a live connection")
			http.Error(w, `{"code":"conflict"}`, http.StatusConflict)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var notice map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&notice)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gw.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", gw.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

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
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "broker-dying", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_dying"
	onboarding.BrokerAuthorizationURL = "https://broker.example/connect/request_dying"
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	body, err := control.requiredCredentials(context.Background(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if body["broker_request_id"] != "request_dying" {
		t.Fatalf("live pending request was not reused: %v", body)
	}
	if notice["broker_request_id"] != "request_dying" {
		t.Fatalf("owner was not re-notified about the live request: %v", notice)
	}
}

// For a contract declaring QR intake the status result must steer the agent to
// the channel notice, not parrot the Broker form URL as the primary path —
// otherwise the user keeps clicking the wrong link.
func TestRequiredCredentialsQRIntakePointsToChannelNotice(t *testing.T) {
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
		if r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_live" {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_live", ContractID: "telegram-session", ContractRevision: 2, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "pending", AuthorizationURL: "https://broker.example/connect/request_live", ExpiresAt: time.Now().Add(15 * time.Minute)})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"login_url": "http://localhost:8081/telegram-auth/qr-abc"})
	}))
	defer gw.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", gw.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "telegram-session"
	definition.CredentialContractRevision = 2
	definition.CredentialContractEnv = map[string]string{
		"TELEGRAM_API_ID":           "TELEGRAM_API_ID",
		"TELEGRAM_API_HASH":         "TELEGRAM_API_HASH",
		"TELEGRAM_SESSION_STRING":   "TELEGRAM_SESSION_STRING",
		"TELEGRAM_EXPECTED_USER_ID": "TELEGRAM_EXPECTED_USER_ID",
	}
	definition.Credentials = nil
	for name := range definition.CredentialContractEnv {
		definition.Credentials = append(definition.Credentials, api.CredentialInput{Name: name, Required: true})
	}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "broker-qr", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_live"
	onboarding.BrokerAuthorizationURL = "https://broker.example/connect/request_live"
	onboarding.BrokerContractID = "telegram-session"
	onboarding.BrokerContractRevision = 2
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	body, err := control.requiredCredentials(context.Background(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if body["signin_url"] != "http://localhost:8081/telegram-auth/qr-abc" {
		t.Fatalf("channel sign-in link missing from the tool result: %v", body["signin_url"])
	}
	if _, parrotable := body["authorization_url"]; parrotable {
		t.Fatalf("form URL still exposed for the agent to parrot: %v", body["authorization_url"])
	}
	instructions, _ := body["instructions"].(string)
	if !strings.Contains(instructions, "signin_url") || strings.Contains(instructions, "open the credential URL") {
		t.Fatalf("QR intake still pushes the form as primary path: %q", instructions)
	}
}

// A stale informational error (admission-pending noise recorded before the
// channel link existed) must not suppress signin_url: with neither URL in the
// result the agent reconstructs the Broker connect URL from broker_request_id.
func TestStatusQRIntakeKeepsSignInURLWithStaleError(t *testing.T) {
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
		if r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_live" {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_live", ContractID: "telegram-session", ContractRevision: 2, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "pending", AuthorizationURL: "https://broker.example/connect/request_live", ExpiresAt: time.Now().Add(15 * time.Minute)})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"login_url": "http://localhost:8081/telegram-auth/stale-err"})
	}))
	defer gw.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", gw.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "telegram-session"
	definition.CredentialContractRevision = 2
	definition.CredentialContractEnv = map[string]string{
		"TELEGRAM_API_ID":           "TELEGRAM_API_ID",
		"TELEGRAM_API_HASH":         "TELEGRAM_API_HASH",
		"TELEGRAM_SESSION_STRING":   "TELEGRAM_SESSION_STRING",
		"TELEGRAM_EXPECTED_USER_ID": "TELEGRAM_EXPECTED_USER_ID",
	}
	definition.Credentials = nil
	for name := range definition.CredentialContractEnv {
		definition.Credentials = append(definition.Credentials, api.CredentialInput{Name: name, Required: true})
	}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "broker-qr", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_live"
	onboarding.BrokerAuthorizationURL = "https://broker.example/connect/request_live"
	onboarding.BrokerContractID = "telegram-session"
	onboarding.BrokerContractRevision = 2
	onboarding.ChannelAuthURL = "http://localhost:8081/telegram-auth/stale-err"
	onboarding.AdmissionPending = true
	onboarding.Error = "Provider login is required before this MCP can list tools; enter credentials in the protected Broker form, then verify the read probe."
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	body, err := control.status(context.Background(), auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil {
		t.Fatal(err)
	}
	if body["signin_url"] != "http://localhost:8081/telegram-auth/stale-err" {
		t.Fatalf("stale informational error suppressed the channel sign-in link: %v", body)
	}
	if _, parrotable := body["authorization_url"]; parrotable {
		t.Fatalf("form URL still exposed for the agent to parrot: %v", body["authorization_url"])
	}
	instructions, _ := body["instructions"].(string)
	if strings.Contains(instructions, "authorization_url") || strings.Contains(instructions, "fallback") {
		t.Fatalf("instructions still name the Broker form; the agent will parrot it: %q", instructions)
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
		if r.Method == http.MethodPost && r.URL.Path == "/v1/contracts" {
			var in contract.Contract
			_ = json.NewDecoder(r.Body).Decode(&in)
			in.Storage = "local"
			catalog = append(catalog, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(in)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{
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
	bound.Credentials = []api.CredentialInput{
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

func TestBindReviewedContractKeepsPinnedRevision(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	// Two revisions of one contract coexist in the Broker: rev 1 (every field
	// required) for grants already bound to it, rev 2 (optional fields) for new
	// onboarding. Alphabetically the v1 file loads first — a prepared
	// definition must still bind its pinned revision, not the older one.
	fields := func(required bool) []contract.Field {
		out := make([]contract.Field, 0, 6)
		for i, name := range []string{"jira_url", "jira_username", "jira_api_token", "confluence_url", "confluence_username", "confluence_api_token"} {
			kind := "string"
			if i%3 == 2 {
				kind = "secret"
			}
			out = append(out, contract.Field{ID: name, Kind: kind, Required: required, MaxBytes: 4096})
		}
		return out
	}
	deliveries := []contract.Delivery{
		{Type: "env", Field: "jira_url", Target: "JIRA_URL"},
		{Type: "env", Field: "jira_username", Target: "JIRA_USERNAME"},
		{Type: "env", Field: "jira_api_token", Target: "JIRA_API_TOKEN"},
		{Type: "env", Field: "confluence_url", Target: "CONFLUENCE_URL"},
		{Type: "env", Field: "confluence_username", Target: "CONFLUENCE_USERNAME"},
		{Type: "env", Field: "confluence_api_token", Target: "CONFLUENCE_API_TOKEN"},
	}
	catalog := []contract.Contract{
		{ID: "atlassian-env", Revision: 1, Title: "Atlassian", Storage: "local", Fields: fields(true), Deliveries: deliveries},
		{ID: "atlassian-env", Revision: 2, Title: "Atlassian", Storage: "local", Fields: fields(false), Deliveries: deliveries},
	}
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
	definition.Credentials = []api.CredentialInput{
		{Name: "JIRA_URL", Required: true}, {Name: "JIRA_USERNAME", Required: true}, {Name: "JIRA_API_TOKEN", Required: true},
		{Name: "CONFLUENCE_URL", Required: true}, {Name: "CONFLUENCE_USERNAME", Required: true}, {Name: "CONFLUENCE_API_TOKEN", Required: true},
	}
	definition.CredentialGroups = [][]string{{"JIRA_URL", "JIRA_USERNAME", "JIRA_API_TOKEN"}, {"CONFLUENCE_URL", "CONFLUENCE_USERNAME", "CONFLUENCE_API_TOKEN"}}
	definition.CredentialContractID = "atlassian-env"
	definition.CredentialContractRevision = 2
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if definition.CredentialContractRevision != 2 {
		t.Fatalf("pinned revision slid to an older contract: %+v", definition)
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
	old := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("conn-existing", 1), ConnectionID: "conn-existing", Revision: 1, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus, BrokerContractID: "github-pat", BrokerContractRevision: 1, BrokerGrantID: "grant_1"}
	if err := store.PutCredentialReference(old); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-existing", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: old.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	onboarding := api.Onboarding{OnboardingID: "onboard-new", Locator: "credential_2", BrokerCredentialID: "credential_2", Required: []api.CredentialHint{{Name: "GOOGLE_TOKEN"}}}
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
	if store.credentials[old.CredentialRefID].Status != api.RevokedStatus {
		t.Fatal("previous credential reference was not revoked")
	}
}

// An OAuth onboarding whose rotation already re-minted the grant for this very
// credential (first confirm, then the probe re-routed to OAuth) must keep that
// binding: re-rotating mints a new binding ID, and Broker keys token state by
// credential/context/binding, so the token the callback just wrote vanishes.
func TestMaterializeBindingKeepsMaterializedOAuthRotation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected broker call %s %s", r.Method, r.URL.Path)
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer server.Close()
	auth := aliceAuth()
	definition := statefulContainerDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}}
	definition.CredentialContractID = "workspace-file-session"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	definition.OAuth = &api.OAuthRecipe{AuthorizeURL: "https://accounts.example.com/authorize", TokenURL: "https://accounts.example.com/token", Scopes: []string{"scope"}, ClientInput: "GOOGLE_TOKEN", ClientSections: []string{"installed"}, StateTarget: "/state", TokenFile: "session.json", TokenTemplate: map[string]any{"normal": map[string]any{"access_token": "$oauth.ACCESS_TOKEN"}}, ProbeTool: "probe"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	refID := api.CredentialReferenceID("conn-existing", 2)
	bindingID := api.DeterministicBindingID(auth.PrincipalID, auth.ContextID, auth.RuntimeID, definition.DefinitionID, definition.Version, "conn-existing", refID)
	ref := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: refID, ConnectionID: "conn-existing", Revision: 2, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus, BrokerContractID: "workspace-file-session", BrokerContractRevision: 1, BrokerGrantID: "grant_2", BrokerGrantPolicy: auth.PolicyVersion, BrokerGrantBinding: bindingID}
	if err := store.PutCredentialReference(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-existing", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: refID, Revision: 2, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: filepath.Join(t.TempDir(), "unused"), KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	onboarding := api.Onboarding{OnboardingID: "onboard-oauth", Locator: "credential_1", BrokerCredentialID: "credential_1", BrokerRotateCredentialID: "credential_1", Required: []api.CredentialHint{{Name: "GOOGLE_TOKEN"}}}
	binding, err := control.materializeBinding(t.Context(), auth, onboarding, definition)
	if err != nil {
		t.Fatalf("materialized rotation re-ran: %v", err)
	}
	if binding.ToolBindingID != bindingID || binding.CredentialRefID != refID {
		t.Fatalf("binding moved off the token-bearing grant: %+v", binding)
	}
}

// A connection whose broker credential was revoked (e.g. its onboarding was
// removed after a failed confirm) must not be reused: adopting its dead grant
// would fail credential materialization at confirm. The reuse check verifies
// the credential at the broker and falls back to a fresh request.
func TestEnsureBrokerRequestSkipsRevokedCredentialReuse(t *testing.T) {
	for _, scenario := range []string{"active", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(t.TempDir(), "toolhub.private")
			if err := os.WriteFile(keyPath, private, 0600); err != nil {
				t.Fatal(err)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_1":
					_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_1", ConnectionID: "conn-existing", Status: scenario})
				case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
					var c contract.Contract
					if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
						t.Error(err)
					}
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(c)
				case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
					requests++
					_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_2", ContractID: "github-pat", ContractRevision: 1, ConnectionID: "conn-existing", Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_2"})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_2":
					_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_2", ContractID: "github-pat", ContractRevision: 1, Status: "pending"})
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
			old := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("conn-existing", 1), ConnectionID: "conn-existing", Revision: 1, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus, BrokerContractID: "github-pat", BrokerContractRevision: 1, BrokerGrantID: "grant_1"}
			if err := store.PutCredentialReference(old); err != nil {
				t.Fatal(err)
			}
			if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-existing", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: old.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
				t.Fatal(err)
			}
			cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
			control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now}
			onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "reinstall", definition, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := control.ensureBrokerRequest(t.Context(), auth, &onboarding, definition); err != nil {
				t.Fatal(err)
			}
			if scenario == "active" {
				if requests != 0 || onboarding.BrokerCredentialID != "credential_1" || onboarding.Phase != api.PhaseAwaitingConfirm {
					t.Fatalf("live credential was not reused: requests=%d onboarding=%+v", requests, onboarding)
				}
				return
			}
			if requests != 1 || onboarding.BrokerRequestID != "request_2" || onboarding.BrokerCredentialID == "credential_1" || onboarding.Phase == api.PhaseAwaitingConfirm {
				t.Fatalf("revoked credential was reused: requests=%d onboarding=%+v", requests, onboarding)
			}
		})
	}
}

// Reusing a connection across a definition upgrade keeps the credential but
// must re-mint the broker grant: the grant is scoped to the deterministic
// binding ID, which folds in the definition version. The previous code took
// the locator-equal fast path and presented the stale grant at admission —
// the broker denied Acquire (403) because the new binding ID did not match.
func TestMaterializeBindingRegrantsForNewDefinitionVersion(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var revoked, granted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/grants/grant_1":
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_1", Active: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/grants/grant_1/revoke":
			revoked = true
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			granted = true
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_2", CredentialID: "credential_1", Active: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	stale := remoteDefinition()
	stale.DefinitionID = "github-work"
	stale.Version = "1.0.0"
	stale.CredentialContractID = "github-pat"
	stale.CredentialContractRevision = 1
	stale.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	upgraded := stale
	upgraded.Version = "1.0.1"
	store := NewStore()
	if err := store.RegisterDefinition(stale); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDefinition(upgraded); err != nil {
		t.Fatal(err)
	}
	old := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("conn-existing", 1), ConnectionID: "conn-existing", Revision: 1, Backend: "credential-broker", Locator: "credential_1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus, BrokerContractID: "github-pat", BrokerContractRevision: 1, BrokerGrantID: "grant_1"}
	if err := store.PutCredentialReference(old); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-existing", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: stale.DefinitionID, CredentialRefID: old.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: stale.DefinitionID, DefinitionVersion: stale.Version, ConnectionID: "conn-existing", ConnectionRevision: 1, CredentialRefID: old.CredentialRefID, CredentialRevision: 1, PolicyVersion: auth.PolicyVersion, WorkloadClass: stale.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}

	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	onboarding := api.Onboarding{OnboardingID: "onboard-upgrade", Locator: "credential_1", BrokerCredentialID: "credential_1", Required: []api.CredentialHint{{Name: "GOOGLE_TOKEN"}}}
	next, err := control.materializeBinding(t.Context(), auth, onboarding, upgraded)
	if err != nil {
		t.Fatalf("upgrade materialization rejected: %v", err)
	}
	if !revoked || !granted {
		t.Fatalf("grant was not re-minted for the new binding: revoked=%v granted=%v", revoked, granted)
	}
	if next.DefinitionVersion != "1.0.1" || next.CredentialRevision != 2 || next.ConnectionID != "conn-existing" {
		t.Fatalf("unexpected upgraded binding: %+v", next)
	}
	if _, err := store.Resolve(auth, binding.ToolBindingID); err == nil {
		t.Fatal("previous version retained access after credential upgrade")
	}
	if _, err := store.Resolve(auth, next.ToolBindingID); err != nil {
		t.Fatalf("upgraded version was not admitted: %v", err)
	}
	if _, err := store.Definition(stale.DefinitionID, stale.Version); err != nil {
		t.Fatalf("previous version was not retained for rollback review: %v", err)
	}
	reference := store.credentials[next.CredentialRefID]
	if reference.BrokerGrantID != "grant_2" || reference.Locator != "credential_1" {
		t.Fatalf("new binding kept stale grant: %+v", reference)
	}
}

func TestEnableAfterRevokeAllowsNewConnection(t *testing.T) {
	store := NewStore()
	auth := aliceAuth()
	definition := remoteDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	old := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("conn-old", 1), ConnectionID: "conn-old", Revision: 1, Backend: credstore.BackendLocal, Locator: "old", Keys: []string{"GOOGLE_TOKEN"}, Status: api.RevokedStatus}
	if err := store.PutCredentialReference(old); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-old", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: old.CredentialRefID, Revision: 1, Status: api.RevokedStatus}); err != nil {
		t.Fatal(err)
	}
	tombstone := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: "conn-old", ConnectionRevision: 1, CredentialRefID: old.CredentialRefID, CredentialRevision: 1, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.RevokedStatus, Revision: 2}
	tombstone.ToolBindingID = api.DeterministicBindingID(tombstone.PrincipalID, tombstone.ContextID, tombstone.RuntimeID, tombstone.DefinitionID, tombstone.DefinitionVersion, tombstone.ConnectionID, tombstone.CredentialRefID)
	store.mu.Lock()
	store.bindings[tombstone.ToolBindingID] = tombstone
	store.mu.Unlock()

	next := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("conn-new", 1), ConnectionID: "conn-new", Revision: 1, Backend: credstore.BackendLocal, Locator: "new", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(next); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-new", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: next.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
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
		"GITLAB_API_URL":    "https://gitlab.example.com/api/v4",
		"CUSTOM_API_URL":    "https://self.host:8443/api",
		"SECRET_URL":        "https://secret.example.com",
		"USERINFO_URL":      "https://user:password@userinfo.example.com",
		"QUERY_URL":         "https://query.example.com/?token=x",
		"TOKEN":             "https://token.example.com",
		"INSECURE":          "http://plain.example.com",
		"BROKEN":            "://bad",
		"DB_ENDPOINT":       "db.internal.example.com:5432",
		"DB_ENDPOINT_2":     "10.0.0.9:5432",
		"BAD_ENDPOINT":      "not-a-socket",
		"IPV6_ENDPOINT":     "[::1]:5432",
		"PLAIN_ENDPOINT":    "localhost:5432",
		"USER_ENDPOINT":     "user@db.example.com:5432",
		"PASSWORD_ENDPOINT": "db.example.com:5432",
		"TOKEN_ENDPOINT":    "db.example.com:5432",
	}
	hosts := api.CredentialEgressHosts(env)
	want := map[string]bool{"gitlab.example.com": true, "self.host:8443": true, "db.internal.example.com:5432": true, "10.0.0.9:5432": true}
	if len(hosts) != len(want) {
		t.Fatalf("credentialEgressHosts=%v", hosts)
	}
	for _, host := range hosts {
		if !want[host] {
			t.Fatalf("unexpected egress host %q", host)
		}
	}
	merged := api.MergeEgressHosts([]string{"gitlab.com", "GITLAB.EXAMPLE.COM"}, hosts)
	if len(merged) != 5 {
		t.Fatalf("mergeEgressHosts did not dedupe: %v", merged)
	}
}

// An all-optional definition submits only some contract fields; the injector
// must not reject the lease just because other optional env entries stayed
// empty — required coverage is already checked by CredentialsSatisfied.
func TestBrokerRuntimeInjectorSkipsBlankOptionalInputs(t *testing.T) {
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
		switch r.URL.Path {
		case "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_1", CredentialID: "credential_1", Revision: 1})
		case "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_1", Env: map[string]string{"TOOL_TIER": "core"}})
		case "/v1/runtime/leases/lease_1/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{
		{Name: "TOOL_TIER"},
		{Name: "GOOGLE_OAUTH_CLIENT_ID"},
	}
	effective := api.EffectiveBinding{
		Definition: definition,
		Binding:    api.ToolBinding{ToolBindingID: "binding_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1"},
		Credential: &api.CredentialReference{Backend: "credential-broker", BrokerGrantID: "grant_1", Keys: []string{"TOOL_TIER", "GOOGLE_OAUTH_CLIENT_ID"}, BrokerEnv: map[string]string{"TOOL_TIER": "TOOL_TIER", "GOOGLE_OAUTH_CLIENT_ID": "GOOGLE_OAUTH_CLIENT_ID"}},
		WorkloadID: "workload_1",
	}
	injection, err := brokerRuntimeInjector(&cfg, &cfg)(t.Context(), effective)
	if err != nil {
		t.Fatalf("optional blank credential rejected: %v", err)
	}
	if injection.Environment["TOOL_TIER"] != "core" || len(injection.Environment) != 1 {
		t.Fatalf("unexpected environment: %v", injection.Environment)
	}
}
