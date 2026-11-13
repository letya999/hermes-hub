package toolhub

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// storeStamp is the observed identity of the on-disk snapshot: the file's
// modification time and size at the last successful load or save. Every store
// write goes through CreateTemp+Rename, so an unchanged stamp means the bytes
// are identical and a full Load (decode + revalidate of the whole registry)
// would only reproduce the current in-memory state.
type storeStamp struct {
	mtime int64
	size  int64
}

func statStoreStamp(path string) (storeStamp, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return storeStamp{}, false
	}
	stamp := storeStamp{mtime: info.ModTime().UnixNano(), size: info.Size()}
	// WAL-mode SQLite commits land in the sidecar until a checkpoint, so a
	// cross-instance write is invisible in the main file's stamp alone.
	if wal, walErr := os.Stat(path + "-wal"); walErr == nil {
		stamp.mtime = max(stamp.mtime, wal.ModTime().UnixNano())
		stamp.size += wal.Size()
	}
	return stamp, true
}

func NewStore() *Store {
	return &Store{definitions: map[string]api.ToolDefinition{}, connections: map[string]api.Connection{}, credentials: map[string]api.CredentialReference{}, bindings: map[string]api.ToolBinding{}, workloads: map[string]api.WorkloadInstance{}, projectionRevisions: map[string]uint64{}, brokerCursors: map[string]uint64{}, restartApplied: map[string]uint64{}, restartDigests: map[string]string{}, grants: map[string]api.Grant{}, onboardings: map[string]api.Onboarding{}, operations: map[string]api.Operation{}, refreshLeases: map[string]api.RefreshLease{}, connStatus: map[string]api.Status{}, recipes: map[string]api.RecipeResolution{}, publications: map[string]api.DefinitionPublication{}, sharedPolicies: map[string]api.SharedCredentialPolicy{}, teams: map[string]api.Team{}, bindingBrokerGrants: map[string]api.BrokerBindingGrant{}, capabilityPolicies: map[string]api.CapabilityPolicy{}, capabilityProfiles: map[string]api.CapabilityProfile{}, exclusions: map[string]api.ProjectionExclusion{}}
}

func (s *Store) Save(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(path)
}

// CanonicalSnapshotDigest verifies a substrate migration against its source
// head, independently of the backend's physical row/digest format.
func (s *Store) CanonicalSnapshotDigest() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.buildSnapshotLocked()
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

