package toolhub

// Package toolhub contains the versioned ToolHub catalog and authorization
// model. It deliberately stops before provider execution and secret material.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"strings"
	"sync"
)

type Store struct {
	mu                  sync.RWMutex
	path                string
	savedPath           string
	diskDigest          [32]byte
	diskStamp           storeStamp
	Reconnect           *ReconnectController
	Stopper             api.WorkloadStopper
	definitions         map[string]api.ToolDefinition
	connections         map[string]api.Connection
	credentials         map[string]api.CredentialReference
	bindings            map[string]api.ToolBinding
	workloads           map[string]api.WorkloadInstance
	projectionRevisions map[string]uint64
	grants              map[string]api.Grant
	onboardings         map[string]api.Onboarding
	operations          map[string]api.Operation
	recipes             map[string]api.RecipeResolution
	publications        map[string]api.DefinitionPublication
	sharedPolicies      map[string]api.SharedCredentialPolicy
	teams               map[string]api.Team
	// bindingBrokerGrants is the per-binding Credential Broker grant ledger
	// for team-owned credentials: a shared reference cannot name one grant
	// because every member mints their own against it. It has its own lock:
	// the member-call injector mints while the store's read lock is already
	// held, so s.mu can never guard this map. grantMu is a leaf — code
	// holding it must not take s.mu; s.mu-holding code may take grantMu.
	grantMu             sync.Mutex
	bindingBrokerGrants map[string]api.BrokerBindingGrant
	// BrokerRevoke is the wired broker revoke seam for ledger rows — nil in
	// stores without a broker client (tests, hubctl-local use). Retired rows
	// leave the ledger even when the seam is absent or fails, because an
	// unreachable broker must not keep a dead grant authoritative.
	BrokerRevoke       func(ctx context.Context, grant api.BrokerBindingGrant) error
	capabilityPolicies map[string]api.CapabilityPolicy
	capabilityProfiles map[string]api.CapabilityProfile
	capabilityChanges  []api.CapabilityChange
	bindingChanges     []bindingChange
	// exMu guards the exclusion set separately: exclusions are observability
	// written from read-locked projection scans, so they must not ride s.mu.
	exMu           sync.Mutex
	exclusions     map[string]api.ProjectionExclusion
	exclusionOrder []string
	// leaseMu guards the refresh-lease ledger and the connStatus mirror.
	// Refresh can run inside the admission read lock (s.mu.RLock), so these
	// never take s.mu — a write there would self-deadlock.
	leaseMu           sync.Mutex
	refreshLeases     map[string]api.RefreshLease
	connStatus        map[string]api.Status
	leasesLoaded      bool
	viewerLinks       map[string]viewerLink
	viewerLinksLoaded bool
	viewerSessions    map[string]viewerSession
}

func projectionKey(principalID, contextID, runtimeID string) string {
	return principalID + "\x00" + contextID + "\x00" + runtimeID
}

func (s *Store) touchProjectionLocked(binding *api.ToolBinding) uint64 {
	key := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
	next := s.projectionRevisions[key] + 1
	if next <= binding.ProjectionRevision {
		next = binding.ProjectionRevision + 1
	}
	s.projectionRevisions[key] = next
	binding.ProjectionRevision = next
	return next
}

// ProjectionRevision returns the monotonic revision for one exact runtime
// projection. It is persisted with the registry so reconnect decisions do not
// reset after a process restart.
func (s *Store) ProjectionRevision(auth identity.Envelope) (uint64, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return 0, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.projectionRevisions[projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)], nil
}

// ProjectionRevisions snapshots every runtime projection revision. Keys come
// only from persisted bindings, so a token-file enrolment alone never creates
// a target.
func (s *Store) ProjectionRevisions() ([]api.ProjectionTarget, error) {
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]api.ProjectionTarget, 0, len(s.projectionRevisions))
	for key, revision := range s.projectionRevisions {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) != 3 || revision == 0 {
			continue
		}
		out = append(out, api.ProjectionTarget{PrincipalID: parts[0], ContextID: parts[1], RuntimeID: parts[2], Revision: revision})
	}
	return out, nil
}

func definitionKey(id, version string) string { return id + "@" + version }

func (s *Store) RegisterDefinition(definition api.ToolDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	// The immutable definition must not share mutable schema/rule slices with
	// its caller. Validate above ensures every embedded JSON document is valid.
	body, err := json.Marshal(definition)
	if err != nil {
		return err
	}
	var owned api.ToolDefinition
	if err := json.Unmarshal(body, &owned); err != nil {
		return err
	}
	definition = owned
	s.mu.Lock()
	defer s.mu.Unlock()
	key := definitionKey(definition.DefinitionID, definition.Version)
	if existing, ok := s.definitions[key]; ok && !api.DefinitionsEqual(existing, definition) {
		return fmt.Errorf("%w: immutable definition %s", api.ErrConflict, key)
	}
	s.definitions[key] = definition
	return nil
}

func (s *Store) PutCredentialReference(reference api.CredentialReference) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.credentials[reference.CredentialRefID]; ok && !api.RecordsEqual(existing, reference) {
		return fmt.Errorf("%w: immutable credential reference", api.ErrConflict)
	}
	s.credentials[reference.CredentialRefID] = reference
	return nil
}

func (s *Store) PutConnection(connection api.Connection) error {
	if err := connection.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasDefinitionID(connection.DefinitionID) {
		return fmt.Errorf("%w: connection definition", api.ErrNotFound)
	}
	if connection.CredentialRefID != "" {
		reference, ok := s.credentials[connection.CredentialRefID]
		if !ok || reference.ConnectionID != connection.ConnectionID {
			return fmt.Errorf("%w: connection credential reference", api.ErrInvalid)
		}
	}
	if existing, ok := s.connections[connection.ConnectionID]; ok && !api.RecordsEqual(existing, connection) {
		return fmt.Errorf("%w: connection is immutable within a revision", api.ErrConflict)
	}
	s.connections[connection.ConnectionID] = connection
	s.noteConnectionStatusLocked(connection)
	return nil
}

