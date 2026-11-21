package hermes_test

import (
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	hermes "github.com/letya999/hermes-hub/services/harness-runtime/internal/hermes"
	"net/http"
	"strings"
	"testing"
	"time"
)

func init() {
	hermes.TestLineageEndpoint = func(t *testing.T, auth identity.Envelope, backend api.ToolBackend) http.Handler {
		t.Helper()
		store, known := lineageStore(t)
		if !known.Equal(auth) {
			t.Fatal("lineage identity mismatch")
		}
		handler, err := toolhub.NewEndpointHandler(toolhub.EndpointConfig{Token: strings.Repeat("s", 32), Auth: auth, Backend: backend}, store)
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
}

// lineageStore mirrors the toolhub fixture through its exported API: a remote
// definition plus its credential, connection, definition grant and binding —
// the smallest registry that projects one tool for the owner envelope.
func lineageStore(t *testing.T) (*toolhub.Store, identity.Envelope) {
	t.Helper()
	store := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:      api.DefinitionSource{URL: "https://example.com/mcp", TLSMode: "required"},
		Tools:       []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}},
		Credentials: []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}},
		Workload:    api.WorkloadPolicy{Class: api.PerUser, Rationale: "user-owned provider session"},
		Execution:   api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.com"}},
		Health:      api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	grant := api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)
	if err := api.Confirm(&grant, "", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(grant); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	return store, auth
}
