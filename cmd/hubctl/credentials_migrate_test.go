package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// CHG-0133 S6.5: `hubctl credentials migrate` walks the toolhub store, mints
// a broker request per legacy local reference bound to a reviewed contract,
// and prints the per-entry report. The owner submits through the link; a
// later pass anchors the reference.
func TestCredentialsMigrateCLI(t *testing.T) {
	ctx := context.Background()
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
		if r.Method == http.MethodGet && r.URL.Path == "/v1/requests/req-1" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "req-1", "status": "ready", "credential_id": "cred-broker-1", "contract_id": "google-token"})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/requests" {
			http.NotFound(w, r)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["contract_id"] != "google-token" || in["connection_id"] != "google-work" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "req-1", "status": "pending", "contract_id": "google-token", "authorization_url": "https://broker.example/connect/req-1"})
	}))
	defer server.Close()
	t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_URL", server.URL)
	t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_KEY_FILE", keyPath)
	t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_KEY_ID", "toolhub")
	t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_ISSUER", "hermes-toolhub")

	storePath := filepath.Join(t.TempDir(), "registry.json")
	registry := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:               api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:                []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}},
		Credentials:          []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}},
		Workload:             api.WorkloadPolicy{Class: api.PerUser, Rationale: "user-owned"},
		Execution:            api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:               api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
		CredentialContractID: "google-token", CredentialContractRevision: 1,
		CredentialContractEnv: map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"},
	}
	if err := registry.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := registry.PutCredentialReference(reference); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: "google-work", CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(storePath); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return runCredentials(ctx, []string{"migrate", "--toolhub-store", storePath})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"request_pending"`) || !strings.Contains(out, "req-1") || strings.Contains(out, "local://alice") {
		t.Fatalf("migrate output=%q", out)
	}
	reloaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if conns := reloaded.Connections(); len(conns) != 1 || conns[0].CredentialRefID != reference.CredentialRefID {
		t.Fatalf("pending migrate must keep the legacy reference: %+v", conns)
	}
	// Second pass: the broker reports the request ready, so the reference
	// re-anchors onto the minted credential while keeping the legacy locator.
	out, err = captureStdout(t, func() error {
		return runCredentials(ctx, []string{"migrate", "--toolhub-store", storePath})
	})
	if err != nil || !strings.Contains(out, `"anchored"`) {
		t.Fatalf("second pass output=%q err=%v", out, err)
	}
	// --rollback restores the legacy locator for a one-release dual-read
	// window: calls resolve through the local reader again.
	out, err = captureStdout(t, func() error {
		return runCredentials(ctx, []string{"migrate", "--rollback", "--toolhub-store", storePath})
	})
	if err != nil || !strings.Contains(out, `"rolled_back"`) {
		t.Fatalf("rollback output=%q err=%v", out, err)
	}
}

// Rollback must work when the broker is unreachable: it only flips local
// records back, so no control client is needed.
func TestCredentialsRollbackWithoutBrokerEnv(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_URL", "")
	storePath := filepath.Join(t.TempDir(), "registry.json")
	registry := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:      api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:       []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}},
		Credentials: []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}},
		Workload:    api.WorkloadPolicy{Class: api.PerUser, Rationale: "user-owned"},
		Execution:   api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:      api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := registry.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	legacy := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.RevokedStatus}
	anchored := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 2), ConnectionID: "google-work", Revision: 2, Backend: "credential-broker", Locator: "cred-broker-1", Keys: []string{"GOOGLE_TOKEN"}, BrokerContractID: "google-token", BrokerContractRevision: 1, BrokerEnv: map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}, LegacyLocator: "local://alice/google/1", Status: api.ActiveStatus}
	if err := registry.PutCredentialReference(legacy); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutCredentialReference(anchored); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: "google-work", CredentialRefID: anchored.CredentialRefID, Revision: 2, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(storePath); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return runCredentials(ctx, []string{"migrate", "--rollback", "--toolhub-store", storePath})
	})
	if err != nil || !strings.Contains(out, `"rolled_back"`) {
		t.Fatalf("rollback without broker env: %q %v", out, err)
	}
}

// The top-level `credentials` verb dispatches into runCredentials.
func TestCredentialsVerbDispatch(t *testing.T) {
	if err := run(context.Background(), []string{"credentials"}); err == nil || !strings.Contains(err.Error(), "credentials requires migrate") {
		t.Fatalf("dispatch: %v", err)
	}
}