// HasConnection reports whether a connection record exists in the store —
// the reconciler uses it to tell orphans from desired resources.
func (s *Store) PutTeam(team api.Team) error {
	if err := team.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	var droppedMembers map[string]bool
	if existing, ok := s.teams[team.TeamID]; ok {
		if team.Revision <= existing.Revision {
			s.mu.Unlock()
			return api.ErrConflict
		}
		droppedMembers = map[string]bool{}
		for _, member := range existing.Members {
			droppedMembers[member] = true
		}
		for _, member := range team.Members {
			delete(droppedMembers, member)
		}
		for _, member := range team.WriteMembers {
			delete(droppedMembers, member)
		}
	}
	s.teams[team.TeamID] = team
	// A member leaving the team loses the operative parts of their seats on
	// that team's connections: workloads stop and broker grants retire. The
	// binding row stays — per-call membership evaluation already denies
	// resolve — so a re-added member keeps their seat.
	var stopped []string
	var retired []api.BrokerBindingGrant
	for bindingID, binding := range s.bindings {
		if binding.ConnectionID == "" || !droppedMembers[binding.PrincipalID] {
			continue
		}
		connection, ok := s.connections[binding.ConnectionID]
		if !ok || connection.Owner.Type != api.TeamOwner || connection.Owner.ID != team.TeamID {
			continue
		}
		stopped = append(stopped, s.stopBindingLocked(bindingID)...)
		retired = append(retired, s.retireBindingGrantsLocked(bindingID)...)
	}
	s.mu.Unlock()
	stopErr := s.stopWorkloads(stopped)
	if err := s.persist(); err != nil {
		return err
	}
	return errors.Join(stopErr, s.revokeGrants(retired))
}

// ShareTeamConnection is the operator's one-shot team publication: it mints
// the deterministic team-owned connection and credential reference for a
// definition that declares a shareable mode. Members converge through
// Enable — no per-member credential is ever minted.
func (s *Store) ShareTeamConnection(teamID, definitionID, version, backend, locator, brokerContext string, keys []string) (api.Connection, api.CredentialReference, error) {
	s.mu.Lock()
	team, ok := s.teams[teamID]
	if !ok || team.Status != api.ActiveStatus {
		s.mu.Unlock()
		return api.Connection{}, api.CredentialReference{}, fmt.Errorf("%w: team", api.ErrNotFound)
	}
	definition, ok := s.definitions[definitionKey(definitionID, version)]
	if !ok {
		s.mu.Unlock()
		return api.Connection{}, api.CredentialReference{}, fmt.Errorf("%w: manifest", api.ErrNotFound)
	}
	if definition.Workload.Shareable != "credential" && definition.Workload.Shareable != "connection" {
		s.mu.Unlock()
		return api.Connection{}, api.CredentialReference{}, fmt.Errorf("%w: definition is not team-shareable", api.ErrUnauthorized)
	}
	if backend == "credential-broker" {
		if !identity.ValidID(brokerContext) {
			s.mu.Unlock()
			return api.Connection{}, api.CredentialReference{}, fmt.Errorf("%w: team broker credential needs its owning context", api.ErrInvalid)
		}
		if definition.CredentialContractID == "" || definition.CredentialContractRevision < 1 {
			s.mu.Unlock()
			return api.Connection{}, api.CredentialReference{}, fmt.Errorf("%w: definition %s has no reviewed credential broker contract", api.ErrInvalid, definitionID)
		}
	}
	connectionID := api.DeterministicID("conn", "team", teamID, definitionID, version)
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connectionID, 1), ConnectionID: connectionID, Revision: 1, Backend: backend, Locator: locator, Keys: append([]string(nil), keys...), BrokerContextID: brokerContext, Status: api.ActiveStatus}
	if backend == "credential-broker" {
		reference.BrokerContractID = definition.CredentialContractID
		reference.BrokerContractRevision = definition.CredentialContractRevision
		reference.BrokerEnv = api.CloneMap(definition.CredentialContractEnv)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: connectionID, Owner: api.OwnerRef{Type: api.TeamOwner, ID: teamID}, DefinitionID: definitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus, Metadata: map[string]string{"shareable": definition.Workload.Shareable}}
	if err := reference.Validate(); err != nil {
		s.mu.Unlock()
		return api.Connection{}, api.CredentialReference{}, err
	}
	if err := connection.Validate(); err != nil {
		s.mu.Unlock()
		return api.Connection{}, api.CredentialReference{}, err
	}
	s.credentials[reference.CredentialRefID] = reference
	s.connections[connectionID] = connection
	s.noteConnectionStatusLocked(connection)
	s.mu.Unlock()
	return connection, reference, s.persist()
}

// DropTeamConnection is the operator remove path for a team-owned connection:
// every member binding referencing it is revoked and dropped with its
// workloads, then the shared connection and its credential references go away.
func (s *Store) DropTeamConnection(connectionID string) error {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if connection.Owner.Type != api.TeamOwner {
		s.mu.Unlock()
		return fmt.Errorf("%w: not a team connection", api.ErrInvalid)
	}
	var stopped []string
	var retired []api.BrokerBindingGrant
	for bindingID, binding := range s.bindings {
		if binding.ConnectionID != connectionID {
			continue
		}
		binding.Status = api.RevokedStatus
		s.touchProjectionLocked(&binding)
		s.bindings[bindingID] = binding
		stopped = append(stopped, s.stopBindingLocked(bindingID)...)
		delete(s.bindings, bindingID)
		retired = append(retired, s.retireBindingGrantsLocked(bindingID)...)
	}
	for refID, ref := range s.credentials {
		if ref.ConnectionID == connectionID {
			delete(s.credentials, refID)
		}
	}
	delete(s.connections, connectionID)
	s.forgetConnectionStatusLocked(connectionID)
	s.mu.Unlock()
	stopErr := s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		return err
	}
	return errors.Join(stopErr, s.revokeGrants(retired))
}

// BrokerGrantFor returns the recorded member grant a binding currently holds
// against refID — the injector's idempotency check before re-minting.
func (s *Store) BrokerGrantFor(bindingID, refID string) (api.BrokerBindingGrant, bool) {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	grant, ok := s.bindingBrokerGrants[bindingID]
	if !ok || grant.CredentialRefID != refID {
		return api.BrokerBindingGrant{}, false
	}
	return grant, true
}

// RecordBrokerGrant records a minted member grant. A row already recorded
// for the binding and credential revision wins: concurrent first calls race
// the mint, and only the recorded row may drive future acquires. The ledger
// cannot take s.mu — the minting injector runs under the store's read lock —
// so rows flush with the next natural save. Losing an unsaved row is safe:
// the deterministic broker idempotency key re-mints the identical grant and
// the row is re-recorded.
func (s *Store) RecordBrokerGrant(grant api.BrokerBindingGrant) (string, error) {
	if grant.BindingID == "" || grant.GrantID == "" || grant.CredentialRefID == "" || grant.ConnectionID == "" || grant.ContextID == "" {
		return "", fmt.Errorf("%w: broker binding grant", api.ErrInvalid)
	}
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	if existing, ok := s.bindingBrokerGrants[grant.BindingID]; ok && existing.CredentialRefID == grant.CredentialRefID {
		return existing.GrantID, nil
	}
	s.bindingBrokerGrants[grant.BindingID] = grant
	return grant.GrantID, nil
}

