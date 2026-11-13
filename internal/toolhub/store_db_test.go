package toolhub

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func TestStoreDBReloadUsesDataVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")
	seed := NewStore()
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.CloseDB() })
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })
	prevPoll := storeDigestPollInterval
	storeDigestPollInterval = 0
	t.Cleanup(func() { storeDigestPollInterval = prevPoll })
	// A checkpoint-style stamp drift without a data commit is invisible to
	// the db watcher — file stamps churn on bind mounts, so only a digest
	// change re-triggers a load.
	loaded.diskStamp = storeStamp{}
	if err := loaded.Reload(); err != nil {
		t.Fatal(err)
	}
	if loaded.diskStamp != (storeStamp{}) {
		t.Fatal("stamp-only drift reloaded the store")
	}
	if _, err := seed.db.Exec("UPDATE meta SET v='foreign' WHERE k='digest'"); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Reload(); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("foreign SQLite commit was not detected: %v", err)
	}
}

// Regression for the dev-stack wedge: on a bind mount whose shared WAL
// index never propagates (Docker Desktop), a foreign process's commit
// moves only the file stamps while this connection's data_version stays
// pinned — a host-side apply once left ToolHub deciding on a stale
// passport pin until a restart forced a fresh Load.
func TestStoreDBReloadAdoptsForeignCommitWhenDataVersionStalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")
	seed := NewStore()
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.CloseDB() })
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })

	prev := storeDataVersion
	storeDataVersion = func(*sql.DB) (int64, error) { return loaded.dbVersion, nil }
	t.Cleanup(func() { storeDataVersion = prev })
	prevPoll := storeDigestPollInterval
	storeDigestPollInterval = 0
	t.Cleanup(func() { storeDigestPollInterval = prevPoll })

	seed.projectionRevisions["alice"]++
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Reload(); err != nil {
		t.Fatalf("stalled data_version blocked the foreign commit: %v", err)
	}
	if loaded.projectionRevisions["alice"] != 1 {
		t.Fatal("foreign commit was never adopted")
	}
	stamp := loaded.diskStamp
	if err := loaded.Reload(); err != nil {
		t.Fatal(err)
	}
	if loaded.diskStamp != stamp {
		t.Fatal("settled store reloaded again")
	}
}

// A commit another process lands after this store's last load is invisible
// to its frozen WAL view — its next save must refuse the conflict rather
// than checkpoint an outdated snapshot over the newer rows.
func TestStoreDBSaveRefusesToOverwriteForeignCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")
	seed := NewStore()
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.CloseDB() })
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })

	prevVersion := storeDataVersion
	storeDataVersion = func(*sql.DB) (int64, error) { return loaded.dbVersion, nil }
	t.Cleanup(func() { storeDataVersion = prevVersion })
	prevPoll := storeDigestPollInterval
	storeDigestPollInterval = 0
	t.Cleanup(func() { storeDigestPollInterval = prevPoll })

	seed.projectionRevisions["alice"]++
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}

	loaded.projectionRevisions["bob"]++
	if err := loaded.Save(path); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("blind save overwrote foreign commit: %v", err)
	}
	fresh, err := Load(path)
	if err != nil {
		t.Fatalf("foreign commit destroyed: %v", err)
	}
	defer func() { _ = fresh.CloseDB() }()
	if fresh.projectionRevisions["alice"] != 1 || fresh.projectionRevisions["bob"] != 0 {
		t.Fatal("file lost foreign rows or absorbed the refused write")
	}
	if err := loaded.Reload(); err != nil {
		t.Fatalf("foreign commit never adopted: %v", err)
	}
	if loaded.projectionRevisions["alice"] != 1 {
		t.Fatal("foreign commit not adopted after refused save")
	}
}

// A commit landing while Reload fetches its snapshot leaves that fetch a
// step behind; the store refuses the stale fetch once, then converges on
// the newer state at the next probe — even when the commits came from a
// process this connection cannot observe.
func TestStoreDBReloadRefusesSnapshotOvertakenDuringLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")
	seed := NewStore()
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.CloseDB() })
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })

	prevVersion := storeDataVersion
	storeDataVersion = func(*sql.DB) (int64, error) { return loaded.dbVersion, nil }
	t.Cleanup(func() { storeDataVersion = prevVersion })
	prevPoll := storeDigestPollInterval
	storeDigestPollInterval = 0
	t.Cleanup(func() { storeDigestPollInterval = prevPoll })

	seed.projectionRevisions["alice"]++
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	prevLoad := storeLoad
	storeLoad = func(p string) (*Store, error) {
		fresh, err := prevLoad(p)
		if err != nil {
			return nil, err
		}
		seed.projectionRevisions["bob"]++
		return fresh, seed.Save(p)
	}
	t.Cleanup(func() { storeLoad = prevLoad })

	if err := loaded.Reload(); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("overtaken snapshot adopted: %v", err)
	}
	storeLoad = prevLoad
	if err := loaded.Reload(); err != nil {
		t.Fatalf("settled store never adopted: %v", err)
	}
	if loaded.projectionRevisions["alice"] != 1 || loaded.projectionRevisions["bob"] != 1 {
		t.Fatal("foreign commits lost")
	}
}

