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
// decision intersects three authorities:
//
//  1. vocabulary — RightsDoc from the immutable platform catalog release the
//     image ships; unknown capability.operation never allows;
//  2. ceiling — the managed capability profile's Rights rows, the flattened
//     grants.yaml allow the published revision carried (unmanaged callers
//     skip this layer: the live ledger alone bounds them);
//  3. ledger — Governance.Rights grants: active, unexpired, scope-covering.
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
		ceiling = profile.Rights
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
	epoch := s.rightsEpochLocked()
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
	grant := s.rightGrantLocked(principal, capability, operation, scope, row, now)
	if grant == nil {
		return api.RightDecision{Reason: "no active grant", Epoch: epoch}, nil
	}
	return api.RightDecision{Allow: true, Epoch: epoch}, nil
}

// DecideAsserted serves trusted in-space services: they hold the
// control-plane bearer and assert this space's principal. The ceiling
// resolves from that principal's active capability profile — a profile
// carrying no Rights rows enforces nothing (legacy posture), exactly like
// the envelope path.
func (s *Store) DecideAsserted(principal, environment, capability, operation, scope string, now time.Time) (api.RightDecision, error) {
	if !identity.ValidID(principal) {
		return api.RightDecision{Reason: "principal invalid"}, fmt.Errorf("%w: asserted principal", api.ErrInvalid)
	}
	if err := s.Reload(); err != nil {
		return api.RightDecision{Reason: "registry unavailable"}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ceiling, found := s.profileCeilingLocked(principal, environment)
	return s.decideCoreLocked(principal, ceiling, found, capability, operation, scope, now.UTC())
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

// profileCeilingLocked resolves the active profile bound to principal; an
// environment argument narrows the match when several profiles exist.
func (s *Store) profileCeilingLocked(principal, environment string) ([]api.RightRule, bool) {
	var fallback *api.CapabilityProfile
	for _, profile := range s.capabilityProfiles {
		if profile.PrincipalID != principal || profile.Status != api.ActiveStatus {
			continue
		}
		if environment != "" && profile.Environment == environment {
			return profile.Rights, true
		}
		if fallback == nil {
			copy := profile
			fallback = &copy
		}
	}
	if fallback != nil {
		return fallback.Rights, true
	}
	return nil, false
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

// rightGrantLocked finds the strongest active ledger grant covering the call
// scope: exact scope wins over an unscoped grant; a cross-subject call needs
// a grant naming the target scope exactly.
func (s *Store) rightGrantLocked(principal, capability, operation, scope string, row api.RightOperation, now time.Time) *api.RightGrant {
	ledger, err := s.loadRightsLedgerLocked()
	if ledger == nil || err != nil {
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

// rightsEpochLocked is the governance revision the verdict reads: revokes and
// grants both advance it, so a caller can tell when the rights ground moved.
func (s *Store) rightsEpochLocked() uint64 {
	ledger, err := s.loadRightsLedgerLocked()
	if ledger == nil || err != nil {
		return 0
	}
	return ledger.Revision
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
	profile, _, err := s.capabilityProfileLocked(auth)
	if err != nil || len(profile.Rights) == 0 {
		return false, nil
	}
	decision, err := s.decideLocked(auth, capability, operation, "", now.UTC())
	if err != nil {
		return true, err
	}
	return !decision.Allow, nil
}

// rightsMissingLocked returns the declared rights the PDP denies for this
// caller — nil when the profile carries no ceiling (unmanaged or legacy
// profile: the requires gate does not apply there).
func (s *Store) rightsMissingLocked(auth identity.Envelope, profile api.CapabilityProfile, tool api.ToolSpec, now time.Time) []string {
	if len(profile.Rights) == 0 || len(tool.Requires) == 0 {
		return nil
	}
	var missing []string
	for _, right := range tool.Requires {
		capability, operation, ok := strings.Cut(right, ".")
		if !ok {
			missing = append(missing, right)
			continue
		}
		decision, err := s.decideLocked(auth, capability, operation, "", now)
		if err != nil || !decision.Allow {
			missing = append(missing, right)
		}
	}
	return missing
}

// RightsDenial is one selection a rights ceiling hides: the projected name
// and the missing capability.operation so tools_list can answer "why can't
// I do X" truthfully. Capability-rule denials stay silent — they are not
// the principal's rights story.
type RightsDenial struct {
	Name    string `json:"name"`
	Missing string `json:"missing_right"`
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
	if len(profile.Rights) == 0 {
		return nil, nil
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