// retireBindingGrantsLocked drops one binding's grant rows and returns them
// for the revoke seam. Callers hold s.mu; grantMu is taken inside.
func (s *Store) retireBindingGrantsLocked(bindingID string) []api.BrokerBindingGrant {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	grant, ok := s.bindingBrokerGrants[bindingID]
	if !ok {
		return nil
	}
	delete(s.bindingBrokerGrants, bindingID)
	return []api.BrokerBindingGrant{grant}
}

// retireConnectionGrantsLocked drops grant rows minted against a connection —
// all of them, or only rows pinned to one stale credential revision.
// Callers hold s.mu; grantMu is taken inside.
func (s *Store) retireConnectionGrantsLocked(connectionID, staleRefOnly string) []api.BrokerBindingGrant {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	var out []api.BrokerBindingGrant
	for bindingID, grant := range s.bindingBrokerGrants {
		if grant.ConnectionID != connectionID || (staleRefOnly != "" && grant.CredentialRefID != staleRefOnly) {
			continue
		}
		delete(s.bindingBrokerGrants, bindingID)
		out = append(out, grant)
	}
	return out
}

func (s *Store) revokeGrants(grants []api.BrokerBindingGrant) error {
	if s == nil || s.BrokerRevoke == nil {
		return nil
	}
	var firstErr error
	for _, grant := range grants {
		if err := s.BrokerRevoke(context.Background(), grant); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Store) TeamFor(id string) (api.Team, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	team, ok := s.teams[id]
	return team, ok
}

func (s *Store) HasConnection(id string) bool {
	_, ok := s.connectionFor(id)
	return ok
}

// connectionFor returns a connection record by id without an identity
// check; internal reconciliation paths only.
func (s *Store) connectionFor(id string) (api.Connection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	connection, ok := s.connections[id]
	return connection, ok
}

// credentialFor returns a credential reference record by id — internal
// reconciliation paths only.
func (s *Store) credentialFor(id string) (api.CredentialReference, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reference, ok := s.credentials[id]
	return reference, ok
}

// Connections returns a snapshot of every connection record — the
// reconciler's desired-state list.
func (s *Store) Connections() []api.Connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]api.Connection, 0, len(s.connections))
	for _, connection := range s.connections {
		out = append(out, connection)
	}
	return out
}

func (s *Store) hasDefinitionID(id string) bool {
	for key := range s.definitions {
		if strings.HasPrefix(key, id+"@") {
			return true
		}
	}
	return false
}

// definitionForConnection resolves the definition version a connection's
// surface binding carries — a map-order pick would compare the binding's
// pinned digest against a different version's surface and degrade falsely.
func (s *Store) definitionForConnection(connection api.Connection) (api.ToolDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, binding := range s.bindings {
		if binding.ConnectionID == connection.ConnectionID && binding.Status == api.ActiveStatus && binding.DefinitionVersion != "" {
			if definition, ok := s.definitions[definitionKey(connection.DefinitionID, binding.DefinitionVersion)]; ok {
				return definition, nil
			}
		}
	}
	var picked api.ToolDefinition
	found := false
	for key, definition := range s.definitions {
		if strings.HasPrefix(key, connection.DefinitionID+"@") && (!found || versionLess(picked.Version, definition.Version)) {
			picked, found = definition, true
		}
	}
	if !found {
		return api.ToolDefinition{}, fmt.Errorf("%w: toolhub definition %s", api.ErrNotFound, connection.DefinitionID)
	}
	return picked, nil
}

func (s *Store) PutBinding(binding api.ToolBinding) error {
	if binding.ToolBindingID == "" {
		binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	definition, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
	if !ok {
		return fmt.Errorf("%w: binding definition", api.ErrNotFound)
	}
	if definition.Workload.Class != binding.WorkloadClass {
		return fmt.Errorf("%w: binding workload class", api.ErrInvalid)
	}
	var connection api.Connection
	var hasConnection bool
	var reference api.CredentialReference
	if binding.ConnectionID != "" {
		connection, hasConnection = s.connections[binding.ConnectionID]
		if !hasConnection || connection.DefinitionID != binding.DefinitionID {
			return fmt.Errorf("%w: binding connection", api.ErrInvalid)
		}
		if binding.ConnectionRevision != connection.Revision {
			return fmt.Errorf("%w: binding connection revision", api.ErrStale)
		}
		if binding.CredentialRefID != "" {
			var ok bool
			reference, ok = s.credentials[binding.CredentialRefID]
			if !ok || reference.ConnectionID != connection.ConnectionID || reference.Revision != binding.CredentialRevision || connection.CredentialRefID != reference.CredentialRefID {
				return fmt.Errorf("%w: binding credential revision", api.ErrStale)
			}
		} else if len(definition.Credentials) > 0 {
			return fmt.Errorf("%w: credential-bearing binding needs a credential reference", api.ErrInvalid)
		}
	} else if len(definition.Credentials) > 0 {
		return fmt.Errorf("%w: credential-bearing definition needs a connection", api.ErrInvalid)
	} else if binding.CredentialRefID != "" {
		return fmt.Errorf("%w: anonymous binding cannot carry a credential reference", api.ErrInvalid)
	}
	expectedID := api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if binding.ToolBindingID != expectedID {
		return fmt.Errorf("%w: nondeterministic binding id", api.ErrInvalid)
	}
	if existing, ok := s.bindings[binding.ToolBindingID]; ok && !reflect.DeepEqual(existing, binding) {
		return fmt.Errorf("%w: binding is immutable within a revision", api.ErrConflict)
	}
	key := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
	if binding.ProjectionRevision > s.projectionRevisions[key] {
		s.projectionRevisions[key] = binding.ProjectionRevision
	}
	s.bindings[binding.ToolBindingID] = binding
	return nil
}

func (s *Store) PutWorkloadInstance(workload api.WorkloadInstance) error {
	if err := workload.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[workload.BindingID]
	if !ok || binding.DefinitionID != workload.DefinitionID || binding.DefinitionVersion != workload.DefinitionVersion || binding.WorkloadClass != workload.Class {
		return fmt.Errorf("%w: workload binding", api.ErrInvalid)
	}
	if existing, ok := s.workloads[workload.WorkloadID]; ok && !api.RecordsEqual(existing, workload) {
		return fmt.Errorf("%w: workload identity", api.ErrConflict)
	}
	s.workloads[workload.WorkloadID] = workload
	return nil
}

func (s *Store) RotateCredential(connectionID, backend, locator string, keys []string) (api.CredentialReference, error) {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return api.CredentialReference{}, fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if err := rotatableConnectionLocked(connection); err != nil {
		s.mu.Unlock()
		return api.CredentialReference{}, err
	}
	next := connection.Revision + 1
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connectionID, next), ConnectionID: connectionID, Revision: next, Backend: backend, Locator: locator, Keys: append([]string(nil), keys...), Status: api.ActiveStatus}
	return s.rotateCredentialLocked(connection, reference)
}

