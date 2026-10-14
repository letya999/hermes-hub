package toolhub

// Package toolhub contains the versioned ToolHub catalog and authorization
// model. It deliberately stops before provider execution and secret material.

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
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
	Stopper             WorkloadStopper
	definitions         map[string]ToolDefinition
	connections         map[string]Connection
	credentials         map[string]CredentialReference
	bindings            map[string]ToolBinding
	workloads           map[string]WorkloadInstance
	projectionRevisions map[string]uint64
	grants              map[string]Grant
	onboardings         map[string]Onboarding
	publications        map[string]DefinitionPublication
	sharedPolicies      map[string]SharedCredentialPolicy
	capabilityPolicies  map[string]CapabilityPolicy
	capabilityProfiles  map[string]CapabilityProfile
	capabilityChanges   []capabilityChange
	bindingChanges      []bindingChange
	// exMu guards the exclusion set separately: exclusions are observability
	// written from read-locked projection scans, so they must not ride s.mu.
	exMu           sync.Mutex
	exclusions     map[string]ProjectionExclusion
	exclusionOrder []string
}

func projectionKey(principalID, contextID, runtimeID string) string {
	return principalID + "\x00" + contextID + "\x00" + runtimeID
}

func (s *Store) touchProjectionLocked(binding *ToolBinding) uint64 {
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
		return 0, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.projectionRevisions[projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)], nil
}

// ProjectionRevisions snapshots every runtime projection revision. Keys come
// only from persisted bindings, so a token-file enrolment alone never creates
// a target.
func (s *Store) ProjectionRevisions() ([]ProjectionTarget, error) {
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProjectionTarget, 0, len(s.projectionRevisions))
	for key, revision := range s.projectionRevisions {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) != 3 || revision == 0 {
			continue
		}
		out = append(out, ProjectionTarget{PrincipalID: parts[0], ContextID: parts[1], RuntimeID: parts[2], Revision: revision})
	}
	return out, nil
}

func definitionKey(id, version string) string { return id + "@" + version }

func (s *Store) RegisterDefinition(definition ToolDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	// The immutable definition must not share mutable schema/rule slices with
	// its caller. Validate above ensures every embedded JSON document is valid.
	body, err := json.Marshal(definition)
	if err != nil {
		return err
	}
	var owned ToolDefinition
	if err := json.Unmarshal(body, &owned); err != nil {
		return err
	}
	definition = owned
	s.mu.Lock()
	defer s.mu.Unlock()
	key := definitionKey(definition.DefinitionID, definition.Version)
	if existing, ok := s.definitions[key]; ok && !definitionsEqual(existing, definition) {
		return fmt.Errorf("%w: immutable definition %s", ErrConflict, key)
	}
	s.definitions[key] = definition
	return nil
}

func (s *Store) PutCredentialReference(reference CredentialReference) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.credentials[reference.CredentialRefID]; ok && !recordsEqual(existing, reference) {
		return fmt.Errorf("%w: immutable credential reference", ErrConflict)
	}
	s.credentials[reference.CredentialRefID] = reference
	return nil
}

func (s *Store) PutConnection(connection Connection) error {
	if err := connection.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasDefinitionID(connection.DefinitionID) {
		return fmt.Errorf("%w: connection definition", ErrNotFound)
	}
	if connection.CredentialRefID != "" {
		reference, ok := s.credentials[connection.CredentialRefID]
		if !ok || reference.ConnectionID != connection.ConnectionID {
			return fmt.Errorf("%w: connection credential reference", ErrInvalid)
		}
	}
	if existing, ok := s.connections[connection.ConnectionID]; ok && !recordsEqual(existing, connection) {
		return fmt.Errorf("%w: connection is immutable within a revision", ErrConflict)
	}
	s.connections[connection.ConnectionID] = connection
	return nil
}

func (s *Store) hasDefinitionID(id string) bool {
	for key := range s.definitions {
		if strings.HasPrefix(key, id+"@") {
			return true
		}
	}
	return false
}

