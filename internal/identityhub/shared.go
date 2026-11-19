package identityhub

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
)

func (r *Registry) SetSharedMembers(ctx context.Context, actor, space string, members []string) error {
	if len(members) > 4096 {
		return contract.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	var org string
	if err := tx.QueryRowContext(ctx, "SELECT org_id FROM shared_spaces WHERE id=?", space).Scan(&org); err != nil {
		return storeError(err)
	}
	scope := orgScope(org)
	if scope == "" {
		scope = "platform"
	}
	if err := r.authorizeAdmin(ctx, tx, actor, scope, "memberships.write"); err != nil {
		return err
	}
	members = append([]string(nil), members...)
	slices.Sort(members)
	members = slices.Compact(members)
	for _, member := range members {
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=? AND kind='human' AND enabled=1", member).Scan(&active); err != nil {
			return storeError(err)
		}
		if active != 1 {
			return contract.ErrForbidden
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM space_members WHERE space=?", space); err != nil {
		return storeError(err)
	}
	for _, member := range members {
		if _, err := tx.ExecContext(ctx, "INSERT INTO space_members(space,human) VALUES(?,?)", space, member); err != nil {
			return storeError(err)
		}
	}
	if err := audit(ctx, tx, r.now(), "shared_members_set", actor, space, "operator-maintenance"); err != nil {
		return storeError(err)
	}
	return storeError(tx.Commit())
}

// A channel-only member has no private space or org membership. Exact legacy
// group ACLs grant only their existing shared spaces, never an inferred home.
func legacyGroupMember(ctx context.Context, tx *sql.Tx, external int64, reserved bool) (string, error) {
	id := strconv.FormatInt(external, 10)
	var owner string
	err := tx.QueryRowContext(ctx, "SELECT l.principal_id FROM links l JOIN principals p ON p.id=l.principal_id WHERE channel=? AND external_id=? AND state='verified' AND p.kind='human' AND p.enabled=1", contract.Telegram, id).Scan(&owner)
	if err == nil {
		return owner, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", storeError(err)
	}
	if reserved {
		return "", contract.ErrConflict
	}
	var retired int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM links l JOIN principals p ON p.id=l.principal_id WHERE l.channel=? AND l.external_id=? AND p.enabled=0", contract.Telegram, id).Scan(&retired); err != nil {
		return "", storeError(err)
	}
	if retired != 0 {
		return "", contract.ErrConflict
	}
	owner = "guest-telegram-" + id
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=?", owner).Scan(&count); err != nil {
		return "", storeError(err)
	}
	if count != 0 {
		return "", contract.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO principals(id,kind,enabled) VALUES(?,'human',1)", owner); err != nil {
		return "", storeError(err)
	}
	agent := "agent:" + owner + ":default"
	if _, err := tx.ExecContext(ctx, "INSERT INTO principals(id,kind,owner_id,enabled) VALUES(?,'agent',?,1)", agent, owner); err != nil {
		return "", storeError(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO profiles(id,name,space,restrictions) VALUES(?,'default','','{}')", agent); err != nil {
		return "", storeError(err)
	}
	linkID, err := randomID()
	if err != nil {
		return "", contract.ErrUnavailable
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO links(id,channel,external_id,principal_id,state,expires_at) VALUES(?,?,?,?,'pending',0)", linkID, contract.Telegram, id, owner); err != nil {
		return "", storeError(err)
	}
	if err := transitionLink(ctx, tx, linkID, "verify"); err != nil {
		return "", err
	}
	return owner, nil
}

func bindSubject(ctx context.Context, tx *sql.Tx, issuer, subject, owner string) error {
	var previous string
	err := tx.QueryRowContext(ctx, "SELECT principal_id FROM subjects WHERE issuer=? AND subject=?", issuer, subject).Scan(&previous)
	if err == nil {
		if previous != owner {
			return contract.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storeError(err)
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO subjects(issuer,subject,principal_id) VALUES(?,?,?)", issuer, subject, owner)
	return storeError(err)
}