// RotateCredentialRecord replaces the connection's credential with a fully
// populated reference (broker backend fields included). The caller sets
// Revision to the expected next connection revision so a concurrent rotation
// fails instead of recording a grant bound to the wrong reference.
func (s *Store) RotateCredentialRecord(reference api.CredentialReference) (api.CredentialReference, error) {
	s.mu.Lock()
	connection, ok := s.connections[reference.ConnectionID]
	if !ok {
		s.mu.Unlock()
		return api.CredentialReference{}, fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if err := rotatableConnectionLocked(connection); err != nil {
		s.mu.Unlock()
		return api.CredentialReference{}, err
	}
	next := connection.Revision + 1
	if reference.Revision != next || reference.CredentialRefID != api.CredentialReferenceID(reference.ConnectionID, next) {
		s.mu.Unlock()
		return api.CredentialReference{}, fmt.Errorf("%w: credential rotation conflict", api.ErrConflict)
	}
	reference.Schema = api.SchemaVersion
	reference.Status = api.ActiveStatus
	return s.rotateCredentialLocked(connection, reference)
}

func (s *Store) rotateCredentialLocked(connection api.Connection, reference api.CredentialReference) (api.CredentialReference, error) {
	if err := reference.Validate(); err != nil {
		s.mu.Unlock()
		return api.CredentialReference{}, err
	}
	var retired []api.BrokerBindingGrant
	if connection.CredentialRefID != "" {
		old := s.credentials[connection.CredentialRefID]
		old.Status = api.RevokedStatus
		s.credentials[old.CredentialRefID] = old
		// Grants minted against the superseded reference cannot acquire the
		// new credential — retire them so members re-mint on next call.
		retired = s.retireConnectionGrantsLocked(connection.ConnectionID, old.CredentialRefID)
	}
	s.credentials[reference.CredentialRefID] = reference
	connection.CredentialRefID = reference.CredentialRefID
	connection.Revision = reference.Revision
	connection.CredentialVisibility = ""
	connection.CredentialCheckedAt = time.Time{}
	connection.HealthCause = ""
	connection.CredentialRetryAt = time.Time{}
	connection.CredentialRetryCount = 0
	connection.RecipeRetryAt = time.Time{}
	connection.RecipeRetryCount = 0
	connection.CredRecipeRetryAt = time.Time{}
	connection.CredRecipeRetryCount = 0
	connection.SurfaceRetryAt = time.Time{}
	connection.SurfaceRetryCount = 0
	// Fresh credentials are the heal for a parked connection: reauth and a
	// dead-credential degrade return to service, and the credential-recipe
	// drift the reconciler observed becomes the new pin — the owner
	// accepting by resubmission is what makes the new contract authoritative.
	if connection.Status == api.ReauthStatus || connection.Status == api.DegradedStatus {
		connection.Status = api.ActiveStatus
	}
	if observed := connection.Metadata[api.LabelAcceptCredential]; observed != "" {
		connection.Metadata[api.LabelCredentialRecipe] = observed
		delete(connection.Metadata, api.LabelAcceptCredential)
	}
	s.connections[connection.ConnectionID] = connection
	s.noteConnectionStatusLocked(connection)
	for id, binding := range s.bindings {
		if binding.ConnectionID == connection.ConnectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connection.ConnectionID)
	s.mu.Unlock()
	stopErr := s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		_ = s.MarkDegraded(connection.ConnectionID)
		return api.CredentialReference{}, err
	}
	return reference, errors.Join(stopErr, s.revokeGrants(retired))
}

func (s *Store) SetConnectionStatus(connectionID string, status api.Status) error {
	if status != api.ActiveStatus && status != api.DisabledStatus && status != api.RevokedStatus && status != api.DeletingStatus && status != api.ReauthStatus {
		return fmt.Errorf("%w: connection status", api.ErrInvalid)
	}
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if connection.Status == api.RevokedStatus && status == api.ActiveStatus {
		s.mu.Unlock()
		return fmt.Errorf("%w: revoked connection cannot be re-enabled", api.ErrRevoked)
	}
	connection.Status = status
	connection.Revision++
	s.connections[connectionID] = connection
	s.noteConnectionStatusLocked(connection)
	for id, binding := range s.bindings {
		if binding.ConnectionID == connectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connectionID)
	var retired []api.BrokerBindingGrant
	if status != api.ActiveStatus {
		retired = s.retireConnectionGrantsLocked(connectionID, "")
	}
	s.mu.Unlock()
	stopErr := s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		return err
	}
	return errors.Join(stopErr, s.revokeGrants(retired))
}

// rotatableConnectionLocked decides whether a connection may accept a
// credential rotation. s.mu must be held.
func rotatableConnectionLocked(connection api.Connection) error {
	if connection.Status != api.ActiveStatus && connection.Status != api.ReauthStatus && connection.Status != api.DegradedStatus {
		return fmt.Errorf("%w: connection is not active", api.ErrRevoked)
	}
	if connection.Status != api.ActiveStatus && (connection.Metadata[api.LabelAcceptSurface] != "" || connection.Metadata[api.LabelAcceptRecipe] != "") {
		return fmt.Errorf("%w: drift accept pending human confirm", api.ErrRevoked)
	}
	return nil
}

// setMigrateRequest records a pending broker migration request on a live
// reference without rotating it — the local locator keeps serving calls until
// the owner submits and the request resolves.
func (s *Store) setMigrateRequest(refID, requestID string) error {
	if !identity.ValidID(requestID) {
		return fmt.Errorf("%w: migrate request", api.ErrInvalid)
	}
	s.mu.Lock()
	reference, ok := s.credentials[refID]
	if !ok || reference.Status != api.ActiveStatus {
		s.mu.Unlock()
		return fmt.Errorf("%w: credential reference", api.ErrNotFound)
	}
	reference.MigrateRequestID = requestID
	s.credentials[refID] = reference
	s.mu.Unlock()
	return s.persistAndNotify()
}

// MarkReauth moves a connection to reauth after credential-relevant drift:
// the credential reference is revoked so no injector can serve the stale
// material, and affected workloads stop. The status mirror follows the
// same path as every other connection mutation.
func (s *Store) MarkReauth(connectionID string) error {
	_, err := s.markReauthIfCurrent(connectionID, nil, "")
	return err
}