func (s *Store) PutBinding(binding ToolBinding) error {
	if binding.ToolBindingID == "" {
		binding.ToolBindingID = DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	definition, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
	if !ok {
		return fmt.Errorf("%w: binding definition", ErrNotFound)
	}
	if definition.Workload.Class != binding.WorkloadClass {
		return fmt.Errorf("%w: binding workload class", ErrInvalid)
	}
	var connection Connection
	var hasConnection bool
	var reference CredentialReference
	if binding.ConnectionID != "" {
		connection, hasConnection = s.connections[binding.ConnectionID]
		if !hasConnection || connection.DefinitionID != binding.DefinitionID {
			return fmt.Errorf("%w: binding connection", ErrInvalid)
		}
		if binding.ConnectionRevision != connection.Revision {
			return fmt.Errorf("%w: binding connection revision", ErrStale)
		}
		if binding.CredentialRefID != "" {
			var ok bool
			reference, ok = s.credentials[binding.CredentialRefID]
			if !ok || reference.ConnectionID != connection.ConnectionID || reference.Revision != binding.CredentialRevision || connection.CredentialRefID != reference.CredentialRefID {
				return fmt.Errorf("%w: binding credential revision", ErrStale)
			}
		} else if len(definition.Credentials) > 0 {
			return fmt.Errorf("%w: credential-bearing binding needs a credential reference", ErrInvalid)
		}
	} else if len(definition.Credentials) > 0 {
		return fmt.Errorf("%w: credential-bearing definition needs a connection", ErrInvalid)
	} else if binding.CredentialRefID != "" {
		return fmt.Errorf("%w: anonymous binding cannot carry a credential reference", ErrInvalid)
	}
	expectedID := DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if binding.ToolBindingID != expectedID {
		return fmt.Errorf("%w: nondeterministic binding id", ErrInvalid)
	}
	if existing, ok := s.bindings[binding.ToolBindingID]; ok && existing != binding {
		return fmt.Errorf("%w: binding is immutable within a revision", ErrConflict)
	}
	key := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
	if binding.ProjectionRevision > s.projectionRevisions[key] {
		s.projectionRevisions[key] = binding.ProjectionRevision
	}
	s.bindings[binding.ToolBindingID] = binding
	return nil
}

func (s *Store) PutWorkloadInstance(workload WorkloadInstance) error {
	if err := workload.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[workload.BindingID]
	if !ok || binding.DefinitionID != workload.DefinitionID || binding.DefinitionVersion != workload.DefinitionVersion || binding.WorkloadClass != workload.Class {
		return fmt.Errorf("%w: workload binding", ErrInvalid)
	}
	if existing, ok := s.workloads[workload.WorkloadID]; ok && !recordsEqual(existing, workload) {
		return fmt.Errorf("%w: workload identity", ErrConflict)
	}
	s.workloads[workload.WorkloadID] = workload
	return nil
}

func (s *Store) RotateCredential(connectionID, backend, locator string, keys []string) (CredentialReference, error) {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: connection", ErrNotFound)
	}
	if connection.Status != ActiveStatus {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: connection is not active", ErrRevoked)
	}
	next := connection.Revision + 1
	reference := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID(connectionID, next), ConnectionID: connectionID, Revision: next, Backend: backend, Locator: locator, Keys: append([]string(nil), keys...), Status: ActiveStatus}
	return s.rotateCredentialLocked(connection, reference)
}

// RotateCredentialRecord replaces the connection's credential with a fully
// populated reference (broker backend fields included). The caller sets
// Revision to the expected next connection revision so a concurrent rotation
// fails instead of recording a grant bound to the wrong reference.
func (s *Store) RotateCredentialRecord(reference CredentialReference) (CredentialReference, error) {
	s.mu.Lock()
	connection, ok := s.connections[reference.ConnectionID]
	if !ok {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: connection", ErrNotFound)
	}
	if connection.Status != ActiveStatus {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: connection is not active", ErrRevoked)
	}
	next := connection.Revision + 1
	if reference.Revision != next || reference.CredentialRefID != CredentialReferenceID(reference.ConnectionID, next) {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: credential rotation conflict", ErrConflict)
	}
	reference.Schema = SchemaVersion
	reference.Status = ActiveStatus
	return s.rotateCredentialLocked(connection, reference)
}

func (s *Store) rotateCredentialLocked(connection Connection, reference CredentialReference) (CredentialReference, error) {
	if err := reference.Validate(); err != nil {
		s.mu.Unlock()
		return CredentialReference{}, err
	}
	if connection.CredentialRefID != "" {
		old := s.credentials[connection.CredentialRefID]
		old.Status = RevokedStatus
		s.credentials[old.CredentialRefID] = old
	}
	s.credentials[reference.CredentialRefID] = reference
	connection.CredentialRefID = reference.CredentialRefID
	connection.Revision = reference.Revision
	s.connections[connection.ConnectionID] = connection
	for id, binding := range s.bindings {
		if binding.ConnectionID == connection.ConnectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connection.ConnectionID)
	s.mu.Unlock()
	s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		_ = s.MarkDegraded(connection.ConnectionID)
		return CredentialReference{}, err
	}
	return reference, nil
}

