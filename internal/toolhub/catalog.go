package toolhub

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"slices"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

// CatalogEntry is a secret-free view of one immutable manifest and its exact
// owner-scoped effective binding. It is the replacement data shape for the
// legacy service catalog.
type CatalogEntry struct {
	Name               string            `json:"name"`
	Version            string            `json:"version"`
	Transport          api.Transport     `json:"transport"`
	WorkloadClass      api.WorkloadClass `json:"workload_class"`
	Tools              []string          `json:"tools"`
	BindingID          string            `json:"binding_id,omitempty"`
	Status             string            `json:"status"`
	Enabled            bool              `json:"enabled"`
	MissingCredentials []string          `json:"missing_credentials,omitempty"`
}

// Catalog returns only manifest metadata and the current exact-owner status.
// It never infers enablement from a legacy feature list.
func (s *Store) Catalog(auth identity.Envelope) ([]CatalogEntry, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if auth.CapabilityProfile != "" {
		tools, err := s.managedToolsLocked(auth)
		if err != nil {
			return nil, err
		}
		byDefinition := map[string]*CatalogEntry{}
		for _, tool := range tools {
			key := definitionKey(tool.DefinitionID, tool.Version)
			entry := byDefinition[key]
			if entry == nil {
				definition := s.definitions[key]
				entry = &CatalogEntry{Name: tool.DefinitionID, Version: tool.Version, Transport: definition.Transport, WorkloadClass: definition.Workload.Class, Enabled: true, Status: string(api.ActiveStatus)}
				byDefinition[key] = entry
			}
			entry.Tools = append(entry.Tools, tool.Name)
		}
		entries := make([]CatalogEntry, 0, len(byDefinition))
		for _, entry := range byDefinition {
			slices.Sort(entry.Tools)
			entries = append(entries, *entry)
		}
		slices.SortFunc(entries, func(a, b CatalogEntry) int {
			return strings.Compare(definitionKey(a.Name, a.Version), definitionKey(b.Name, b.Version))
		})
		return entries, nil
	}
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
			entry.Enabled = binding.Status == api.ActiveStatus
			if binding.Status == api.ActiveStatus {
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

func requiredCredentialNames(definition api.ToolDefinition) []string {
	result := make([]string, 0)
	for _, input := range definition.Credentials {
		if input.Required && !input.PerRequest && contractCoverable(input) {
			result = append(result, input.Name)
		}
	}
	slices.Sort(result)
	return result
}

func (s *Store) findBindingLocked(auth identity.Envelope, definition api.ToolDefinition) *api.ToolBinding {
	var tombstone *api.ToolBinding
	for _, binding := range s.bindings {
		if binding.PrincipalID == auth.PrincipalID && binding.ContextID == auth.ContextID && binding.RuntimeID == auth.RuntimeID && binding.PolicyVersion == auth.PolicyVersion && binding.DefinitionID == definition.DefinitionID && binding.DefinitionVersion == definition.Version {
			if binding.Status == api.RevokedStatus {
				if tombstone == nil {
					copy := binding
					tombstone = &copy
				}
				continue
			}
			copy := binding
			return &copy
		}
	}
	return tombstone
}

// reusableSelfInstallDefinition returns the user's immutable definition for an
// already reviewed source. Rebuilding the same commit can produce different
// artifact metadata, so retries must reuse the approved record instead of
// attempting to overwrite it. The newest matching version wins: a definition
// superseded by a contract-bound revision must not be resurrected.
func (s *Store) reusableSelfInstallDefinition(auth identity.Envelope, source api.ArtifactSource, definitionID, version string) (api.ToolDefinition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best api.ToolDefinition
	found := false
	for _, definition := range s.definitions {
		if definition.DefinitionID != definitionID || versionLess(definition.Version, version) || definition.Source.Repository != source.Repository || definition.Source.Subfolder != source.Subfolder || !strings.EqualFold(definition.Source.CommitSHA, source.CommitSHA) {
			continue
		}
		publication := s.publicationLocked(definition)
		if publication.Visibility != api.PublicationUser || publication.OwnerPrincipalID != auth.PrincipalID {
			continue
		}
		if !found || versionLess(best.Version, definition.Version) {
			best, found = definition, true
		}
	}
	return best, found
}

// versionLess compares dot-separated numeric versions ("0.0.10" > "0.0.2").
// Non-numeric components fall back to lexical order.
func versionLess(a, b string) bool {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		an, aErr := strconv.Atoi(ap[i])
		bn, bErr := strconv.Atoi(bp[i])
		if aErr == nil && bErr == nil {
			if an != bn {
				return an < bn
			}
			continue
		}
		if ap[i] != bp[i] {
			return ap[i] < bp[i]
		}
	}
	return len(ap) < len(bp)
}

// nextPatchVersion bumps the last numeric component of a version string.
func nextPatchVersion(version string) string {
	parts := strings.Split(version, ".")
	for i := len(parts) - 1; i >= 0; i-- {
		if n, err := strconv.Atoi(parts[i]); err == nil {
			parts[i] = strconv.Itoa(n + 1)
			return strings.Join(parts, ".")
		}
	}
	return version + ".1"
}

// Enable creates the smallest effective binding for an immutable manifest,
// or re-enables an existing non-revoked binding. Connections and credentials
// must already exist and match the authenticated owner.
func (s *Store) Enable(auth identity.Envelope, definitionID, version string) (api.ToolBinding, error) {
	return s.enableWithReady(context.Background(), auth, definitionID, version, nil)
}

// EnableReady admits a newly materialized workload before its binding is
// persisted and its projection revision is published.
func (s *Store) EnableReady(ctx context.Context, auth identity.Envelope, definitionID, version string, ready func(context.Context, api.EffectiveBinding) error) (api.ToolBinding, error) {
	if ready == nil {
		return s.Enable(auth, definitionID, version)
	}
	return s.enableWithReady(ctx, auth, definitionID, version, ready)
}

func (s *Store) enableWithReady(ctx context.Context, auth identity.Envelope, definitionID, version string, ready func(context.Context, api.EffectiveBinding) error) (api.ToolBinding, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return api.ToolBinding{}, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	s.mu.Lock()
	definition, ok := s.definitions[definitionKey(definitionID, version)]
	if !ok {
		s.mu.Unlock()
		return api.ToolBinding{}, fmt.Errorf("%w: manifest", api.ErrNotFound)
	}
	pub := s.publicationLocked(definition)
	if pub.Visibility == api.PublicationUser && pub.OwnerPrincipalID != auth.PrincipalID {
		s.mu.Unlock()
		return api.ToolBinding{}, fmt.Errorf("%w: user definition owner", api.ErrUnauthorized)
	}
	var connection *api.Connection
	var credential *api.CredentialReference
	if len(definition.Credentials) > 0 {
		matches := s.matchingOwnerConnectionsLocked(auth, definition)
		if len(matches) == 0 {
			s.mu.Unlock()
			return api.ToolBinding{}, fmt.Errorf("%w: active owner connection and credential are required", api.ErrUnauthorized)
		}
		if len(matches) > 1 {
			s.mu.Unlock()
			return api.ToolBinding{}, fmt.Errorf("%w: ambiguous owner connection", api.ErrUnauthorized)
		}
		c, r := matches[0].connection, matches[0].credential
		connection, credential = &c, &r
	}
	if existing := s.findBindingLocked(auth, definition); existing != nil && existing.Status != api.RevokedStatus {
		current := connection == nil || (existing.ConnectionID == connection.ConnectionID && existing.CredentialRefID == credential.CredentialRefID)
		if !current {
			// The owner connection was rotated or superseded; the stale
			// binding can never resolve again, so revoke it before
			// materializing the replacement below.
			stale := *existing
			stale.Status = api.RevokedStatus
			stale.Revision++
			s.touchProjectionLocked(&stale)
			s.bindings[stale.ToolBindingID] = stale
		} else {
			key := projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)
			previous := *existing
			previousProjection := s.projectionRevisions[key]
			if existing.ToolDigest == "" {
				existing.ToolDigest = api.ToolSurfaceDigest(definition.Tools)
				s.bindings[existing.ToolBindingID] = *existing
			}
			if existing.Status != api.ActiveStatus {
				existing.Status = api.ActiveStatus
				existing.Revision++
				s.touchProjectionLocked(existing)
				s.bindings[existing.ToolBindingID] = *existing
			}
			// The recheck probe runs outside the store lock for the same
			// reason the new-binding reservation does: a slow remote probe
			// must not serialize other principals' store access. On failure
			// only this reservation is rolled back (Revision compare-and-swap).
			if ready != nil && existing.Status == api.ActiveStatus {
				effective, err := s.resolveLocked(auth, existing.ToolBindingID)
				reserved := existing.Revision
				s.mu.Unlock()
				if err == nil {
					err = ready(ctx, effective)
				}
				if err != nil {
					s.mu.Lock()
					if cur, ok := s.bindings[existing.ToolBindingID]; ok && cur.Revision == reserved {
						s.bindings[existing.ToolBindingID] = previous
						s.projectionRevisions[key] = previousProjection
					}
					s.mu.Unlock()
					return api.ToolBinding{}, err
				}
				s.mu.Lock()
				// Revoke wins: a connection revoked while the probe ran must
				// not be re-enabled by the recheck commit.
				if existing.ConnectionID != "" {
					current, ok := s.connections[existing.ConnectionID]
					if !ok || current.Status != api.ActiveStatus {
						s.mu.Unlock()
						return api.ToolBinding{}, fmt.Errorf("%w: connection changed during readiness", api.ErrRevoked)
					}
				}
			}
			s.supersedeOtherBindingsLocked(auth, definitionID, existing.ToolBindingID)
			result := s.bindings[existing.ToolBindingID]
			s.mu.Unlock()
			if err := s.persistAndNotify(); err != nil {
				return api.ToolBinding{}, err
			}
			return result, nil
		}
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: s.projectionRevisions[projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)] + 1, ToolDigest: api.ToolSurfaceDigest(definition.Tools)}
	if connection != nil {
		binding.ConnectionID, binding.ConnectionRevision = connection.ConnectionID, connection.Revision
		binding.CredentialRefID, binding.CredentialRevision = credential.CredentialRefID, credential.Revision
	}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if err := binding.Validate(); err != nil {
		s.mu.Unlock()
		return api.ToolBinding{}, err
	}
	if previous, ok := s.bindings[binding.ToolBindingID]; ok && previous.Status == api.RevokedStatus {
		s.mu.Unlock()
		return api.ToolBinding{}, api.ErrRevoked
	}
	if ready != nil {
		// Reserve the binding, then run readiness outside the store lock: a
		// slow remote probe must not serialize every other principal's store
		// access. A failed probe deletes only its own reservation (the
		// compare-and-swap is Revision equality).
		s.bindings[binding.ToolBindingID] = binding
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		s.mu.Unlock()
		if err == nil {
			err = ready(ctx, effective)
		}
		if err != nil {
			s.mu.Lock()
			if cur, ok := s.bindings[binding.ToolBindingID]; ok && cur.Revision == binding.Revision {
				delete(s.bindings, binding.ToolBindingID)
			}
			s.mu.Unlock()
			return api.ToolBinding{}, err
		}
		s.mu.Lock()
		// The probe ran unlocked; a revoke in that window already bumped the
		// connection revision, so committing the reserved binding now would
		// resurrect a dead authority. Revoke wins: drop the reservation and
		// fail the enable.
		if binding.ConnectionID != "" {
			current, ok := s.connections[binding.ConnectionID]
			if !ok || current.Revision != binding.ConnectionRevision || current.Status != api.ActiveStatus {
				delete(s.bindings, binding.ToolBindingID)
				s.mu.Unlock()
				return api.ToolBinding{}, fmt.Errorf("%w: connection changed during readiness", api.ErrRevoked)
			}
		}
	}
	s.bindings[binding.ToolBindingID] = binding
	s.supersedeOtherBindingsLocked(auth, definitionID, binding.ToolBindingID)
	key := projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)
	if binding.ProjectionRevision > s.projectionRevisions[key] {
		s.projectionRevisions[key] = binding.ProjectionRevision
	}
	s.mu.Unlock()
	if err := s.persistAndNotify(); err != nil {
		return api.ToolBinding{}, err
	}
	return binding, nil
}