func (s *Store) markReauthIfCurrent(connectionID string, expected *api.Connection, observed string) (bool, error) {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		if expected != nil {
			return false, nil
		}
		return false, fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if expected != nil && (connection.Status != expected.Status || connection.Revision != expected.Revision || connection.CredentialRefID != expected.CredentialRefID) {
		s.mu.Unlock()
		return false, nil
	}
	if observed != "" {
		if connection.Metadata == nil {
			connection.Metadata = map[string]string{}
		}
		connection.Metadata[api.LabelAcceptCredential] = observed
	}
	connection.Status = api.ReauthStatus
	connection.Revision++
	s.connections[connectionID] = connection
	s.noteConnectionStatusLocked(connection)
	if connection.CredentialRefID != "" {
		if reference, ok := s.credentials[connection.CredentialRefID]; ok {
			reference.Status = api.RevokedStatus
			s.credentials[connection.CredentialRefID] = reference
		}
	}
	for id, binding := range s.bindings {
		if binding.ConnectionID == connectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connectionID)
	s.mu.Unlock()
	stopErr := s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		return true, err
	}
	return true, stopErr
}

func (s *Store) MarkDegraded(connectionID string) error {
	_, err := s.markDegradedIfCurrent(connectionID, nil, "", "")
	return err
}

func (s *Store) observeCredentialCheckIfCurrent(expected api.Connection, visibility, cause string, at time.Time) (bool, error) {
	s.mu.Lock()
	connection, ok := s.connections[expected.ConnectionID]
	if !ok || connection.Status != expected.Status || connection.Revision != expected.Revision || connection.CredentialRefID != expected.CredentialRefID {
		s.mu.Unlock()
		return false, nil
	}
	connection.CredentialVisibility = visibility
	connection.CredentialCheckedAt = at.UTC()
	connection.HealthCause = cause
	if visibility == "unknown" {
		if connection.CredentialRetryCount < 5 {
			connection.CredentialRetryCount++
		}
		connection.CredentialRetryAt = at.UTC().Add(time.Minute)
		if connection.CredentialRetryCount >= 5 {
			connection.CredentialRetryAt = at.UTC().Add(time.Hour)
		}
	} else {
		connection.CredentialRetryAt = time.Time{}
		connection.CredentialRetryCount = 0
	}
	s.connections[connection.ConnectionID] = connection
	s.mu.Unlock()
	return true, s.persistAndNotify()
}

// retryState selects the persisted retry fields for one reconcile stage;
// every stage shares the same bound: one-minute steps for the first five
// failures, then one-hour cooldowns.
func retryState(connection *api.Connection, stage string) (*time.Time, *int) {
	switch stage {
	case "recipe":
		return &connection.RecipeRetryAt, &connection.RecipeRetryCount
	case "credential_recipe":
		return &connection.CredRecipeRetryAt, &connection.CredRecipeRetryCount
	case "surface":
		return &connection.SurfaceRetryAt, &connection.SurfaceRetryCount
	}
	return nil, nil
}

func (s *Store) deferCheckIfCurrent(expected api.Connection, stage string, at time.Time) (bool, error) {
	s.mu.Lock()
	connection, ok := s.connections[expected.ConnectionID]
	if !ok || connection.Status != expected.Status || connection.Revision != expected.Revision || connection.CredentialRefID != expected.CredentialRefID {
		s.mu.Unlock()
		return false, nil
	}
	retryAt, count := retryState(&connection, stage)
	if *count < 5 {
		*count++
	}
	*retryAt = at.UTC().Add(time.Minute)
	if *count >= 5 {
		*retryAt = at.UTC().Add(time.Hour)
	}
	s.connections[connection.ConnectionID] = connection
	s.mu.Unlock()
	return true, s.persistAndNotify()
}

func (s *Store) clearCheckRetryIfCurrent(expected api.Connection, stage string) (bool, error) {
	s.mu.Lock()
	connection, ok := s.connections[expected.ConnectionID]
	if !ok || connection.Status != expected.Status || connection.Revision != expected.Revision || connection.CredentialRefID != expected.CredentialRefID {
		s.mu.Unlock()
		return false, nil
	}
	retryAt, count := retryState(&connection, stage)
	if *count == 0 {
		s.mu.Unlock()
		return true, nil
	}
	*count = 0
	*retryAt = time.Time{}
	s.connections[connection.ConnectionID] = connection
	s.mu.Unlock()
	return true, s.persistAndNotify()
}

func (s *Store) markDegradedIfCurrent(connectionID string, expected *api.Connection, label, observed string) (bool, error) {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		if expected != nil {
			return false, nil
		}
		return false, fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if expected != nil && (connection.Status != expected.Status || connection.Revision != expected.Revision || connection.CredentialRefID != expected.CredentialRefID) {
		s.mu.Unlock()
		return false, nil
	}
	if label != "" {
		if connection.Metadata == nil {
			connection.Metadata = map[string]string{}
		}
		connection.Metadata[label] = observed
	}
	connection.Status = api.DegradedStatus
	connection.Revision++
	s.connections[connectionID] = connection
	s.noteConnectionStatusLocked(connection)
	for id, binding := range s.bindings {
		if binding.ConnectionID == connectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connectionID)
	s.mu.Unlock()
	stopErr := s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		return true, err
	}
	return true, stopErr
}

func (s *Store) restoreApprovedSurfaceIfCurrent(expected api.Connection, pinned api.ToolBinding) (bool, error) {
	s.mu.Lock()
	connection, ok := s.connections[expected.ConnectionID]
	binding, bound := s.bindings[pinned.ToolBindingID]
	if !ok || !bound || connection.Revision != expected.Revision || connection.Status != api.DegradedStatus ||
		connection.Metadata[api.LabelAcceptSurface] == "" || connection.Metadata[api.LabelAcceptRecipe] != "" ||
		binding.ConnectionID != connection.ConnectionID || binding.Status != api.ActiveStatus || binding.ToolDigest != pinned.ToolDigest {
		s.mu.Unlock()
		return false, nil
	}
	delete(connection.Metadata, api.LabelAcceptSurface)
	connection.Status = api.ActiveStatus
	connection.Revision++
	s.connections[connection.ConnectionID] = connection
	s.noteConnectionStatusLocked(connection)
	for id, candidate := range s.bindings {
		if candidate.ConnectionID == connection.ConnectionID {
			if candidate.Status == api.ActiveStatus && candidate.ToolDigest == pinned.ToolDigest {
				candidate.ConnectionRevision = connection.Revision
				candidate.Revision++
			}
			s.touchProjectionLocked(&candidate)
			s.bindings[id] = candidate
		}
	}
	s.mu.Unlock()
	return true, s.persistAndNotify()
}