func (s *Store) SetConnectionStatus(connectionID string, status Status) error {
	if status != ActiveStatus && status != DisabledStatus && status != RevokedStatus {
		return fmt.Errorf("%w: connection status", ErrInvalid)
	}
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", ErrNotFound)
	}
	if connection.Status == RevokedStatus && status == ActiveStatus {
		s.mu.Unlock()
		return fmt.Errorf("%w: revoked connection cannot be re-enabled", ErrRevoked)
	}
	connection.Status = status
	connection.Revision++
	s.connections[connectionID] = connection
	for id, binding := range s.bindings {
		if binding.ConnectionID == connectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connectionID)
	s.mu.Unlock()
	s.stopWorkloads(stopped)
	return s.persistAndNotify()
}

func (s *Store) MarkDegraded(connectionID string) error {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", ErrNotFound)
	}
	connection.Status = DegradedStatus
	connection.Revision++
	s.connections[connectionID] = connection
	for id, binding := range s.bindings {
		if binding.ConnectionID == connectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connectionID)
	s.mu.Unlock()
	s.stopWorkloads(stopped)
	return s.persistAndNotify()
}

func (s *Store) SetTerminalExposure(connectionID string, enabled bool) error {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", ErrNotFound)
	}
	connection.TerminalExposure = enabled
	connection.Revision++
	s.connections[connectionID] = connection
	s.mu.Unlock()
	return s.persistAndNotify()
}

func (s *Store) SetBindingStatus(bindingID string, status Status) error {
	if status != ActiveStatus && status != DisabledStatus && status != RevokedStatus {
		return fmt.Errorf("%w: binding status", ErrInvalid)
	}
	s.mu.Lock()
	binding, ok := s.bindings[bindingID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: binding", ErrNotFound)
	}
	if binding.Status == RevokedStatus && status == ActiveStatus {
		s.mu.Unlock()
		return fmt.Errorf("%w: revoked binding cannot be re-enabled", ErrRevoked)
	}
	binding.Status = status
	binding.Revision++
	s.touchProjectionLocked(&binding)
	s.bindings[bindingID] = binding
	stopped := s.stopBindingLocked(bindingID)
	s.mu.Unlock()
	s.stopWorkloads(stopped)
	return s.persistAndNotify()
}

