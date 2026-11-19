package identityhub

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identity"
)

type Diagnostic struct {
	Schema        int `json:"schema"`
	Humans        int `json:"humans"`
	Agents        int `json:"agents"`
	Systems       int `json:"systems"`
	Organizations int `json:"organizations"`
	VerifiedLinks int `json:"verified_links"`
	PendingLinks  int `json:"pending_links"`
	AuditEvents   int `json:"audit_events"`
}

var grantTransitions = map[string]map[string]string{"active": {"revoke": "revoked", "expire": "expired"}}

func adminGrantState(revoked bool, expires int64, now time.Time) string {
	if revoked {
		return "revoked"
	}
	if now.UnixNano() >= expires {
		return grantTransitions["active"]["expire"]
	}
	return "active"
}

// BootstrapPlatformAdmin is an explicit local operator action, never an HTTP
// endpoint or an automatic consequence of importing the first user.
func (r *Registry) BootstrapPlatformAdmin(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=? AND kind='human' AND enabled=1", id).Scan(&count); err != nil {
		return storeError(err)
	}
	if count != 1 {
		return contract.ErrForbidden
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM memberships m JOIN principals p ON p.id=m.principal_id WHERE m.role='platform-admin' AND p.enabled=1").Scan(&count); err != nil {
		return storeError(err)
	}
	if count != 0 {
		return contract.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO memberships(principal_id,scope,role) VALUES(?,'platform','platform-admin')", id); err != nil {
		return storeError(err)
	}
	if err := audit(ctx, tx, r.now(), "platform_admin_bootstrap", "operator", id, "operator-maintenance"); err != nil {
		return storeError(err)
	}
	return storeError(tx.Commit())
}

func validAdminScope(scope string) bool {
	if scope == "platform" {
		return true
	}
	org, ok := strings.CutPrefix(scope, "org:")
	return ok && identity.ValidID(org)
}

func validAdminOperation(operation string) bool {
	switch operation {
	case "principals.write", "memberships.write", "layers.write":
		return true
	}
	return false
}

