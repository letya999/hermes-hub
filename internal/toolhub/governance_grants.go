package toolhub

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"slices"
	"strings"
	"time"
)

func (t ToolGrant) validate() error {
	if t.Schema != SchemaVersion || !identity.ValidID(t.GrantID) || !identity.ValidID(t.PrincipalID) ||
		!sectionIDPattern.MatchString(t.Section) || !identity.ValidID(t.GrantedBy) || t.Revision == 0 {
		return fmt.Errorf("%w: grant identity", ErrInvalid)
	}
	if t.GrantedBy == "model" || t.GrantedBy == "hermes" {
		return fmt.Errorf("%w: model cannot grant", ErrUnauthorized)
	}
	if t.ExpiresAt.IsZero() || t.ExpiresAt.Year() < 1 || t.ExpiresAt.Year() > 9999 {
		return fmt.Errorf("%w: grant needs an expiry", ErrInvalid)
	}
	if strings.TrimSpace(t.Reason) == "" || len(t.Reason) > 512 || strings.ContainsAny(t.Reason, "\x00\r\n") {
		return fmt.Errorf("%w: grant reason", ErrInvalid)
	}
	switch t.Status {
	case GrantPending, GrantActive, GrantRevoked:
	default:
		return fmt.Errorf("%w: grant status", ErrInvalid)
	}
	for key, value := range t.Match {
		if !attrKeyPattern.MatchString(key) || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: grant match pair", ErrInvalid)
		}
	}
	if len(t.Match) > 8 {
		return fmt.Errorf("%w: grant match breadth", ErrInvalid)
	}
	return nil
}

func (t ToolGrant) matches(principal, section string, attrs map[string]string) bool {
	if t.PrincipalID != principal || t.Section != section {
		return false
	}
	for key, want := range t.Match {
		if attrs[key] != want {
			return false
		}
	}
	return true
}

func (t ToolGrant) active(now time.Time) bool {
	return t.Status == GrantActive && now.Before(t.ExpiresAt)
}

// grantAwaitingAck reports a pending grant the in-conversation request already
// stamped a live nonce onto.
func (t ToolGrant) grantAwaitingAck(now time.Time) bool {
	return t.Status == GrantPending && t.RequestNonce != "" && now.Before(t.RequestExpires) && now.Before(t.ExpiresAt)
}

// NewToolGrant builds a pending host grant for one principal and section.
// The Confirmation must be stamped by the caller (hubctl --confirm) before
// PutToolGrant accepts the record.
func NewToolGrant(grantID, principal, section, reason, grantedBy string, expiresAt time.Time, match map[string]string) ToolGrant {
	return ToolGrant{
		Schema: SchemaVersion, GrantID: grantID, PrincipalID: principal, Section: section,
		Match: match, Reason: reason, GrantedBy: grantedBy,
		ExpiresAt: expiresAt.UTC(), Status: GrantPending, Revision: 1, CreatedAt: time.Now().UTC(),
	}
}

// PutToolGrant registers a confirmed pending grant. Re-registration of the
// same id requires a higher revision.
func (g *Governance) PutToolGrant(grant ToolGrant, now time.Time) error {
	if grant.Status != GrantPending && grant.Status != GrantRevoked {
		return fmt.Errorf("%w: a grant enters pending; the in-conversation request and acknowledge activate it", ErrInvalid)
	}
	if err := grant.validate(); err != nil {
		return err
	}
	if grant.Status == GrantPending && !validConfirmation(grant.Confirmation, grant, grant.GrantedBy) {
		return fmt.Errorf("%w: unconfirmed tool grant", ErrUnauthorized)
	}
	for i, existing := range g.Grants {
		if existing.GrantID == grant.GrantID {
			if grant.Revision <= existing.Revision {
				return fmt.Errorf("%w: grant revision must advance", ErrConflict)
			}
			g.Grants[i] = grant
			g.record("grant", grant.GrantID, grant.GrantedBy, grant.Reason, now)
			return nil
		}
	}
	g.Grants = append(g.Grants, grant)
	g.record("grant", grant.GrantID, grant.GrantedBy, grant.Reason, now)
	return nil
}

// PendingToolGrants lists the principal's pending grants, newest first.
func (g *Governance) PendingToolGrants(principal, section string, now time.Time) []ToolGrant {
	out := []ToolGrant{}
	for _, grant := range g.Grants {
		if grant.Status == GrantPending && grant.PrincipalID == principal &&
			(section == "" || grant.Section == section) && now.Before(grant.ExpiresAt) {
			out = append(out, grant)
		}
	}
	slices.SortFunc(out, func(a, b ToolGrant) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out
}

// RequestToolGrant stamps the in-conversation request onto a pending grant:
// a fresh single-use nonce with a short TTL. Repeated requests rotate the
// nonce so a stale acknowledge can never activate.
func (g *Governance) RequestToolGrant(principal, grantID string, now time.Time) (ToolGrant, error) {
	for i, grant := range g.Grants {
		if grant.GrantID != grantID || grant.PrincipalID != principal {
			continue
		}
		if grant.Status != GrantPending || !now.Before(grant.ExpiresAt) {
			return ToolGrant{}, fmt.Errorf("%w: grant is not pending", ErrInvalid)
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return ToolGrant{}, err
		}
		grant.RequestNonce = hex.EncodeToString(nonce)
		grant.RequestExpires = now.Add(15 * time.Minute).UTC()
		grant.RequestedAt = now.UTC()
		grant.Revision++
		g.Grants[i] = grant
		g.record("request", grant.GrantID, principal, grant.Section, now)
		return grant, nil
	}
	return ToolGrant{}, fmt.Errorf("%w: pending grant", ErrNotFound)
}

// AcknowledgeToolGrant activates a pending grant when the nonce from its
// in-conversation request matches. The audit entry is written into History
// before the status flips so an activation never exists unrecorded.
func (g *Governance) AcknowledgeToolGrant(principal, grantID, nonce string, now time.Time) (ToolGrant, error) {
	for i, grant := range g.Grants {
		if grant.GrantID != grantID || grant.PrincipalID != principal {
			continue
		}
		if !grant.grantAwaitingAck(now) {
			return ToolGrant{}, fmt.Errorf("%w: grant has no live request", ErrInvalid)
		}
		if nonce == "" || nonce != grant.RequestNonce {
			return ToolGrant{}, fmt.Errorf("%w: acknowledge nonce", ErrUnauthorized)
		}
		grant.Status = GrantActive
		grant.AckedAt = now.UTC()
		grant.RequestNonce = ""
		grant.Revision++
		g.Grants[i] = grant
		g.record("acknowledge", grant.GrantID, principal, "grant activated: "+grant.Section, now)
		return grant, nil
	}
	return ToolGrant{}, fmt.Errorf("%w: pending grant", ErrNotFound)
}

// RevokeToolGrant cuts a grant immediately; evaluation denies on the next
// call without a restart.
func (g *Governance) RevokeToolGrant(grantID, by, reason string, now time.Time) error {
	if !identity.ValidID(by) || by == "model" || by == "hermes" {
		return fmt.Errorf("%w: revoke issuer", ErrUnauthorized)
	}
	for i, grant := range g.Grants {
		if grant.GrantID != grantID {
			continue
		}
		grant.Status = GrantRevoked
		grant.RequestNonce = ""
		grant.Revision++
		g.Grants[i] = grant
		g.record("revoke", grant.GrantID, by, reason, now)
		return nil
	}
	return fmt.Errorf("%w: grant", ErrNotFound)
}
