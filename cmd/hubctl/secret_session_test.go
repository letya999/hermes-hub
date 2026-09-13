package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/secrets"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type hubctlStopRecorder struct{ ids []string }

func (s *hubctlStopRecorder) Stop(id string) error {
	s.ids = append(s.ids, id)
	return nil
}

type hubctlBearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t hubctlBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyRequest.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copyRequest)
}

func TestCLISecretSetCutsOpenToolHubSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "credential.key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "runtime", "credentials", "store.enc")
	svc, err := secrets.Open(storePath, keyFile, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "hubctl-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	registry := toolhub.NewStore()
	definition := toolhub.ToolDefinition{
		Schema: toolhub.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: toolhub.RemoteMCP,
		Source:      toolhub.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:       []toolhub.ToolSpec{{Name: "search", Effect: toolhub.ReadEffect}},
		Credentials: []toolhub.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}},
		Workload:    toolhub.WorkloadPolicy{Class: toolhub.PerUser, Rationale: "OAuth account and provider session are user-owned"},
		Execution:   toolhub.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:      toolhub.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := registry.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := toolhub.CredentialReference{Schema: toolhub.SchemaVersion, CredentialRefID: toolhub.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: infos[0].Locator, Keys: []string{"GOOGLE_TOKEN"}, Status: toolhub.ActiveStatus}
	if err := registry.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := toolhub.Connection{Schema: toolhub.SchemaVersion, ConnectionID: "google-work", Owner: toolhub.OwnerRef{Type: toolhub.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: toolhub.ActiveStatus}
	if err := registry.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	binding := toolhub.ToolBinding{Schema: toolhub.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: toolhub.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := registry.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID = toolhub.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	workload, err := toolhub.NewWorkloadInstance(binding, &toolhub.OwnerRef{Type: toolhub.ContextOwner, ID: "alice"}, "", 1, time.Now().UTC(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status = toolhub.RunningStatus
	if err := registry.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	toolHubPath := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if err := registry.Save(toolHubPath); err != nil {
		t.Fatal(err)
	}
	live, err := toolhub.Load(toolHubPath)
	if err != nil {
		t.Fatal(err)
	}
	stops := &hubctlStopRecorder{}
	live.Stopper = stops
	var calls atomic.Int32
	handler, err := (&toolhub.Gateway{
		Store:   live,
		Tokens:  map[string]identity.Envelope{"01234567890123456789012345678901": auth},
		Backend: hubctlCallCounter{n: &calls},
	}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "hubctl-cut", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + toolhub.DefaultEndpointPath, HTTPClient: &http.Client{Transport: hubctlBearerTransport{base: http.DefaultTransport, token: "01234567890123456789012345678901"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	name := toolhub.ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "ok"}}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	valueFile := filepath.Join(t.TempDir(), "rotated.txt")
	if err := os.WriteFile(valueFile, []byte("rotated-hubctl-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"secret", "set", "--dir", dir, "--user", "alice", "--name", "GOOGLE_TOKEN", "--from-file", valueFile, "--key-file", keyFile, "--store", storePath, "--toolhub-store", toolHubPath}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "after"}}); err == nil {
		t.Fatal("hubctl secret set did not cut open session")
	}
	if calls.Load() != 1 {
		t.Fatalf("backend ran after hubctl rotate: %d", calls.Load())
	}
	if len(stops.ids) == 0 {
		t.Fatal("Reload did not stop the affected workload")
	}
	deleted := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "delete", "--dir", dir, "--user", "alice", "--name", "GOOGLE_TOKEN", "--key-file", keyFile, "--store", storePath, "--toolhub-store", toolHubPath})
	})
	if strings.Contains(deleted, "rotated-hubctl-secret") || !strings.Contains(deleted, "removed") {
		t.Fatalf("delete output=%q", deleted)
	}
}

type hubctlCallCounter struct{ n *atomic.Int32 }

func (c hubctlCallCounter) Call(context.Context, toolhub.EffectiveBinding, toolhub.ToolSpec, map[string]any) (toolhub.BackendResult, error) {
	c.n.Add(1)
	return toolhub.BackendResult{Text: "ok"}, nil
}
