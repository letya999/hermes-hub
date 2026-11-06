package toolhub

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Rights-ledger mutations (SPEC-0092 R2.3): capability grants live inside
// the governance document so the PDP's live view, the audit history and the
// grants.yaml projection all read one store. Issuers are enforced against
// the rights vocabulary — cross-subject rights refuse operator-minted
// records by construction.

// GrantRight validates the record against the vocabulary row and appends it
// to the governance ledger. The caller stamps api.Confirm first — like every
// authority-bearing record in the document.
func (g *GovernanceStore) GrantRight(grant api.RightGrant, rights api.RightsDoc, now time.Time) error {
	row, ok := rights.Lookup(grant.Capability, grant.Operation)
	if !ok {
		return api.ErrInvalid
	}
	return g.Update(now, func(doc *api.Governance) error {
		return doc.PutRightGrant(grant, row, now)
	})
}

// RevokeRight flips one capability grant to revoked; the next Decide denies.
func (g *GovernanceStore) RevokeRight(grantID, by, reason string, now time.Time) error {
	return g.Update(now, func(doc *api.Governance) error {
		return doc.RevokeRightGrant(grantID, by, reason, now)
	})
}

// RightGrants lists the principal's capability-grant ledger rows, active and
// historical — the live view grants.yaml snapshots are compiled from.
func (g *GovernanceStore) RightGrants() ([]api.RightGrant, error) {
	doc, err := g.Load()
	if err != nil {
		return nil, err
	}
	return doc.Rights, nil
}

// profilesForPrincipal lists the active capability profiles owned by the
// principal — the ceiling rows DeriveRights rewrites.
func (s *Store) profilesForPrincipal(principal string) []api.CapabilityProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []api.CapabilityProfile
	for _, profile := range s.capabilityProfiles {
		if profile.PrincipalID == principal && profile.Status == api.ActiveStatus {
			out = append(out, profile)
		}
	}
	return out
}

// SetBoundProfileActive changes only the named managed generation. Recovery
// never revives an independently revoked profile or touches a sibling env.
func (s *Store) SetBoundProfileActive(principal, environment, profileID string, active bool, now time.Time) error {
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	profile, ok := s.capabilityProfiles[profileID]
	s.mu.RUnlock()
	if !ok || profile.PrincipalID != principal || profile.Environment != environment ||
		(profile.Status != api.ActiveStatus && profile.Status != api.DisabledStatus) {
		return fmt.Errorf("%w: bound profile unavailable", api.ErrUnauthorized)
	}
	want := api.DisabledStatus
	if active {
		want = api.ActiveStatus
	}
	if profile.Status == want {
		return nil
	}
	profile.Status = want
	profile.Revision++
	profile.IssuedBy = "platform"
	profile.IssuedAt = now.UTC()
	profile.Confirmation = nil
	if err := api.Confirm(&profile, "platform", now); err != nil {
		return err
	}
	return s.PutCapabilityProfile(profile)
}

// RequireActiveBoundProfile prevents a publish from silently succeeding when
// its named generation is absent or was disabled by an earlier rollback.
func (s *Store) RequireActiveBoundProfile(principal, environment, profileID string, generation uint64) error {
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	profile, ok := s.capabilityProfiles[profileID]
	s.mu.RUnlock()
	if !ok || profile.PrincipalID != principal || profile.Environment != environment ||
		profile.Generation != generation || profile.Status != api.ActiveStatus {
		return fmt.Errorf("%w: bound profile unavailable", api.ErrUnauthorized)
	}
	return nil
}