// ObserveDrift records a drift the reconciler resolved under an accept key;
// the connection stays parked until a human /confirm re-pins it or fresh
// credentials consume the credential pin.
func (s *Store) ObserveDrift(connectionID, label, value string) error {
	_, err := s.observeDriftIfCurrent(connectionID, nil, label, value)
	return err
}

func (s *Store) observeDriftIfCurrent(connectionID string, expected *api.Connection, label, value string) (bool, error) {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		if expected != nil {
			return false, nil
		}
		return false, fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if expected != nil && (connection.Status != expected.Status || connection.Revision != expected.Revision || connection.CredentialRefID != expected.CredentialRefID) {
		s.mu.Unlock()
		return false, nil
	}
	if connection.Metadata == nil {
		connection.Metadata = map[string]string{}
	}
	connection.Metadata[label] = value
	s.connections[connectionID] = connection
	s.mu.Unlock()
	return true, s.persistAndNotify()
}

// AcceptConnectionDrift applies the human-confirmed accept pins: the observed
// digests become the new trust pins and the drift-parked connection returns
// to service.
func (s *Store) AcceptConnectionDrift(connectionID string) error {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if connection.Status != api.DegradedStatus {
		s.mu.Unlock()
		return fmt.Errorf("%w: no drift pending on connection", api.ErrInvalid)
	}
	if observed := connection.Metadata[api.LabelAcceptSurface]; observed != "" {
		for id, binding := range s.bindings {
			if binding.ConnectionID != connectionID || binding.Status != api.ActiveStatus {
				continue
			}
			binding.ToolDigest = observed
			binding.Revision++
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
		delete(connection.Metadata, api.LabelAcceptSurface)
	}
	if observed := connection.Metadata[api.LabelAcceptRecipe]; observed != "" {
		connection.Metadata[api.LabelRecipe] = observed
		delete(connection.Metadata, api.LabelAcceptRecipe)
	}
	connection.Status = api.ActiveStatus
	connection.Revision++
	s.connections[connectionID] = connection
	s.noteConnectionStatusLocked(connection)
	for id, binding := range s.bindings {
		if binding.ConnectionID == connectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connectionID)
	s.mu.Unlock()
	stopErr := s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		return err
	}
	return stopErr
}

// pinnedBinding returns the live binding that carries a connection's tool
// surface pin — the digest the reconciler checks drift against.
func (s *Store) pinnedBinding(connectionID string) (api.ToolBinding, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, binding := range s.bindings {
		if binding.ConnectionID == connectionID && binding.Status == api.ActiveStatus && binding.ToolDigest != "" {
			return binding, true
		}
	}
	return api.ToolBinding{}, false
}

func (s *Store) SetTerminalExposure(connectionID string, enabled bool) error {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	connection.TerminalExposure = enabled
	connection.Revision++
	s.connections[connectionID] = connection
	s.mu.Unlock()
	return s.persistAndNotify()
}

func (s *Store) SetBindingStatus(bindingID string, status api.Status) error {
	if status != api.ActiveStatus && status != api.DisabledStatus && status != api.RevokedStatus {
		return fmt.Errorf("%w: binding status", api.ErrInvalid)
	}
	s.mu.Lock()
	binding, ok := s.bindings[bindingID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: binding", api.ErrNotFound)
	}
	if binding.Status == api.RevokedStatus && status == api.ActiveStatus {
		s.mu.Unlock()
		return fmt.Errorf("%w: revoked binding cannot be re-enabled", api.ErrRevoked)
	}
	binding.Status = status
	binding.Revision++
	s.touchProjectionLocked(&binding)
	s.bindings[bindingID] = binding
	stopped := s.stopBindingLocked(bindingID)
	var retired []api.BrokerBindingGrant
	if status != api.ActiveStatus {
		retired = s.retireBindingGrantsLocked(bindingID)
	}
	s.mu.Unlock()
	stopErr := s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		return err
	}
	return errors.Join(stopErr, s.revokeGrants(retired))
}

// RemoveBindingRecords drops the binding row and any rows only it referenced:
// the workload instances, the owner connection and its credential reference.
// A connection still named by another live binding is kept — shared
// credentials survive one member's remove. The onboarding tombstone is the
// caller's concern; this only clears the operative rows.
func (s *Store) RemoveBindingRecords(bindingID string) error {
	s.mu.Lock()
	binding, ok := s.bindings[bindingID]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	binding.Status = api.RevokedStatus
	s.touchProjectionLocked(&binding)
	s.bindings[bindingID] = binding
	stopped := s.stopBindingLocked(bindingID)
	delete(s.bindings, bindingID)
	referenced := false
	for _, other := range s.bindings {
		if other.ConnectionID != "" && other.ConnectionID == binding.ConnectionID {
			referenced = true
			break
		}
	}
	if binding.ConnectionID != "" && !referenced {
		connection := s.connections[binding.ConnectionID]
		if connection.Owner.Type != api.TeamOwner {
			delete(s.connections, binding.ConnectionID)
			s.forgetConnectionStatusLocked(binding.ConnectionID)
			for refID, ref := range s.credentials {
				if ref.ConnectionID == binding.ConnectionID {
					delete(s.credentials, refID)
				}
			}
		}
	}
	for id, workload := range s.workloads {
		if workload.BindingID == bindingID {
			delete(s.workloads, id)
		}
	}
	retired := s.retireBindingGrantsLocked(bindingID)
	s.mu.Unlock()
	stopErr := s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		return err
	}
	return errors.Join(stopErr, s.revokeGrants(retired))
}

