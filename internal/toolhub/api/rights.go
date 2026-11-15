package api

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// RBAC rights vocabulary (SPEC-0092 R2 "Capability vocabulary"). These types
// live in api — not internal/catalog — because the policy decision point is
// ToolHub and catalog already imports this package. The immutable
// rights.yaml release the platform ships uses exactly this shape: a right is
// an agent permission over a domain, never a tool; toolset members declare
// which capability.operation they require.

// RightOperation is one grantable operation inside a capability domain.
// Subject self|cross separates rights over the agent's own space from
// rights that reach another space or user. Scope names the resource kind an
// enforcement point may bind the call to. Issuers list the authorities that
// may mint the grant record — cross-subject rights are never operator-minted.
type RightOperation struct {
	Subject     string   `yaml:"subject" json:"subject"`
	Scope       string   `yaml:"scope" json:"scope"`
	Issuers     []string `yaml:"issuers" json:"issuers"`
	PEP         []string `yaml:"pep" json:"pep"`
	Effect      string   `yaml:"effect" json:"effect"`
	Description string   `yaml:"description" json:"description"`
}

// RightCapability is one RBAC domain: its norm description plus the full
// operation vocabulary a grants.yaml allow entry may select from.
type RightCapability struct {
	Description string                    `yaml:"description" json:"description"`
	Operations  map[string]RightOperation `yaml:"operations" json:"operations"`
}

// RightsDoc is the immutable RBAC vocabulary file (platform/catalogs/<rel>/rights.yaml).
type RightsDoc struct {
	Release      string                     `yaml:"release" json:"release"`
	DefinitionID string                     `yaml:"definition_id" json:"definition_id"`
	Capabilities map[string]RightCapability `yaml:"capabilities" json:"capabilities"`
}

// Lookup returns the operation row for capability.operation.
func (d RightsDoc) Lookup(capability, operation string) (RightOperation, bool) {
	cap, ok := d.Capabilities[capability]
	if !ok {
		return RightOperation{}, false
	}
	row, ok := cap.Operations[operation]
	return row, ok
}

// Validate fails closed on any capability.operation the release does not
// know — a right outside the vocabulary can never be granted or invoked.
func (d RightsDoc) Validate(capability, operation string) error {
	if _, ok := d.Lookup(capability, operation); !ok {
		return fmt.Errorf("%w: right capability %q operation %q", ErrInvalid, capability, operation)
	}
	return nil
}

var rightNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// RightGrant is the capability-kind ledger record inside the governance
// document: one host-minted, expiring, revocable grant of capability.operation
// to a principal, optionally bound to a scope (path, model, toolset, skill,
// connector, service, channel or log value). Pending/request/ack ceremony is
// not replayed here — the operator confirmation stamp is the human gate, the
// same trust level capability policies already carry.
type RightGrant struct {
	Schema       int           `json:"schema"`
	GrantID      string        `json:"grant_id"`
	PrincipalID  string        `json:"principal_id"`
	Capability   string        `json:"capability"`
	Operation    string        `json:"operation"`
	Scope        string        `json:"scope,omitempty"`
	Reason       string        `json:"reason"`
	GrantedBy    string        `json:"granted_by"`
	ExpiresAt    time.Time     `json:"expires_at"`
	Status       string        `json:"status"`
	Revision     uint64        `json:"revision"`
	CreatedAt    time.Time     `json:"created_at"`
	Confirmation *Confirmation `json:"confirmation,omitempty"`
}

func (g RightGrant) Validate() error {
	if g.Schema != SchemaVersion || !identity.ValidID(g.GrantID) || !identity.ValidID(g.PrincipalID) ||
		!identity.ValidID(g.GrantedBy) || g.Revision == 0 {
		return fmt.Errorf("%w: right grant identity", ErrInvalid)
	}
	if g.GrantedBy == "model" || g.GrantedBy == "hermes" {
		return fmt.Errorf("%w: model cannot grant a right", ErrUnauthorized)
	}
	if !rightNamePattern.MatchString(g.Capability) || !rightNamePattern.MatchString(g.Operation) {
		return fmt.Errorf("%w: right capability.operation shape", ErrInvalid)
	}
	if len(g.Scope) > 512 || strings.ContainsAny(g.Scope, "\x00\r\n") {
		return fmt.Errorf("%w: right grant scope", ErrInvalid)
	}
	if g.ExpiresAt.IsZero() || g.ExpiresAt.Year() < 1 || g.ExpiresAt.Year() > 9999 {
		return fmt.Errorf("%w: right grant needs an expiry", ErrInvalid)
	}
	if strings.TrimSpace(g.Reason) == "" || len(g.Reason) > 512 || strings.ContainsAny(g.Reason, "\x00\r\n") {
		return fmt.Errorf("%w: right grant reason", ErrInvalid)
	}
	switch g.Status {
	case GrantPending, GrantActive, GrantRevoked:
	default:
		return fmt.Errorf("%w: right grant status", ErrInvalid)
	}
	return nil
}

