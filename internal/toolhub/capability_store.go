package toolhub

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"slices"
	"strings"
)

// Called with s.mu held, preserving the same lock order as saveLocked. The
// shared disk fence prevents another host process from committing a revoke
// between our snapshot check and admission. Projection refresh alone cannot.
func (s *Store) lockCapabilityAdmission() (func(), error) {
	if s.path == "" {
		return func() {}, nil
	}
	if err := api.SafeStorePath(s.path); err != nil {
		return nil, err
	}
	if err := api.SafeStorePath(s.path + ".lock"); err != nil {
		return nil, err
	}
	lock := flock.New(s.path + ".lock")
	if err := lock.RLock(); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(s.path) // #nosec G304 -- protected registry path validated above.
	if err != nil || sha256.Sum256(body) != s.diskDigest {
		_ = lock.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: capability snapshot changed before admission", api.ErrStale)
	}
	return func() { _ = lock.Unlock() }, nil
}

func (s *Store) PutCapabilityPolicy(policy api.CapabilityPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	// Own slices/maps: an operator's later in-memory edits cannot mutate a grant.
	body, _ := json.Marshal(policy)
	var owned api.CapabilityPolicy
	_ = json.Unmarshal(body, &owned)
	policy = owned
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.capabilityPolicies[policy.PolicyID]
	// ponytail: publication scans bounded-by-store history; index group revisions
	// only if operator policy histories become large enough to measure a slowdown.
	for _, change := range s.capabilityChanges {
		if change.Policy != nil && change.Policy.PolicyID == policy.PolicyID {
			if err := api.CheckGroupRevisions(change.Policy.DefaultGroups, policy.DefaultGroups); err != nil {
				return err
			}
		}
	}
	if exists && policy.Revision <= old.Revision {
		if api.RecordsEqual(old, policy) {
			if s.path != "" {
				return s.saveLocked(s.path)
			}
			return nil
		}
		return fmt.Errorf("%w: capability policy revision", api.ErrConflict)
	}
	s.capabilityPolicies[policy.PolicyID] = policy
	s.capabilityChanges = append(s.capabilityChanges, api.CapabilityChange{Policy: &policy})
	if s.path != "" {
		if err := s.saveLocked(s.path); err != nil {
			s.capabilityChanges = s.capabilityChanges[:len(s.capabilityChanges)-1]
			if exists {
				s.capabilityPolicies[policy.PolicyID] = old
			} else {
				delete(s.capabilityPolicies, policy.PolicyID)
			}
			return err
		}
	}
	return nil
}

// validateProfileAdmissionLocked runs the authority checks a profile
// publication must pass against the current store: the named policy exists at
// the pinned revision and lists the principal, every allow stays inside the
// ceiling, and every selection pins a reviewed definition revision by digest.
// Preview runs the identical checks so a previewed profile is exactly what
// apply would admit.
func (s *Store) validateProfileAdmissionLocked(profile api.CapabilityProfile) (api.CapabilityPolicy, error) {
	policy, exists := s.capabilityPolicies[profile.PolicyID]
	if !exists || policy.Revision != profile.PolicyRevision || !slices.Contains(policy.Members, profile.PrincipalID) {
		return api.CapabilityPolicy{}, fmt.Errorf("%w: profile policy or membership", api.ErrUnauthorized)
	}
	for _, rules := range append([][]api.CapabilityRule{profile.Allows}, api.GroupRuleSets(profile.AllowGroups)...) {
		for _, rule := range rules {
			if !slices.ContainsFunc(policy.Ceiling, func(ceiling api.CapabilityRule) bool { return api.RuleInside(ceiling, rule) }) {
				return api.CapabilityPolicy{}, fmt.Errorf("%w: profile allow exceeds ceiling", api.ErrUnauthorized)
			}
		}
	}
	for _, selection := range profile.Selections {
		definition, exists := s.definitions[definitionKey(selection.DefinitionID, selection.DefinitionVersion)]
		if !exists || api.DefinitionDigest(definition) != selection.ImplementationDigest || !slices.ContainsFunc(definition.Tools, func(tool api.ToolSpec) bool {
			return tool.Name == selection.ToolName && tool.CapabilityID == selection.CapabilityID && len(tool.Uses) > 0
		}) {
			return api.CapabilityPolicy{}, fmt.Errorf("%w: unreviewed capability implementation", api.ErrUnauthorized)
		}
	}
	return policy, nil
}

