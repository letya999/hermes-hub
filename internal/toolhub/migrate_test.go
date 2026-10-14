package toolhub

import (
	"path/filepath"
	"testing"
)

// policyMigrationDefinition is a credential-free fixture so migration tests
// bind principals without connections.
func policyMigrationDefinition(id string) ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: id, Version: "1.0.0", Transport: RemoteMCP,
		Source:    DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:     []ToolSpec{{Name: "read", Effect: ReadEffect}},
		Workload:  WorkloadPolicy{Class: Shared, Rationale: "stateless shared workload"},
		Execution: ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
}

func putMigrationBinding(t *testing.T, store *Store, principal, contextID, runtimeID, definitionID, policy string, status Status) ToolBinding {
	t.Helper()
	binding := ToolBinding{
		Schema: SchemaVersion, PrincipalID: principal, ContextID: contextID, RuntimeID: runtimeID,
		DefinitionID: definitionID, DefinitionVersion: "1.0.0", PolicyVersion: policy,
		WorkloadClass: Shared, Status: status, Revision: 1, ProjectionRevision: 1,
	}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID = DeterministicBindingID(principal, contextID, runtimeID, definitionID, "1.0.0", "", "")
	return binding
}

// SPEC-0053 R1.2: only active bindings move to the new policy; disabled and
// revoked bindings keep their status and version, and other principals are
// untouched (acceptance A1.2).
func TestMigrateBindingsMovesOnlyActive(t *testing.T) {
	store := NewStore()
	for _, id := range []string{"mig-active", "mig-disabled", "mig-revoked", "mig-foreign"} {
		if err := store.RegisterDefinition(policyMigrationDefinition(id)); err != nil {
			t.Fatal(err)
		}
	}
	active := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-active", "policy-1", ActiveStatus)
	disabled := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-disabled", "policy-1", DisabledStatus)
	revoked := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-revoked", "policy-1", RevokedStatus)
	foreign := putMigrationBinding(t, store, "bob", "bob", "runtime", "mig-foreign", "policy-1", ActiveStatus)

	moved, err := MigrateBindings(store, "alice", "alice", "runtime", "policy-1", "policy-2")
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("expected one binding to migrate, got %d", moved)
	}
	got := store.bindings[active.ToolBindingID]
	if got.PolicyVersion != "policy-2" || got.Revision <= active.Revision || got.ProjectionRevision <= active.ProjectionRevision {
		t.Fatalf("active binding not migrated: %+v", got)
	}
	for id, want := range map[string]ToolBinding{disabled.ToolBindingID: disabled, revoked.ToolBindingID: revoked, foreign.ToolBindingID: foreign} {
		if store.bindings[id] != want {
			t.Fatalf("binding %s must stay untouched: %+v", id, store.bindings[id])
		}
	}
}

// A second migrate of the same (from, to) pair finds nothing to do and leaves
// every binding byte-identical — hubctl up may re-run the same hash roll.
func TestMigrateBindingsIdempotent(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(policyMigrationDefinition("mig-idem")); err != nil {
		t.Fatal(err)
	}
	binding := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-idem", "policy-1", ActiveStatus)
	if moved, err := MigrateBindings(store, "alice", "alice", "runtime", "policy-1", "policy-2"); err != nil || moved != 1 {
		t.Fatalf("first migrate: moved=%d err=%v", moved, err)
	}
	after := store.bindings[binding.ToolBindingID]
	moved, err := MigrateBindings(store, "alice", "alice", "runtime", "policy-1", "policy-2")
	if err != nil || moved != 0 {
		t.Fatalf("repeat migrate: moved=%d err=%v", moved, err)
	}
	if store.bindings[binding.ToolBindingID] != after {
		t.Fatalf("repeat migrate mutated the binding: %+v", store.bindings[binding.ToolBindingID])
	}
}

// Every migrated binding leaves a durable audit record naming the policy
// transition; the record survives a store reload (SPEC-0053 R1.2).
func TestMigrateBindingsAudited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	seed := NewStore()
	if err := seed.RegisterDefinition(policyMigrationDefinition("mig-audit")); err != nil {
		t.Fatal(err)
	}
	putMigrationBinding(t, seed, "alice", "alice", "runtime", "mig-audit", "policy-1", ActiveStatus)
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if moved, err := MigrateBindings(store, "alice", "alice", "runtime", "policy-1", "policy-2"); err != nil || moved != 1 {
		t.Fatalf("migrate: moved=%d err=%v", moved, err)
	}
	if len(store.bindingChanges) != 1 {
		t.Fatalf("expected one audit record, got %d", len(store.bindingChanges))
	}
	record := store.bindingChanges[0]
	if record.BindingID == "" || record.FromPolicy != "policy-1" || record.ToPolicy != "policy-2" || record.Revision != 2 {
		t.Fatalf("audit record missing the transition: %+v", record)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.bindingChanges) != 1 || reloaded.bindingChanges[0] != record {
		t.Fatalf("audit record did not persist: %+v", reloaded.bindingChanges)
	}
}

// A concurrent writer commits between our snapshot and Save: the stale-writer
// fence rejects the write, so migrate must reload the freshest registry and
// retry instead of dropping the migration or clobbering the other write.
func TestMigrateBindingsConcurrentWriterRetries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	seed := NewStore()
	if err := seed.RegisterDefinition(policyMigrationDefinition("mig-conc")); err != nil {
		t.Fatal(err)
	}
	putMigrationBinding(t, seed, "alice", "alice", "runtime", "mig-conc", "policy-1", ActiveStatus)
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	stale, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.RegisterDefinition(policyMigrationDefinition("mig-other")); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Save(path); err != nil {
		t.Fatal(err)
	}
	moved, err := MigrateBindings(stale, "alice", "alice", "runtime", "policy-1", "policy-2")
	if err != nil {
		t.Fatalf("stale writer must reload and retry: %v", err)
	}
	if moved != 1 {
		t.Fatalf("expected one migrated binding, got %d", moved)
	}
	final, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id := DeterministicBindingID("alice", "alice", "runtime", "mig-conc", "1.0.0", "", "")
	if final.bindings[id].PolicyVersion != "policy-2" {
		t.Fatalf("migration lost: %+v", final.bindings[id])
	}
	if !final.hasDefinitionID("mig-other") {
		t.Fatal("concurrent writer's definition was clobbered")
	}
}

// A migrate with no real transition (from == to) or malformed scope must not
// rewrite revisions — it would churn projection revisions for nothing.
func TestMigrateBindingsRejectsBadScope(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(policyMigrationDefinition("mig-scope")); err != nil {
		t.Fatal(err)
	}
	binding := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-scope", "policy-1", ActiveStatus)
	for _, call := range []struct {
		name     string
		from, to string
	}{
		{"same version", "policy-1", "policy-1"},
		{"empty from", "", "policy-2"},
		{"empty to", "policy-1", ""},
	} {
		if moved, err := MigrateBindings(store, "alice", "alice", "runtime", call.from, call.to); err == nil || moved != 0 {
			t.Fatalf("%s: expected rejection, got moved=%d err=%v", call.name, moved, err)
		}
	}
	if _, err := MigrateBindings(store, "", "alice", "runtime", "policy-1", "policy-2"); err == nil {
		t.Fatal("empty principal accepted")
	}
	if store.bindings[binding.ToolBindingID] != binding {
		t.Fatalf("rejected calls must not touch bindings: %+v", store.bindings[binding.ToolBindingID])
	}
}
