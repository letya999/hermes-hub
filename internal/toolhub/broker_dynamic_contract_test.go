package toolhub

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A definition whose credentials no catalog contract covers must get a
// dynamically registered contract synthesized from its declared inputs, not a
// dead-end "no reviewed contract" error. The form the user fills in is the
// consent point; the contract only declares collection and delivery.
func TestBindReviewedContractSynthesizesDynamicContract(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered []contract.Contract
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode(registered)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var in contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			in.Storage = "local"
			registered = append(registered, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(in)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.DefinitionID = "notion-mcp-server"
	definition.Credentials = []api.CredentialInput{
		{Name: "NOTION_TOKEN", Required: true},
		{Name: "NOTION_EXTRA", Required: false},
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(definition.CredentialContractID, "dyn-") || definition.CredentialContractRevision != 1 {
		t.Fatalf("dynamic contract not bound: %+v", definition)
	}
	if len(registered) != 1 {
		t.Fatalf("expected one registered contract, got %d", len(registered))
	}
	got := registered[0]
	if got.ID != definition.CredentialContractID || got.Revision != 1 {
		t.Fatalf("registered contract mismatch: %+v", got)
	}
	if len(got.Fields) != 2 || len(got.Deliveries) != 2 {
		t.Fatalf("contract does not cover declared inputs: %+v", got)
	}
	for _, d := range got.Deliveries {
		if d.Type != "env" || (d.Target != "NOTION_TOKEN" && d.Target != "NOTION_EXTRA") {
			t.Fatalf("unexpected delivery: %+v", d)
		}
	}
	want := map[string]string{"NOTION_TOKEN": "NOTION_TOKEN", "NOTION_EXTRA": "NOTION_EXTRA"}
	if len(definition.CredentialContractEnv) != 2 || definition.CredentialContractEnv["NOTION_TOKEN"] != want["NOTION_TOKEN"] || definition.CredentialContractEnv["NOTION_EXTRA"] != want["NOTION_EXTRA"] {
		t.Fatalf("env mapping not bound: %v", definition.CredentialContractEnv)
	}
}

// Credentials that are all optional (the google_workspace case) still need a
// registered contract: the broker path requires one whenever the definition
// declares credentials, even when none is individually required.
func TestBindReviewedContractSynthesizesAllOptional(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered []contract.Contract
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode(registered)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var in contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			in.Storage = "local"
			registered = append(registered, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(in)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.DefinitionID = "google-workspace"
	definition.Credentials = []api.CredentialInput{
		{Name: "GOOGLE_OAUTH_CLIENT_ID"},
		{Name: "GOOGLE_OAUTH_CLIENT_SECRET"},
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 {
		t.Fatalf("all-optional credentials still need a contract, got %d registrations", len(registered))
	}
	for _, field := range registered[0].Fields {
		if field.Required {
			t.Fatalf("optional input must stay optional in the contract: %+v", field)
		}
	}
	if !strings.HasPrefix(definition.CredentialContractID, "dyn-") {
		t.Fatalf("dynamic contract not bound: %+v", definition)
	}
}

// A definition pinning a contract that the broker catalog lacks (like the
// notion-token gap found in live replay) registers the synthesized contract
// under the pinned id and revision instead of dead-ending.
func TestBindReviewedContractHealsPinnedContractGap(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered []contract.Contract
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode(registered)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var in contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			in.Storage = "local"
			registered = append(registered, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(in)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.DefinitionID = "notion-mcp-server"
	definition.Credentials = []api.CredentialInput{{Name: "NOTION_TOKEN", Required: true}}
	definition.CredentialContractID = "notion-token"
	definition.CredentialContractRevision = 1
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 || registered[0].ID != "notion-token" || registered[0].Revision != 1 {
		t.Fatalf("pinned contract gap not healed: %+v", registered)
	}
	if definition.CredentialContractID != "notion-token" || definition.CredentialContractRevision != 1 {
		t.Fatalf("binding lost the pinned contract: %+v", definition)
	}
	if definition.CredentialContractEnv["NOTION_TOKEN"] != "NOTION_TOKEN" {
		t.Fatalf("env mapping not bound: %v", definition.CredentialContractEnv)
	}
}

// Bounded-cli credentials are owner-declared env values served by the local
// protected form, not a broker contract. An unpinned bounded-cli definition
// must not be silently moved onto the broker path by dynamic registration.
func TestBindReviewedContractSkipsBoundedCLI(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode([]contract.Contract{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			registered++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(contract.Contract{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.Transport = api.BoundedCLI
	definition.Credentials = []api.CredentialInput{{Name: "LOCAL_TOKEN", Required: true}}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if registered != 0 || definition.CredentialContractID != "" {
		t.Fatalf("bounded-cli must keep the local form path: registered=%d contract=%q", registered, definition.CredentialContractID)
	}
}

// A credential declared for header delivery must not be silently collected
// into an env contract: the value would land in the workload environment,
// not on the request header the remote server expects.
func TestBindReviewedContractSkipsUnsupportedDelivery(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode([]contract.Contract{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			registered++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(contract.Contract{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{
		{Name: "AUTH_HEADER", Required: true, Delivery: "http_header", Target: "Authorization", Prefix: "Bearer "},
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if registered != 0 || definition.CredentialContractID != "" {
		t.Fatalf("header delivery must not synthesize an env contract: registered=%d contract=%q", registered, definition.CredentialContractID)
	}
}

// Credential groups are either/or alternatives: the synthesized contract must
// mark grouped members optional, or the form would force the user to fill
// every alternative instead of one of them.
func TestBindReviewedContractGroupedInputsStayOptional(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered []contract.Contract
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode(registered)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var in contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			in.Storage = "local"
			registered = append(registered, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(in)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{
		{Name: "OAUTH_TOKEN", Required: true},
		{Name: "API_KEY", Required: true},
	}
	definition.CredentialGroups = [][]string{{"OAUTH_TOKEN", "API_KEY"}}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 {
		t.Fatalf("no contract registered: %d", len(registered))
	}
	for _, field := range registered[0].Fields {
		if field.Required {
			t.Fatalf("grouped alternative must not be required in the contract: %+v", field)
		}
	}
}

// Dynamic contracts are per-user collect-and-deliver only: a shared-workload
// definition still requires an operator-reviewed catalog contract with
// allow_shared, never a synthesized one.
func TestBindReviewedContractSkipsSharedWorkload(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode([]contract.Contract{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			registered++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(contract.Contract{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.Workload.Class = api.Shared
	definition.Credentials = []api.CredentialInput{{Name: "SHARED_TOKEN", Required: true}}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if registered != 0 || definition.CredentialContractID != "" {
		t.Fatalf("shared workload must not synthesize a contract: registered=%d contract=%q", registered, definition.CredentialContractID)
	}
}

// A contract registration rejected with a digest conflict must surface the
// contract identity, not a bare 409.
func TestBindReviewedContractReportsRegisterConflict(t *testing.T) {
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
			_ = json.NewEncoder(w).Encode([]contract.Contract{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "conflict"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "MY_TOKEN", Required: true}}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	err = control.bindReviewedContract(t.Context(), aliceAuth(), &definition)
	if err == nil || !strings.Contains(err.Error(), "dyn-") {
		t.Fatalf("conflict must name the rejected contract: %v", err)
	}
}

// Per-request credentials are collected at call time, never through the
// onboarding form: a definition whose only credentials are per-request must
// not get a contract at all, and mixed definitions exclude them from fields.
func TestBindReviewedContractExcludesPerRequestInputs(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered []contract.Contract
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode(registered)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var in contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			in.Storage = "local"
			registered = append(registered, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(in)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	perRequestOnly := remoteDefinition()
	perRequestOnly.Workload.Class = api.Shared
	perRequestOnly.Credentials = []api.CredentialInput{{Name: "CALLER_TOKEN", Required: true, PerRequest: true}}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &perRequestOnly); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 0 || perRequestOnly.CredentialContractID != "" {
		t.Fatalf("per-request-only credentials must not get a contract: %+v", perRequestOnly.CredentialContractID)
	}

	mixed := remoteDefinition()
	mixed.Credentials = []api.CredentialInput{
		{Name: "STATIC_TOKEN", Required: true},
		{Name: "CALLER_TOKEN", PerRequest: true, Delivery: "http_header"},
	}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &mixed); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 || len(registered[0].Fields) != 1 || registered[0].Fields[0].Label != "STATIC_TOKEN" {
		t.Fatalf("per-request input leaked into the contract: %+v", registered)
	}
	if _, leaked := mixed.CredentialContractEnv["CALLER_TOKEN"]; leaked {
		t.Fatalf("per-request input leaked into env mapping: %v", mixed.CredentialContractEnv)
	}
}

// Credential names that normalize to the same field id ("FOO-BAR" and
// "FOO_BAR") must not collide into an invalid contract.
func TestBindReviewedContractDisambiguatesFieldIDs(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered []contract.Contract
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode(registered)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var in contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			in.Storage = "local"
			registered = append(registered, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(in)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{
		{Name: "FOO-BAR", Required: true},
		{Name: "FOO_BAR"},
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 || len(registered[0].Fields) != 2 || registered[0].Fields[0].ID == registered[0].Fields[1].ID {
		t.Fatalf("colliding field ids must be disambiguated: %+v", registered)
	}
}
