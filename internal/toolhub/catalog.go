package toolhub

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

// CatalogEntry is a secret-free view of one immutable manifest and its exact
// owner-scoped effective binding. It is the replacement data shape for the
// legacy service catalog.
type CatalogEntry struct {
	Name               string        `json:"name"`
	Version            string        `json:"version"`
	Transport          Transport     `json:"transport"`
	WorkloadClass      WorkloadClass `json:"workload_class"`
	Tools              []string      `json:"tools"`
	BindingID          string        `json:"binding_id,omitempty"`
	Status             string        `json:"status"`
	Enabled            bool          `json:"enabled"`
	MissingCredentials []string      `json:"missing_credentials,omitempty"`
}

// Catalog returns only manifest metadata and the current exact-owner status.
// It never infers enablement from a legacy feature list.
func (s *Store) Catalog(auth identity.Envelope) ([]CatalogEntry, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries := make([]CatalogEntry, 0, len(s.definitions))
	for _, definition := range s.definitions {
		if !s.visibleDefinitionLocked(auth, definition) {
			continue
		}
		entry := CatalogEntry{Name: definition.DefinitionID, Version: definition.Version, Transport: definition.Transport, WorkloadClass: definition.Workload.Class, Status: "available"}
		for _, tool := range definition.Tools {
			entry.Tools = append(entry.Tools, tool.Name)
		}
		slices.Sort(entry.Tools)
		binding := s.findBindingLocked(auth, definition)
		if binding == nil {
			entry.MissingCredentials = requiredCredentialNames(definition)
			if len(entry.MissingCredentials) > 0 {
				entry.Status = "missing-connection"
			}
		} else {
			entry.BindingID = binding.ToolBindingID
			entry.Status = string(binding.Status)
			entry.Enabled = binding.Status == ActiveStatus
			if binding.Status == ActiveStatus {
				if _, err := s.resolveLocked(auth, binding.ToolBindingID); err != nil {
					entry.Enabled = false
					entry.Status = "stale"
				}
			}
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b CatalogEntry) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
	return entries, nil
}

func requiredCredentialNames(definition ToolDefinition) []string {
	result := make([]string, 0)
	for _, input := range definition.Credentials {
		if input.Required {
			result = append(result, input.Name)
		}
	}
	slices.Sort(result)
	return result
}

func (s *Store) findBindingLocked(auth identity.Envelope, definition ToolDefinition) *ToolBinding {
	for _, binding := range s.bindings {
		if binding.PrincipalID == auth.PrincipalID && binding.ContextID == auth.ContextID && binding.RuntimeID == auth.RuntimeID && binding.PolicyVersion == auth.PolicyVersion && binding.DefinitionID == definition.DefinitionID && binding.DefinitionVersion == definition.Version {
			copy := binding
			return &copy
		}
	}
	return nil
}

// reusableSelfInstallDefinition returns the user's immutable definition for an
// already reviewed source. Rebuilding the same commit can produce different
// artifact metadata, so retries must reuse the approved record instead of
// attempting to overwrite it.
func (s *Store) reusableSelfInstallDefinition(auth identity.Envelope, source ArtifactSource, definitionID, version string) (ToolDefinition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.definitions[definitionKey(definitionID, version)]
	if !ok || definition.Source.Repository != source.Repository || !strings.EqualFold(definition.Source.CommitSHA, source.CommitSHA) {
		return ToolDefinition{}, false
	}
	publication := s.publicationLocked(definition)
	if publication.Visibility != PublicationUser || publication.OwnerPrincipalID != auth.PrincipalID {
		return ToolDefinition{}, false
	}
	return definition, true
}

// Enable creates the smallest effective binding for an immutable manifest,
// or re-enables an existing non-revoked binding. Connections and credentials
// must already exist and match the authenticated owner.
func (s *Store) Enable(auth identity.Envelope, definitionID, version string) (ToolBinding, error) {
	return s.enableWithReady(context.Background(), auth, definitionID, version, nil)
}

// EnableReady admits a newly materialized workload before its binding is
// persisted and its projection revision is published.
func (s *Store) EnableReady(ctx context.Context, auth identity.Envelope, definitionID, version string, ready func(context.Context, EffectiveBinding) error) (ToolBinding, error) {
	if ready == nil {
		return s.Enable(auth, definitionID, version)
	}
	return s.enableWithReady(ctx, auth, definitionID, version, ready)
}

func (s *Store) enableWithReady(ctx context.Context, auth identity.Envelope, definitionID, version string, ready func(context.Context, EffectiveBinding) error) (ToolBinding, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return ToolBinding{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	s.mu.Lock()
	definition, ok := s.definitions[definitionKey(definitionID, version)]
	if !ok {
		s.mu.Unlock()
		return ToolBinding{}, fmt.Errorf("%w: manifest", ErrNotFound)
	}
	pub := s.publicationLocked(definition)
	if pub.Visibility == PublicationUser && pub.OwnerPrincipalID != auth.PrincipalID {
		s.mu.Unlock()
		return ToolBinding{}, fmt.Errorf("%w: user definition owner", ErrUnauthorized)
	}
	if existing := s.findBindingLocked(auth, definition); existing != nil {
		if existing.Status == RevokedStatus {
			s.mu.Unlock()
			return ToolBinding{}, ErrRevoked
		}
		previous := *existing
		previousProjection := s.projectionRevisions[projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)]
		if existing.Status != ActiveStatus {
			existing.Status = ActiveStatus
			existing.Revision++
			s.touchProjectionLocked(existing)
			s.bindings[existing.ToolBindingID] = *existing
		}
		if ready != nil && existing.Status == ActiveStatus {
			effective, err := s.resolveLocked(auth, existing.ToolBindingID)
			if err == nil {
				err = ready(ctx, effective)
			}
			if err != nil {
				s.bindings[existing.ToolBindingID] = previous
				s.projectionRevisions[projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)] = previousProjection
				s.mu.Unlock()
				return ToolBinding{}, err
			}
		}
		result := *existing
		s.mu.Unlock()
		if err := s.persistAndNotify(); err != nil {
			return ToolBinding{}, err
		}
		return result, nil
	}
	var connection *Connection
	var credential *CredentialReference
	if len(definition.Credentials) > 0 {
		matches := s.matchingOwnerConnectionsLocked(auth, definition)
		if len(matches) == 0 {
			s.mu.Unlock()
			return ToolBinding{}, fmt.Errorf("%w: active owner connection and credential are required", ErrUnauthorized)
		}
		if len(matches) > 1 {
			s.mu.Unlock()
			return ToolBinding{}, fmt.Errorf("%w: ambiguous owner connection", ErrUnauthorized)
		}
		c, r := matches[0].connection, matches[0].credential
		connection, credential = &c, &r
	}
	binding := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: ActiveStatus, Revision: 1, ProjectionRevision: s.projectionRevisions[projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)] + 1}
	if connection != nil {
		binding.ConnectionID, binding.ConnectionRevision = connection.ConnectionID, connection.Revision
		binding.CredentialRefID, binding.CredentialRevision = credential.CredentialRefID, credential.Revision
	}
	binding.ToolBindingID = DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if err := binding.Validate(); err != nil {
		s.mu.Unlock()
		return ToolBinding{}, err
	}
	if ready != nil {
		s.bindings[binding.ToolBindingID] = binding
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err == nil {
			err = ready(ctx, effective)
		}
		if err != nil {
			delete(s.bindings, binding.ToolBindingID)
			s.mu.Unlock()
			return ToolBinding{}, err
		}
	}
	s.bindings[binding.ToolBindingID] = binding
	key := projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)
	if binding.ProjectionRevision > s.projectionRevisions[key] {
		s.projectionRevisions[key] = binding.ProjectionRevision
	}
	s.mu.Unlock()
	if err := s.persistAndNotify(); err != nil {
		return ToolBinding{}, err
	}
	return binding, nil
}

