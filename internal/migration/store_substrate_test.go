package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The substrate migration copies store.json into store.db without touching
// the source, verifies the imported rows byte-for-byte against a second
// load, and records a manifest that links the two stores.
func TestMigrateToolHubStoreToSQLite(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "state", "store.json")
	to := filepath.Join(dir, "state", "store.db")

	seed := toolhub.NewStore()
	definition := api.ToolDefinition{Schema: api.SchemaVersion, DefinitionID: "def", Version: "1.0.0", Transport: api.RemoteMCP, Source: api.DefinitionSource{URL: "https://example.invalid/mcp", TLSMode: "required"}, Workload: api.WorkloadPolicy{Class: api.PerUser, Rationale: "per-user remote tool"}, Execution: api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.invalid"}}, Health: api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5}, Tools: []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}}
	if err := seed.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := seed.Save(from); err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}

	report, err := MigrateStoreSubstrate(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if report.Collections == 0 {
		t.Fatal("no collections imported")
	}
	sourceAfter, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(sourceBefore) != sha256.Sum256(sourceAfter) {
		t.Fatal("source store modified")
	}
	if _, err := os.Stat(report.ManifestPath); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := os.Stat(report.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	migrated, err := toolhub.Load(to)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.CloseDB()
	if err := migrated.Save(to); err != nil {
		t.Fatal(err)
	}
	sourceDigest := sha256.Sum256(sourceBefore)
	canonicalDigest, err := migrated.CanonicalSnapshotDigest()
	if err != nil || report.SourceSHA256 != hex.EncodeToString(sourceDigest[:]) || canonicalDigest != report.SourceSHA256 || report.DBDigest == "" {
		t.Fatalf("migrated digest diverged: source=%s db=%s", report.SourceSHA256, report.DBDigest)
	}

	if _, err := MigrateStoreSubstrate(from, to); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("re-migration over initialized DB must refuse: %v", err)
	}
}

// Activation retires the legacy file: store.json moves to .legacy under the
// verified manifest, the DB stays live and a writer still pointed at the
// old path meets the initialized-missing fence instead of forking state.
func TestActivateToolHubStoreSubstrate(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "state", "store.json")
	to := filepath.Join(dir, "state", "store.db")

	seed := toolhub.NewStore()
	definition := api.ToolDefinition{Schema: api.SchemaVersion, DefinitionID: "def", Version: "1.0.0", Transport: api.RemoteMCP, Source: api.DefinitionSource{URL: "https://example.invalid/mcp", TLSMode: "required"}, Workload: api.WorkloadPolicy{Class: api.PerUser, Rationale: "per-user remote tool"}, Execution: api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.invalid"}}, Health: api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5}, Tools: []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}}
	if err := seed.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := seed.Save(from); err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ActivateStoreSubstrate(from, to); err == nil {
		t.Fatal("activation without a verified migration must refuse")
	}
	if _, err := MigrateStoreSubstrate(from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivateStoreSubstrate(from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(from); !os.IsNotExist(err) {
		t.Fatal("legacy store still live after activation")
	}
	legacy, err := os.ReadFile(from + ".legacy")
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(legacy) != sha256.Sum256(sourceBefore) {
		t.Fatal("retired copy diverged from the migrated source")
	}
	migrated, err := toolhub.Load(to)
	if err != nil {
		t.Fatal(err)
	}
	migrated.CloseDB()
	if err := toolhub.NewStore().Save(from); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("a stale writer must meet the initialized fence: %v", err)
	}
	if _, err := ActivateStoreSubstrate(from, to); err == nil {
		t.Fatal("second activation must refuse")
	}
}

// A live writer holds the store's flock; migrating underneath it could copy
// a torn snapshot, so the migration must refuse while the lock is held.
func TestMigrateToolHubStoreRefusesActiveWriter(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "state", "store.json")
	to := filepath.Join(dir, "state", "store.db")

	seed := toolhub.NewStore()
	definition := api.ToolDefinition{Schema: api.SchemaVersion, DefinitionID: "def", Version: "1.0.0", Transport: api.RemoteMCP, Source: api.DefinitionSource{URL: "https://example.invalid/mcp", TLSMode: "required"}, Workload: api.WorkloadPolicy{Class: api.PerUser, Rationale: "per-user remote tool"}, Execution: api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.invalid"}}, Health: api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5}, Tools: []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}}
	if err := seed.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := seed.Save(from); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(from + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	if _, err := MigrateStoreSubstrate(from, to); err == nil {
		t.Fatal("migration ran while a writer held the store lock")
	}
	if _, err := os.Lstat(to); !os.IsNotExist(err) {
		t.Fatal("destination created despite the active writer")
	}
}