func (s *Store) Resolve(auth identity.Envelope, bindingID string) (api.EffectiveBinding, error) {
	if auth.CapabilityProfile != "" {
		return api.EffectiveBinding{}, fmt.Errorf("%w: managed lookup requires a capability", api.ErrUnauthorized)
	}
	if err := s.Reload(); err != nil {
		return api.EffectiveBinding{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resolveLocked(auth, bindingID)
}

// Authorize holds the registry read lock through the execution admission
// callback. This makes revoke/rotate win before backend admission, while an
// already admitted call finishes under its recorded revision.
// ponytail: one registry lock serializes authorization and lifecycle mutations;
// use per-connection locks only if measured connector concurrency needs it.
func (s *Store) Authorize(auth identity.Envelope, bindingID string, admit func(api.EffectiveBinding) error) error {
	if auth.CapabilityProfile != "" {
		return fmt.Errorf("%w: managed admission requires a capability and arguments", api.ErrUnauthorized)
	}
	if admit == nil {
		return fmt.Errorf("%w: nil admission callback", api.ErrInvalid)
	}
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	effective, err := s.resolveLocked(auth, bindingID)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := s.noteRecovery(effective); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	effective, err = s.resolveLocked(auth, bindingID)
	if err != nil {
		return err
	}
	return admit(effective)
}

// noteRecovery bounds connector respawns: an admission whose recorded
// workload is known but not running is a restart, and restarts are counted
// on the connection. Exhausting the budget degrades the connection instead
// of restart-looping — a human re-enables it after fixing the cause. An
// absent workload record is first demand, not recovery.
func (s *Store) noteRecovery(effective api.EffectiveBinding) error {
	const recoveryBudget = 3
	if effective.Connection == nil || effective.WorkloadID == "" {
		return nil
	}
	s.mu.Lock()
	workload, known := s.workloads[effective.WorkloadID]
	if !known || workload.Status == api.RunningStatus || workload.Status == api.StartingStatus {
		s.mu.Unlock()
		return nil
	}
	connection, ok := s.connections[effective.Connection.ConnectionID]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	connection.RecoveryCount++
	s.connections[connection.ConnectionID] = connection
	exhausted := connection.RecoveryCount >= recoveryBudget
	s.mu.Unlock()
	if exhausted {
		_, _ = s.markDegradedIfCurrent(connection.ConnectionID, &connection, "", "")
		return fmt.Errorf("%w: recovery budget exhausted", api.ErrDegraded)
	}
	return s.persist()
}

func (s *Store) resolveLocked(auth identity.Envelope, bindingID string) (api.EffectiveBinding, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return api.EffectiveBinding{}, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if !identity.ValidID(bindingID) {
		return api.EffectiveBinding{}, fmt.Errorf("%w: binding id", api.ErrInvalid)
	}
	binding, ok := s.bindings[bindingID]
	if !ok {
		return api.EffectiveBinding{}, fmt.Errorf("%w: binding", api.ErrNotFound)
	}
	if binding.Status == api.RevokedStatus {
		return api.EffectiveBinding{}, fmt.Errorf("%w: binding", api.ErrRevoked)
	}
	if binding.Status != api.ActiveStatus {
		return api.EffectiveBinding{}, fmt.Errorf("%w: binding disabled", api.ErrUnauthorized)
	}
	if binding.PrincipalID != auth.PrincipalID || binding.ContextID != auth.ContextID || binding.RuntimeID != auth.RuntimeID || binding.PolicyVersion != auth.PolicyVersion {
		return api.EffectiveBinding{}, fmt.Errorf("%w: exact runtime owner", api.ErrUnauthorized)
	}
	definition, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
	if !ok {
		return api.EffectiveBinding{}, fmt.Errorf("%w: definition", api.ErrStale)
	}
	if definition.Workload.Class != binding.WorkloadClass {
		return api.EffectiveBinding{}, fmt.Errorf("%w: workload class changed", api.ErrStale)
	}
	effective := api.EffectiveBinding{Binding: binding, Definition: definition}
	if binding.ConnectionID == "" {
		if len(definition.Credentials) > 0 {
			return api.EffectiveBinding{}, fmt.Errorf("%w: missing connection", api.ErrUnauthorized)
		}
		ownerID := ""
		if definition.Workload.Class == api.PerUser {
			ownerID = binding.ToolBindingID
		}
		effective.WorkloadID = api.WorkloadInstanceID(definition.DefinitionID, definition.Workload.Class, ownerID, "")
		return effective, nil
	}
	connection, ok := s.connections[binding.ConnectionID]
	if !ok {
		return api.EffectiveBinding{}, fmt.Errorf("%w: connection", api.ErrStale)
	}
	teamCovered := s.teamConnectionCoversLocked(auth, connection)
	if !connection.Owner.Matches(auth) && !s.sharedConnectionCoversLocked(auth, connection) && !teamCovered {
		return api.EffectiveBinding{}, fmt.Errorf("%w: exact connection owner", api.ErrUnauthorized)
	}
	if connection.Status == api.RevokedStatus {
		return api.EffectiveBinding{}, fmt.Errorf("%w: connection", api.ErrRevoked)
	}
	if connection.Status == api.DegradedStatus {
		return api.EffectiveBinding{}, fmt.Errorf("%w: connection", api.ErrDegraded)
	}
	// A member's team binding follows the connection: the operator's rotation
	// bumps the connection revision and the next call must pick it up without
	// a member reconnect, so the binding's recorded pins are not the check —
	// the connection's current revision and reference are.
	if !teamCovered && (connection.Status != api.ActiveStatus || connection.Revision != binding.ConnectionRevision || connection.DefinitionID != definition.DefinitionID) {
		return api.EffectiveBinding{}, fmt.Errorf("%w: current connection revision", api.ErrStale)
	}
	if teamCovered && (connection.Status != api.ActiveStatus || connection.DefinitionID != definition.DefinitionID) {
		return api.EffectiveBinding{}, fmt.Errorf("%w: current connection revision", api.ErrStale)
	}
	effective.Connection = &connection
	if len(definition.Credentials) > 0 {
		refID, refRevision := binding.CredentialRefID, binding.CredentialRevision
		if teamCovered {
			// Follow the connection's current reference; its own revision is
			// authoritative — a status flip bumps the connection revision
			// without minting a new credential.
			refID = connection.CredentialRefID
			refRevision = 1
		}
		if refID == "" || refRevision == 0 {
			return api.EffectiveBinding{}, fmt.Errorf("%w: current credential reference", api.ErrStale)
		}
		reference, ok := s.credentials[refID]
		if !ok || reference.Status != api.ActiveStatus || reference.ConnectionID != connection.ConnectionID {
			return api.EffectiveBinding{}, fmt.Errorf("%w: current credential revision", api.ErrStale)
		}
		if !teamCovered && reference.Revision != refRevision {
			return api.EffectiveBinding{}, fmt.Errorf("%w: current credential revision", api.ErrStale)
		}
		keys := map[string]bool{}
		for _, key := range reference.Keys {
			keys[key] = true
		}
		if !definition.CredentialsSatisfied(func(name string) bool { return keys[name] }) {
			return api.EffectiveBinding{}, fmt.Errorf("%w: missing credential input", api.ErrUnauthorized)
		}
		effective.Credential = &reference
	}
	ownerID := auth.ContextID
	if definition.Workload.Class == api.Shared {
		ownerID = ""
	}
	if definition.Workload.Class == api.PerUser {
		ownerID += ":" + auth.PrincipalID + ":" + connection.ConnectionID
	}
	effective.WorkloadID = api.WorkloadInstanceID(definition.DefinitionID, definition.Workload.Class, ownerID, "")
	return effective, nil
}

// EnableToolsets stages a binding's extra projected toolsets: the binding
// mutates in place with a revision bump, and the owning projection advances
// so cached tool lists refresh.
func (s *Store) EnableToolsets(auth identity.Envelope, bindingID string, sets []string) error {
	s.mu.Lock()
	binding, ok := s.bindings[bindingID]
	if !ok || binding.PrincipalID != auth.PrincipalID || binding.ContextID != auth.ContextID || binding.RuntimeID != auth.RuntimeID {
		s.mu.Unlock()
		return api.ErrNotFound
	}
	key := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
	binding.EnabledToolsets = append([]string(nil), sets...)
	slices.Sort(binding.EnabledToolsets)
	binding.Revision++
	s.projectionRevisions[key]++
	binding.ProjectionRevision = s.projectionRevisions[key]
	s.bindings[bindingID] = binding
	s.mu.Unlock()
	return s.persistAndNotify()
}

// PutRecipe stores a resolution under its content digest; identical content
// always returns the same digest, so recipes deduplicate across onboardings.
func (s *Store) PutRecipe(resolution api.RecipeResolution) (string, error) {
	digest, err := api.ResolutionDigest(resolution)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	_, known := s.recipes[digest]
	if !known {
		s.recipes[digest] = api.CloneResolution(resolution)
	}
	s.mu.Unlock()
	if known {
		return digest, nil
	}
	if err := s.persist(); err != nil {
		s.mu.Lock()
		delete(s.recipes, digest)
		s.mu.Unlock()
		return "", err
	}
	return digest, nil
}

// Recipe resolves a content-addressed resolution stored by PutRecipe.
func (s *Store) Recipe(digest string) (api.RecipeResolution, error) {
	s.mu.RLock()
	resolution, ok := s.recipes[digest]
	s.mu.RUnlock()
	if !ok {
		return api.RecipeResolution{}, api.ErrNotFound
	}
	return api.CloneResolution(resolution), nil
}

type snapshot struct {
	Schema              int                             `json:"schema"`
	Definitions         []api.ToolDefinition            `json:"definitions"`
	Connections         []api.Connection                `json:"connections"`
	Credentials         []api.CredentialReference       `json:"credential_references"`
	Bindings            []api.ToolBinding               `json:"bindings"`
	Workloads           []api.WorkloadInstance          `json:"workloads"`
	ProjectionRevisions map[string]uint64               `json:"projection_revisions,omitempty"`
	Grants              []api.Grant                     `json:"grants,omitempty"`
	Onboardings         []api.Onboarding                `json:"onboardings,omitempty"`
	Operations          []api.Operation                 `json:"operations,omitempty"`
	Recipes             map[string]api.RecipeResolution `json:"recipes,omitempty"`
	Publications        []api.DefinitionPublication     `json:"publications,omitempty"`
	SharedPolicies      []api.SharedCredentialPolicy    `json:"shared_credential_policies,omitempty"`
	Teams               []api.Team                      `json:"teams,omitempty"`
	BrokerGrants        []api.BrokerBindingGrant        `json:"broker_grants,omitempty"`
	CapabilityChanges   []api.CapabilityChange          `json:"capability_changes,omitempty"`
	BindingChanges      []bindingChange                 `json:"binding_changes,omitempty"`
}

// SharedConsumers counts the active bindings that still consume one shared
// workload: the persisted binding registry is the consumer ledger for a
// shared workload, so demand survives a restart of both the hub and the
// controller process. exceptBindingID excludes the binding being released.
func (s *Store) SharedConsumers(definitionID, version, exceptBindingID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sharedConsumersLocked(definitionID, version, exceptBindingID)
}

// SharedBindingConsumed reports whether a labelled binding belongs to a
// shared workload that still has live consumers — the guard the reconciler
// applies before purging an orphan whose spawning connection is gone.
func (s *Store) SharedBindingConsumed(bindingID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.bindings[bindingID]
	if !ok || binding.WorkloadClass != api.Shared {
		return false
	}
	return s.sharedConsumersLocked(binding.DefinitionID, binding.DefinitionVersion, "") > 0
}

func (s *Store) sharedConsumersLocked(definitionID, version, exceptBindingID string) int {
	count := 0
	for _, binding := range s.bindings {
		if binding.ToolBindingID == exceptBindingID || binding.Status != api.ActiveStatus || binding.DefinitionID != definitionID || binding.DefinitionVersion != version || binding.WorkloadClass != api.Shared {
			continue
		}
		count++
	}
	return count
}

func (s *Store) stopAffectedLocked(connectionID string) []string {
	ids := make([]string, 0)
	for id, workload := range s.workloads {
		binding, ok := s.bindings[workload.BindingID]
		if !ok || binding.ConnectionID != connectionID {
			continue
		}
		if workload.Status == api.RunningStatus || workload.Status == api.StartingStatus {
			workload.Status = api.StoppedStatus
			s.workloads[id] = workload
			ids = append(ids, workload.WorkloadID)
		}
	}
	return ids
}

func (s *Store) stopBindingLocked(bindingID string) []string {
	// A workload governed by a shared connection is keyed to whichever
	// member's binding registered it, so the stop set is the connection's
	// bindings, not only the caller's. It keeps running while another member
	// still holds an active binding — one member's disable removes only that
	// member's projection.
	shared := false
	if binding, ok := s.bindings[bindingID]; ok && binding.ConnectionID != "" {
		if connection, ok := s.connections[binding.ConnectionID]; ok && connection.Metadata["shared_policy"] != "" {
			shared = true
			for _, other := range s.bindings {
				if other.ToolBindingID != bindingID && other.ConnectionID == binding.ConnectionID && other.Status == api.ActiveStatus {
					return nil
				}
			}
		}
	}
	connectionID := ""
	if shared {
		connectionID = s.bindings[bindingID].ConnectionID
	}
	ids := make([]string, 0)
	for id, workload := range s.workloads {
		mine := workload.BindingID == bindingID
		if !mine && shared {
			if other, ok := s.bindings[workload.BindingID]; ok && other.ConnectionID == connectionID {
				mine = true
			}
		}
		if !mine {
			continue
		}
		if workload.Status == api.RunningStatus || workload.Status == api.StartingStatus {
			workload.Status = api.StoppedStatus
			s.workloads[id] = workload
			ids = append(ids, workload.WorkloadID)
		}
	}
	return ids
}

func (s *Store) stopWorkloads(ids []string) error {
	if s == nil || s.Stopper == nil {
		return nil
	}
	var firstErr error
	for _, id := range ids {
		if err := s.Stopper.Stop(id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