// supersedeOtherBindingsLocked disables every other active binding of the
// same definition owned by this principal, including versions bound under an
// older policy revision. One definition projects at most one enabled binding:
// sibling versions would otherwise multiply the same hub-* tool name in the
// caller's surface or collide on it outright. s.mu must be held. Returns the
// number of bindings it disabled.
func (s *Store) supersedeOtherBindingsLocked(auth identity.Envelope, definitionID, keepBindingID string) int {
	disabled := 0
	for id, other := range s.bindings {
		if id == keepBindingID || other.DefinitionID != definitionID || other.Status != api.ActiveStatus {
			continue
		}
		if other.PrincipalID != auth.PrincipalID || other.ContextID != auth.ContextID || other.RuntimeID != auth.RuntimeID {
			continue
		}
		other.Status = api.DisabledStatus
		other.Revision++
		s.touchProjectionLocked(&other)
		s.bindings[id] = other
		disabled++
	}
	return disabled
}

// SupersedeSiblings disables sibling bindings of the same definition while
// keeping keepBindingID active. It is the dedupe path for callers that hold an
// already-active binding and therefore never reach enableWithReady — a
// re-enable on such a binding must still retire stale sibling versions or
// their projected tools keep multiplying in the caller's surface.
func (s *Store) SupersedeSiblings(auth identity.Envelope, definitionID, keepBindingID string) error {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	s.mu.Lock()
	changed := s.supersedeOtherBindingsLocked(auth, definitionID, keepBindingID)
	s.mu.Unlock()
	if changed == 0 {
		return nil
	}
	return s.persistAndNotify()
}

