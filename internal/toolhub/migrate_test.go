package toolhub

import (
	"reflect"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"path/filepath"
	"testing"
)

// policyMigrationDefinition is a credential-free fixture so migration tests
// bind principals without connections.
func policyMigrationDefinition(id string) api.ToolDefinition {
	return api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: id, Version: "1.0.0", Transport: api.RemoteMCP,
		Source:    api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:     []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}},
		Workload:  api.WorkloadPolicy{Class: api.Shared, Rationale: "stateless shared workload"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
}

func putMigrationBinding(t *testing.T, store *Store, principal, contextID, runtimeID, definitionID, policy string, status api.Status) api.ToolBinding {
	t.Helper()
	binding := api.ToolBinding{
		Schema: api.SchemaVersion, PrincipalID: principal, ContextID: contextID, RuntimeID: runtimeID,
		DefinitionID: definitionID, DefinitionVersion: "1.0.0", PolicyVersion: policy,
		WorkloadClass: api.Shared, Status: status, Revision: 1, ProjectionRevision: 1,
	}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID = api.DeterministicBindingID(principal, contextID, runtimeID, definitionID, "1.0.0", "", "")
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
	active := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-active", "policy-1", api.ActiveStatus)
	disabled := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-disabled", "policy-1", api.DisabledStatus)
	revoked := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-revoked", "policy-1", api.RevokedStatus)
	foreign := putMigrationBinding(t, store, "bob", "bob", "runtime", "mig-foreign", "policy-1", api.ActiveStatus)

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
	for id, want := range map[string]api.ToolBinding{disabled.ToolBindingID: disabled, revoked.ToolBindingID: revoked, foreign.ToolBindingID: foreign} {
		if !reflect.DeepEqual(store.bindings[id], want) {
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
	binding := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-idem", "policy-1", api.ActiveStatus)
	if moved, err := MigrateBindings(store, "alice", "alice", "runtime", "policy-1", "policy-2"); err != nil || moved != 1 {
		t.Fatalf("first migrate: moved=%d err=%v", moved, err)
	}
	after := store.bindings[binding.ToolBindingID]
	moved, err := MigrateBindings(store, "alice", "alice", "runtime", "policy-1", "policy-2")
	if err != nil || moved != 0 {
		t.Fatalf("repeat migrate: moved=%d err=%v", moved, err)
	}
	if !reflect.DeepEqual(store.bindings[binding.ToolBindingID], after) {
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
	putMigrationBinding(t, seed, "alice", "alice", "runtime", "mig-audit", "policy-1", api.ActiveStatus)
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
	putMigrationBinding(t, seed, "alice", "alice", "runtime", "mig-conc", "policy-1", api.ActiveStatus)
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
	id := api.DeterministicBindingID("alice", "alice", "runtime", "mig-conc", "1.0.0", "", "")
	if final.bindings[id].PolicyVersion != "policy-2" {
		t.Fatalf("migration lost: %+v", final.bindings[id])
	}
	if !final.hasDefinitionID("mig-other") {
		t.Fatal("concurrent writer's definition was clobbered")
	}
}

// A projection roll must not trust a single `from` version: envelopes are
// minted at the live policy hash, so a binding pinned at a version no
// manifest recorded still bricks the projection. MigrateProjection moves
// every active stray, keeps disabled/revoked bindings put, and leaves other
// principals untouched.
func TestMigrateProjectionMovesStrayBindings(t *testing.T) {
	store := NewStore()
	for _, id := range []string{"mig-p1", "mig-p2", "mig-off", "mig-foreign"} {
		if err := store.RegisterDefinition(policyMigrationDefinition(id)); err != nil {
			t.Fatal(err)
		}
	}
	p1 := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-p1", "policy-1", api.ActiveStatus)
	p2 := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-p2", "policy-2", api.ActiveStatus)
	off := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-off", "policy-1", api.DisabledStatus)
	foreign := putMigrationBinding(t, store, "bob", "bob", "runtime", "mig-foreign", "policy-1", api.ActiveStatus)

	report, err := MigrateProjection(store, "alice", "alice", "runtime", "policy-3")
	if err != nil {
		t.Fatal(err)
	}
	if report.BindingsMoved != 2 {
		t.Fatalf("expected two stray bindings migrated, got %d", report.BindingsMoved)
	}
	for _, id := range []string{p1.ToolBindingID, p2.ToolBindingID} {
		if got := store.bindings[id]; got.PolicyVersion != "policy-3" {
			t.Fatalf("binding %s not migrated: %+v", id, got)
		}
	}
	froms := map[string]bool{}
	for _, record := range store.bindingChanges {
		froms[record.FromPolicy] = true
		if record.ToPolicy != "policy-3" {
			t.Fatalf("audit must record the roll to policy-3, got %+v", record)
		}
	}
	if !froms["policy-1"] || !froms["policy-2"] {
		t.Fatalf("audit must record each actual stale version, got %v", froms)
	}
	if store.bindings[off.ToolBindingID].PolicyVersion != "policy-1" || store.bindings[foreign.ToolBindingID].PolicyVersion != "policy-1" {
		t.Fatal("disabled or foreign binding moved")
	}
}

// Capability profiles are confirmation-signed: a policy-version roll can
// never rewrite one without invalidating the human review digest — the
// CHG-0230 live store proved a mutated record bricks every later Load.
// Migration therefore reports stale active profiles for operator republish
// and leaves the signed record byte-identical so the store still loads.
func TestMigrateProjectionReportsStaleProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	seed, auth, _, profile := managedStore(t)
	stale := profile
	stale.ProfileID = "alice-stale"
	stale.PolicyVersion = "policy-stale"
	if err := api.Confirm(&stale, "operator", stale.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := seed.PutCapabilityProfile(stale); err != nil {
		t.Fatal(err)
	}
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := MigrateProjection(s, auth.PrincipalID, auth.ContextID, auth.RuntimeID, "policy-live")
	if err != nil {
		t.Fatal(err)
	}
	blocked := map[string]bool{}
	for _, id := range report.ProfilesBlocked {
		blocked[id] = true
	}
	if !blocked[stale.ProfileID] {
		t.Fatalf("stale confirmed profile must be reported for republish, got %+v", report)
	}
	got := s.capabilityProfiles[stale.ProfileID]
	if got.PolicyVersion != "policy-stale" || got.Revision != stale.Revision {
		t.Fatalf("signed profile record must stay untouched: %+v", got)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("store must reload after migration — a rewritten profile bricks Load: %v", err)
	}
	if reloaded.capabilityProfiles[stale.ProfileID].PolicyVersion != "policy-stale" {
		t.Fatal("persisted profile lost its signed content")
	}
}

// The projection scan must stay scoped like the binding roll: only the
// owning principal's stale ACTIVE profile is reported; foreign, disabled,
// other-context, and other-runtime profiles are untouched, a profile already
// at the target version is ignored, and a repeat roll reports nothing.
func TestMigrateProjectionProfileScopeAndIdempotent(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	foreign := profile
	foreign.ProfileID = "bob-default"
	foreign.PrincipalID = "bob"
	foreign.PolicyVersion = "policy-stale"
	disabled := profile
	disabled.ProfileID = "alice-disabled"
	disabled.PolicyVersion = "policy-stale"
	disabled.Status = api.DisabledStatus
	current := profile
	current.ProfileID = "alice-current"
	current.PolicyVersion = "policy-live"
	otherCtx := profile
	otherCtx.ProfileID = "alice-otherctx"
	otherCtx.ContextID = "other-context"
	otherCtx.PolicyVersion = "policy-stale"
	otherRt := profile
	otherRt.ProfileID = "alice-otherrt"
	otherRt.RuntimeID = "other-runtime"
	otherRt.PolicyVersion = "policy-stale"
	s.capabilityProfiles[foreign.ProfileID] = foreign
	s.capabilityProfiles[disabled.ProfileID] = disabled
	s.capabilityProfiles[current.ProfileID] = current
	s.capabilityProfiles[otherCtx.ProfileID] = otherCtx
	s.capabilityProfiles[otherRt.ProfileID] = otherRt

	report, err := MigrateProjection(s, auth.PrincipalID, auth.ContextID, auth.RuntimeID, "policy-live")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ProfilesBlocked) != 1 || report.ProfilesBlocked[0] != profile.ProfileID {
		t.Fatalf("only alice's stale active profile may be reported: %+v", report)
	}
	for id, want := range map[string]api.CapabilityProfile{profile.ProfileID: profile, foreign.ProfileID: foreign, disabled.ProfileID: disabled, current.ProfileID: current, otherCtx.ProfileID: otherCtx, otherRt.ProfileID: otherRt} {
		if got := s.capabilityProfiles[id]; got.PolicyVersion != want.PolicyVersion || got.Revision != want.Revision || got.Status != want.Status {
			t.Fatalf("profile %s must stay untouched: %+v", id, got)
		}
	}
	again, err := MigrateProjection(s, auth.PrincipalID, auth.ContextID, auth.RuntimeID, "policy-live")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.ProfilesBlocked) != 1 {
		t.Fatalf("repeat roll re-reports the same stale profile, got %+v", again)
	}
}

// A concurrent writer commits between our snapshot and Save: MigrateProjection
// must reload the freshest registry and retry, like MigrateBindings.
func TestMigrateProjectionConcurrentWriterRetries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	seed := NewStore()
	if err := seed.RegisterDefinition(policyMigrationDefinition("mig-proj")); err != nil {
		t.Fatal(err)
	}
	putMigrationBinding(t, seed, "alice", "alice", "runtime", "mig-proj", "policy-1", api.ActiveStatus)
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
	if err := fresh.RegisterDefinition(policyMigrationDefinition("mig-proj-other")); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Save(path); err != nil {
		t.Fatal(err)
	}
	report, err := MigrateProjection(stale, "alice", "alice", "runtime", "policy-2")
	if err != nil {
		t.Fatalf("stale writer must reload and retry: %v", err)
	}
	if report.BindingsMoved != 1 {
		t.Fatalf("expected one migrated binding, got %d", report.BindingsMoved)
	}
	final, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id := api.DeterministicBindingID("alice", "alice", "runtime", "mig-proj", "1.0.0", "", "")
	if final.bindings[id].PolicyVersion != "policy-2" {
		t.Fatalf("migration lost: %+v", final.bindings[id])
	}
	if !final.hasDefinitionID("mig-proj-other") {
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
	binding := putMigrationBinding(t, store, "alice", "alice", "runtime", "mig-scope", "policy-1", api.ActiveStatus)
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
	if !reflect.DeepEqual(store.bindings[binding.ToolBindingID], binding) {
		t.Fatalf("rejected calls must not touch bindings: %+v", store.bindings[binding.ToolBindingID])
	}
}
