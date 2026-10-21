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

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A definition whose credentials no catalog contract covers must get a
// dynamically registered contract synthesized from its declared inputs, not a
// dead-end "no reviewed contract" error. The form the user fills in is the
// consent point; the contract only declares collection and delivery.
func TestBindReviewedContractSynthesizesDynamicContract(t *testing.T) {
	testBindReviewedContractSynthesis(t, false)
}

func TestOAuthContractDoesNotReuseUnrelatedCollection(t *testing.T) {
	testBindReviewedContractSynthesis(t, true)
}

func testBindReviewedContractSynthesis(t *testing.T, withOAuth bool) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var registered []contract.Contract
	if withOAuth {
		registered = append(registered, contract.Contract{ID: "other-collection", Revision: 1, Title: "Other collection", Storage: "local", Fields: []contract.Field{{ID: "token", Label: "Token", Kind: "secret", Required: true, MaxBytes: 65536}}, Deliveries: []contract.Delivery{{Type: "env", Field: "token", Target: "NOTION_TOKEN"}}})
	}
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
	if withOAuth {
		definition = statefulContainerDefinition()
	}
	definition.DefinitionID = "notion-mcp-server"
	definition.Credentials = []api.CredentialInput{
		{Name: "NOTION_TOKEN", Required: true},
		{Name: "NOTION_EXTRA", Required: false},
	}
	if withOAuth {
		definition.Credentials[0].Delivery = "file"
		definition.Credentials[0].Target = "/run/client.json"
		definition.OAuth = &api.OAuthRecipe{ClientInput: "NOTION_TOKEN", StateTarget: "/session", TokenFile: "session.json"}
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(definition.CredentialContractID, "dyn-") || definition.CredentialContractRevision != 1 {
		t.Fatalf("dynamic contract not bound: %+v", definition)
	}
	wantRegistered := 1
	if withOAuth {
		wantRegistered = 2
	}
	if len(registered) != wantRegistered {
		t.Fatalf("expected one registered contract, got %d", len(registered))
	}
	got := registered[len(registered)-1]
	if got.ID != definition.CredentialContractID || got.Revision != 1 {
		t.Fatalf("registered contract mismatch: %+v", got)
	}
	if len(got.Fields) != 2 || len(got.Deliveries) != 2 {
		t.Fatalf("contract does not cover declared inputs: %+v", got)
	}
	for _, d := range got.Deliveries {
		if withOAuth && d.EnvName == "NOTION_TOKEN" {
			if d.Type != "file" || d.Target != "/run/client.json" {
				t.Fatal("OAuth client file lost")
			}
			continue
		}
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

// The uncovered-credentials error must name only inputs a contract could
// actually deliver: a header credential is collected by the remote client
// path and must never appear in the operator instruction.
func TestRequiredCredentialNamesExcludesLocalPlaneInputs(t *testing.T) {
	definition := api.ToolDefinition{Credentials: []api.CredentialInput{
		{Name: "AUTH_HEADER", Required: true, Delivery: "http_header", Target: "Authorization", Prefix: "Bearer "},
		{Name: "CELL_KEY", Required: true, Delivery: "brokered", Target: "api.example"},
		{Name: "API_KEY", Required: true},
		{Name: "PER_CALL", Required: true, PerRequest: true},
	}}
	got := requiredCredentialNames(definition)
	if len(got) != 1 || got[0] != "API_KEY" {
		t.Fatalf("non-contract credentials leaked into the error names: %v", got)
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

// A shared definition without the Sharable flag cannot have static
// credentials at all — it is definition-invalid, so synthesis must not
// rescue it with a contract.
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
	definition.Workload.Sharable = false
	definition.Credentials = []api.CredentialInput{{Name: "SHARED_TOKEN", Required: true}}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if registered != 0 || definition.CredentialContractID != "" {
		t.Fatalf("non-sharable shared workload must not synthesize a contract: registered=%d contract=%q", registered, definition.CredentialContractID)
	}
}

// A Sharable shared definition keeps one policy-owned credential collected
// through the same protected form; the synthesized contract must carry
// allow_shared so the shared workload's binding can reference it.
func TestBindReviewedContractSynthesizesAllowShared(t *testing.T) {
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
	definition.Workload.Class = api.Shared
	definition.Workload.Sharable = true
	definition.Credentials = []api.CredentialInput{{Name: "SHARED_TOKEN", Required: true}}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 || definition.CredentialContractID == "" {
		t.Fatalf("sharable definition must synthesize a contract: %+v", definition)
	}
	if !registered[0].AllowShared {
		t.Fatalf("shared definition's contract must allow shared: %+v", registered[0])
	}
}

// A container file credential is collected through the same broker form and
// materialized as a read-only mount plus an env var carrying the path — the
// contract needs a file delivery, not an env one.
func TestBindReviewedContractSynthesizesFileDelivery(t *testing.T) {
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
	definition.Transport = api.ContainerMCP
	definition.Credentials = []api.CredentialInput{
		{Name: "GOOGLE_APPLICATION_CREDENTIALS", Required: true, Delivery: "file", Target: "/run/secrets/sa.json"},
		{Name: "GCLOUD_PROJECT", Required: false},
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 {
		t.Fatalf("no contract registered: %d", len(registered))
	}
	var fileDelivery *contract.Delivery
	for i := range registered[0].Deliveries {
		d := &registered[0].Deliveries[i]
		if d.Type == "file" {
			fileDelivery = d
		}
		if d.Type == "env" && d.Target != "GCLOUD_PROJECT" {
			t.Fatalf("unexpected env delivery: %+v", d)
		}
	}
	if fileDelivery == nil || fileDelivery.Target != "/run/secrets/sa.json" || fileDelivery.EnvName != "GOOGLE_APPLICATION_CREDENTIALS" {
		t.Fatalf("file credential not mapped to file delivery: %+v", registered[0].Deliveries)
	}
	if definition.CredentialContractEnv["GOOGLE_APPLICATION_CREDENTIALS"] != "GOOGLE_APPLICATION_CREDENTIALS" {
		t.Fatalf("file credential env path not bound: %v", definition.CredentialContractEnv)
	}
}

// A "json" credential input is a JSON value collected through the form's
// textarea and delivered to the environment: the contract field must carry
// the "json" kind so the broker validates the payload, with an env delivery.
func TestBindReviewedContractSynthesizesJSONField(t *testing.T) {
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
	definition.Transport = api.ContainerMCP
	definition.Credentials = []api.CredentialInput{
		{Name: "SERVICE_ACCOUNT_JSON", Required: true, Delivery: "json"},
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 {
		t.Fatalf("no contract registered: %d", len(registered))
	}
	got := registered[0]
	if len(got.Fields) != 1 || got.Fields[0].Kind != "json" {
		t.Fatalf("json credential not mapped to json field kind: %+v", got.Fields)
	}
	if len(got.Deliveries) != 1 || got.Deliveries[0].Type != "env" || got.Deliveries[0].Target != "SERVICE_ACCOUNT_JSON" {
		t.Fatalf("json credential must deliver to env: %+v", got.Deliveries)
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

// Regression for the live replay: a definition whose credentials are all
// optional (google_workspace) binds a dynamic contract, then confirm builds a
// broker credential reference. Required hints are empty, so the reference keys
// must come from the contract env map — otherwise validate rejects an empty
// key set and confirm dead-ends with "credential reference metadata".
func TestBrokerConfirmAllOptionalCredentials(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	request := brokerv1.Request{ID: "request_1", ContractID: "dyn-abc", ContractRevision: 1, ConnectionID: "connection_1", OnboardingID: "onboard_1", Status: "ready", CredentialID: "credential_1", Revision: 1, AuthorizationURL: "http://127.0.0.1/connect/request_1"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			_ = json.NewEncoder(w).Encode(request)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_1":
			_ = json.NewEncoder(w).Encode(request)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_1":
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_1", ConnectionID: "connection_1", ConsumerID: "google-work-optional", Status: "active"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/credential_1/grants":
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_1", CredentialID: "credential_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", BindingID: "binding_1", WorkloadID: "work_1", Execution: "dedicated", ContractID: "dyn-abc", ContractRevision: 1, PolicyVersion: "policy-1", Active: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.DefinitionID = "google-work-optional"
	definition.Credentials = []api.CredentialInput{
		{Name: "GOOGLE_OAUTH_CLIENT_ID"},
		{Name: "GOOGLE_OAUTH_CLIENT_SECRET"},
	}
	definition.CredentialContractID = "dyn-abc"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{
		"GOOGLE_OAUTH_CLIENT_ID":     "GOOGLE_OAUTH_CLIENT_ID",
		"GOOGLE_OAUTH_CLIENT_SECRET": "GOOGLE_OAUTH_CLIENT_SECRET",
	}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "opt-confirm", definition, "", "")
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
	if stored.Phase != api.PhaseAwaitingConfirm {
		t.Fatalf("unexpected phase: %+v", stored)
	}
	if _, err := control.confirm(t.Context(), auth, map[string]any{"onboarding_id": stored.OnboardingID, "nonce": stored.ConfirmationNonce}); err != nil {
		t.Fatalf("confirm rejected all-optional credential reference: %v", err)
	}
	effective, err := store.Resolve(auth, stored.OnboardingID)
	if err == nil {
		_ = effective
	}
	stored, _ = store.OnboardingFor(auth, stored.OnboardingID)
	if stored.BindingID == "" {
		t.Fatal("confirm did not produce a binding")
	}
}
