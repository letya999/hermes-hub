package identityhub

import (
	"context"
	"database/sql"
	"errors"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
)

var principalTransitions = map[string]map[string]string{"active": {"delete": "inactive"}}

// DeactivateHuman retires registry authority and owned profiles atomically;
// principal tombstones retain history and prevent accidental ID reuse.
func (r *Registry) DeactivateHuman(ctx context.Context, actor, owner string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	var org string
	var enabled bool
	if err := tx.QueryRowContext(ctx, "SELECT org_id,enabled FROM principals WHERE id=? AND kind='human'", owner).Scan(&org, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrForbidden
		}
		return storeError(err)
	}
	if actor != owner {
		scope := orgScope(org)
		if scope == "" {
			scope = "platform"
		}
		if err := r.authorizeAdmin(ctx, tx, actor, scope, "principals.write"); err != nil {
			return err
		}
	}
	state := "inactive"
	if enabled {
		state = "active"
	}
	if principalTransitions[state]["delete"] != "inactive" {
		return contract.ErrConflict
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,state FROM links WHERE principal_id=? AND state IN ('pending','verified')", owner)
	if err != nil {
		return storeError(err)
	}
	var links [][2]string
	for rows.Next() {
		var pair [2]string
		if err := rows.Scan(&pair[0], &pair[1]); err != nil {
			_ = rows.Close()
			return storeError(err)
		}
		links = append(links, pair)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return storeError(err)
	}
	if err := rows.Close(); err != nil {
		return storeError(err)
	}
	for _, link := range links {
		event := "delete"
		if link[1] == "pending" {
			event = "reject"
		}
		if err := transitionLink(ctx, tx, link[0], event); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		"UPDATE principals SET enabled=0 WHERE id=? OR owner_id=?",
		"UPDATE grants SET revoked=1 WHERE principal_id=? OR issuer=?",
	} {
		if _, err := tx.ExecContext(ctx, statement, owner, owner); err != nil {
			return storeError(err)
		}
	}
	for _, statement := range []string{
		"DELETE FROM memberships WHERE principal_id=?",
		"DELETE FROM spaces WHERE human=?",
		"DELETE FROM profiles WHERE id IN (SELECT id FROM principals WHERE owner_id=?)",
	} {
		if _, err := tx.ExecContext(ctx, statement, owner); err != nil {
			return storeError(err)
		}
	}
	if err := audit(ctx, tx, r.now(), "human_deactivated", actor, owner, "owner_deleted"); err != nil {
		return storeError(err)
	}
	return storeError(tx.Commit())
}