// Administrative delegation and role issuance require a human administrator.
func requireAdminRole(ctx context.Context, tx *sql.Tx, principal, scope string) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memberships m JOIN principals p ON p.id=m.principal_id
 WHERE p.id=? AND p.kind='human' AND p.enabled=1 AND
 ((m.role='platform-admin' AND m.scope='platform') OR (m.role='org-admin' AND m.scope=? AND m.scope!='platform'))`, principal, scope).Scan(&count)
	if err != nil {
		return storeError(err)
	}
	if count == 0 {
		return contract.ErrForbidden
	}
	return nil
}

func (r *Registry) SetMembership(ctx context.Context, actor, principal, scope, role string) error {
	if !validAdminScope(scope) || (scope == "platform" && role != "platform-admin") || (scope != "platform" && role != "member" && role != "org-admin") {
		return contract.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	if err := requireAdminRole(ctx, tx, actor, scope); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=? AND kind='human' AND enabled=1", principal).Scan(&count); err != nil {
		return storeError(err)
	}
	if count != 1 {
		return contract.ErrForbidden
	}
	if scope != "platform" {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM organizations WHERE id=?", strings.TrimPrefix(scope, "org:")).Scan(&count); err != nil {
			return storeError(err)
		}
		if count != 1 {
			return contract.ErrNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO memberships(principal_id,scope,role) VALUES(?,?,?) ON CONFLICT DO NOTHING", principal, scope, role); err != nil {
		return storeError(err)
	}
	if err := audit(ctx, tx, r.now(), "membership_set", actor, principal, role); err != nil {
		return storeError(err)
	}
	return storeError(tx.Commit())
}

func (r *Registry) CreateSystem(ctx context.Context, actor, service string) (contract.Principal, error) {
	if !identity.ValidID(service) {
		return contract.Principal{}, contract.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return contract.Principal{}, storeError(err)
	}
	defer tx.Rollback()
	if err := requireAdminRole(ctx, tx, actor, "platform"); err != nil {
		return contract.Principal{}, err
	}
	p := contract.Principal{ID: "system:" + service, Kind: contract.System}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=?", p.ID).Scan(&count); err != nil {
		return contract.Principal{}, storeError(err)
	}
	if count != 0 {
		return contract.Principal{}, contract.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO principals(id,kind,enabled) VALUES(?,'system',1)", p.ID); err != nil {
		return contract.Principal{}, storeError(err)
	}
	if err := audit(ctx, tx, r.now(), "system_created", actor, p.ID, "operator-maintenance"); err != nil {
		return contract.Principal{}, storeError(err)
	}
	return p, storeError(tx.Commit())
}

func (r *Registry) GrantAdministration(ctx context.Context, actor, principal, scope, operation, reason string, expires time.Time, breakGlass bool) (string, error) {
	now := r.now()
	ttl := 24 * time.Hour
	if breakGlass {
		ttl = time.Hour
	}
	if !validAdminScope(scope) || !validAdminOperation(operation) || !expires.After(now) || expires.After(now.Add(ttl)) {
		return "", contract.ErrInvalid
	}
	switch reason {
	case "operator-maintenance", "incident-response", "account-recovery":
	default:
		return "", contract.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", storeError(err)
	}
	defer tx.Rollback()
	if err := requireAdminRole(ctx, tx, actor, scope); err != nil {
		return "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=? AND kind IN ('human','system') AND enabled=1", principal).Scan(&count); err != nil {
		return "", storeError(err)
	}
	if count != 1 {
		return "", contract.ErrForbidden
	}
	if scope != "platform" {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM organizations WHERE id=?", strings.TrimPrefix(scope, "org:")).Scan(&count); err != nil {
			return "", storeError(err)
		}
		if count != 1 {
			return "", contract.ErrNotFound
		}
	}
	id, err := randomID()
	if err != nil {
		return "", contract.ErrUnavailable
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO grants(id,principal_id,scope,operation,issuer,reason,expires_at,break_glass) VALUES(?,?,?,?,?,?,?,?)", id, principal, scope, operation, actor, reason, expires.UnixNano(), breakGlass); err != nil {
		return "", storeError(err)
	}
	if err := audit(ctx, tx, now, "admin_grant", actor, principal, reason); err != nil {
		return "", storeError(err)
	}
	return id, storeError(tx.Commit())
}

func (r *Registry) authorizeAdmin(ctx context.Context, tx *sql.Tx, principal, scope, operation string) error {
	if !validAdminScope(scope) || !validAdminOperation(operation) {
		return contract.ErrForbidden
	}
	var kind string
	if err := tx.QueryRowContext(ctx, "SELECT kind FROM principals WHERE id=? AND enabled=1", principal).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrForbidden
		}
		return storeError(err)
	}
	if kind != "human" && kind != "system" {
		return contract.ErrForbidden
	}
	if kind == "human" {
		err := requireAdminRole(ctx, tx, principal, scope)
		if err == nil {
			return nil
		}
		if !errors.Is(err, contract.ErrForbidden) {
			return err
		}
	}
	var reason string
	var breakGlass bool
	err := tx.QueryRowContext(ctx, `SELECT g.reason,g.break_glass FROM grants g
 JOIN principals issuer ON issuer.id=g.issuer AND issuer.kind='human' AND issuer.enabled=1
 WHERE g.principal_id=? AND g.scope=? AND g.operation=? AND g.revoked=0 AND g.expires_at>?
 AND EXISTS (SELECT 1 FROM memberships m WHERE m.principal_id=issuer.id AND
 ((m.role='platform-admin' AND m.scope='platform') OR (m.role='org-admin' AND m.scope=g.scope AND g.scope!='platform')))
 ORDER BY g.break_glass,g.id LIMIT 1`, principal, scope, operation, r.now().UnixNano()).Scan(&reason, &breakGlass)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.ErrForbidden
	}
	if err != nil {
		return storeError(err)
	}
	if breakGlass {
		return storeError(audit(ctx, tx, r.now(), "break_glass_use", principal, principal, reason))
	}
	return nil
}

func (r *Registry) AuthorizeAdministration(ctx context.Context, principal, scope, operation string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	if err := r.authorizeAdmin(ctx, tx, principal, scope, operation); err != nil {
		return err
	}
	return storeError(tx.Commit())
}

func (r *Registry) RevokeAdministration(ctx context.Context, actor, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	var scope, principal string
	var revoked bool
	var expires int64
	if err := tx.QueryRowContext(ctx, "SELECT scope,principal_id,revoked,expires_at FROM grants WHERE id=?", id).Scan(&scope, &principal, &revoked, &expires); err != nil {
		return storeError(err)
	}
	if err := requireAdminRole(ctx, tx, actor, scope); err != nil {
		return err
	}
	if grantTransitions[adminGrantState(revoked, expires, r.now())]["revoke"] != "revoked" {
		return contract.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE grants SET revoked=1 WHERE id=?", id); err != nil {
		return storeError(err)
	}
	if err := audit(ctx, tx, r.now(), "admin_grant_revoked", actor, principal, "operator-maintenance"); err != nil {
		return storeError(err)
	}
	return storeError(tx.Commit())
}

func (r *Registry) Diagnostics(ctx context.Context) (Diagnostic, error) {
	d := Diagnostic{Schema: schema}
	err := r.db.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM principals WHERE kind='human' AND enabled=1),
 (SELECT count(*) FROM principals WHERE kind='agent' AND enabled=1),
 (SELECT count(*) FROM principals WHERE kind='system' AND enabled=1),
 (SELECT count(*) FROM organizations),
 (SELECT count(*) FROM links WHERE state='verified'),
 (SELECT count(*) FROM links WHERE state='pending'),
 (SELECT count(*) FROM audit)`).Scan(&d.Humans, &d.Agents, &d.Systems, &d.Organizations, &d.VerifiedLinks, &d.PendingLinks, &d.AuditEvents)
	return d, storeError(err)
}