// saveLocked lets authorization mutations become visible only after the same
// snapshot fence and durable write used by Save have succeeded.
func (s *Store) saveLocked(path string) error {
	if s.db != nil || isStoreDB(path) {
		return s.saveDBLocked(path)
	}
	if err := api.SafeStorePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := api.SafeStorePath(path + ".lock"); err != nil {
		return err
	}
	marker := path + ".initialized"
	if err := api.SafeStorePath(marker); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	markerInfo, markerErr := os.Lstat(marker)
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return api.ErrInvalid
	}
	if markerErr == nil && !markerInfo.Mode().IsRegular() {
		return api.ErrInvalid
	}
	previous, err := os.ReadFile(path) // #nosec G304 -- trusted, symlink-checked registry path.
	if os.IsNotExist(err) && (s.savedPath == path || markerErr == nil) {
		return api.ErrConflict
	}
	if err == nil && (s.savedPath != path || sha256.Sum256(previous) != s.diskDigest) {
		return api.ErrConflict
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	state, err := s.buildSnapshotLocked()
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	counter, err := s.storageCounter(filepath.Dir(path))
	if err != nil {
		return err
	}
	write := func() error {
		f, err := os.CreateTemp(filepath.Dir(path), ".toolhub-*")
		if err != nil {
			return err
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(body)
		}
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		return os.Rename(tmp, path)
	}
	if err := counter.Write(path, int64(len(body)), true, write); err != nil {
		return err
	}
	s.savedPath, s.diskDigest = path, sha256.Sum256(body)
	s.diskStamp, _ = statStoreStamp(path)
	syncDirectory := s.syncDirectory
	if syncDirectory == nil {
		syncDirectory = syncHostingFleetDirectory
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if markerErr != nil {
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) // #nosec G304 -- marker path was symlink-checked under the store lock
		if err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	s.maintainStorageLocked(time.Now())
	return nil
}

func Load(path string) (*Store, error) {
	if err := api.SafeStorePath(path); err != nil {
		return nil, err
	}
	if isStoreDB(path) {
		return loadDB(path)
	}
	// Stamp before the read: a rename landing between stat and read leaves a
	// conservative stamp (older than the bytes), so the next Reload simply
	// loads once more instead of skipping a change it never saw.
	stamp, _ := statStoreStamp(path)
	b, err := os.ReadFile(path) // #nosec G304 -- the trusted registry path is checked for symlink components above
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	var state snapshot
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("%w: store JSON: %v", api.ErrInvalid, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: one store JSON document expected", api.ErrInvalid)
	}
	if state.Schema != api.SchemaVersion {
		return nil, fmt.Errorf("%w: store schema", api.ErrInvalid)
	}
	store, err := storeFromSnapshot(state)
	if err != nil {
		return nil, err
	}
	store.path = path
	store.savedPath, store.diskDigest = path, sha256.Sum256(b)
	store.diskStamp = stamp
	return store, nil
}

// storeFromSnapshot validates a decoded snapshot into a Store; file and
// SQLite backends share this exact admission path.
func storeFromSnapshot(state snapshot) (*Store, error) {
	var err error
	store := NewStore()
	for _, definition := range state.Definitions {
		if err := store.RegisterDefinition(definition); err != nil {
			return nil, err
		}
	}
	for _, change := range state.CapabilityChanges {
		records := 0
		for _, present := range []bool{change.Policy != nil, change.Profile != nil, change.Grant != nil} {
			if present {
				records++
			}
		}
		if records != 1 {
			return nil, fmt.Errorf("%w: exactly one capability change expected", api.ErrInvalid)
		}
		before := len(store.capabilityChanges)
		switch {
		case change.Policy != nil:
			err = store.PutCapabilityPolicy(*change.Policy)
		case change.Profile != nil:
			err = store.PutCapabilityProfile(*change.Profile)
		default:
			err = store.PutGrant(*change.Grant)
		}
		if err != nil {
			return nil, err
		}
		if len(store.capabilityChanges) != before+1 {
			return nil, fmt.Errorf("%w: duplicate capability change", api.ErrInvalid)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, reference := range state.Credentials {
		if err := reference.Validate(); err != nil {
			return nil, err
		}
		store.credentials[reference.CredentialRefID] = reference
	}
	for _, connection := range state.Connections {
		if err := connection.Validate(); err != nil {
			return nil, err
		}
		if !store.hasDefinitionID(connection.DefinitionID) {
			return nil, fmt.Errorf("%w: connection definition", api.ErrNotFound)
		}
		if connection.CredentialRefID != "" {
			reference, ok := store.credentials[connection.CredentialRefID]
			if !ok || reference.ConnectionID != connection.ConnectionID {
				return nil, fmt.Errorf("%w: connection credential", api.ErrInvalid)
			}
		}
		store.connections[connection.ConnectionID] = connection
		store.noteConnectionStatusLocked(connection)
	}
	for _, binding := range state.Bindings {
		if err := binding.Validate(); err != nil {
			return nil, err
		}
		store.bindings[binding.ToolBindingID] = binding
	}
	for _, workload := range state.Workloads {
		if err := workload.Validate(); err != nil {
			return nil, err
		}
		store.workloads[workload.WorkloadID] = workload
	}
	for key, revision := range state.ProjectionRevisions {
		if revision == 0 {
			return nil, fmt.Errorf("%w: persisted projection revision", api.ErrInvalid)
		}
		store.projectionRevisions[key] = revision
	}
	for key, revision := range state.BrokerCursors {
		store.brokerCursors[key] = revision
	}
	for key, revision := range state.RestartApplied {
		store.restartApplied[key] = revision
	}
	for key, digest := range state.RestartDigests {
		store.restartDigests[key] = digest
	}
	for _, change := range state.BindingChanges {
		if !identity.ValidID(change.BindingID) || change.FromPolicy == "" || change.ToPolicy == "" || change.Revision == 0 {
			return nil, fmt.Errorf("%w: persisted binding change", api.ErrInvalid)
		}
		store.bindingChanges = append(store.bindingChanges, change)
	}
	for _, grant := range state.Grants {
		if err := grant.Validate(); err != nil {
			return nil, err
		}
		store.grants[grant.GrantID] = grant
	}
	for _, onboarding := range state.Onboardings {
		if err := onboarding.Validate(); err != nil {
			return nil, err
		}
		if onboarding.EmbeddedRecipe != nil {
			// Pre-digest records stored the resolution inline; move it under
			// its content digest so the embedded form never writes back.
			digest, err := api.ResolutionDigest(*onboarding.EmbeddedRecipe)
			if err != nil {
				return nil, fmt.Errorf("%w: persisted recipe", api.ErrInvalid)
			}
			if _, known := store.recipes[digest]; !known {
				store.recipes[digest] = *onboarding.EmbeddedRecipe
			}
			if onboarding.RecipeDigest == "" {
				onboarding.RecipeDigest = digest
			}
			onboarding.EmbeddedRecipe = nil
		}
		store.onboardings[onboarding.OnboardingID] = onboarding
	}
	for _, op := range state.Operations {
		if err := op.Validate(); err != nil {
			return nil, err
		}
		store.operations[op.OperationID] = op
	}
	for digest, resolution := range state.Recipes {
		if check, err := api.ResolutionDigest(resolution); err != nil || check != digest {
			return nil, fmt.Errorf("%w: persisted recipe digest", api.ErrInvalid)
		}
		store.recipes[digest] = resolution
	}
	for _, pub := range state.Publications {
		if err := pub.Validate(); err != nil {
			return nil, err
		}
		store.publications[definitionKey(pub.DefinitionID, pub.Version)] = pub
	}
	for _, policy := range state.SharedPolicies {
		if err := policy.Validate(); err != nil {
			return nil, err
		}
		store.sharedPolicies[policy.PolicyID] = policy
	}
	for _, team := range state.Teams {
		if err := team.Validate(); err != nil {
			return nil, err
		}
		store.teams[team.TeamID] = team
	}
	for _, grant := range state.BrokerGrants {
		if grant.BindingID == "" || grant.GrantID == "" || grant.CredentialRefID == "" || grant.ConnectionID == "" || grant.ContextID == "" {
			return nil, fmt.Errorf("%w: persisted broker binding grant", api.ErrInvalid)
		}
		if _, ok := store.bindings[grant.BindingID]; !ok {
			return nil, fmt.Errorf("%w: persisted broker grant binding", api.ErrInvalid)
		}
		if _, dup := store.bindingBrokerGrants[grant.BindingID]; dup {
			return nil, fmt.Errorf("%w: duplicate broker binding grant", api.ErrInvalid)
		}
		store.bindingBrokerGrants[grant.BindingID] = grant
	}
	for _, binding := range store.bindings {
		key := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
		if binding.ProjectionRevision > store.projectionRevisions[key] {
			store.projectionRevisions[key] = binding.ProjectionRevision
		}
	}
	if err := store.validateLocked(); err != nil {
		return nil, err
	}
	store.installationID = state.InstallationID
	return store, nil
}

// buildSnapshotLocked compacts, stamps the installation and collects every
// persisted collection into the snapshot in its canonical sort order — the
// single serialization both backends write. s.mu must be held.
func (s *Store) buildSnapshotLocked() (snapshot, error) {
	s.compactLocked(time.Now())
	if s.installationID == "" {
		id, err := newInstallationID()
		if err != nil {
			return snapshot{}, err
		}
		s.installationID = id
	}
	state := snapshot{BrokerCursors: maps.Clone(s.brokerCursors), Schema: api.SchemaVersion, InstallationID: s.installationID, ProjectionRevisions: map[string]uint64{}, RestartApplied: map[string]uint64{}, RestartDigests: map[string]string{}, CapabilityChanges: s.capabilityChanges, BindingChanges: s.bindingChanges}
	for _, d := range s.definitions {
		state.Definitions = append(state.Definitions, d)
	}
	for _, c := range s.connections {
		state.Connections = append(state.Connections, c)
	}
	for _, r := range s.credentials {
		state.Credentials = append(state.Credentials, r)
	}
	for _, b := range s.bindings {
		state.Bindings = append(state.Bindings, b)
	}
	for _, w := range s.workloads {
		state.Workloads = append(state.Workloads, w)
	}
	for key, revision := range s.projectionRevisions {
		state.ProjectionRevisions[key] = revision
	}
	for key, revision := range s.restartApplied {
		state.RestartApplied[key] = revision
	}
	for key, digest := range s.restartDigests {
		state.RestartDigests[key] = digest
	}
	for _, grant := range s.grants {
		state.Grants = append(state.Grants, grant)
	}
	for _, onboarding := range s.onboardings {
		// A registered definition is already persisted once in the registry;
		// embedding it per onboarding duplicates the same bytes. Drafts still
		// awaiting admission keep their copy — nothing else can rebuild it.
		if onboarding.Definition != nil && !onboarding.AdmissionPending {
			if _, ok := s.definitions[definitionKey(onboarding.DefinitionID, onboarding.DefinitionVersion)]; ok {
				onboarding.Definition = nil
			}
		}
		state.Onboardings = append(state.Onboardings, onboarding)
	}
	for _, op := range s.operations {
		state.Operations = append(state.Operations, op)
	}
	if len(s.recipes) > 0 {
		state.Recipes = s.recipes
	}
	for _, pub := range s.publications {
		state.Publications = append(state.Publications, pub)
	}
	for _, policy := range s.sharedPolicies {
		state.SharedPolicies = append(state.SharedPolicies, policy)
	}
	for _, team := range s.teams {
		state.Teams = append(state.Teams, team)
	}
	s.grantMu.Lock()
	for _, grant := range s.bindingBrokerGrants {
		state.BrokerGrants = append(state.BrokerGrants, grant)
	}
	s.grantMu.Unlock()
	slices.SortFunc(state.Definitions, func(a, b api.ToolDefinition) int {
		return strings.Compare(definitionKey(a.DefinitionID, a.Version), definitionKey(b.DefinitionID, b.Version))
	})
	slices.SortFunc(state.Connections, func(a, b api.Connection) int { return strings.Compare(a.ConnectionID, b.ConnectionID) })
	slices.SortFunc(state.Credentials, func(a, b api.CredentialReference) int { return strings.Compare(a.CredentialRefID, b.CredentialRefID) })
	slices.SortFunc(state.Bindings, func(a, b api.ToolBinding) int { return strings.Compare(a.ToolBindingID, b.ToolBindingID) })
	slices.SortFunc(state.Workloads, func(a, b api.WorkloadInstance) int { return strings.Compare(a.WorkloadID, b.WorkloadID) })
	slices.SortFunc(state.Grants, func(a, b api.Grant) int { return strings.Compare(a.GrantID, b.GrantID) })
	slices.SortFunc(state.Onboardings, func(a, b api.Onboarding) int { return strings.Compare(a.OnboardingID, b.OnboardingID) })
	slices.SortFunc(state.Operations, func(a, b api.Operation) int { return strings.Compare(a.OperationID, b.OperationID) })
	slices.SortFunc(state.Publications, func(a, b api.DefinitionPublication) int {
		return strings.Compare(definitionKey(a.DefinitionID, a.Version), definitionKey(b.DefinitionID, b.Version))
	})
	slices.SortFunc(state.SharedPolicies, func(a, b api.SharedCredentialPolicy) int { return strings.Compare(a.PolicyID, b.PolicyID) })
	slices.SortFunc(state.Teams, func(a, b api.Team) int { return strings.Compare(a.TeamID, b.TeamID) })
	slices.SortFunc(state.BrokerGrants, func(a, b api.BrokerBindingGrant) int { return strings.Compare(a.BindingID, b.BindingID) })
	return state, nil
}

// compactLocked deletes terminal records past their retention windows so a
// long-lived store stays bounded: removed onboardings older than 30 days
// and done operations older than 7 days. Live state is untouched. s.mu
// must be held.
func (s *Store) compactLocked(now time.Time) {
	for id, onboarding := range s.onboardings {
		if onboarding.Phase == api.PhaseRemoved && now.Sub(onboarding.CreatedAt) > 30*24*time.Hour {
			delete(s.onboardings, id)
		}
	}
	for id, op := range s.operations {
		if (op.State == api.OpStateDone || op.State == api.OpStateFailed) && now.Sub(op.UpdatedAt) > 7*24*time.Hour {
			delete(s.operations, id)
		}
	}
}

func (s *Store) persist() error {
	if s == nil || s.path == "" {
		return nil
	}
	return s.Save(s.path)
}

func (s *Store) persistAndNotify() error {
	if err := s.persist(); err != nil {
		return err
	}
	if s == nil || s.Reconnect == nil {
		return nil
	}
	_, _, err := s.Reconnect.Reconcile()
	return err
}

// Reload replaces in-memory state from the persisted snapshot. In-memory
// stores (no path) are unchanged so tests can mutate without a round-trip.
func (s *Store) changedOnDiskLocked() (bool, int64, error) {
	return s.changedOnDisk(false)
}

// forced bypasses the digest poll's rate limiter — the rechecks inside a
// Reload in flight must answer with the freshest content signal, not a
// cached "unchanged" from the entry probe that just detected the change.
func (s *Store) changedOnDisk(forced bool) (bool, int64, error) {
	if s.db != nil {
		info, err := os.Stat(s.path)
		if err != nil {
			return false, 0, err
		}
		if s.dbFile != nil && !os.SameFile(info, s.dbFile) {
			return false, 0, api.ErrConflict
		}
		// WAL checkpoints can change file timestamps without changing data.
		// data_version advances only for commits from another connection.
		version, err := storeDataVersion(s.db)
		if err != nil {
			return false, 0, err
		}
		if version != s.dbVersion {
			return true, version, nil
		}
		// data_version only advances for commits this connection can
		// observe; on bind mounts whose shared WAL index never propagates
		// (Docker Desktop) a blind connection stays stale forever, file
		// stamps churn without commits, and a fresh SQLite connection
		// short-reads the live file outright — so the bytes a commit
		// actually moves are the one signal that cannot be masked.
		if !forced {
			last := s.digestPollUnix.Load()
			now := time.Now().UnixNano()
			if now-last < int64(storeDigestPollInterval) || !s.digestPollUnix.CompareAndSwap(last, now) {
				return false, version, nil
			}
		}
		hash, ok := storeFileHash(s.path)
		return ok && hash != s.fileHash, version, nil
	}
	stamp, ok := statStoreStamp(s.path)
	return !ok || s.savedPath != s.path || stamp != s.diskStamp, 0, nil
}

// storeLoad is the seam through which tests can observe or delay the disk
// snapshot Reload fetches.
var storeLoad = loadStoreSnapshot

// copyStoreFiles byte-copies the database and its WAL under dir so a fresh
// SQLite connection can replay the latest commits: a second connection
// against the live file on a Docker Desktop bind mount fails with
// SQLITE_IOERR_SHORT_READ once the shared wal-index drifts.
func copyStoreFiles(path, dir string) {
	for _, suffix := range []string{"", "-wal"} {
		if body, err := os.ReadFile(path + suffix); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "store.db"+suffix), body, 0600) // #nosec G306 -- copy of an already store-scoped file.
		}
	}
}

