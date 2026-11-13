package toolhub

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The rights PDP (SPEC-0092 R2.2): every protected action asks
// Store.Decide(envelope, capability, operation, scope) before acting. The
// decision intersects four authorities:
//
//  1. vocabulary — RightsDoc from the immutable platform catalog release the
//     image ships; unknown capability.operation never allows;
//  2. ceiling — the managed capability profile's Rights rows, the flattened
//     grants.yaml allow the published revision carried (unmanaged callers
//     skip this layer: the live ledger alone bounds them);
//  3. organization right ceiling — the active policy's protected maximum;
//  4. ledger — Governance.Rights grants: active, unexpired, scope-covering.
//
// A revoke flips the ledger record, so the next Decide denies without a
// republish; Epoch reports the governance revision the verdict was taken at.

// Rights is the pinned vocabulary the store enforces. Wired at server
// start from the image's platform catalog; an empty doc fails closed —
// Decide denies every right the vocabulary cannot name.
func (s *Store) decideLocked(auth identity.Envelope, capability, operation, scope string, now time.Time) (api.RightDecision, error) {
	// Ceiling: managed callers hold only what the published revision carried.
	ceiling := []api.RightRule(nil)
	enforce := auth.CapabilityProfile != ""
	if enforce {
		profile, _, err := s.capabilityProfileLocked(auth)
		if err != nil {
			return api.RightDecision{Reason: "capability profile unavailable"}, err
		}
		ceiling = s.managedRightCeilingLocked(profile)
	}
	return s.decideCoreLocked(auth.PrincipalID, ceiling, enforce, capability, operation, scope, now)
}

// decideCoreLocked is the shared verdict body: vocabulary first, then the
// published ceiling when one applies, then the live ledger grant.
func (s *Store) decideCoreLocked(principal string, ceiling []api.RightRule, enforceCeiling bool, capability, operation, scope string, now time.Time) (api.RightDecision, error) {
	row, ok := s.RightsDoc.Lookup(capability, operation)
	if !ok {
		return api.RightDecision{Reason: "unknown capability.operation"}, nil
	}
	ledger, err := s.loadRightsLedgerLocked()
	if err != nil {
		return api.RightDecision{Reason: "rights ledger unavailable"}, err
	}
	var epoch uint64
	if ledger != nil {
		epoch = ledger.Revision
	}
	if enforceCeiling {
		inside := slices.Contains(ceiling, api.RightRule{Capability: capability, Operation: operation})
		if !inside {
			return api.RightDecision{Reason: "outside the published ceiling", Epoch: epoch}, nil
		}
	}
	// Cross-subject ops bind the call scope to a named target; an unscoped
	// request can never be satisfied by a scoped grant.
	if row.Subject == "cross" && scope == "" {
		return api.RightDecision{Reason: "cross-subject right requires a scope target", Epoch: epoch}, nil
	}
	if row.Subject == "self" && scope != "" && row.Scope != "path" && row.Scope != "model" && row.Scope != "toolset" && row.Scope != "skill" && row.Scope != "connector" && row.Scope != "service" && row.Scope != "channel" && row.Scope != "log" {
		return api.RightDecision{Reason: "right does not accept a scope", Epoch: epoch}, nil
	}
	grant := rightGrant(ledger, principal, capability, operation, scope, row, now)
	if grant == nil {
		return api.RightDecision{Reason: "no active grant", Epoch: epoch}, nil
	}
	return api.RightDecision{Allow: true, Epoch: epoch}, nil
}