func (s *Store) Resolve(auth identity.Envelope, bindingID string) (EffectiveBinding, error) {
	if auth.CapabilityProfile != "" {
		return EffectiveBinding{}, fmt.Errorf("%w: managed lookup requires a capability", ErrUnauthorized)
	}
	if err := s.Reload(); err != nil {
		return EffectiveBinding{}, err
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
func (s *Store) Authorize(auth identity.Envelope, bindingID string, admit func(EffectiveBinding) error) error {
	if auth.CapabilityProfile != "" {
		return fmt.Errorf("%w: managed admission requires a capability and arguments", ErrUnauthorized)
	}
	if admit == nil {
		return fmt.Errorf("%w: nil admission callback", ErrInvalid)
	}
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	effective, err := s.resolveLocked(auth, bindingID)
	if err != nil {
		return err
	}
	return admit(effective)
}

func (s *Store) resolveLocked(auth identity.Envelope, bindingID string) (EffectiveBinding, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return EffectiveBinding{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if !identity.ValidID(bindingID) {
		return EffectiveBinding{}, fmt.Errorf("%w: binding id", ErrInvalid)
	}
	binding, ok := s.bindings[bindingID]
	if !ok {
		return EffectiveBinding{}, fmt.Errorf("%w: binding", ErrNotFound)
	}
	if binding.Status == RevokedStatus {
		return EffectiveBinding{}, fmt.Errorf("%w: binding", ErrRevoked)
	}
	if binding.Status != ActiveStatus {
		return EffectiveBinding{}, fmt.Errorf("%w: binding disabled", ErrUnauthorized)
	}
	if binding.PrincipalID != auth.PrincipalID || binding.ContextID != auth.ContextID || binding.RuntimeID != auth.RuntimeID || binding.PolicyVersion != auth.PolicyVersion {
		return EffectiveBinding{}, fmt.Errorf("%w: exact runtime owner", ErrUnauthorized)
	}
	definition, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
	if !ok {
		return EffectiveBinding{}, fmt.Errorf("%w: definition", ErrStale)
	}
	if definition.Workload.Class != binding.WorkloadClass {
		return EffectiveBinding{}, fmt.Errorf("%w: workload class changed", ErrStale)
	}
	effective := EffectiveBinding{Binding: binding, Definition: definition}
	if binding.ConnectionID == "" {
		if len(definition.Credentials) > 0 {
			return EffectiveBinding{}, fmt.Errorf("%w: missing connection", ErrUnauthorized)
		}
		ownerID := ""
		if definition.Workload.Class == PerUser {
			ownerID = binding.ToolBindingID
		}
		effective.WorkloadID = WorkloadInstanceID(definition.DefinitionID, definition.Workload.Class, ownerID, "")
		return effective, nil
	}
	connection, ok := s.connections[binding.ConnectionID]
	if !ok {
		return EffectiveBinding{}, fmt.Errorf("%w: connection", ErrStale)
	}
	if !connection.Owner.Matches(auth) {
		return EffectiveBinding{}, fmt.Errorf("%w: exact connection owner", ErrUnauthorized)
	}
	if connection.Status == RevokedStatus {
		return EffectiveBinding{}, fmt.Errorf("%w: connection", ErrRevoked)
	}
	if connection.Status == DegradedStatus {
		return EffectiveBinding{}, fmt.Errorf("%w: connection", ErrDegraded)
	}
	if connection.Status != ActiveStatus || connection.Revision != binding.ConnectionRevision || connection.DefinitionID != definition.DefinitionID {
		return EffectiveBinding{}, fmt.Errorf("%w: current connection revision", ErrStale)
	}
	effective.Connection = &connection
	if len(definition.Credentials) > 0 {
		if binding.CredentialRefID == "" || connection.CredentialRefID != binding.CredentialRefID || binding.CredentialRevision == 0 {
			return EffectiveBinding{}, fmt.Errorf("%w: current credential reference", ErrStale)
		}
		reference, ok := s.credentials[binding.CredentialRefID]
		if !ok || reference.Status != ActiveStatus || reference.ConnectionID != connection.ConnectionID || reference.Revision != binding.CredentialRevision {
			return EffectiveBinding{}, fmt.Errorf("%w: current credential revision", ErrStale)
		}
		keys := map[string]bool{}
		for _, key := range reference.Keys {
			keys[key] = true
		}
		if !definition.credentialsSatisfied(func(name string) bool { return keys[name] }) {
			return EffectiveBinding{}, fmt.Errorf("%w: missing credential input", ErrUnauthorized)
		}
		effective.Credential = &reference
	}
	ownerID := auth.ContextID
	if definition.Workload.Class == Shared {
		ownerID = ""
	}
	if definition.Workload.Class == PerUser {
		ownerID += ":" + auth.PrincipalID + ":" + connection.ConnectionID
	}
	effective.WorkloadID = WorkloadInstanceID(definition.DefinitionID, definition.Workload.Class, ownerID, "")
	return effective, nil
}

type snapshot struct {
	Schema              int                      `json:"schema"`
	Definitions         []ToolDefinition         `json:"definitions"`
	Connections         []Connection             `json:"connections"`
	Credentials         []CredentialReference    `json:"credential_references"`
	Bindings            []ToolBinding            `json:"bindings"`
	Workloads           []WorkloadInstance       `json:"workloads"`
	ProjectionRevisions map[string]uint64        `json:"projection_revisions,omitempty"`
	Grants              []Grant                  `json:"grants,omitempty"`
	Onboardings         []Onboarding             `json:"onboardings,omitempty"`
	Publications        []DefinitionPublication  `json:"publications,omitempty"`
	SharedPolicies      []SharedCredentialPolicy `json:"shared_credential_policies,omitempty"`
	CapabilityChanges   []capabilityChange       `json:"capability_changes,omitempty"`
	BindingChanges      []bindingChange          `json:"binding_changes,omitempty"`
}

func (s *Store) stopAffectedLocked(connectionID string) []string {
	ids := make([]string, 0)
	for id, workload := range s.workloads {
		binding, ok := s.bindings[workload.BindingID]
		if !ok || binding.ConnectionID != connectionID {
			continue
		}
		if workload.Status == RunningStatus || workload.Status == StartingStatus {
			workload.Status = StoppedStatus
			s.workloads[id] = workload
			ids = append(ids, workload.WorkloadID)
		}
	}
	return ids
}

func (s *Store) stopBindingLocked(bindingID string) []string {
	ids := make([]string, 0)
	for id, workload := range s.workloads {
		if workload.BindingID != bindingID {
			continue
		}
		if workload.Status == RunningStatus || workload.Status == StartingStatus {
			workload.Status = StoppedStatus
			s.workloads[id] = workload
			ids = append(ids, workload.WorkloadID)
		}
	}
	return ids
}

func (s *Store) stopWorkloads(ids []string) {
	if s == nil || s.Stopper == nil {
		return
	}
	for _, id := range ids {
		_ = s.Stopper.Stop(id)
	}
}