// Active reports a live grant: activated status inside its expiry window.
func (g RightGrant) Active(now time.Time) bool {
	return g.Status == GrantActive && now.Before(g.ExpiresAt)
}

// ScopeCovers reports whether this grant's scope admits the requested call
// scope. An unscoped grant covers any call scope; a path-kind scope covers
// the exact value or anything under it.
func (g RightGrant) ScopeCovers(scope string) bool {
	if g.Scope == "" {
		return true
	}
	return scope == g.Scope || strings.HasPrefix(scope, g.Scope+"/")
}

// NewRightGrant builds an operator-confirmed capability grant. The caller
// stamps api.Confirm before PutRightGrant accepts the record — the
// confirmation binds issuer identity to the exact reviewed bytes.
func NewRightGrant(grantID, principal, capability, operation, scope, reason, grantedBy string, expiresAt time.Time) RightGrant {
	return RightGrant{
		Schema: SchemaVersion, GrantID: grantID, PrincipalID: principal,
		Capability: capability, Operation: operation, Scope: scope,
		Reason: reason, GrantedBy: grantedBy,
		ExpiresAt: expiresAt.UTC(), Status: GrantActive, Revision: 1, CreatedAt: time.Now().UTC(),
	}
}

// PutRightGrant registers a confirmed capability grant; re-registration of
// the same id requires a higher revision. Issuers are enforced by the
// caller against the rights vocabulary — cross-subject rights refuse
// operator-minted records.
func (g *Governance) PutRightGrant(grant RightGrant, row RightOperation, now time.Time) error {
	if err := grant.Validate(); err != nil {
		return err
	}
	if !contains(row.Issuers, grant.GrantedBy) {
		return fmt.Errorf("%w: %s may not issue %s.%s (issuers %v)", ErrUnauthorized, grant.GrantedBy, grant.Capability, grant.Operation, row.Issuers)
	}
	if row.Subject == "cross" && grant.Scope == "" {
		return fmt.Errorf("%w: cross-subject right %s.%s requires a scope naming the target", ErrInvalid, grant.Capability, grant.Operation)
	}
	if !validConfirmation(grant.Confirmation, grant, grant.GrantedBy) {
		return fmt.Errorf("%w: unconfirmed right grant", ErrUnauthorized)
	}
	for i, existing := range g.Rights {
		if existing.GrantID == grant.GrantID {
			if grant.Revision <= existing.Revision {
				return fmt.Errorf("%w: right grant revision must advance", ErrConflict)
			}
			g.Rights[i] = grant
			g.record("grant", grant.GrantID, grant.GrantedBy, grant.Capability+"."+grant.Operation+": "+grant.Reason, now)
			return nil
		}
	}
	g.Rights = append(g.Rights, grant)
	g.record("grant", grant.GrantID, grant.GrantedBy, grant.Capability+"."+grant.Operation+": "+grant.Reason, now)
	return nil
}

// RevokeRightGrant cuts a capability grant immediately: the next Decide
// denies without a republish or restart.
func (g *Governance) RevokeRightGrant(grantID, by, reason string, now time.Time) error {
	if !identity.ValidID(by) || by == "model" || by == "hermes" {
		return fmt.Errorf("%w: revoke issuer", ErrUnauthorized)
	}
	for i, grant := range g.Rights {
		if grant.GrantID != grantID {
			continue
		}
		grant.Status = GrantRevoked
		grant.Revision++
		g.Rights[i] = grant
		g.record("revoke", grant.GrantID, by, reason, now)
		return nil
	}
	return fmt.Errorf("%w: right grant", ErrNotFound)
}

// RightRule is one ceiling entry on a capability profile — the compiled
// grants.yaml allow flattened to capability.operation rows.
type RightRule struct {
	Capability string `json:"capability"`
	Operation  string `json:"operation"`
}

// RightDecision is the PDP verdict every PEP consumes: allow, a
// machine-safe reason and the ledger epoch the verdict was taken at.
type RightDecision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
	Epoch  uint64 `json:"epoch"`
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
