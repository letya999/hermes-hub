package toolhub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
	"os"
	"slices"
	"strings"
	"time"
)

// Called with s.mu held, preserving the same lock order as saveLocked. The
// shared disk fence prevents another host process from committing a revoke
// between our snapshot check and admission. Projection refresh alone cannot.
func (s *Store) lockCapabilityAdmission() (func(), error) {
	if s.path == "" {
		return func() {}, nil
	}
	if err := safeStorePath(s.path); err != nil {
		return nil, err
	}
	if err := safeStorePath(s.path + ".lock"); err != nil {
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
		return nil, fmt.Errorf("%w: capability snapshot changed before admission", ErrStale)
	}
	return func() { _ = lock.Unlock() }, nil
}

func DefinitionDigest(definition ToolDefinition) string {
	body, err := json.Marshal(definition)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ConfirmationDigest is the canonical digest the CLI prints for review and the
// store re-derives at admission. It covers the record with the confirmation
// field removed, so a stamped record digests identically to its reviewed form.
func ConfirmationDigest(record any) string {
	body, err := json.Marshal(record)
	if err != nil {
		return ""
	}
	var shadow map[string]json.RawMessage
	if err := json.Unmarshal(body, &shadow); err != nil {
		return ""
	}
	delete(shadow, "confirmation")
	canonical, err := json.Marshal(shadow)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Confirm stamps issuer's confirmation onto a policy, profile, or grant
// pointer. An empty issuer defaults to the record's own issuer, which is also
// what cross-field identity validation enforces at admission.
func Confirm(record any, issuer string, now time.Time) error {
	slot, recordIssuer := confirmationTarget(record)
	if slot == nil {
		return fmt.Errorf("%w: unsupported confirmation record", ErrInvalid)
	}
	if issuer == "" {
		issuer = recordIssuer
	}
	*slot = nil
	confirmation, err := newConfirmation(record, issuer, now)
	if err != nil {
		return err
	}
	*slot = &confirmation
	return nil
}

func confirmationTarget(record any) (**Confirmation, string) {
	switch r := record.(type) {
	case *CapabilityPolicy:
		return &r.Confirmation, r.IssuedBy
	case *CapabilityProfile:
		return &r.Confirmation, r.IssuedBy
	case *Grant:
		return &r.Confirmation, r.IssuedBy
	case *ToolGrant:
		return &r.Confirmation, r.GrantedBy
	}
	return nil, ""
}

func newConfirmation(record any, issuer string, now time.Time) (Confirmation, error) {
	if !identity.ValidID(issuer) || issuer == "model" || issuer == "hermes" {
		return Confirmation{}, fmt.Errorf("%w: confirmation issuer", ErrUnauthorized)
	}
	digest := ConfirmationDigest(record)
	if digest == "" {
		return Confirmation{}, fmt.Errorf("%w: confirmation digest", ErrInvalid)
	}
	return Confirmation{Digest: digest, IssuedBy: issuer, ConfirmedAt: now.UTC()}, nil
}

func (s *Store) PutCapabilityPolicy(policy CapabilityPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	// Own slices/maps: an operator's later in-memory edits cannot mutate a grant.
	body, _ := json.Marshal(policy)
	var owned CapabilityPolicy
	_ = json.Unmarshal(body, &owned)
	policy = owned
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.capabilityPolicies[policy.PolicyID]
	// ponytail: publication scans bounded-by-store history; index group revisions
	// only if operator policy histories become large enough to measure a slowdown.
	for _, change := range s.capabilityChanges {
		if change.Policy != nil && change.Policy.PolicyID == policy.PolicyID {
			if err := checkGroupRevisions(change.Policy.DefaultGroups, policy.DefaultGroups); err != nil {
				return err
			}
		}
	}
	if exists && policy.Revision <= old.Revision {
		if recordsEqual(old, policy) {
			if s.path != "" {
				return s.saveLocked(s.path)
			}
			return nil
		}
		return fmt.Errorf("%w: capability policy revision", ErrConflict)
	}
	s.capabilityPolicies[policy.PolicyID] = policy
	s.capabilityChanges = append(s.capabilityChanges, capabilityChange{Policy: &policy})
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
func (s *Store) validateProfileAdmissionLocked(profile CapabilityProfile) (CapabilityPolicy, error) {
	policy, exists := s.capabilityPolicies[profile.PolicyID]
	if !exists || policy.Revision != profile.PolicyRevision || !slices.Contains(policy.Members, profile.PrincipalID) {
		return CapabilityPolicy{}, fmt.Errorf("%w: profile policy or membership", ErrUnauthorized)
	}
	for _, rules := range append([][]CapabilityRule{profile.Allows}, groupRuleSets(profile.AllowGroups)...) {
		for _, rule := range rules {
			if !slices.ContainsFunc(policy.Ceiling, func(ceiling CapabilityRule) bool { return ruleInside(ceiling, rule) }) {
				return CapabilityPolicy{}, fmt.Errorf("%w: profile allow exceeds ceiling", ErrUnauthorized)
			}
		}
	}
	for _, selection := range profile.Selections {
		definition, exists := s.definitions[definitionKey(selection.DefinitionID, selection.DefinitionVersion)]
		if !exists || DefinitionDigest(definition) != selection.ImplementationDigest || !slices.ContainsFunc(definition.Tools, func(tool ToolSpec) bool {
			return tool.Name == selection.ToolName && tool.CapabilityID == selection.CapabilityID && len(tool.Uses) > 0
		}) {
			return CapabilityPolicy{}, fmt.Errorf("%w: unreviewed capability implementation", ErrUnauthorized)
		}
	}
	return policy, nil
}

func (s *Store) PutCapabilityProfile(profile CapabilityProfile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	body, _ := json.Marshal(profile)
	var owned CapabilityProfile
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
			if err := checkGroupRevisions(change.Profile.AllowGroups, profile.AllowGroups); err != nil {
				return err
			}
		}
	}
	if exists && profile.Revision <= old.Revision {
		if recordsEqual(old, profile) {
			if s.path != "" {
				return s.saveLocked(s.path)
			}
			return nil
		}
		return fmt.Errorf("%w: capability profile revision", ErrConflict)
	}
	s.capabilityProfiles[profile.ProfileID] = profile
	s.capabilityChanges = append(s.capabilityChanges, capabilityChange{Profile: &profile})
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

func (s *Store) capabilityProfileLocked(auth identity.Envelope) (CapabilityProfile, CapabilityPolicy, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return CapabilityProfile{}, CapabilityPolicy{}, fmt.Errorf("%w: capability identity", ErrUnauthorized)
	}
	profile, exists := s.capabilityProfiles[auth.CapabilityProfile]
	if !exists || profile.Status != ActiveStatus || profile.PrincipalID != auth.PrincipalID || profile.ContextID != auth.ContextID ||
		profile.RuntimeID != auth.RuntimeID || profile.Environment != auth.Environment || profile.Generation != auth.Generation || profile.PolicyVersion != auth.PolicyVersion {
		return CapabilityProfile{}, CapabilityPolicy{}, fmt.Errorf("%w: capability runtime scope", ErrUnauthorized)
	}
	policy, exists := s.capabilityPolicies[profile.PolicyID]
	if !exists || policy.Status != ActiveStatus || policy.Revision != profile.PolicyRevision || !slices.Contains(policy.Members, auth.PrincipalID) {
		return CapabilityProfile{}, CapabilityPolicy{}, fmt.Errorf("%w: current capability policy", ErrUnauthorized)
	}
	return profile, policy, nil
}

func (s *Store) managedToolsLocked(auth identity.Envelope) ([]ProjectedTool, error) {
	profile, policy, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return nil, err
	}
	result := []ProjectedTool{}
	for _, selection := range profile.Selections {
		projected, _, err := s.managedSelectionLocked(auth, profile, policy, selection, nil, true)
		if err == nil {
			result = append(result, projected)
		} else if err != ErrUnauthorized {
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
	slices.SortFunc(result, func(a, b ProjectedTool) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// managedSelfInstallsLocked returns the active bindings this principal
// materialized through the self-install pipeline under the current managed
// policy generation. Provenance is the onboarding record: it carries the
// authenticated confirmation the binding was enabled under, so bindings from
// before the managed era (different policy_version) never leak in.
func (s *Store) managedSelfInstallsLocked(auth identity.Envelope, profile CapabilityProfile) map[string]bool {
	if !profile.SelfInstall {
		return nil
	}
	admitted := map[string]bool{}
	for _, onboarding := range s.onboardings {
		if (onboarding.Mode != OnboardingSelfInstall && onboarding.Mode != OnboardingRemote) || onboarding.BindingID == "" ||
			onboarding.PrincipalID != auth.PrincipalID || onboarding.ContextID != auth.ContextID ||
			onboarding.RuntimeID != auth.RuntimeID || onboarding.PolicyVersion != auth.PolicyVersion {
			continue
		}
		if onboarding.Phase != PhaseConfirmed && onboarding.Phase != PhaseEnabled && onboarding.Phase != PhaseDisabled {
			continue
		}
		admitted[onboarding.BindingID] = true
	}
	return admitted
}

func (s *Store) managedSelfInstallToolsLocked(auth identity.Envelope, profile CapabilityProfile) []ProjectedTool {
	admitted := s.managedSelfInstallsLocked(auth, profile)
	if len(admitted) == 0 {
		return nil
	}
	result := []ProjectedTool{}
	for _, binding := range s.bindings {
		if !admitted[binding.ToolBindingID] || binding.Status != ActiveStatus {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		effective.CapabilityProfileID, effective.CapabilityProfileRevision = profile.ProfileID, profile.Revision
		effective.CapabilityPolicyID, effective.CapabilityPolicyRevision = profile.PolicyID, profile.PolicyRevision
		effective.ImplementationDigest = DefinitionDigest(effective.Definition)
		for _, tool := range effective.Definition.Tools {
			result = append(result, ProjectedTool{
				Name:         ProjectedToolName(effective.Definition.DefinitionID, effective.Definition.Version, tool.Name),
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
func (s *Store) managedSelfInstallToolLocked(auth identity.Envelope, profile CapabilityProfile, name string) (ProjectedTool, EffectiveBinding, error) {
	admitted := s.managedSelfInstallsLocked(auth, profile)
	for _, binding := range s.bindings {
		if !admitted[binding.ToolBindingID] || binding.Status != ActiveStatus {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		for _, tool := range effective.Definition.Tools {
			if ProjectedToolName(effective.Definition.DefinitionID, effective.Definition.Version, tool.Name) != name {
				continue
			}
			effective.CapabilityProfileID, effective.CapabilityProfileRevision = profile.ProfileID, profile.Revision
			effective.CapabilityPolicyID, effective.CapabilityPolicyRevision = profile.PolicyID, profile.PolicyRevision
			effective.ImplementationDigest = DefinitionDigest(effective.Definition)
			return ProjectedTool{Name: name, BindingID: binding.ToolBindingID, DefinitionID: effective.Definition.DefinitionID,
				Version: effective.Definition.Version, Tool: tool}, effective, nil
		}
	}
	return ProjectedTool{}, EffectiveBinding{}, fmt.Errorf("%w: projected tool", ErrNotFound)
}

// PreviewCapabilityProfile diffs a candidate profile against the stored one
// using the exact evaluator and admission checks apply and dispatch run, so a
// previewed grant can never diverge from what publish+dispatch would do. The
// draft needs only structural validity; confirmation remains the apply gate.
func (s *Store) PreviewCapabilityProfile(candidate CapabilityProfile) (ProfilePreview, error) {
	if err := candidate.validateStructure(); err != nil {
		return ProfilePreview{}, err
	}
	body, _ := json.Marshal(candidate)
	var owned CapabilityProfile
	_ = json.Unmarshal(body, &owned)
	candidate = owned
	s.mu.RLock()
	defer s.mu.RUnlock()
	preview := ProfilePreview{ProfileID: candidate.ProfileID, CandidateRevision: candidate.Revision,
		Current: []ProfilePreviewEntry{}, Candidate: []ProfilePreviewEntry{},
		Added: []string{}, Removed: []string{}, Changed: []string{}}
	candidatePolicy, err := s.validateProfileAdmissionLocked(candidate)
	if err != nil {
		return preview, err
	}
	if current, ok := s.capabilityProfiles[candidate.ProfileID]; ok {
		if candidate.Revision <= current.Revision {
			return preview, fmt.Errorf("%w: capability profile revision", ErrConflict)
		}
		preview.CurrentRevision = current.Revision
		if current.Status == ActiveStatus {
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
	currentByName := map[string]ProfilePreviewEntry{}
	for _, entry := range preview.Current {
		if entry.Admitted {
			currentByName[entry.Name] = entry
		}
	}
	candidateByName := map[string]ProfilePreviewEntry{}
	for _, entry := range preview.Candidate {
		if entry.Admitted {
			candidateByName[entry.Name] = entry
		}
	}
	for name, next := range candidateByName {
		prev, ok := currentByName[name]
		if !ok {
			preview.Added = append(preview.Added, name)
		} else if !previewEntriesEqual(prev, next) {
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
func (s *Store) previewSelectionsLocked(profile CapabilityProfile, policy CapabilityPolicy) []ProfilePreviewEntry {
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: profile.PrincipalID, ContextID: profile.ContextID, RuntimeID: profile.RuntimeID,
		ExternalIdentityID: profile.ContextID, ConversationID: profile.ContextID, DeliveryTargetID: profile.ContextID,
		PolicyVersion: profile.PolicyVersion, CapabilityProfile: profile.ProfileID, Environment: profile.Environment, Generation: profile.Generation}
	entries := []ProfilePreviewEntry{}
	for _, selection := range profile.Selections {
		entry := ProfilePreviewEntry{Name: selection.Name, CapabilityID: selection.CapabilityID}
		_, effective, err := s.managedSelectionLocked(auth, profile, policy, selection, nil, true)
		if err != nil {
			entry.Reason = "unauthorized"
			if errors.Is(err, ErrConflict) {
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
func (s *Store) policyAtRevisionLocked(policyID string, revision uint64) (CapabilityPolicy, bool) {
	if policy, ok := s.capabilityPolicies[policyID]; ok && policy.Revision == revision {
		return policy, true
	}
	for _, change := range s.capabilityChanges {
		if change.Policy != nil && change.Policy.PolicyID == policyID && change.Policy.Revision == revision {
			return *change.Policy, true
		}
	}
	return CapabilityPolicy{}, false
}

func (s *Store) managedSelectionLocked(auth identity.Envelope, profile CapabilityProfile, policy CapabilityPolicy, selection CapabilitySelection, arguments map[string]any, projection bool) (ProjectedTool, EffectiveBinding, error) {
	var found *EffectiveBinding
	for _, binding := range s.bindings {
		if binding.DefinitionID != selection.DefinitionID || binding.DefinitionVersion != selection.DefinitionVersion || binding.ConnectionID != selection.ConnectionID {
			continue
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		if found != nil {
			return ProjectedTool{}, EffectiveBinding{}, fmt.Errorf("%w: ambiguous capability binding", ErrConflict)
		}
		found = &effective
	}
	if found == nil || DefinitionDigest(found.Definition) != selection.ImplementationDigest {
		return ProjectedTool{}, EffectiveBinding{}, ErrUnauthorized
	}
	for _, tool := range found.Definition.Tools {
		if tool.Name != selection.ToolName || tool.CapabilityID != selection.CapabilityID {
			continue
		}
		limits, scopes, allowed := capabilityDecision(profile, policy, selection, tool, arguments, projection)
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
		return ProjectedTool{Name: selection.Name, BindingID: found.Binding.ToolBindingID, DefinitionID: selection.DefinitionID, Version: selection.DefinitionVersion, Tool: tool}, *found, nil
	}
	return ProjectedTool{}, EffectiveBinding{}, ErrUnauthorized
}

// ReverifyEffective rechecks the authority a call was admitted under. Export
// and apply paths run this so a mid-flight revocation, binding update or
// policy/profile revision change stops the write instead of landing on
// stale authority. The fence can fire from inside AuthorizeProjectedCall,
// which holds s.mu.RLock for the whole dispatch: queuing a nested read lock
// behind a pending writer would deadlock the call forever, so contention
// fails closed — under the held outer lock the records cannot have changed,
// and without it a live writer means authority is genuinely in flux.
func (s *Store) ReverifyEffective(effective EffectiveBinding) error {
	if !s.mu.TryRLock() {
		return fmt.Errorf("%w: authority recheck contended", ErrUnauthorized)
	}
	defer s.mu.RUnlock()
	binding, ok := s.bindings[effective.Binding.ToolBindingID]
	if !ok || binding.Status != ActiveStatus || binding.Revision != effective.Binding.Revision || binding.ProjectionRevision != effective.Binding.ProjectionRevision {
		return fmt.Errorf("%w: binding revoked or changed", ErrUnauthorized)
	}
	// Capability profile/policy ids exist only on managed admissions. An
	// unmanaged effective carries neither, and the binding freshness above is
	// its whole authority — demanding a profile here would deny every
	// unmanaged export as "changed".
	if effective.CapabilityProfileID == "" {
		if effective.CapabilityProfileRevision != 0 || effective.CapabilityPolicyID != "" || effective.CapabilityPolicyRevision != 0 {
			return fmt.Errorf("%w: capability identity incomplete", ErrUnauthorized)
		}
		return nil
	}
	profile, ok := s.capabilityProfiles[effective.CapabilityProfileID]
	if !ok || profile.Status != ActiveStatus || profile.Revision != effective.CapabilityProfileRevision {
		return fmt.Errorf("%w: capability profile changed", ErrUnauthorized)
	}
	policy, ok := s.capabilityPolicies[profile.PolicyID]
	if !ok || policy.Status != ActiveStatus || policy.Revision != effective.CapabilityPolicyRevision {
		return fmt.Errorf("%w: capability policy changed", ErrUnauthorized)
	}
	return nil
}

func (s *Store) managedToolLocked(auth identity.Envelope, name string, arguments map[string]any, projection bool) (ProjectedTool, EffectiveBinding, error) {
	profile, policy, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return ProjectedTool{}, EffectiveBinding{}, err
	}
	for _, selection := range profile.Selections {
		if selection.Name == name {
			tool, effective, err := s.managedSelectionLocked(auth, profile, policy, selection, arguments, projection)
			if err != ErrUnauthorized {
				return tool, effective, err
			}
			break
		}
	}
	return s.managedSelfInstallToolLocked(auth, profile, name)
}
