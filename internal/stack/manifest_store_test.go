package stack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// After the SQLite cutover retires store.json, render-time migration must
// still roll the live bindings onto the rendered policy version instead of
// treating the space as unmanaged.
func TestRenderMigratesBindingsOnSQLiteStore(t *testing.T) {
	dir := t.TempDir()
	s := Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080}
	dbPath := filepath.Join(dir, "runtime", "toolhub", "store.db")
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "render-mig", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:    api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:     []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}},
		Workload:  api.WorkloadPolicy{Class: api.Shared, Rationale: "stateless shared workload"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(api.ToolBinding{
		Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice",
		DefinitionID: "render-mig", DefinitionVersion: "1.0.0", PolicyVersion: "stale-policy",
		WorkloadClass: api.Shared, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(dbPath); err != nil {
		t.Fatal(err)
	}
	before, err := store.CanonicalSnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CloseDB(); err != nil {
		t.Fatal(err)
	}

	if err := migrateToolhubBindings(dir, s); err != nil {
		t.Fatal(err)
	}
	migrated, err := toolhub.Load(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrated.CloseDB() }()
	after, err := migrated.CanonicalSnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("render skipped the binding migration on the SQLite store")
	}
}
