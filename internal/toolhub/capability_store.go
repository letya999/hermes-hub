package toolhub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
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
	if s.db != nil {
		return s.lockDBCapabilityAdmission()
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

// lockDBCapabilityAdmission is the SQLite form of the shared disk fence:
// BEGIN IMMEDIATE pins the writer lock for the whole admission window, so no
// process can commit a revoke between the digest check and the admitted
// call — the same exclusion the .lock file gives the JSON store. Writers
// store the snapshot digest inside their commit, which replaces the
// file-bytes comparison.
func (s *Store) lockDBCapabilityAdmission() (func(), error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	release := func() {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		_ = conn.Close()
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		release()
		return nil, err
	}
	var storedDigest string
	err = conn.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'digest'`).Scan(&storedDigest)
	if err != nil {
		release()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: capability snapshot", api.ErrStale)
		}
		return nil, err
	}
	stored, err := hex.DecodeString(storedDigest)
	if err != nil || !bytes.Equal(stored, s.diskDigest[:]) {
		release()
		return nil, fmt.Errorf("%w: capability snapshot changed before admission", api.ErrStale)
	}
	return release, nil
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
// ceiling, every selection pins a reviewed definition revision by digest, and
// — for a new write of an active profile — no selection the write
// introduces or repins may abandon the rules' implementation digest: when
// the same capability tuple is governed at another digest, the selection is
// the digest-split wedge a publish creates repinning ahead of catalog
// enable, and rejecting the write at apply surfaces the cause instead of
// silently emptying the projection. Plain uncovered selections (revoked
// rules, never-authorized intent) stay writable — suppression under
// projection is a legitimate state the denial surface and doctor report.
// A selection carried byte-identical from the stored record re-affirms
// existing authority: if its rules were since revoked, the gap is a
// policy-side decision, not something this write introduced. Journal
// replay skips the check: historical records load as written so a stored
// split remains loadable and repairable, and a disabled profile projects
// nothing to guard. Preview runs the identical checks so a previewed
// profile is exactly what apply would admit.
func (s *Store) validateProfileAdmissionLocked(profile api.CapabilityProfile, replay bool) (api.CapabilityPolicy, error) {
	policy, exists := s.capabilityPolicies[profile.PolicyID]
	if !exists || policy.Status != api.ActiveStatus || policy.Revision != profile.PolicyRevision || !slices.Contains(policy.Members, profile.PrincipalID) {
		return api.CapabilityPolicy{}, fmt.Errorf("%w: profile policy or membership", api.ErrUnauthorized)
	}
	if policy.Organization != "" {
		for _, right := range profile.Rights {
			if !slices.Contains(policy.RightCeiling, right) {
				return api.CapabilityPolicy{}, fmt.Errorf("%w: profile right exceeds organization ceiling", api.ErrUnauthorized)
			}
		}
	}
	for _, rules := range append([][]api.CapabilityRule{profile.Allows}, api.GroupRuleSets(profile.AllowGroups)...) {
		for _, rule := range rules {
			if !slices.ContainsFunc(policy.Ceiling, func(ceiling api.CapabilityRule) bool { return api.RuleInside(ceiling, rule) }) {
				return api.CapabilityPolicy{}, fmt.Errorf("%w: profile allow exceeds ceiling", api.ErrUnauthorized)
			}
		}
	}
	carried := map[string]bool{}
	for _, stored := range s.capabilityProfiles[profile.ProfileID].Selections {
		body, _ := json.Marshal(stored)
		carried[string(body)] = true
	}
	for _, selection := range profile.Selections {
		definition, exists := s.definitions[definitionKey(selection.DefinitionID, selection.DefinitionVersion)]
		if !exists || api.DefinitionDigest(definition) != selection.ImplementationDigest {
			return api.CapabilityPolicy{}, fmt.Errorf("%w: unreviewed capability implementation", api.ErrUnauthorized)
		}
		tool, ok := selectionToolSpec(definition, selection)
		if !ok {
			return api.CapabilityPolicy{}, fmt.Errorf("%w: unreviewed capability implementation", api.ErrUnauthorized)
		}
		if replay || profile.Status != api.ActiveStatus {
			continue
		}
		if body, _ := json.Marshal(selection); carried[string(body)] {
			continue
		}
		now := time.Now().UTC()
		if !api.CapabilityCoverage(profile, policy, selection, tool, now) && api.CapabilityRepinConflict(profile, policy, selection, tool, now) {
			return api.CapabilityPolicy{}, fmt.Errorf("%w: selection %s repinned past capability rules still pinned to another implementation digest", api.ErrUnauthorized, selection.Name)
		}
	}
	return policy, nil
}

// selectionToolSpec resolves the catalog tool a selection pins — the same
// name/capability match admission and projection use.
func selectionToolSpec(definition api.ToolDefinition, selection api.CapabilitySelection) (api.ToolSpec, bool) {
	for _, tool := range definition.Tools {
		if tool.Name == selection.ToolName && tool.CapabilityID == selection.CapabilityID && (len(tool.Uses) > 0 || api.RemoteExecutor(tool.Executor)) {
			return tool, true
		}
	}
	return api.ToolSpec{}, false
}

func (s *Store) PutCapabilityProfile(profile api.CapabilityProfile) error {
	return s.putCapabilityProfile(profile, false)
}

// putCapabilityProfile appends a profile record to the journal. Replay mode
// exists only for snapshot restore: a stored record may predate a check the
// current code enforces, and refusing it would strand the whole store.
func (s *Store) putCapabilityProfile(profile api.CapabilityProfile, replay bool) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	body, _ := json.Marshal(profile)
	var owned api.CapabilityProfile
	_ = json.Unmarshal(body, &owned)
	profile = owned
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.validateProfileAdmissionLocked(profile, replay); err != nil {
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
	if !exists || policy.Status != api.ActiveStatus || policy.Revision != profile.PolicyRevision || policy.Organization != auth.Organization || !slices.Contains(policy.Members, auth.PrincipalID) {
		return api.CapabilityProfile{}, api.CapabilityPolicy{}, fmt.Errorf("%w: current capability policy", api.ErrUnauthorized)
	}
	return profile, policy, nil
}

// ProfileOrganization reads the protected policy binding for a host CLI that
// constructs its own envelope before invoking a managed control operation.
func (s *Store) ProfileOrganization(auth identity.Envelope) (string, error) {
	if err := s.Reload(); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	profile, ok := s.capabilityProfiles[auth.CapabilityProfile]
	if !ok || profile.Status != api.ActiveStatus || profile.PrincipalID != auth.PrincipalID || profile.ContextID != auth.ContextID ||
		profile.RuntimeID != auth.RuntimeID || profile.Environment != auth.Environment || profile.Generation != auth.Generation || profile.PolicyVersion != auth.PolicyVersion {
		return "", api.ErrUnauthorized
	}
	policy, ok := s.capabilityPolicies[profile.PolicyID]
	if !ok || policy.Status != api.ActiveStatus || policy.Revision != profile.PolicyRevision || !slices.Contains(policy.Members, auth.PrincipalID) {
		return "", api.ErrUnauthorized
	}
	return policy.Organization, nil
}

// CoreToolNames returns the profile's pinned direct-projection set (R6.1).
// A nil result means the profile carries no core bound — every admitted tool
// keeps its direct schema (legacy posture for unmanaged bindings).
func (s *Store) CoreToolNames(auth identity.Envelope) (map[string]bool, error) {
	if auth.CapabilityProfile == "" {
		return nil, nil
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	profile, _, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return nil, err
	}
	if len(profile.CoreTools) == 0 && !profile.CoreBound {
		return nil, nil
	}
	out := make(map[string]bool, len(profile.CoreTools))
	for _, name := range profile.CoreTools {
		out[name] = true
	}
	return out, nil
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
	for id, binding := range s.bindings {
		if binding.Status != api.ActiveStatus {
			continue
		}
		onboarding, ok := s.selfInstallOnboardingLocked(id)
		if !ok || onboarding.PrincipalID != auth.PrincipalID || onboarding.ContextID != auth.ContextID || onboarding.RuntimeID != auth.RuntimeID || onboarding.PolicyVersion != auth.PolicyVersion {
			continue
		}
		admitted[id] = true
	}
	return admitted
}

// A credential rotation creates a new binding ID. The confirmed self-install
// provenance follows only a revoked predecessor on the same owner, connection,
// immutable definition version and policy generation.
func (s *Store) selfInstallOnboardingLocked(bindingID string) (api.Onboarding, bool) {
	binding, exists := s.bindings[bindingID]
	if !exists {
		return api.Onboarding{}, false
	}
	var inherited api.Onboarding
	var found bool
	for _, onboarding := range s.onboardings {
		if (onboarding.Mode != api.OnboardingSelfInstall && onboarding.Mode != api.OnboardingRemote) ||
			(onboarding.Phase != api.PhaseConfirmed && onboarding.Phase != api.PhaseEnabled && onboarding.Phase != api.PhaseDisabled) {
			continue
		}
		if onboarding.BindingID == bindingID {
			return onboarding, true
		}
		old, ok := s.bindings[onboarding.BindingID]
		if !ok || old.Status != api.RevokedStatus || onboarding.Phase == api.PhaseDisabled ||
			old.ConnectionID == "" || old.ConnectionID != binding.ConnectionID ||
			old.DefinitionID != binding.DefinitionID || old.DefinitionVersion != binding.DefinitionVersion ||
			old.PrincipalID != binding.PrincipalID || old.ContextID != binding.ContextID ||
			old.RuntimeID != binding.RuntimeID || old.PolicyVersion != binding.PolicyVersion ||
			binding.CredentialRevision <= old.CredentialRevision {
			continue
		}
		if !found || !onboarding.WritesEnabled {
			inherited = onboarding
			found = true
		}
	}
	return inherited, found
}

func (s *Store) selfInstallWritesLocked(bindingID string) bool {
	onboarding, ok := s.selfInstallOnboardingLocked(bindingID)
	return ok && onboarding.WritesEnabled
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
		for _, tool := range effective.Definition.ProjectedToolSpecs(effective.Binding) {
			if tool.Effect == api.WriteEffect && !s.selfInstallWritesLocked(binding.ToolBindingID) {
				continue
			}
			if len(s.rightsMissingLocked(auth, profile, tool, time.Now().UTC())) > 0 {
				continue
			}
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
		for _, tool := range effective.Definition.ProjectedToolSpecs(effective.Binding) {
			if tool.Effect == api.WriteEffect && !s.selfInstallWritesLocked(binding.ToolBindingID) {
				continue
			}
			if len(s.rightsMissingLocked(auth, profile, tool, time.Now().UTC())) > 0 {
				continue
			}
			if api.ProjectedToolName(effective.Definition.DefinitionID, effective.Definition.Version, tool.Name) != name {
				continue
			}
			effective.CapabilityProfileID, effective.CapabilityProfileRevision = profile.ProfileID, profile.Revision
			effective.CapabilityPolicyID, effective.CapabilityPolicyRevision = profile.PolicyID, profile.PolicyRevision
			effective.ImplementationDigest = api.DefinitionDigest(effective.Definition)
			effective.SelectedToolName = tool.Name
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
	candidatePolicy, err := s.validateProfileAdmissionLocked(candidate, false)
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
		PolicyVersion: profile.PolicyVersion, CapabilityProfile: profile.ProfileID, Environment: profile.Environment, Generation: profile.Generation, Organization: policy.Organization}
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
	onboarding, selfInstall := s.selfInstallOnboardingLocked(found.Binding.ToolBindingID)
	for _, tool := range found.Definition.ProjectedToolSpecs(found.Binding) {
		if tool.Name != selection.ToolName || tool.CapabilityID != selection.CapabilityID {
			continue
		}
		if selfInstall && tool.Effect == api.WriteEffect && !onboarding.WritesEnabled {
			break
		}
		limits, scopes, allowed := api.CapabilityDecision(profile, policy, selection, tool, arguments, projection)
		if !allowed {
			break
		}
		scopes, allowed = api.NarrowSelectionScopes(selection, tool, arguments, scopes, projection)
		if !allowed {
			break
		}
		// RBAC requires (SPEC-0092 R2): when the published profile carries a
		// rights ceiling, every right the tool declares must decide-allow —
		// a missing grant hides the tool rather than erroring at call time.
		if len(s.rightsMissingLocked(auth, profile, tool, time.Now().UTC())) > 0 {
			break
		}
		found.Definition.Execution.OutputBytes = min(found.Definition.Execution.OutputBytes, limits.OutputBytes)
		found.Definition.Execution.TimeoutSeconds = min(found.Definition.Execution.TimeoutSeconds, limits.TimeoutSeconds)
		found.CapabilityID, found.CapabilityPolicyRevision, found.CapabilityProfileRevision = selection.CapabilityID, policy.Revision, profile.Revision
		found.CapabilityPolicyID, found.CapabilityProfileID = policy.PolicyID, profile.ProfileID
		found.ImplementationDigest = selection.ImplementationDigest
		found.SelectedToolName = tool.Name
		found.CapabilityScopes = scopes
		if projection {
			// Allowed-only description: the model sees the admitted boundary,
			// never the full catalog surface this tool could serve elsewhere.
			if len(selection.PathScopes) > 0 {
				tool.Description = strings.TrimSpace(tool.Description + " Selected paths: " + strings.Join(selection.PathScopes, ", ") + ".")
			}
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
	for _, tool := range effective.Definition.ProjectedToolSpecs(binding) {
		if tool.Name == effective.SelectedToolName {
			if missing := s.rightsMissingLocked(identity.Envelope{PrincipalID: profile.PrincipalID}, profile, tool, time.Now().UTC()); len(missing) == 0 {
				return nil
			}
			return fmt.Errorf("%w: right revoked", api.ErrUnauthorized)
		}
	}
	return fmt.Errorf("%w: selected tool unavailable", api.ErrUnauthorized)
}

func (s *Store) managedToolLocked(auth identity.Envelope, name string, arguments map[string]any, projection bool) (api.ProjectedTool, api.EffectiveBinding, error) {
	profile, policy, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return api.ProjectedTool{}, api.EffectiveBinding{}, err
	}
	for _, selection := range profile.Selections {
		if selection.Name != name {
			continue
		}
		tool, effective, err := s.managedSelectionLocked(auth, profile, policy, selection, arguments, projection)
		if !errors.Is(err, api.ErrUnauthorized) {
			return tool, effective, err
		}
		// The caller's own profile declares this name, so the call is denied —
		// arguments outside the admitted capability, a missing right or a stale
		// binding — not missing. Masking it as record-not-found sends the model
		// back into tool_search for a name it can already see in its inventory.
		return api.ProjectedTool{}, api.EffectiveBinding{}, fmt.Errorf("%w: %s is denied for this call — run tool_search for its admitted shape and missing right", api.ErrUnauthorized, name)
	}
	return api.ProjectedTool{}, api.EffectiveBinding{}, api.ErrNotFound
}