// storeSnapshotDigest re-reads the persisted digest from a byte copy. File
// drift alone cannot tell a foreign commit apart from this connection's own
// out-of-band writes — the digest is the arbiter.
func storeSnapshotDigest(path string) (string, bool) {
	dir, err := os.MkdirTemp("", "toolhub-digest")
	if err != nil {
		return "", false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	copyStoreFiles(path, dir)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "store.db")+"?mode=ro")
	if err != nil {
		return "", false
	}
	db.SetMaxIdleConns(0)
	defer func() { _ = db.Close() }()
	var digest string
	if err := db.QueryRow(`SELECT v FROM meta WHERE k = 'digest'`).Scan(&digest); err != nil {
		return "", false
	}
	return digest, true
}

// loadStoreSnapshot reads a point-in-time store. A second SQLite connection
// against the live file on a Docker Desktop bind mount fails with
// SQLITE_IOERR_SHORT_READ once the shared wal-index drifts, so the db backend
// is loaded from a byte copy — the WAL copy replays the latest commits on
// open. fileHash is the live files' hash at fetch start, the overtaken fence
// baseline.
func loadStoreSnapshot(path string) (*Store, error) {
	if !isStoreDB(path) {
		return Load(path)
	}
	baseline, _ := storeFileHash(path)
	dir, err := os.MkdirTemp("", "toolhub-snapshot")
	if err != nil {
		return Load(path)
	}
	copyStoreFiles(path, dir)
	fresh, err := Load(filepath.Join(dir, "store.db"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	_ = fresh.CloseDB()
	_ = os.RemoveAll(dir)
	// The adopted snapshot carries the real file's identity, not the temp
	// copy's — savedPath feeds the save fence, dbFile the SameFile guard.
	fresh.path = path
	fresh.savedPath = path
	fresh.dbFile, _ = os.Stat(path)
	fresh.diskStamp, _ = statStoreStamp(path)
	fresh.fileHash = baseline
	return fresh, nil
}

// storeDataVersion reads the connection's data_version; tests pin it to
// simulate mounts where a foreign process's commit never advances it.
var storeDataVersion = dbDataVersion

// storeDigestPollInterval bounds how often an otherwise-unchanged store
// re-hashes the persisted bytes for commits its connection cannot see.
var storeDigestPollInterval = time.Second

// storeFileHash hashes the bytes a commit actually moves: the WAL while it
// carries frames, else the main database file. A second SQLite connection on
// a Docker Desktop bind mount fails with SQLITE_IOERR_SHORT_READ once the
// shared wal-index drifts, so persisted-content checks must not go through
// SQLite at all. shm-map drift and mtime churn cannot fake a different hash.
func storeFileHash(path string) ([32]byte, bool) {
	if body, err := os.ReadFile(path + "-wal"); err == nil {
		return sha256.Sum256(body), true
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return [32]byte{}, false
	}
	return sha256.Sum256(body), true
}

func (s *Store) Reload() error {
	if s == nil || s.path == "" {
		return nil
	}
	// Keep unchanged reads off the write lock; projection checks call Reload
	// repeatedly while other requests may already hold the read lock.
	s.mu.RLock()
	changed, _, err := s.changedOnDiskLocked()
	if err != nil || !changed {
		s.mu.RUnlock()
		return err
	}
	s.mu.RUnlock()
	// Coalesce concurrent reloads without holding the state lock during I/O.
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	s.mu.RLock()
	changed, _, err = s.changedOnDisk(true)
	s.mu.RUnlock()
	if err != nil || !changed {
		return err
	}
	// The snapshot read runs outside the write lock: a second SQLite
	// connection and full scan can stall for tens of seconds on slow backing
	// stores (Windows bind mounts), and holding s.mu across it serializes
	// every concurrent reader behind that one fetch.
	fresh, err := storeLoad(s.path)
	if err != nil {
		return err
	}
	// The adopted snapshot replaces in-memory state, but the fresh handle is
	// redundant — s keeps its own. Without this every cross-instance reload
	// leaks a SQLite connection (and locks the file on Windows).
	defer func() { _ = fresh.CloseDB() }()
	s.mu.Lock()
	changed, version, err := s.changedOnDisk(true)
	if err != nil || !changed {
		s.mu.Unlock()
		return err
	}
	// Reads must not replace pending in-process mutations with the unchanged
	// disk snapshot, nor race a Save and restore its older predecessor.
	if fresh.diskDigest == s.diskDigest {
		// The drift was stamp-only (a checkpoint moved the file, or the
		// baseline predates it); re-baseline so the fallback stops
		// re-triggering loads that can only reproduce in-memory state.
		s.dbVersion = version
		s.diskStamp = fresh.diskStamp
		s.fileHash = fresh.fileHash
		s.mu.Unlock()
		return nil
	}
	// A commit landing after the fetch leaves fresh stale; the file hash
	// catches commits this connection cannot observe through a broken
	// shared WAL index. The next probe converges on the newer state.
	overtaken := false
	if isStoreDB(s.path) {
		if hash, ok := storeFileHash(s.path); ok {
			overtaken = hash != fresh.fileHash
		}
	} else if body, err := os.ReadFile(s.path); err == nil { // #nosec G304 -- same store file already locked above.
		overtaken = sha256.Sum256(body) != fresh.diskDigest
	}
	if overtaken {
		s.mu.Unlock()
		return api.ErrConflict
	}
	stopped := make([]string, 0)
	for id, old := range s.workloads {
		if old.Status != api.RunningStatus && old.Status != api.StartingStatus {
			continue
		}
		next, ok := fresh.workloads[id]
		if !ok || (next.Status != api.RunningStatus && next.Status != api.StartingStatus) {
			stopped = append(stopped, id)
		}
	}
	s.definitions = fresh.definitions
	s.connections = fresh.connections
	s.credentials = fresh.credentials
	s.bindings = fresh.bindings
	s.workloads = fresh.workloads
	s.projectionRevisions = fresh.projectionRevisions
	s.brokerCursors = fresh.brokerCursors
	s.restartApplied = fresh.restartApplied
	s.restartDigests = fresh.restartDigests
	s.grants = fresh.grants
	s.onboardings = fresh.onboardings
	s.operations = fresh.operations
	s.recipes = fresh.recipes
	s.publications = fresh.publications
	s.sharedPolicies = fresh.sharedPolicies
	s.teams = fresh.teams
	s.grantMu.Lock()
	s.bindingBrokerGrants = fresh.bindingBrokerGrants
	s.grantMu.Unlock()
	s.capabilityPolicies = fresh.capabilityPolicies
	s.capabilityProfiles = fresh.capabilityProfiles
	s.capabilityChanges = fresh.capabilityChanges
	s.bindingChanges = fresh.bindingChanges
	s.savedPath, s.diskDigest = fresh.savedPath, fresh.diskDigest
	s.diskStamp = fresh.diskStamp
	s.fileHash = fresh.fileHash
	s.dbVersion = version
	s.dbFile = fresh.dbFile
	s.dbRows, s.dbListCounts = fresh.dbRows, fresh.dbListCounts
	s.mu.Unlock()
	// Re-sync the lock-free status mirror wholesale: the freshly loaded set
	// is authoritative, so connections removed on disk leave no stale entries.
	s.leaseMu.Lock()
	s.connStatus = fresh.connStatus
	s.leaseMu.Unlock()
	return s.stopWorkloads(stopped)
}

func (s *Store) validateLocked() error {
	for _, binding := range s.bindings {
		definition, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
		if !ok || definition.Workload.Class != binding.WorkloadClass {
			return fmt.Errorf("%w: persisted binding definition", api.ErrInvalid)
		}
		if binding.ConnectionID != "" {
			connection, ok := s.connections[binding.ConnectionID]
			if !ok || connection.DefinitionID != binding.DefinitionID {
				return fmt.Errorf("%w: persisted binding connection", api.ErrInvalid)
			}
		}
	}
	for _, workload := range s.workloads {
		binding, ok := s.bindings[workload.BindingID]
		if !ok || binding.DefinitionID != workload.DefinitionID || binding.DefinitionVersion != workload.DefinitionVersion {
			return fmt.Errorf("%w: persisted workload binding", api.ErrInvalid)
		}
	}
	return nil
}
