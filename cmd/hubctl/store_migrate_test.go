package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The store-migrate command turns an existing store.json into store.db via
// the verified substrate migration; it refuses without --apply and never
// writes to the source.
func TestStoreMigrateCLI(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "state", "store.json")

	seed := toolhub.NewStore()
	definition := api.ToolDefinition{Schema: api.SchemaVersion, DefinitionID: "def", Version: "1.0.0", Transport: api.RemoteMCP, Source: api.DefinitionSource{URL: "https://example.invalid/mcp", TLSMode: "required"}, Workload: api.WorkloadPolicy{Class: api.PerUser, Rationale: "per-user remote tool"}, Execution: api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.invalid"}}, Health: api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5}, Tools: []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}}
	if err := seed.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := seed.Save(from); err != nil {
		t.Fatal(err)
	}

	if err := run(t.Context(), []string{"store-migrate", "--toolhub-store", from}); err == nil {
		t.Fatal("store-migrate without --apply must refuse")
	}
	to := filepath.Join(dir, "state", "store.db")
	if _, err := os.Lstat(to); !os.IsNotExist(err) {
		t.Fatal("dry-run created the destination")
	}
	if err := run(t.Context(), []string{"store-migrate", "--toolhub-store", from, "--apply"}); err != nil {
		t.Fatalf("apply via run(): %v", err)
	}
	migrated, err := toolhub.Load(to)
	if err != nil {
		t.Fatal(err)
	}
	migrated.CloseDB()
	if _, err := os.Stat(to + ".manifest.json"); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := os.Stat(from + ".substrate-backup"); err != nil {
		t.Fatalf("rollback copy missing: %v", err)
	}
	if err := run(t.Context(), []string{"store-activate", "--toolhub-store", from, "--to", to}); err == nil {
		t.Fatal("store-activate without --apply must refuse")
	}
	if err := run(t.Context(), []string{"store-activate", "--toolhub-store", from, "--to", to, "--apply"}); err != nil {
		t.Fatalf("activate via run(): %v", err)
	}
	if _, err := os.Lstat(from); !os.IsNotExist(err) {
		t.Fatal("legacy store still live after activation")
	}
	if _, err := os.Stat(from + ".legacy"); err != nil {
		t.Fatalf("retired copy missing: %v", err)
	}
}

// The same commands take --spool for the communication spool: import lands
// in <spool>/spool.db, activation flips records.activated and retires files.
func TestStoreMigrateCLIForSpool(t *testing.T) {
	spool := t.TempDir()
	pending := filepath.Join(spool, "pending")
	if err := os.MkdirAll(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, "job-c-1.json"), []byte(`{"id":"job-c-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"store-migrate", "--spool", spool}); err == nil {
		t.Fatal("store-migrate without --apply must refuse")
	}
	if _, err := os.Lstat(filepath.Join(spool, "spool.db")); !os.IsNotExist(err) {
		t.Fatal("dry-run created spool.db")
	}
	if err := run(t.Context(), []string{"store-migrate", "--spool", spool, "--apply"}); err != nil {
		t.Fatalf("spool import: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(spool, "spool.db.manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if err := run(t.Context(), []string{"store-activate", "--spool", spool}); err == nil {
		t.Fatal("store-activate without --apply must refuse")
	}
	if err := run(t.Context(), []string{"store-activate", "--spool", spool, "--apply"}); err != nil {
		t.Fatalf("spool activation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(spool, "records.activated")); err != nil {
		t.Fatal("activation marker missing")
	}
	if _, err := os.Lstat(filepath.Join(spool, "substrate-backup", "pending", "job-c-1.json")); err != nil {
		t.Fatal("retired record missing")
	}
}
