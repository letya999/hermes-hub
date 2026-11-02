package toolhub

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"time"

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

// DeriveRights stamps the published grants.yaml allow onto the space's
// managed records (SPEC-0092 R2.1): every active capability profile bound
// to the principal carries the flattened allow as its Rights ceiling, and
// the governance ledger holds platform-issued grants for exactly those
// capability.operation rows. Rows the revision dropped are revoked in
// place, so Decide denies them on the next action — rights a passport no
// longer carries cannot linger in the ledger.
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

func DeriveRights(store *Store, gov *GovernanceStore, doc api.RightsDoc, principal string, allow map[string][]string, now time.Time) error {
	rules := make([]api.RightRule, 0, len(allow))
	for capability, ops := range allow {
		for _, op := range ops {
			rules = append(rules, api.RightRule{Capability: capability, Operation: op})
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Capability != rules[j].Capability {
			return rules[i].Capability < rules[j].Capability
		}
		return rules[i].Operation < rules[j].Operation
	})
	for _, profile := range store.profilesForPrincipal(principal) {
		if equalRightRules(profile.Rights, rules) {
			continue
		}
		profile.Rights = rules
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
	if gov == nil {
		return nil
	}
	return gov.Update(now, func(ledger *api.Governance) error {
		keep := map[string]bool{}
		caps := make([]string, 0, len(allow))
		for capability := range allow {
			caps = append(caps, capability)
		}
		sort.Strings(caps)
		for _, capability := range caps {
			ops := append([]string(nil), allow[capability]...)
			sort.Strings(ops)
			for _, op := range ops {
				row, ok := doc.Lookup(capability, op)
				if !ok {
					return fmt.Errorf("%w: derived right %s.%s", api.ErrInvalid, capability, op)
				}
				if row.Subject == "cross" || !slices.Contains(row.Issuers, "platform") {
					continue // scoped cross-subject grants stay operator-minted
				}
				// Grant IDs must stay inside the 40-char identity bound;
				// the principal is readable-prefix, capability.operation
				// hashes onto a fixed suffix so long pairs always fit.
				sum := sha256.Sum256([]byte(principal + "|" + capability + "." + op))
				short := principal
				if len(short) > 23 {
					short = short[:23]
				}
				grantID := fmt.Sprintf("right-%s-%x", short, sum[:5])
				keep[grantID] = true
				var existing *api.RightGrant
				for i := range ledger.Rights {
					if ledger.Rights[i].GrantID == grantID {
						existing = &ledger.Rights[i]
						break
					}
				}
				if existing != nil && existing.Active(now) {
					continue
				}
				grant := api.NewRightGrant(grantID, principal, capability, op, "", "passport-derived right", "platform", now.AddDate(1, 0, 0))
				if existing != nil {
					grant.Revision = existing.Revision + 1
					grant.CreatedAt = existing.CreatedAt
				}
				if err := api.Confirm(&grant, "platform", now); err != nil {
					return err
				}
				if err := ledger.PutRightGrant(grant, row, now); err != nil {
					return err
				}
			}
		}
		for _, grant := range ledger.Rights {
			if grant.PrincipalID != principal || grant.GrantedBy != "platform" || keep[grant.GrantID] || grant.Status == api.GrantRevoked {
				continue
			}
			if err := ledger.RevokeRightGrant(grant.GrantID, "platform", "passport revision no longer carries the right", now); err != nil {
				return err
			}
		}
		return nil
	})
}
