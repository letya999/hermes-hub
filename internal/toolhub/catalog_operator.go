package toolhub

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"maps"
	"slices"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// EnableCatalog is a trusted host operation, never a model control method.
// It commits bindings, selections and optional ceiling extensions together.
// Existing denies survive; sibling profiles are re-pinned when a shared
// policy revision changes, without receiving the target's new allows.
func (s *Store) EnableCatalog(auth identity.Envelope, definitionID, version, issuer string, extendCeiling bool) (api.ToolBinding, error) {
	if !identity.ValidID(issuer) || issuer == "model" || issuer == "hermes" {
		return api.ToolBinding{}, api.ErrUnauthorized
	}
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return api.ToolBinding{}, fmt.Errorf("%w: catalog identity", api.ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.definitions[definitionKey(definitionID, version)]
	if !ok {
		return api.ToolBinding{}, api.ErrNotFound
	}
	if s.publicationLocked(d).Visibility != api.PublicationCatalog || len(d.Credentials) != 0 {
		return api.ToolBinding{}, fmt.Errorf("%w: select a credential-free catalog definition", api.ErrUnauthorized)
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
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: d.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, d.DefinitionID, d.Version, "", "")
	_, bindingExists := s.bindings[binding.ToolBindingID]
	changed := !bindingExists
	if old, exists := s.bindings[binding.ToolBindingID]; exists {
		if old.Status != api.ActiveStatus || old.PolicyVersion != auth.PolicyVersion {
			return api.ToolBinding{}, fmt.Errorf("%w: existing catalog binding is disabled, revoked or stale", api.ErrConflict)
		}
		binding = old
	}
	if err := binding.Validate(); err != nil {
		return api.ToolBinding{}, err
	}
	if auth.CapabilityProfile != "" {
		profile, policy, err := s.capabilityProfileLocked(auth)
		if err != nil {
			return api.ToolBinding{}, err
		}
		// Policy slices also need ownership before append changes their backing array.
		raw, _ := json.Marshal(policy)
		policy = api.CapabilityPolicy{}
		_ = json.Unmarshal(raw, &policy)
		digest := api.DefinitionDigest(d)
		ceilingChanged := false
		selectionChanged := false
		allowsChanged := false
		oldPolicyRevision := profile.PolicyRevision
		for _, tool := range d.Tools {
			if tool.CapabilityID == "" || (len(tool.Uses) == 0 && !api.RemoteExecutor(tool.Executor)) {
				return api.ToolBinding{}, fmt.Errorf("%w: catalog tool has no capability contract", api.ErrInvalid)
			}
			selection := api.CapabilitySelection{CapabilityID: tool.CapabilityID, DefinitionID: d.DefinitionID, DefinitionVersion: d.Version,
				ImplementationDigest: digest, ToolName: tool.Name, Name: tool.Name}
			if i := slices.IndexFunc(profile.Selections, func(v api.CapabilitySelection) bool {
				return v.DefinitionID == d.DefinitionID && v.ToolName == tool.Name && v.CapabilityID == tool.CapabilityID && v.Name == tool.Name && v.ConnectionID == ""
			}); i >= 0 {
				selection.PathScopes = slices.Clone(profile.Selections[i].PathScopes)
				if profile.Selections[i].DefinitionVersion != d.Version || profile.Selections[i].ImplementationDigest != digest {
					profile.Selections[i] = selection
					selectionChanged = true
				}
			} else {
				profile.Selections = append(profile.Selections, selection)
				selectionChanged = true
			}
			for _, use := range tool.Uses {
				rule := api.CapabilityRule{CapabilityID: tool.CapabilityID, ImplementationDigest: digest, Action: use.Action, Resource: use.Resource,
					Limits: api.CapabilityLimits{OutputBytes: d.Execution.OutputBytes, TimeoutSeconds: d.Execution.TimeoutSeconds}}
				if !slices.ContainsFunc(policy.Ceiling, func(v api.CapabilityRule) bool { return api.RuleInside(v, rule) }) {
					if !extendCeiling {
						return api.ToolBinding{}, fmt.Errorf("%w: catalog exceeds policy ceiling; operator may use --extend-ceiling", api.ErrUnauthorized)
					}
					policy.Ceiling = append(policy.Ceiling, rule)
					ceilingChanged = true
				}
				if !slices.ContainsFunc(profile.Allows, func(v api.CapabilityRule) bool { return api.RuleInside(v, rule) }) {
					profile.Allows = append(profile.Allows, rule)
					allowsChanged = true
				}
			}
		}
		// Retire only digest-scoped rules for this catalog that no current
		// selection needs. Immutable audit records and every deny survive.
		obsolete := map[string]bool{}
		for _, prior := range s.definitions {
			if prior.DefinitionID == d.DefinitionID && prior.Version != d.Version {
				obsolete[api.DefinitionDigest(prior)] = true
			}
		}
		selected := func(p api.CapabilityProfile, digest string) bool {
			return slices.ContainsFunc(p.Selections, func(v api.CapabilitySelection) bool { return v.ImplementationDigest == digest })
		}
		beforeAllows := len(profile.Allows)
		profile.Allows = slices.DeleteFunc(profile.Allows, func(rule api.CapabilityRule) bool {
			return obsolete[rule.ImplementationDigest] && !selected(profile, rule.ImplementationDigest)
		})
		allowsChanged = allowsChanged || beforeAllows != len(profile.Allows)
		used := func(digest string) bool {
			for id, p := range s.capabilityProfiles {
				if p.PolicyID != policy.PolicyID {
					continue
				}
				if id == profile.ProfileID {
					p = profile
				}
				if selected(p, digest) || slices.ContainsFunc(p.Allows, func(r api.CapabilityRule) bool { return r.ImplementationDigest == digest }) {
					return true
				}
				for _, group := range api.GroupRuleSets(p.AllowGroups) {
					if slices.ContainsFunc(group, func(r api.CapabilityRule) bool { return r.ImplementationDigest == digest }) {
						return true
					}
				}
			}
			if slices.ContainsFunc(policy.Defaults, func(r api.CapabilityRule) bool { return r.ImplementationDigest == digest }) {
				return true
			}
			for _, group := range api.GroupRuleSets(policy.DefaultGroups) {
				if slices.ContainsFunc(group, func(r api.CapabilityRule) bool { return r.ImplementationDigest == digest }) {
					return true
				}
			}
			return false
		}
		beforeCeiling := len(policy.Ceiling)
		policy.Ceiling = slices.DeleteFunc(policy.Ceiling, func(rule api.CapabilityRule) bool {
			return obsolete[rule.ImplementationDigest] && !used(rule.ImplementationDigest)
		})
		ceilingChanged = ceilingChanged || beforeCeiling != len(policy.Ceiling)
		now := time.Now().UTC()
		if ceilingChanged {
			policy.Revision++
			policy.IssuedBy, policy.IssuedAt, policy.Reason = issuer, now, "Operator catalog ceiling extension"
			if err := api.Confirm(&policy, issuer, now); err != nil {
				return api.ToolBinding{}, err
			}
			if err := policy.Validate(); err != nil {
				return api.ToolBinding{}, err
			}
			s.capabilityPolicies[policy.PolicyID] = policy
			s.capabilityChanges = append(s.capabilityChanges, api.CapabilityChange{Policy: &policy})
		}
		if ceilingChanged {
			profile.PolicyRevision = policy.Revision
		}
		if selectionChanged || ceilingChanged || allowsChanged {
			changed = true
			profile.Revision++
			profile.IssuedBy, profile.IssuedAt, profile.Reason = issuer, now, "Operator catalog selection"
			if err := api.Confirm(&profile, issuer, now); err != nil {
				return api.ToolBinding{}, err
			}
			if err := profile.Validate(); err != nil {
				return api.ToolBinding{}, err
			}
			if _, err := s.validateProfileAdmissionLocked(profile); err != nil {
				return api.ToolBinding{}, err
			}
			copy := profile
			s.capabilityChanges = append(s.capabilityChanges, api.CapabilityChange{Profile: &copy})
		}
		s.capabilityProfiles[profile.ProfileID] = profile
		for id, candidate := range s.capabilityProfiles {
			if id == profile.ProfileID || !ceilingChanged || candidate.PolicyID != policy.PolicyID || candidate.PolicyRevision != oldPolicyRevision {
				continue
			}
			candidate.PolicyRevision = policy.Revision
			candidate.Revision++
			candidate.IssuedBy, candidate.IssuedAt, candidate.Reason = issuer, now, "Operator catalog selection"
			if err := api.Confirm(&candidate, issuer, now); err != nil {
				return api.ToolBinding{}, err
			}
			if err := candidate.Validate(); err != nil {
				return api.ToolBinding{}, err
			}
			if _, err := s.validateProfileAdmissionLocked(candidate); err != nil {
				return api.ToolBinding{}, err
			}
			s.capabilityProfiles[id] = candidate
			copy := candidate
			s.capabilityChanges = append(s.capabilityChanges, api.CapabilityChange{Profile: &copy})
		}
	}
	if !changed {
		committed = true
		return binding, nil
	}
	s.bindings[binding.ToolBindingID] = binding
	key := projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)
	s.projectionRevisions[key] = max(s.projectionRevisions[key], binding.ProjectionRevision) + 1
	if s.path != "" {
		if err := s.saveLocked(s.path); err != nil {
			return api.ToolBinding{}, err
		}
	}
	committed = true
	return binding, nil
}