type ownerConnectionMatch struct {
	connection Connection
	credential CredentialReference
}

func (s *Store) matchingOwnerConnectionsLocked(auth identity.Envelope, definition ToolDefinition) []ownerConnectionMatch {
	matches := make([]ownerConnectionMatch, 0)
	for _, candidate := range s.connections {
		if candidate.DefinitionID != definition.DefinitionID || candidate.Status != ActiveStatus || !candidate.Owner.Matches(auth) || candidate.CredentialRefID == "" {
			continue
		}
		reference, found := s.credentials[candidate.CredentialRefID]
		if !found || reference.Status != ActiveStatus || reference.ConnectionID != candidate.ConnectionID {
			continue
		}
		keys := map[string]bool{}
		for _, key := range reference.Keys {
			keys[key] = true
		}
		valid := true
		for _, input := range definition.Credentials {
			if input.Required && !keys[input.Name] {
				valid = false
			}
		}
		if valid {
			matches = append(matches, ownerConnectionMatch{connection: candidate, credential: reference})
		}
	}
	return matches
}

// Disable marks an effective binding disabled; it does not delete historical
// ownership or credential metadata.
func (s *Store) Disable(auth identity.Envelope, definitionID, version string) error {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	s.mu.Lock()
	definition, ok := s.definitions[definitionKey(definitionID, version)]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: manifest", ErrNotFound)
	}
	binding := s.findBindingLocked(auth, definition)
	if binding == nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: binding", ErrNotFound)
	}
	if binding.Status == RevokedStatus {
		s.mu.Unlock()
		return ErrRevoked
	}
	binding.Status = DisabledStatus
	binding.Revision++
	s.touchProjectionLocked(binding)
	s.bindings[binding.ToolBindingID] = *binding
	s.mu.Unlock()
	return s.persistAndNotify()
}
