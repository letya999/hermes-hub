package toolhub

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The SQLite store round-trips through one transaction; reloads reject
// foreign digests the same way the file snapshot does.
func TestStoreDBSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "store.db")

	store := NewStore()
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.db.Close() })
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loaded.db.Close() })
	if loaded == nil {
		t.Fatal("empty load")
	}
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}

	// A foreign writer between saves trips the conflict fence.
	stale := NewStore()
	if err := stale.Save(filepath.Join(dir, "state", "other.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stale.db.Close() })
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE meta SET v='foreign' WHERE k='digest'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := stale.Save(path); err == nil {
		t.Fatal("stale save overwrote foreign state")
	}

	if _, err := Load(filepath.Join(dir, "missing.db")); err == nil {
		t.Fatal("missing store loaded")
	}
}

// A fully populated registry decomposes into rows and rebuilds to a
// byte-identical canonical snapshot — every collection survives the trip.
func TestStoreDBPopulatedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")
	now := time.Now().UTC()

	store := NewStore()
	definition := agentDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: "cred-1", ConnectionID: "conn-1", Revision: 1, Backend: "vault", Locator: "vault:secret/x", Keys: []string{"API_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(reference); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-1", DefinitionID: definition.DefinitionID, Revision: 1, Status: api.ActiveStatus, Owner: api.OwnerRef{Type: api.ContextOwner, ID: "alice"}, CredentialRefID: "cred-1"}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1, ConnectionID: "conn-1", ConnectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "conn-1", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	workload := api.WorkloadInstance{Schema: api.SchemaVersion, WorkloadID: "wl-1", BindingID: binding.ToolBindingID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, Generation: 1, Class: api.PerUser, Status: api.RunningStatus, Owner: &api.OwnerRef{Type: api.ContextOwner, ID: "alice"}, RuntimeID: "runtime"}
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	grant := api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")
	if err := api.Confirm(&grant, "", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(grant); err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "ob-1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingSelfInstall, Phase: api.PhaseFailed, Revision: 1, CreatedAt: now}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	operation := api.Operation{OperationID: "op-1", PrincipalID: "alice", Target: "target", Intent: api.OpIntentInstall, State: api.OpStateDone}
	if err := store.PutOperation(operation); err != nil {
		t.Fatal(err)
	}
	resolution := api.RecipeResolution{State: "ready", Launch: api.LaunchRecipe{Transport: api.ContainerMCP, Artifact: "ghcr.io/acme/weather", Digest: "sha256:" + repeatHex('a')}}
	if _, err := store.PutRecipe(resolution); err != nil {
		t.Fatal(err)
	}
	publication := api.DefinitionPublication{DefinitionID: definition.DefinitionID, Version: definition.Version, Visibility: api.PublicationCatalog}
	if err := store.PutPublication(publication); err != nil {
		t.Fatal(err)
	}
	policy := api.SharedCredentialPolicy{Schema: api.SchemaVersion, PolicyID: "pol-1", DefinitionID: definition.DefinitionID, StoreOwner: "alice", IssuedBy: "operator", Locator: "vault:x", Status: api.ActiveStatus, Principals: []string{"alice"}}
	if err := store.PutSharedCredentialPolicy(policy); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: now, Status: api.ActiveStatus}
	if err := api.Confirm(&team, "", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordBrokerGrant(api.BrokerBindingGrant{BindingID: binding.ToolBindingID, GrantID: "bg-1", CredentialRefID: "cred-1", ConnectionID: "conn-1", ContextID: "alice"}); err != nil {
		t.Fatal(err)
	}
	capabilityPolicy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "cpol-1", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "test", Status: api.ActiveStatus}
	if err := api.Confirm(&capabilityPolicy, "", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(capabilityPolicy); err != nil {
		t.Fatal(err)
	}
	store.bindingChanges = append(store.bindingChanges, bindingChange{BindingID: binding.ToolBindingID, FromPolicy: "policy-1", ToPolicy: "policy-2", Revision: 1})
	if err := store.MarkRestartApplied(api.ProjectionTarget{PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, Revision: 3, Digest: "digest-3"}); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	before, err := store.buildSnapshotLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.db.Close() })

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loaded.db.Close() })
	loaded.mu.Lock()
	after, err := loaded.buildSnapshotLocked()
	loaded.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatalf("snapshot diverged:\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}
}

// Parity with the file snapshot: an initialized marker refuses silent
// re-initialization after the DB is lost, and a tampered stored digest is
// rejected as a foreign write on load.
func TestStoreDBIntegrityAndMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "store.db")

	store := NewStore()
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	store.db.Close()
	store.db = nil
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	empty := NewStore()
	err := empty.Save(path)
	if empty.db != nil {
		empty.db.Close()
	}
	if !errors.Is(err, api.ErrConflict) {
		t.Fatalf("initialized store was silently replaced: %v", err)
	}

	path2 := filepath.Join(dir, "state2", "store.db")
	fresh := NewStore()
	if err := fresh.Save(path2); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.db.Close() })
	db, err := sql.Open("sqlite", "file:"+path2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE meta SET v='tampered' WHERE k='digest'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Load(path2); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("tampered store loaded: %v", err)
	}
}

// A foreign write after our save changes the stored digest; the owning
// store's next save must refuse instead of overwriting it, exactly like
// the file snapshot's sha256 fence.
func TestStoreDBForeignWriteFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")

	store := NewStore()
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.db.Close() })
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE meta SET v='foreign' WHERE k='digest'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := store.Save(path); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("foreign write overwritten: %v", err)
	}
}

// The SQLite write goes through the same storage budget as the JSON
// snapshot: a store whose next write exceeds the configured limit is
// refused before the transaction commits.
func TestStoreDBStorageBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")
	t.Setenv("HUB_TOOLHUB_STATE_MAX_NEXT_WRITE_BYTES", "16")

	store := NewStore()
	if err := store.Save(path); err == nil {
		t.Fatal("oversized write accepted")
	}
	if store.db != nil {
		store.db.Close()
	}
}