func (s *Store) PutCapabilityProfile(profile api.CapabilityProfile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	body, _ := json.Marshal(profile)
	var owned api.CapabilityProfile
	_ = json.Unmarshal(body, &owned)
	profile = owned
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.validateProfileAdmissionLocked(profile); err != nil {
		return err
	}
	old, exists := s.capabilityProfiles[profile.ProfileID]
	for _, change := range s.capabilityChanges {
		if change.Profile != nil && change.Profile.ProfileID == profile.ProfileID {
			if err := api.CheckGroupRevisions(change.Profile.AllowGroups, profile.AllowGroups); err != nil {
				return err
			}
		}
	}
	if exists && profile.Revision <= old.Revision {
		if api.RecordsEqual(old, profile) {
			if s.path != "" {
				return s.saveLocked(s.path)
			}
			return nil
		}
		return fmt.Errorf("%w: capability profile revision", api.ErrConflict)
	}
	s.capabilityProfiles[profile.ProfileID] = profile
	s.capabilityChanges = append(s.capabilityChanges, api.CapabilityChange{Profile: &profile})
	if s.path != "" {
		if err := s.saveLocked(s.path); err != nil {
			s.capabilityChanges = s.capabilityChanges[:len(s.capabilityChanges)-1]
			if exists {
				s.capabilityProfiles[profile.ProfileID] = old
			} else {
				delete(s.capabilityProfiles, profile.ProfileID)
			}
			return err
		}
	}
	return nil
}

func (s *Store) capabilityProfileLocked(auth identity.Envelope) (api.CapabilityProfile, api.CapabilityPolicy, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return api.CapabilityProfile{}, api.CapabilityPolicy{}, fmt.Errorf("%w: capability identity", api.ErrUnauthorized)
	}
	profile, exists := s.capabilityProfiles[auth.CapabilityProfile]
	if !exists || profile.Status != api.ActiveStatus || profile.PrincipalID != auth.PrincipalID || profile.ContextID != auth.ContextID ||
		profile.RuntimeID != auth.RuntimeID || profile.Environment != auth.Environment || profile.Generation != auth.Generation || profile.PolicyVersion != auth.PolicyVersion {
		return api.CapabilityProfile{}, api.CapabilityPolicy{}, fmt.Errorf("%w: capability runtime scope", api.ErrUnauthorized)
	}
	policy, exists := s.capabilityPolicies[profile.PolicyID]
	if !exists || policy.Status != api.ActiveStatus || policy.Revision != profile.PolicyRevision || !slices.Contains(policy.Members, auth.PrincipalID) {
		return api.CapabilityProfile{}, api.CapabilityPolicy{}, fmt.Errorf("%w: current capability policy", api.ErrUnauthorized)
	}
	return profile, policy, nil
}

