package toolhub

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
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
	return storeStamp{mtime: info.ModTime().UnixNano(), size: info.Size()}, true
}

func NewStore() *Store {
	return &Store{definitions: map[string]api.ToolDefinition{}, connections: map[string]api.Connection{}, credentials: map[string]api.CredentialReference{}, bindings: map[string]api.ToolBinding{}, workloads: map[string]api.WorkloadInstance{}, projectionRevisions: map[string]uint64{}, grants: map[string]api.Grant{}, onboardings: map[string]api.Onboarding{}, operations: map[string]api.Operation{}, recipes: map[string]api.RecipeResolution{}, publications: map[string]api.DefinitionPublication{}, sharedPolicies: map[string]api.SharedCredentialPolicy{}, capabilityPolicies: map[string]api.CapabilityPolicy{}, capabilityProfiles: map[string]api.CapabilityProfile{}, exclusions: map[string]api.ProjectionExclusion{}}
}

func (s *Store) Save(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(path)
}

// saveLocked lets authorization mutations become visible only after the same
// snapshot fence and durable write used by Save have succeeded.
func (s *Store) saveLocked(path string) error {
	if err := api.SafeStorePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := api.SafeStorePath(path + ".lock"); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	previous, err := os.ReadFile(path) // #nosec G304 -- trusted, symlink-checked registry path.
	if err == nil && (s.savedPath != path || sha256.Sum256(previous) != s.diskDigest) {
		return api.ErrConflict
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	s.compactLocked(time.Now())
	state := snapshot{Schema: api.SchemaVersion, ProjectionRevisions: map[string]uint64{}, CapabilityChanges: s.capabilityChanges, BindingChanges: s.bindingChanges}
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
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
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
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	s.savedPath, s.diskDigest = path, sha256.Sum256(body)
	s.diskStamp, _ = statStoreStamp(path)
	return nil
}

func Load(path string) (*Store, error) {
	if err := api.SafeStorePath(path); err != nil {
		return nil, err
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
	for _, binding := range store.bindings {
		key := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
		if binding.ProjectionRevision > store.projectionRevisions[key] {
			store.projectionRevisions[key] = binding.ProjectionRevision
		}
	}
	if err := store.validateLocked(); err != nil {
		return nil, err
	}
	store.path = path
	store.savedPath, store.diskDigest = path, sha256.Sum256(b)
	store.diskStamp = stamp
	return store, nil
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

// Reload replaces in-memory state from the file-backed snapshot. In-memory
// stores (no path) are unchanged so tests can mutate without a round-trip.
func (s *Store) Reload() error {
	if s == nil || s.path == "" {
		return nil
	}
	// Fast path under the read lock: the stat stamp comparison only reads
	// s.savedPath/s.diskStamp, and callers invoke Reload per request (plus
	// once per projected tool from filterProjectedTools and per tick from the
	// projection refresher). Taking the write lock here would queue a writer
	// behind every in-flight admitted call, which starves nested rechecks.
	s.mu.RLock()
	if stamp, ok := statStoreStamp(s.path); ok && s.savedPath == s.path && stamp == s.diskStamp {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()
	s.mu.Lock()
	if stamp, ok := statStoreStamp(s.path); ok && s.savedPath == s.path && stamp == s.diskStamp {
		s.mu.Unlock()
		return nil
	}
	fresh, err := Load(s.path)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	// Reads must not replace pending in-process mutations with the unchanged
	// disk snapshot, nor race a Save and restore its older predecessor.
	if fresh.diskDigest == s.diskDigest {
		s.mu.Unlock()
		return nil
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
	s.grants = fresh.grants
	s.onboardings = fresh.onboardings
	s.publications = fresh.publications
	s.sharedPolicies = fresh.sharedPolicies
	s.capabilityPolicies = fresh.capabilityPolicies
	s.capabilityProfiles = fresh.capabilityProfiles
	s.capabilityChanges = fresh.capabilityChanges
	s.bindingChanges = fresh.bindingChanges
	s.savedPath, s.diskDigest = fresh.savedPath, fresh.diskDigest
	s.diskStamp = fresh.diskStamp
	s.mu.Unlock()
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