type ownerConnectionMatch struct {
	connection api.Connection
	credential api.CredentialReference
}

func (s *Store) matchingOwnerConnectionsLocked(auth identity.Envelope, definition api.ToolDefinition) []ownerConnectionMatch {
	matches := make([]ownerConnectionMatch, 0)
	for _, candidate := range s.connections {
		if candidate.DefinitionID != definition.DefinitionID || candidate.Status != api.ActiveStatus || candidate.CredentialRefID == "" {
			continue
		}
		if !candidate.Owner.Matches(auth) && !s.sharedConnectionCoversLocked(auth, candidate) {
			continue
		}
		reference, found := s.credentials[candidate.CredentialRefID]
		if !found || reference.Status != api.ActiveStatus || reference.ConnectionID != candidate.ConnectionID {
			continue
		}
		keys := map[string]bool{}
		for _, key := range reference.Keys {
			keys[key] = true
		}
		valid := definition.CredentialsSatisfied(func(name string) bool { return keys[name] })
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
		return fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	s.mu.Lock()
	definition, ok := s.definitions[definitionKey(definitionID, version)]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: manifest", api.ErrNotFound)
	}
	binding := s.findBindingLocked(auth, definition)
	if binding == nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: binding", api.ErrNotFound)
	}
	if binding.Status == api.RevokedStatus {
		s.mu.Unlock()
		return api.ErrRevoked
	}
	binding.Status = api.DisabledStatus
	binding.Revision++
	s.touchProjectionLocked(binding)
	s.bindings[binding.ToolBindingID] = *binding
	s.mu.Unlock()
	return s.persistAndNotify()
}
