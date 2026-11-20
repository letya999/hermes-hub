package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	_ "modernc.org/sqlite"
)

// After the SQLite cutover retires store.json, render-time migration must
// still roll the live bindings onto the rendered policy version instead of
// treating the space as unmanaged.
func TestRenderMigratesBindingsOnSQLiteStore(t *testing.T) {
	dir := t.TempDir()
	s := stack.Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080}
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
	if err := store.CloseDB(); err != nil {
		t.Fatal(err)
	}

	manifest := stack.ExpectedToolManifest(s)
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "tool-manifest.dev.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateToolhubBindings(dir, s); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT body FROM ent WHERE collection = 'binding'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	moved := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var binding api.ToolBinding
		if err := json.Unmarshal(raw, &binding); err != nil {
			t.Fatalf("binding row is not JSON: %v", err)
		}
		if binding.PolicyVersion == manifest.PolicyVersion {
			moved++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("expected exactly one binding rolled to %s, found %d", manifest.PolicyVersion, moved)
	}
}

// A managed space renders its own manifest but its live ToolHub store sits
// in the infra sibling's runtime dir — the migration must reach it through
// InfraSpaceDir or the render leaves the shared store stale.
func TestRenderMigratesManagedSpaceBindingInInfraStore(t *testing.T) {
	root := t.TempDir()
	infraDir := filepath.Join(root, "spaces", "infra")
	managedDir := filepath.Join(root, "spaces", "managed")
	for dir, user := range map[string]string{infraDir: "infra", managedDir: "managed"} {
		if err := os.MkdirAll(filepath.Join(dir, "passports"), 0o700); err != nil {
			t.Fatal(err)
		}
		infra := "false"
		if dir == infraDir {
			infra = "true"
		}
		passport := "kind: stack\nidentity: {user: " + user + ", infra: " + infra + "}\n"
		if err := os.WriteFile(filepath.Join(dir, "passports", "stack.yaml"), []byte(passport), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := stack.Settings{Schema: 1, User: "managed", Environment: "dev", Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080}
	dbPath := filepath.Join(infraDir, "runtime", "toolhub", "store.db")
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
		Schema: api.SchemaVersion, PrincipalID: "managed", ContextID: "managed", RuntimeID: "managed",
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
	if err := store.CloseDB(); err != nil {
		t.Fatal(err)
	}

	manifest := stack.ExpectedToolManifest(s)
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(managedDir, "generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managedDir, "generated", "tool-manifest.dev.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateToolhubBindings(managedDir, s); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ent WHERE collection = 'binding'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT body FROM ent WHERE collection = 'binding'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	migrated := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var binding api.ToolBinding
		if err := json.Unmarshal(raw, &binding); err != nil {
			t.Fatalf("binding row is not JSON: %v", err)
		}
		if binding.PolicyVersion == manifest.PolicyVersion {
			migrated++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if total != 1 || migrated != 1 {
		t.Fatalf("expected the managed binding in the infra store rolled to %s: rows=%d migrated=%d", manifest.PolicyVersion, total, migrated)
	}
}
