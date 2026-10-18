package toolhub

import (
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
	brokercontract "github.com/letya999/credential-broker/contract"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A reviewed contract that cannot cover every discovered upstream input must
// not block onboarding: the covered input stays a contract field, uncovered
// optional inputs travel as request-scoped extras on the same protected form.
func TestBrokerRequestCarriesDiscoveredOptionalExtras(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.DefinitionID = "gitlab"
	definition.Credentials = []api.CredentialInput{
		{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true},
		{Name: "GITLAB_PROJECT_ID", Required: false},
	}
	definition.CredentialContractID = "gitlab-pat"
	definition.CredentialContractRevision = 2
	definition.CredentialContractEnv = map[string]string{"GITLAB_PERSONAL_ACCESS_TOKEN": "GITLAB_PERSONAL_ACCESS_TOKEN"}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var created brokerv1.CreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/requests" {
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_1", ContractID: "gitlab-pat", ContractRevision: 2, Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_1"})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_1" {
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_1", ContractID: "gitlab-pat", ContractRevision: 2, Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_1"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: NewStore(), Broker: &cfg, Now: time.Now}
	auth := aliceAuth()
	onboarding, err := control.newOnboarding(auth, api.OnboardingSelfInstall, "gitlab-broker", definition, "https://github.com/zereight/gitlab-mcp", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	optional := map[string]bool{}
	for _, hint := range onboarding.Optional {
		optional[hint.Name] = true
	}
	if !optional["GITLAB_PROJECT_ID"] || optional["GITLAB_PERSONAL_ACCESS_TOKEN"] {
		t.Fatalf("optional hints: %+v", onboarding.Optional)
	}
	if err := control.ensureBrokerRequest(t.Context(), auth, &onboarding, definition); err != nil {
		t.Fatal(err)
	}
	if created.Extra == nil {
		t.Fatalf("no extras on broker request: %+v", created)
	}
	var field brokercontract.Field
	found := false
	for _, f := range created.Extra.Fields {
		if f.Label == "GITLAB_PROJECT_ID" {
			field, found = f, true
		}
	}
	if !found || field.Required {
		t.Fatalf("extra fields: %+v", created.Extra.Fields)
	}
	delivered := false
	for _, d := range created.Extra.Deliveries {
		if d.Type == "env" && d.Field == field.ID && d.Target == "GITLAB_PROJECT_ID" {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("extra deliveries: %+v", created.Extra.Deliveries)
	}
}

// An extra the owner filled on the broker form must reach the workload env,
// even though the reviewed contract's env map never names it.
func TestBrokerInjectionDeliversExtraEnvValues(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.DefinitionID = "gitlab"
	definition.Credentials = []api.CredentialInput{
		{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true},
		{Name: "GITLAB_PROJECT_ID", Required: false},
	}
	definition.CredentialContractID = "gitlab-pat"
	definition.CredentialContractRevision = 2
	definition.CredentialContractEnv = map[string]string{"GITLAB_PERSONAL_ACCESS_TOKEN": "GITLAB_PERSONAL_ACCESS_TOKEN"}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_1", CredentialID: "credential_1", Revision: 1})
		case "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_1", Env: map[string]string{
				"GITLAB_PERSONAL_ACCESS_TOKEN": "pat", "GITLAB_PROJECT_ID": "42",
			}})
		case "/v1/runtime/leases/lease_1/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	inject := brokerRuntimeInjector(&cfg, &cfg)
	injection, err := inject(t.Context(), api.EffectiveBinding{
		Binding:    api.ToolBinding{ToolBindingID: "binding_1", PrincipalID: "alice", ContextID: "team", RuntimeID: "hermes", PolicyVersion: "policy-1"},
		WorkloadID: "workload_1", Definition: definition,
		Credential: &api.CredentialReference{Backend: "credential-broker", BrokerGrantID: "grant_1", BrokerEnv: map[string]string{"GITLAB_PERSONAL_ACCESS_TOKEN": "GITLAB_PERSONAL_ACCESS_TOKEN"}, Keys: []string{"GITLAB_PERSONAL_ACCESS_TOKEN", "GITLAB_PROJECT_ID"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = injection.Cleanup() }()
	if injection.Environment["GITLAB_PROJECT_ID"] != "42" {
		t.Fatalf("extra env not injected: %v", injection.Environment)
	}
}

// A discovered input the bound reviewed contract cannot deliver must not kill
// prepare before the form: it is demoted to optional, stays on the form as an
// extra and the definition still validates.
func TestBindReviewedContractDemotesUndeliverableRequired(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.DefinitionID = "gitlab"
	definition.Credentials = []api.CredentialInput{
		{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true},
		{Name: "GITLAB_PROJECT_ID", Required: true},
	}
	definition.CredentialContractID = "gitlab-pat"
	definition.CredentialContractRevision = 2
	definition.CredentialContractEnv = map[string]string{"GITLAB_PERSONAL_ACCESS_TOKEN": "GITLAB_PERSONAL_ACCESS_TOKEN"}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/contracts" {
			_ = json.NewEncoder(w).Encode([]brokercontract.Contract{{
				ID: "gitlab-pat", Revision: 2, Title: "GitLab PAT",
				Fields:     []brokercontract.Field{{ID: "token", Label: "Token", Kind: "secret", Required: true, MaxBytes: 1024}},
				Deliveries: []brokercontract.Delivery{{Type: "env", Field: "token", Target: "GITLAB_PERSONAL_ACCESS_TOKEN"}},
			}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: NewStore(), Broker: &cfg, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if definition.CredentialContractID != "gitlab-pat" || definition.CredentialContractRevision != 2 {
		t.Fatalf("binding lost: %s@%d", definition.CredentialContractID, definition.CredentialContractRevision)
	}
	for _, input := range definition.Credentials {
		if input.Name == "GITLAB_PROJECT_ID" && input.Required {
			t.Fatalf("undeliverable input stayed required: %+v", definition.Credentials)
		}
	}
	if err := definition.Validate(); err != nil {
		t.Fatalf("demoted definition must validate: %v", err)
	}
}

// Discovered optional inputs must never shadow the bound contract: an input
// whose derived field id collides with a contract field is renamed, and an
// input the contract already delivers is dropped from extras — otherwise the
// broker rejects the whole request with invalid_request (observed live:
// gitlab-pat@2 request 400 on extras TOKEN→token and GITLAB_API_URL).
func TestBrokerExtrasAvoidBoundContractCollisions(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.DefinitionID = "gitlab"
	definition.Credentials = []api.CredentialInput{
		{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true},
		{Name: "GITLAB_TOKEN", Required: true},
		{Name: "TOKEN"},
		{Name: "GITLAB_API_URL"},
		{Name: "GITLAB_ENDPOINT"},
		{Name: "GITLAB_PROJECT_ID"},
	}
	definition.CredentialContractID = "gitlab-pat"
	definition.CredentialContractRevision = 2
	definition.CredentialContractEnv = map[string]string{
		"GITLAB_PERSONAL_ACCESS_TOKEN": "GITLAB_PERSONAL_ACCESS_TOKEN",
		"GITLAB_TOKEN":                 "GITLAB_TOKEN",
	}
	extra := brokerExtras(definition)
	if extra == nil {
		t.Fatal("no extras")
	}
	ids := map[string]string{}
	for _, f := range extra.Fields {
		if f.ID == "token" || f.ID == "api_url" || f.ID == "endpoint" {
			t.Fatalf("extra shadows contract field %q: %+v", f.ID, extra.Fields)
		}
		ids[f.Label] = f.ID
	}
	if ids["TOKEN"] == "" {
		t.Fatalf("TOKEN input lost instead of renamed: %+v", extra.Fields)
	}
	for _, d := range extra.Deliveries {
		if d.Target == "GITLAB_API_URL" || d.Target == "GITLAB_ENDPOINT" || d.Target == "GITLAB_TOKEN" {
			t.Fatalf("extra delivery shadows contract target %q: %+v", d.Target, extra.Deliveries)
		}
	}
	if ids["GITLAB_API_URL"] != "" || ids["GITLAB_ENDPOINT"] != "" {
		t.Fatalf("contract-delivered inputs duplicated as extras: %+v", extra.Fields)
	}
	if ids["GITLAB_PROJECT_ID"] == "" {
		t.Fatalf("uncovered optional input missing from extras: %+v", extra.Fields)
	}
}

// A prepared definition pinning a reviewed contract that is missing from the
// broker catalog must register the reviewed contract itself — never a
// contract synthesized from noisy discovered metadata (README/.env.example
// extras would overflow the 32-field contract bound and lose the reviewed
// delivery mapping).
func TestBindReviewedContractRegistersPinnedPreparedContract(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.DefinitionID = "gitlab"
	definition.Credentials = []api.CredentialInput{
		{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true},
		{Name: "GITLAB_API_URL"},
		{Name: "GITLAB_ENDPOINT"},
		{Name: "GITLAB_PROJECT_ID"},
	}
	definition.CredentialContractID = "gitlab-pat"
	definition.CredentialContractRevision = 2
	definition.CredentialContractEnv = map[string]string{
		"GITLAB_PERSONAL_ACCESS_TOKEN": "GITLAB_PERSONAL_ACCESS_TOKEN",
		"GITLAB_API_URL":               "GITLAB_API_URL",
		"GITLAB_ENDPOINT":              "GITLAB_ENDPOINT",
	}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered brokercontract.Contract
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode([]brokercontract.Contract{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			if err := json.NewDecoder(r.Body).Decode(&registered); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(registered)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: NewStore(), Broker: &cfg, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if registered.ID != "gitlab-pat" || registered.Revision != 2 {
		t.Fatalf("registered %+v, want reviewed gitlab-pat@2", registered)
	}
	if len(registered.Fields) != 3 || len(registered.Deliveries) != 4 {
		t.Fatalf("synthesized contract was registered instead of the reviewed one: %+v", registered)
	}
	if definition.CredentialContractEnv["GITLAB_PERSONAL_ACCESS_TOKEN"] == "" {
		t.Fatalf("binding env lost: %v", definition.CredentialContractEnv)
	}
}