// DeriveRights binds a published grants ceiling to active profiles. Every
// requested row must already have an independently issued live ledger grant;
// publishing a passport never issues or revokes a grant.
func equalRightRules(a, b []api.RightRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// CheckOrganizationRights preflights an authored grants revision against the
// exact protected profile and its active organization policy before publish.
func (s *Store) CheckOrganizationRights(principal, environment, organization, profileID string, generation uint64, allow map[string][]string) error {
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	profile, ok := s.capabilityProfiles[profileID]
	if !ok || profile.Status != api.ActiveStatus || profile.PrincipalID != principal || profile.Environment != environment || profile.Generation != generation {
		return fmt.Errorf("%w: organization profile unavailable", api.ErrUnauthorized)
	}
	policy, ok := s.capabilityPolicies[profile.PolicyID]
	if !ok || policy.Status != api.ActiveStatus || policy.Revision != profile.PolicyRevision || policy.Organization != organization || !slices.Contains(policy.Members, principal) {
		return fmt.Errorf("%w: organization policy unavailable", api.ErrUnauthorized)
	}
	for capability, operations := range allow {
		for _, operation := range operations {
			if !slices.Contains(policy.RightCeiling, api.RightRule{Capability: capability, Operation: operation}) {
				return fmt.Errorf("%w: %s.%s exceeds organization ceiling", api.ErrUnauthorized, capability, operation)
			}
		}
	}
	return nil
}

// DeriveRights rewrites the managed rights ceiling of exactly the profiles
// the published revision binds: same principal, same environment, and — when
// the revision's tools.binding names a profile — only that profile. A dev
// apply must never rewrite the prod profile's rights, and a bound revision
// must never touch a sibling generation.
func DeriveRights(store *Store, gov *GovernanceStore, doc api.RightsDoc, principal string, allow map[string][]string, environment, profileID, policyVersion string, now time.Time) error {
	return deriveRights(store, gov, doc, principal, allow, environment, profileID, policyVersion, "", "", nil, nil, now)
}

// DeriveRightsAndSelections replaces the bound profile's selections with the
// published tools passport. Bindings and connections keep their own lifecycle.
func DeriveRightsAndSelections(store *Store, gov *GovernanceStore, doc api.RightsDoc, principal string, allow map[string][]string, environment, profileID, policyVersion, definitionID string, selections []api.CapabilitySelection, core []string, now time.Time) error {
	return deriveRights(store, gov, doc, principal, allow, environment, profileID, policyVersion, "", definitionID, selections, core, now)
}

// DerivePublishedRightsAndSelections stamps the immutable passport identity
// with the profile update. The gateway compares it to current on each call.
func DerivePublishedRightsAndSelections(store *Store, gov *GovernanceStore, doc api.RightsDoc, principal string, allow map[string][]string, environment, profileID, policyVersion, passportRevision, definitionID string, selections []api.CapabilitySelection, core []string, now time.Time) error {
	return deriveRights(store, gov, doc, principal, allow, environment, profileID, policyVersion, passportRevision, definitionID, selections, core, now)
}

func deriveRights(store *Store, gov *GovernanceStore, doc api.RightsDoc, principal string, allow map[string][]string, environment, profileID, policyVersion, passportRevision, definitionID string, selections []api.CapabilitySelection, core []string, now time.Time) error {
	issued, err := IssuedRightCeiling(gov, doc, principal, now)
	if err != nil {
		return err
	}
	rules := make([]api.RightRule, 0, len(allow))
	for capability, ops := range allow {
		for _, op := range ops {
			if !slices.Contains(issued[capability], op) {
				return fmt.Errorf("%w: %s.%s has no active issued grant", api.ErrUnauthorized, capability, op)
			}
			rules = append(rules, api.RightRule{Capability: capability, Operation: op})
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Capability != rules[j].Capability {
			return rules[i].Capability < rules[j].Capability
		}
		return rules[i].Operation < rules[j].Operation
	})
	var bound []api.CapabilityProfile
	for _, profile := range store.profilesForPrincipal(principal) {
		if profile.Environment != environment {
			continue
		}
		if profileID != "" && profile.ProfileID != profileID {
			continue
		}
		bound = append(bound, profile)
	}
	for _, profile := range bound {
		repin := policyVersion != "" && profile.PolicyVersion != policyVersion
		selectionChanged := definitionID != "" && (!reflect.DeepEqual(profile.Selections, selections) || !slices.Equal(profile.CoreTools, core) || !profile.CoreBound || profile.PassportRevision != passportRevision)
		if equalRightRules(profile.Rights, rules) && !repin && !selectionChanged {
			continue
		}
		profile.Rights = rules
		if definitionID != "" {
			profile.PassportRevision = passportRevision
			profile.Selections = slices.Clone(selections)
			profile.CoreBound = true
			profile.CoreTools = slices.Clone(core)
		}
		if repin {
			profile.PolicyVersion = policyVersion
		}
		profile.Revision++
		profile.IssuedBy = "platform"
		profile.IssuedAt = now.UTC()
		profile.Confirmation = nil
		if err := api.Confirm(&profile, "platform", now); err != nil {
			return err
		}
		if err := store.PutCapabilityProfile(profile); err != nil {
			return err
		}
	}
	for _, profile := range store.profilesForPrincipal(principal) {
		if err := store.repinManagedBindings(profile, environment, policyVersion); err != nil {
			return err
		}
	}
	return nil
}

// IssuedRightCeiling exposes only active, unscoped self grants for this
// principal. A path-scoped grant remains a live PDP constraint, never a
// blanket passport operation.
func IssuedRightCeiling(gov *GovernanceStore, doc api.RightsDoc, principal string, now time.Time) (map[string][]string, error) {
	if gov == nil {
		return nil, fmt.Errorf("%w: rights ledger unavailable", api.ErrUnauthorized)
	}
	grants, err := gov.RightGrants()
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, grant := range grants {
		row, ok := doc.Lookup(grant.Capability, grant.Operation)
		if !ok || !grant.Active(now) || grant.PrincipalID != principal || grant.Scope != "" || row.Subject != "self" || !slices.Contains(row.Issuers, grant.GrantedBy) {
			continue
		}
		if !slices.Contains(out[grant.Capability], grant.Operation) {
			out[grant.Capability] = append(out[grant.Capability], grant.Operation)
		}
	}
	return out, nil
}

// repinManagedBindings carries the principal's existing bindings onto a
// rotated policy_version. resolveLocked requires binding pv == envelope
// pv, so a rotation that repins the profile but not its bindings silently
// drops every managed selection — the space's projection collapses to
// zero tools (CHG-0275 live incident). For each credential-free
// definition the profile's selections reference, an existing active
// binding is re-enabled under the new pv — Enable restamps the
// deterministic row in place and supersedes stale siblings. A definition
// the principal never bound stays an operator act; credential-bearing
// bindings keep their documented manual repin path.
func (s *Store) repinManagedBindings(profile api.CapabilityProfile, environment, policyVersion string) error {
	if profile.Environment != environment || profile.PolicyVersion != policyVersion || policyVersion == "" {
		return nil
	}
	selected := map[string]bool{}
	for _, selection := range profile.Selections {
		selected[selection.DefinitionID+"\x00"+selection.DefinitionVersion] = true
	}
	if len(selected) == 0 {
		return nil
	}
	s.mu.RLock()
	var stale []api.ToolBinding
	for _, binding := range s.bindings {
		if binding.Status != api.ActiveStatus || binding.PolicyVersion == policyVersion ||
			binding.PrincipalID != profile.PrincipalID || binding.ContextID != profile.ContextID || binding.RuntimeID != profile.RuntimeID {
			continue
		}
		if !selected[binding.DefinitionID+"\x00"+binding.DefinitionVersion] {
			continue
		}
		_, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
		if !ok {
			continue
		}
		stale = append(stale, binding)
	}
	s.mu.RUnlock()
	if len(stale) == 0 {
		return nil
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: profile.PrincipalID, ExternalIdentityID: profile.PrincipalID,
		ContextID: profile.ContextID, RuntimeID: profile.RuntimeID, ConversationID: "platform", DeliveryTargetID: "platform", PolicyVersion: policyVersion}
	for _, binding := range stale {
		if _, err := s.Enable(auth, binding.DefinitionID, binding.DefinitionVersion); err != nil {
			return fmt.Errorf("binding repin %s@%s: %w", binding.DefinitionID, binding.DefinitionVersion, err)
		}
	}
	return nil
}