func (s *Store) managedToolsLocked(auth identity.Envelope) ([]api.ProjectedTool, error) {
	profile, policy, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return nil, err
	}
	result := []api.ProjectedTool{}
	for _, selection := range profile.Selections {
		projected, _, err := s.managedSelectionLocked(auth, profile, policy, selection, nil, true)
		if err == nil {
			result = append(result, projected)
		} else if err != api.ErrUnauthorized {
			return nil, err
		}
	}
	seen := make(map[string]bool, len(result))
	for _, tool := range result {
		seen[tool.Name] = true
	}
	for _, tool := range s.managedSelfInstallToolsLocked(auth, profile) {
		if seen[tool.Name] {
			continue
		}
		seen[tool.Name] = true
		result = append(result, tool)
	}
	slices.SortFunc(result, func(a, b api.ProjectedTool) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// managedSelfInstallsLocked returns the active bindings this principal
// materialized through the self-install pipeline under the current managed
// policy generation. Provenance is the onboarding record: it carries the
// authenticated confirmation the binding was enabled under, so bindings from
// before the managed era (different policy_version) never leak in.
func (s *Store) managedSelfInstallsLocked(auth identity.Envelope, profile api.CapabilityProfile) map[string]bool {
	if !profile.SelfInstall {
		return nil
	}
	admitted := map[string]bool{}
	for _, onboarding := range s.onboardings {
		if (onboarding.Mode != api.OnboardingSelfInstall && onboarding.Mode != api.OnboardingRemote) || onboarding.BindingID == "" ||
			onboarding.PrincipalID != auth.PrincipalID || onboarding.ContextID != auth.ContextID ||
			onboarding.RuntimeID != auth.RuntimeID || onboarding.PolicyVersion != auth.PolicyVersion {
			continue
		}
		if onboarding.Phase != api.PhaseConfirmed && onboarding.Phase != api.PhaseEnabled && onboarding.Phase != api.PhaseDisabled {
			continue
		}
		admitted[onboarding.BindingID] = true
	}
	return admitted
}

func (s *Store) managedSelfInstallToolsLocked(auth identity.Envelope, profile api.CapabilityProfile) []api.ProjectedTool {
	admitted := s.managedSelfInstallsLocked(auth, profile)
	if len(admitted) == 0 {
		return nil
	}
	result := []api.ProjectedTool{}
	for _, binding := range s.bindings {
		if !admitted[binding.ToolBindingID] || binding.Status != api.ActiveStatus {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		effective.CapabilityProfileID, effective.CapabilityProfileRevision = profile.ProfileID, profile.Revision
		effective.CapabilityPolicyID, effective.CapabilityPolicyRevision = profile.PolicyID, profile.PolicyRevision
		effective.ImplementationDigest = api.DefinitionDigest(effective.Definition)
		for _, tool := range effective.Definition.Tools {
			result = append(result, api.ProjectedTool{
				Name:         api.ProjectedToolName(effective.Definition.DefinitionID, effective.Definition.Version, tool.Name),
				BindingID:    effective.Binding.ToolBindingID,
				DefinitionID: effective.Definition.DefinitionID,
				Version:      effective.Definition.Version,
				Tool:         tool,
			})
		}
	}
	return result
}

// managedSelfInstallToolLocked is the dispatch side of
// managedSelfInstallToolsLocked: same provenance gate, exact projected name.
func (s *Store) managedSelfInstallToolLocked(auth identity.Envelope, profile api.CapabilityProfile, name string) (api.ProjectedTool, api.EffectiveBinding, error) {
	admitted := s.managedSelfInstallsLocked(auth, profile)
	for _, binding := range s.bindings {
		if !admitted[binding.ToolBindingID] || binding.Status != api.ActiveStatus {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		for _, tool := range effective.Definition.Tools {
			if api.ProjectedToolName(effective.Definition.DefinitionID, effective.Definition.Version, tool.Name) != name {
				continue
			}
			effective.CapabilityProfileID, effective.CapabilityProfileRevision = profile.ProfileID, profile.Revision
			effective.CapabilityPolicyID, effective.CapabilityPolicyRevision = profile.PolicyID, profile.PolicyRevision
			effective.ImplementationDigest = api.DefinitionDigest(effective.Definition)
			return api.ProjectedTool{Name: name, BindingID: binding.ToolBindingID, DefinitionID: effective.Definition.DefinitionID,
				Version: effective.Definition.Version, Tool: tool}, effective, nil
		}
	}
	return api.ProjectedTool{}, api.EffectiveBinding{}, fmt.Errorf("%w: projected tool", api.ErrNotFound)
}

// PreviewCapabilityProfile diffs a candidate profile against the stored one
// using the exact evaluator and admission checks apply and dispatch run, so a
// previewed grant can never diverge from what publish+dispatch would do. The
// draft needs only structural validity; confirmation remains the apply gate.
func (s *Store) PreviewCapabilityProfile(candidate api.CapabilityProfile) (api.ProfilePreview, error) {
	if err := candidate.ValidateStructure(); err != nil {
		return api.ProfilePreview{}, err
	}
	body, _ := json.Marshal(candidate)
	var owned api.CapabilityProfile
	_ = json.Unmarshal(body, &owned)
	candidate = owned
	s.mu.RLock()
	defer s.mu.RUnlock()
	preview := api.ProfilePreview{ProfileID: candidate.ProfileID, CandidateRevision: candidate.Revision,
		Current: []api.ProfilePreviewEntry{}, Candidate: []api.ProfilePreviewEntry{},
		Added: []string{}, Removed: []string{}, Changed: []string{}}
	candidatePolicy, err := s.validateProfileAdmissionLocked(candidate)
	if err != nil {
		return preview, err
	}
	if current, ok := s.capabilityProfiles[candidate.ProfileID]; ok {
		if candidate.Revision <= current.Revision {
			return preview, fmt.Errorf("%w: capability profile revision", api.ErrConflict)
		}
		preview.CurrentRevision = current.Revision
		if current.Status == api.ActiveStatus {
			// The old side may pin a superseded policy revision (e.g. the
			// operator already published the new policy). Evaluate it under
			// its pinned revision from history so the diff shows what the
			// old profile actually admitted, not an empty set.
			if currentPolicy, ok := s.policyAtRevisionLocked(current.PolicyID, current.PolicyRevision); ok {
				preview.Current = s.previewSelectionsLocked(current, currentPolicy)
			}
		}
	}
	preview.Candidate = s.previewSelectionsLocked(candidate, candidatePolicy)
	currentByName := map[string]api.ProfilePreviewEntry{}
	for _, entry := range preview.Current {
		if entry.Admitted {
			currentByName[entry.Name] = entry
		}
	}
	candidateByName := map[string]api.ProfilePreviewEntry{}
	for _, entry := range preview.Candidate {
		if entry.Admitted {
			candidateByName[entry.Name] = entry
		}
	}
	for name, next := range candidateByName {
		prev, ok := currentByName[name]
		if !ok {
			preview.Added = append(preview.Added, name)
		} else if !api.PreviewEntriesEqual(prev, next) {
			preview.Changed = append(preview.Changed, name)
		}
		delete(currentByName, name)
	}
	for name := range currentByName {
		preview.Removed = append(preview.Removed, name)
	}
	slices.Sort(preview.Added)
	slices.Sort(preview.Removed)
	slices.Sort(preview.Changed)
	return preview, nil
}

// previewSelectionsLocked evaluates every selection exactly as projection
// does: same synthesized identity, same per-selection resolution, same
// deny-reason surfacing.
func (s *Store) previewSelectionsLocked(profile api.CapabilityProfile, policy api.CapabilityPolicy) []api.ProfilePreviewEntry {
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: profile.PrincipalID, ContextID: profile.ContextID, RuntimeID: profile.RuntimeID,
		ExternalIdentityID: profile.ContextID, ConversationID: profile.ContextID, DeliveryTargetID: profile.ContextID,
		PolicyVersion: profile.PolicyVersion, CapabilityProfile: profile.ProfileID, Environment: profile.Environment, Generation: profile.Generation}
	entries := []api.ProfilePreviewEntry{}
	for _, selection := range profile.Selections {
		entry := api.ProfilePreviewEntry{Name: selection.Name, CapabilityID: selection.CapabilityID}
		_, effective, err := s.managedSelectionLocked(auth, profile, policy, selection, nil, true)
		if err != nil {
			entry.Reason = "unauthorized"
			if errors.Is(err, api.ErrConflict) {
				entry.Reason = "ambiguous binding"
			}
		} else {
			entry.Admitted = true
			entry.Scopes = effective.CapabilityScopes
			entry.OutputBytes = effective.Definition.Execution.OutputBytes
			entry.TimeoutSeconds = effective.Definition.Execution.TimeoutSeconds
		}
		entries = append(entries, entry)
	}
	return entries
}

// policyAtRevisionLocked resolves a policy at an exact revision: the live
// record when it still matches, else the pinned historical revision replayed
// out of capability history. Preview uses it for the old side of a diff.
func (s *Store) policyAtRevisionLocked(policyID string, revision uint64) (api.CapabilityPolicy, bool) {
	if policy, ok := s.capabilityPolicies[policyID]; ok && policy.Revision == revision {
		return policy, true
	}
	for _, change := range s.capabilityChanges {
		if change.Policy != nil && change.Policy.PolicyID == policyID && change.Policy.Revision == revision {
			return *change.Policy, true
		}
	}
	return api.CapabilityPolicy{}, false
}

func (s *Store) managedSelectionLocked(auth identity.Envelope, profile api.CapabilityProfile, policy api.CapabilityPolicy, selection api.CapabilitySelection, arguments map[string]any, projection bool) (api.ProjectedTool, api.EffectiveBinding, error) {
	var found *api.EffectiveBinding
	for _, binding := range s.bindings {
		if binding.DefinitionID != selection.DefinitionID || binding.DefinitionVersion != selection.DefinitionVersion || binding.ConnectionID != selection.ConnectionID {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		if found != nil {
			return api.ProjectedTool{}, api.EffectiveBinding{}, fmt.Errorf("%w: ambiguous capability binding", api.ErrConflict)
		}
		found = &effective
	}
	if found == nil || api.DefinitionDigest(found.Definition) != selection.ImplementationDigest {
		return api.ProjectedTool{}, api.EffectiveBinding{}, api.ErrUnauthorized
	}
	for _, tool := range found.Definition.Tools {
		if tool.Name != selection.ToolName || tool.CapabilityID != selection.CapabilityID {
			continue
		}
		limits, scopes, allowed := api.CapabilityDecision(profile, policy, selection, tool, arguments, projection)
		if !allowed {
			break
		}
		found.Definition.Execution.OutputBytes = min(found.Definition.Execution.OutputBytes, limits.OutputBytes)
		found.Definition.Execution.TimeoutSeconds = min(found.Definition.Execution.TimeoutSeconds, limits.TimeoutSeconds)
		found.CapabilityID, found.CapabilityPolicyRevision, found.CapabilityProfileRevision = selection.CapabilityID, policy.Revision, profile.Revision
		found.CapabilityPolicyID, found.CapabilityProfileID = policy.PolicyID, profile.ProfileID
		found.ImplementationDigest = selection.ImplementationDigest
		found.CapabilityScopes = scopes
		if projection {
			// Allowed-only description: the model sees the admitted boundary,
			// never the full catalog surface this tool could serve elsewhere.
			for _, scope := range scopes {
				if scope.PathPrefix == "" {
					continue
				}
				tool.Description = strings.TrimSpace(tool.Description + " Scoped to " + scope.Resource + " under " + scope.PathPrefix + ".")
			}
		}
		return api.ProjectedTool{Name: selection.Name, BindingID: found.Binding.ToolBindingID, DefinitionID: selection.DefinitionID, Version: selection.DefinitionVersion, Tool: tool}, *found, nil
	}
	return api.ProjectedTool{}, api.EffectiveBinding{}, api.ErrUnauthorized
}

// ReverifyEffective rechecks the authority a call was admitted under. Export
// and apply paths run this so a mid-flight revocation, binding update or
// policy/profile revision change stops the write instead of landing on
// stale authority. The fence can fire from inside AuthorizeProjectedCall,
// which holds s.mu.RLock for the whole dispatch: queuing a nested read lock
// behind a pending writer would deadlock the call forever, so contention
// fails closed — under the held outer lock the records cannot have changed,
// and without it a live writer means authority is genuinely in flux.
func (s *Store) ReverifyEffective(effective api.EffectiveBinding) error {
	if !s.mu.TryRLock() {
		return fmt.Errorf("%w: authority recheck contended", api.ErrUnauthorized)
	}
	defer s.mu.RUnlock()
	binding, ok := s.bindings[effective.Binding.ToolBindingID]
	if !ok || binding.Status != api.ActiveStatus || binding.Revision != effective.Binding.Revision || binding.ProjectionRevision != effective.Binding.ProjectionRevision {
		return fmt.Errorf("%w: binding revoked or changed", api.ErrUnauthorized)
	}
	// Capability profile/policy ids exist only on managed admissions. An
	// unmanaged effective carries neither, and the binding freshness above is
	// its whole authority — demanding a profile here would deny every
	// unmanaged export as "changed".
	if effective.CapabilityProfileID == "" {
		if effective.CapabilityProfileRevision != 0 || effective.CapabilityPolicyID != "" || effective.CapabilityPolicyRevision != 0 {
			return fmt.Errorf("%w: capability identity incomplete", api.ErrUnauthorized)
		}
		return nil
	}
	profile, ok := s.capabilityProfiles[effective.CapabilityProfileID]
	if !ok || profile.Status != api.ActiveStatus || profile.Revision != effective.CapabilityProfileRevision {
		return fmt.Errorf("%w: capability profile changed", api.ErrUnauthorized)
	}
	policy, ok := s.capabilityPolicies[profile.PolicyID]
	if !ok || policy.Status != api.ActiveStatus || policy.Revision != effective.CapabilityPolicyRevision {
		return fmt.Errorf("%w: capability policy changed", api.ErrUnauthorized)
	}
	return nil
}

func (s *Store) managedToolLocked(auth identity.Envelope, name string, arguments map[string]any, projection bool) (api.ProjectedTool, api.EffectiveBinding, error) {
	profile, policy, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return api.ProjectedTool{}, api.EffectiveBinding{}, err
	}
	for _, selection := range profile.Selections {
		if selection.Name == name {
			tool, effective, err := s.managedSelectionLocked(auth, profile, policy, selection, arguments, projection)
			if err != api.ErrUnauthorized {
				return tool, effective, err
			}
			break
		}
	}
	return s.managedSelfInstallToolLocked(auth, profile, name)
}
