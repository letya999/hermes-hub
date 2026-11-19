package identityhub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
)

const linkTTL = 10 * time.Minute

// Every lifecycle write goes through this table, including operator import.
var linkTransitions = map[string]map[string]string{
	"pending":  {"verify": "verified", "reject": "gone", "expire": "gone"},
	"verified": {"revoke": "revoked", "delete": "gone"},
}

func transitionLink(ctx context.Context, tx *sql.Tx, id, event string) error {
	var state string
	if err := tx.QueryRowContext(ctx, "SELECT state FROM links WHERE id=?", id).Scan(&state); err != nil {
		return storeError(err)
	}
	next := linkTransitions[state][event]
	if next == "" {
		return contract.ErrConflict
	}
	_, err := tx.ExecContext(ctx, "UPDATE links SET state=?,proof_hash=NULL,expires_at=0 WHERE id=?", next, id)
	return storeError(err)
}

func randomID() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func (r *Registry) Resolve(ctx context.Context, c contract.ChannelIdentity) (contract.Principal, error) {
	if !canonicalChannel(c) {
		return contract.Principal{}, contract.ErrInvalid
	}
	var p contract.Principal
	err := r.db.QueryRowContext(ctx, `SELECT p.id,p.kind,coalesce(p.owner_id,''),p.org_id FROM links l
 JOIN principals p ON p.id=l.principal_id WHERE l.channel=? AND l.external_id=? AND l.state='verified' AND p.enabled=1 AND p.kind='human'`, c.Channel, c.ID).Scan(&p.ID, &p.Kind, &p.OwnerID, &p.OrganizationID)
	return p, storeError(err)
}

// BeginLink creates a challenge for delivery to the owner; VerifyLink must be
// called by verified channel ingress with the observed account, never a model.
func (r *Registry) BeginLink(ctx context.Context, owner string, c contract.ChannelIdentity, expires time.Time) (Challenge, error) {
	now := r.now()
	if !canonicalChannel(c) || !expires.After(now) || expires.After(now.Add(linkTTL)) {
		return Challenge{}, contract.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Challenge{}, storeError(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=? AND kind='human' AND enabled=1", owner).Scan(&count); err != nil {
		return Challenge{}, storeError(err)
	}
	if count != 1 {
		return Challenge{}, contract.ErrForbidden
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM links WHERE channel=? AND external_id=? AND state='verified'", c.Channel, c.ID).Scan(&count); err != nil {
		return Challenge{}, storeError(err)
	}
	if count != 0 {
		return Challenge{}, contract.ErrConflict
	}
	id, err := randomID()
	if err != nil {
		return Challenge{}, contract.ErrUnavailable
	}
	proof, err := randomID()
	if err != nil {
		return Challenge{}, contract.ErrUnavailable
	}
	hash := sha256.Sum256([]byte(proof))
	if _, err := tx.ExecContext(ctx, "INSERT INTO links(id,channel,external_id,principal_id,state,proof_hash,expires_at) VALUES(?,?,?,?,'pending',?,?)", id, c.Channel, c.ID, owner, hash[:], expires.UnixNano()); err != nil {
		return Challenge{}, storeError(err)
	}
	if err := audit(ctx, tx, now, "link_pending", owner, owner, "owner_challenge"); err != nil {
		return Challenge{}, storeError(err)
	}
	if err := tx.Commit(); err != nil {
		return Challenge{}, storeError(err)
	}
	return Challenge{ID: id, Proof: proof, ExpiresAt: expires}, nil
}

func (r *Registry) VerifyLink(ctx context.Context, id, proof string, observed contract.ChannelIdentity) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	var state, owner string
	var channel contract.ChannelIdentity
	var hash []byte
	var expires int64
	if err := tx.QueryRowContext(ctx, "SELECT state,principal_id,channel,external_id,proof_hash,expires_at FROM links WHERE id=?", id).Scan(&state, &owner, &channel.Channel, &channel.ID, &hash, &expires); err != nil {
		return storeError(err)
	}
	if state != "pending" {
		return contract.ErrConflict
	}
	var enabled int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=? AND kind='human' AND enabled=1", owner).Scan(&enabled); err != nil {
		return storeError(err)
	}
	event := "verify"
	var refused error
	got := sha256.Sum256([]byte(proof))
	if !r.now().Before(time.Unix(0, expires)) {
		event, refused = "expire", contract.ErrForbidden
	} else if enabled != 1 || channel != observed || subtle.ConstantTimeCompare(hash, got[:]) != 1 {
		event, refused = "reject", contract.ErrForbidden
	}
	if refused == nil {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM links WHERE channel=? AND external_id=? AND state='verified'", channel.Channel, channel.ID).Scan(&count); err != nil {
			return storeError(err)
		}
		if count != 0 {
			return contract.ErrConflict
		}
	}
	if err := transitionLink(ctx, tx, id, event); err != nil {
		return err
	}
	if err := audit(ctx, tx, r.now(), "link_"+event, owner, owner, "channel_proof"); err != nil {
		return storeError(err)
	}
	if err := tx.Commit(); err != nil {
		return storeError(err)
	}
	return refused
}

func (r *Registry) RevokeLink(ctx context.Context, id, actor string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT l.principal_id FROM links l JOIN principals p ON p.id=l.principal_id
 WHERE l.id=? AND p.id=? AND p.kind='human' AND p.enabled=1`, id, actor).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.ErrForbidden
	}
	if err != nil {
		return storeError(err)
	}
	if err := transitionLink(ctx, tx, id, "revoke"); err != nil {
		return err
	}
	if err := audit(ctx, tx, r.now(), "link_revoke", actor, owner, "owner_revoked"); err != nil {
		return storeError(err)
	}
	return storeError(tx.Commit())
}