// A concurrent reader must not wait for the disk snapshot Reload is
// fetching: on a slow backing store (Windows bind mounts) holding the write
// lock across Load serialized every Decide behind one virtiofs read.
func TestStoreDBReloadDoesNotBlockReadersDuringLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")
	seed := NewStore()
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.CloseDB() })
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	prev := storeLoad
	storeLoad = func(p string) (*Store, error) {
		entered <- struct{}{}
		<-release
		return prev(p)
	}
	t.Cleanup(func() { storeLoad = prev })

	// A foreign commit marks the store changed so Reload reaches Load.
	seed.projectionRevisions["alice"]++
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- loaded.Reload() }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("Reload never reached the disk load")
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		loaded.HasActiveProfile("alice")
	}()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reader blocked behind Reload's disk snapshot load")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- loaded.Reload() }()
	close(release)
	if err := <-reloadDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
		t.Fatal("concurrent Reload repeated the disk load")
	default:
	}
}

// Reload must report a stale snapshot instead of succeeding with old rights.
func TestStoreReloadRejectsStaleSnapshot(t *testing.T) {
	for _, name := range []string{"store.db", "store.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state", name)
			seed := NewStore()
			if err := seed.Save(path); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = seed.CloseDB() })
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = loaded.CloseDB() })
			saveTeam := func(id string) error {
				team := api.Team{Schema: api.SchemaVersion, TeamID: id, Members: []string{"carol"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now(), Status: api.ActiveStatus}
				if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
					return err
				}
				seed.teams[id] = team
				return seed.Save(path)
			}
			if err := saveTeam("team-v2"); err != nil {
				t.Fatal(err)
			}
			prev := storeLoad
			var commitOnce sync.Once
			var commitErr error
			storeLoad = func(p string) (*Store, error) {
				fresh, err := prev(p)
				if err != nil {
					return nil, err
				}
				commitOnce.Do(func() { commitErr = saveTeam("team-v3") })
				if commitErr != nil {
					return nil, commitErr
				}
				return fresh, nil
			}
			t.Cleanup(func() { storeLoad = prev })

			if err := loaded.Reload(); !errors.Is(err, api.ErrConflict) {
				t.Fatalf("stale snapshot: got %v, want conflict", err)
			}
			if err := loaded.Reload(); err != nil {
				t.Fatal(err)
			}
			if _, ok := loaded.teams["team-v3"]; !ok {
				t.Fatal("foreign commit landing during snapshot load was lost")
			}
		})
	}
}

func TestProjectionFingerprintTracksPersistedInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")
	seed := NewStore()
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.CloseDB() })
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })
	passportRoot := filepath.Join(t.TempDir(), "passports-by")
	revisionDir := filepath.Join(passportRoot, "alice", "passports", "r1")
	if err := os.MkdirAll(revisionDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(passportRoot, "alice", "passports", "current"), []byte("r1"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range passportview.Names {
		if err := os.WriteFile(filepath.Join(revisionDir, name+".yaml"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	g := &Gateway{Store: loaded, Governance: NewGovernanceStore(filepath.Join(filepath.Dir(path), "governance.json")), PassportRoot: passportRoot}
	first, cacheable, err := g.projectionFingerprint(aliceAuth())
	if err != nil || !cacheable {
		t.Fatalf("initial fingerprint: cacheable=%t err=%v", cacheable, err)
	}
	// SQLite checkpoints may change file metadata only. The stamp drift
	// makes the store look changed until Reload re-baselines it; the
	// projection itself must stay identical.
	loaded.diskStamp = storeStamp{}
	if err := loaded.Reload(); err != nil {
		t.Fatal(err)
	}
	if same, ok, err := g.projectionFingerprint(aliceAuth()); err != nil || !ok || same != first {
		t.Fatalf("checkpoint-only stamp changed projection: cacheable=%t err=%v", ok, err)
	}
	if err := os.WriteFile(g.Governance.path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	second, cacheable, err := g.projectionFingerprint(aliceAuth())
	if err != nil || !cacheable || second == first {
		t.Fatalf("governance change missed: cacheable=%t err=%v", cacheable, err)
	}
	if err := os.WriteFile(filepath.Join(revisionDir, "tools.yaml"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	third, cacheable, err := g.projectionFingerprint(aliceAuth())
	if err != nil || !cacheable || third == second {
		t.Fatalf("passport change missed: cacheable=%t err=%v", cacheable, err)
	}
	if _, err := seed.db.Exec("UPDATE meta SET v='foreign' WHERE k='digest'"); err != nil {
		t.Fatal(err)
	}
	_, cacheable, err = g.projectionFingerprint(aliceAuth())
	if err != nil || cacheable {
		t.Fatalf("external SQLite commit missed: cacheable=%t err=%v", cacheable, err)
	}
}

func TestConcurrentSQLiteProjectionRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "store.db")
	seed := NewStore()
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.CloseDB() })
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })
	g := &Gateway{Store: loaded}
	auth := aliceAuth()
	fingerprint, cacheable, err := g.projectionFingerprint(auth)
	if err != nil || !cacheable {
		t.Fatalf("initial fingerprint: cacheable=%t err=%v", cacheable, err)
	}
	p := &gatewayProjection{auth: auth, server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]api.ToolSpec{}, fingerprint: fingerprint}
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- g.refreshProjection(p) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreDBReloadRejectsRemovedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not rename an open SQLite file")
	}
	path := filepath.Join(t.TempDir(), "state", "store.db")
	seed := NewStore()
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.CloseDB() })
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Reload(); err == nil {
		t.Fatal("removed live SQLite path was still accepted")
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
