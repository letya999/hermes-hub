package toolhub

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// EnableCatalog is a trusted host operation, never a model control method.
// It commits bindings, selections and optional ceiling extensions together.
// Existing denies survive; sibling profiles are re-pinned when a shared
// policy revision changes, without receiving the target's new allows.
func (s *Store) EnableCatalog(auth identity.Envelope, definitionID, version, issuer string, extendCeiling bool) (ToolBinding, error) {
	if !identity.ValidID(issuer) || issuer == "model" || issuer == "hermes" {
		return ToolBinding{}, ErrUnauthorized
	}
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return ToolBinding{}, fmt.Errorf("%w: catalog identity", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.definitions[definitionKey(definitionID, version)]
	if !ok {
		return ToolBinding{}, ErrNotFound
	}
	if s.publicationLocked(d).Visibility != PublicationCatalog || len(d.Credentials) != 0 {
		return ToolBinding{}, fmt.Errorf("%w: select a credential-free catalog definition", ErrUnauthorized)
	}
	// Clone records so failed validation or persistence cannot leak authority.
	oldBindings, oldProfiles, oldPolicies := s.bindings, s.capabilityProfiles, s.capabilityPolicies
	oldRevisions, oldChanges := s.projectionRevisions, s.capabilityChanges
	s.bindings, s.capabilityPolicies = maps.Clone(oldBindings), maps.Clone(oldPolicies)
	s.projectionRevisions = maps.Clone(oldRevisions)
	encoded, _ := json.Marshal(oldProfiles)
	// Unmarshal into a fresh map: it must not update the original map in place.
	s.capabilityProfiles = nil
	_ = json.Unmarshal(encoded, &s.capabilityProfiles)
	s.capabilityChanges = slices.Clone(oldChanges)
	committed := false
	defer func() {
		if !committed {
			s.bindings, s.capabilityProfiles, s.capabilityPolicies = oldBindings, oldProfiles, oldPolicies
			s.projectionRevisions, s.capabilityChanges = oldRevisions, oldChanges
		}
	}()
	binding := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: d.Workload.Class, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, d.DefinitionID, d.Version, "", "")
	if old, exists := s.bindings[binding.ToolBindingID]; exists {
		if old.Status != ActiveStatus || old.PolicyVersion != auth.PolicyVersion {
			return ToolBinding{}, fmt.Errorf("%w: existing catalog binding is disabled, revoked or stale", ErrConflict)
		}
		binding = old
	}
	if err := binding.Validate(); err != nil {
		return ToolBinding{}, err
	}
	if auth.CapabilityProfile != "" {
		profile, policy, err := s.capabilityProfileLocked(auth)
		if err != nil {
			return ToolBinding{}, err
		}
		// Policy slices also need ownership before append changes their backing array.
		raw, _ := json.Marshal(policy)
		policy = CapabilityPolicy{}
		_ = json.Unmarshal(raw, &policy)
		digest := DefinitionDigest(d)
		ceilingChanged := false
		for _, tool := range d.Tools {
			if tool.CapabilityID == "" || len(tool.Uses) == 0 {
				return ToolBinding{}, fmt.Errorf("%w: catalog tool has no capability contract", ErrInvalid)
			}
			selection := CapabilitySelection{CapabilityID: tool.CapabilityID, DefinitionID: d.DefinitionID, DefinitionVersion: d.Version,
				ImplementationDigest: digest, ToolName: tool.Name, Name: d.DefinitionID + "_" + strings.ReplaceAll(tool.Name, "-", "_")}
			if !slices.ContainsFunc(profile.Selections, func(v CapabilitySelection) bool {
				return v.DefinitionID == d.DefinitionID && v.DefinitionVersion == d.Version && v.ToolName == tool.Name && v.ConnectionID == ""
			}) {
				profile.Selections = append(profile.Selections, selection)
			}
			for _, use := range tool.Uses {
				rule := CapabilityRule{CapabilityID: tool.CapabilityID, ImplementationDigest: digest, Action: use.Action, Resource: use.Resource,
					Limits: CapabilityLimits{OutputBytes: d.Execution.OutputBytes, TimeoutSeconds: d.Execution.TimeoutSeconds}}
				if !slices.ContainsFunc(policy.Ceiling, func(v CapabilityRule) bool { return ruleInside(v, rule) }) {
					if !extendCeiling {
						return ToolBinding{}, fmt.Errorf("%w: catalog exceeds policy ceiling; operator may use --extend-ceiling", ErrUnauthorized)
					}
					policy.Ceiling = append(policy.Ceiling, rule)
					ceilingChanged = true
				}
				if !slices.ContainsFunc(profile.Allows, func(v CapabilityRule) bool { return ruleInside(v, rule) }) {
					profile.Allows = append(profile.Allows, rule)
				}
			}
		}
		now := time.Now().UTC()
		if ceilingChanged {
			policy.Revision++
			policy.IssuedBy, policy.IssuedAt, policy.Reason = issuer, now, "Operator catalog ceiling extension"
			if err := Confirm(&policy, issuer, now); err != nil {
				return ToolBinding{}, err
			}
			if err := policy.Validate(); err != nil {
				return ToolBinding{}, err
			}
			s.capabilityPolicies[policy.PolicyID] = policy
			s.capabilityChanges = append(s.capabilityChanges, capabilityChange{Policy: &policy})
		}
		s.capabilityProfiles[profile.ProfileID] = profile
		for id, candidate := range s.capabilityProfiles {
			if id != profile.ProfileID && (!ceilingChanged || candidate.PolicyID != policy.PolicyID || candidate.PolicyRevision != profile.PolicyRevision) {
				continue
			}
			candidate.PolicyRevision = policy.Revision
			candidate.Revision++
			candidate.IssuedBy, candidate.IssuedAt, candidate.Reason = issuer, now, "Operator catalog selection"
			if err := Confirm(&candidate, issuer, now); err != nil {
				return ToolBinding{}, err
			}
			if err := candidate.Validate(); err != nil {
				return ToolBinding{}, err
			}
			if _, err := s.validateProfileAdmissionLocked(candidate); err != nil {
				return ToolBinding{}, err
			}
			s.capabilityProfiles[id] = candidate
			copy := candidate
			s.capabilityChanges = append(s.capabilityChanges, capabilityChange{Profile: &copy})
		}
	}
	s.bindings[binding.ToolBindingID] = binding
	key := projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)
	s.projectionRevisions[key] = max(s.projectionRevisions[key], binding.ProjectionRevision) + 1
	if s.path != "" {
		if err := s.saveLocked(s.path); err != nil {
			return ToolBinding{}, err
		}
	}
	committed = true
	return binding, nil
}