// DecideAsserted serves trusted in-space services: they hold the
// control-plane bearer and assert this space's principal and environment.
// Missing or mismatched profiles enforce an empty ceiling.
func (s *Store) DecideAsserted(principal, environment, capability, operation, scope string, now time.Time) (api.RightDecision, error) {
	if !identity.ValidID(principal) {
		return api.RightDecision{Reason: "principal invalid"}, fmt.Errorf("%w: asserted principal", api.ErrInvalid)
	}
	if err := s.Reload(); err != nil {
		return api.RightDecision{Reason: "registry unavailable"}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.decideCoreLocked(principal, s.profileCeilingLocked(principal, environment), true, capability, operation, scope, now.UTC())
}

// HasActiveProfile reports whether the principal owns any active capability
// profile in this store — the asserted-decide boundary for shared
// deployments where several spaces' principals live in one registry.
func (s *Store) HasActiveProfile(principal string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, profile := range s.capabilityProfiles {
		if profile.PrincipalID == principal && profile.Status == api.ActiveStatus {
			return true
		}
	}
	return false
}

// profileCeilingLocked resolves the exact active principal/environment pair.
func (s *Store) profileCeilingLocked(principal, environment string) []api.RightRule {
	if environment != "dev" && environment != "prod" {
		return nil
	}
	var ceiling []api.RightRule
	found := false
	for _, profile := range s.capabilityProfiles {
		if profile.PrincipalID != principal || profile.Status != api.ActiveStatus || profile.Environment != environment {
			continue
		}
		if policy, ok := s.capabilityPolicies[profile.PolicyID]; !ok || policy.Status != api.ActiveStatus || policy.Revision != profile.PolicyRevision {
			continue
		}
		if found {
			return nil // asserted calls cannot choose between live generations
		}
		ceiling, found = s.managedRightCeilingLocked(profile), true
	}
	return ceiling
}

// An organization policy is an independent, live upper bound on the
// published grants ceiling. A missing or rotated policy grants nothing.
func (s *Store) managedRightCeilingLocked(profile api.CapabilityProfile) []api.RightRule {
	policy, ok := s.policyAtRevisionLocked(profile.PolicyID, profile.PolicyRevision)
	if !ok || !slices.Contains(policy.Members, profile.PrincipalID) {
		return nil
	}
	if policy.Organization == "" {
		return profile.Rights
	}
	var ceiling []api.RightRule
	for _, right := range profile.Rights {
		if slices.Contains(policy.RightCeiling, right) {
			ceiling = append(ceiling, right)
		}
	}
	return ceiling
}

// Decide is the single policy decision point every PEP calls.
func (s *Store) Decide(auth identity.Envelope, capability, operation, scope string, now time.Time) (api.RightDecision, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return api.RightDecision{Reason: "envelope invalid"}, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	if err := s.Reload(); err != nil {
		return api.RightDecision{Reason: "registry unavailable"}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.decideLocked(auth, capability, operation, scope, now.UTC())
}

// rightGrant finds the strongest active ledger grant covering the call
// scope: exact scope wins over an unscoped grant; a cross-subject call needs
// a grant naming the target scope exactly.
func rightGrant(ledger *api.Governance, principal, capability, operation, scope string, row api.RightOperation, now time.Time) *api.RightGrant {
	if ledger == nil {
		return nil
	}
	var best *api.RightGrant
	for i := range ledger.Rights {
		grant := &ledger.Rights[i]
		if !grant.Active(now) || grant.PrincipalID != principal || grant.Capability != capability || grant.Operation != operation {
			continue
		}
		if row.Subject == "cross" {
			if grant.Scope == scope {
				return grant
			}
			continue
		}
		if grant.ScopeCovers(scope) && (best == nil || grant.Scope != "") {
			copy := *grant
			best = &copy
		}
	}
	return best
}

// loadRightsLedgerLocked reads the governance document through the wired
// ledger accessor — nil ledger means no capability grants exist.
func (s *Store) loadRightsLedgerLocked() (*api.Governance, error) {
	if s.RightsLedger == nil {
		return nil, nil
	}
	return s.RightsLedger()
}

// GrantsRight reports whether Decide would allow the call — the bool form
// PEPs that only need a gate consume; Decide remains the authority.
func (s *Store) GrantsRight(auth identity.Envelope, capability, operation, scope string, now time.Time) bool {
	decision, err := s.Decide(auth, capability, operation, scope, now)
	return err == nil && decision.Allow
}

// RightsGated is the PEP shape for sites that keep the legacy posture on
// unmanaged callers: it reports a denial only when the caller's profile
// carries a rights ceiling AND Decide refuses the operation. Profiles
// without Rights rows gate nothing — a published revision stamps the
// ceiling via DeriveRights and the same check turns hard.
func (s *Store) RightsGated(auth identity.Envelope, capability, operation string, now time.Time) (bool, error) {
	if auth.CapabilityProfile == "" {
		return false, nil
	}
	if err := s.Reload(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, _, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return true, err
	}
	decision, err := s.decideLocked(auth, capability, operation, "", now.UTC())
	if err != nil {
		return true, err
	}
	return !decision.Allow, nil
}

// rightsMissingLocked returns the declared rights the PDP denies for this
// managed caller. An empty published ceiling denies every required right.
func (s *Store) rightsMissingLocked(auth identity.Envelope, profile api.CapabilityProfile, tool api.ToolSpec, now time.Time) []string {
	var missing []string
	seen := map[string]bool{}
	for _, right := range append([]string{"tools.use"}, tool.Requires...) {
		if seen[right] {
			continue
		}
		seen[right] = true
		capability, operation, ok := strings.Cut(right, ".")
		if !ok {
			missing = append(missing, right)
			continue
		}
		decision, err := s.decideCoreLocked(auth.PrincipalID, s.managedRightCeilingLocked(profile), true, capability, operation, "", now)
		if err != nil || !decision.Allow {
			missing = append(missing, right)
		}
	}
	return missing
}

// RightsDenial is one selection a rights ceiling hides: the projected name
// and the missing capability.operation so tools_list can answer "why can't
// I do X" truthfully.
type RightsDenial struct {
	Name    string `json:"name"`
	Missing string `json:"missing_right"`
}

// DenialCapabilityRules is the reason token tools_list reports for a
// selection the capability rules deny under projection: the rules never
// followed the selected implementation, expired, or an explicit deny
// suppresses it. Unlike a missing right, no grant the principal can issue
// admits the tool — an operator repairs the capability records.
const DenialCapabilityRules = "capability_rules"

// CapabilityDenial is one selection suppressed at the capability-rule stage
// even though its binding resolves.
type CapabilityDenial struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// RightsDenials lists the selections the rights gate suppresses. Empty for
// unmanaged callers and for profiles without a ceiling.
func (s *Store) RightsDenials(auth identity.Envelope) ([]RightsDenial, error) {
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
		return nil, nil // no usable profile → nothing to attribute
	}
	now := time.Now().UTC()
	var out []RightsDenial
	for _, selection := range profile.Selections {
		var found *api.EffectiveBinding
		for _, binding := range s.bindings {
			if binding.DefinitionID != selection.DefinitionID || binding.DefinitionVersion != selection.DefinitionVersion || binding.ConnectionID != selection.ConnectionID {
				continue
			}
			effective, err := s.resolveLocked(auth, binding.ToolBindingID)
			if err != nil {
				continue
			}
			found = &effective
		}
		if found == nil {
			continue
		}
		for _, tool := range found.Definition.ProjectedToolSpecs(found.Binding) {
			if tool.Name != selection.ToolName || tool.CapabilityID != selection.CapabilityID {
				continue
			}
			for _, right := range s.rightsMissingLocked(auth, profile, tool, now) {
				out = append(out, RightsDenial{Name: selection.Name, Missing: right})
			}
		}
	}
	return out, nil
}

// CapabilityDenials lists the selections capability rules deny under
// projection — distinct from RightsDenials: the selection resolves its
// binding and definition, yet no allow∩ceiling pair admits it (or a deny
// does). tools_list surfaces these so the model reports "suppressed by
// capability rules" instead of concluding the tool does not exist. Empty for
// unmanaged callers.
func (s *Store) CapabilityDenials(auth identity.Envelope) ([]CapabilityDenial, error) {
	if auth.CapabilityProfile == "" {
		return nil, nil
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	profile, policy, err := s.capabilityProfileLocked(auth)
	if err != nil {
		return nil, nil // no usable profile → nothing to attribute
	}
	var out []CapabilityDenial
	for _, selection := range profile.Selections {
		var found *api.EffectiveBinding
		for _, binding := range s.bindings {
			if binding.DefinitionID != selection.DefinitionID || binding.DefinitionVersion != selection.DefinitionVersion || binding.ConnectionID != selection.ConnectionID {
				continue
			}
			effective, err := s.resolveLocked(auth, binding.ToolBindingID)
			if err != nil {
				continue
			}
			found = &effective
		}
		if found == nil {
			continue
		}
		for _, tool := range found.Definition.ProjectedToolSpecs(found.Binding) {
			if tool.Name != selection.ToolName || tool.CapabilityID != selection.CapabilityID {
				continue
			}
			if _, _, allowed := api.CapabilityDecision(profile, policy, selection, tool, nil, true); !allowed {
				out = append(out, CapabilityDenial{Name: selection.Name, Reason: DenialCapabilityRules})
			}
		}
	}
	return out, nil
}
