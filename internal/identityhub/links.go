package identityhub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
)

const linkTTL = 10 * time.Minute

func (r *Registry) Bindings(ctx context.Context, owner string) ([]contract.ChannelBinding, error) {
	principal, err := r.Principal(ctx, owner)
	if err != nil {
		return nil, err
	}
	if principal.Kind != contract.Human {
		return nil, contract.ErrForbidden
	}
	rows, err := r.db.QueryContext(ctx, "SELECT id,channel,external_id,issuer,subject FROM links WHERE principal_id=? AND state='verified' ORDER BY channel,external_id", owner)
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	result := []contract.ChannelBinding{}
	for rows.Next() {
		value := contract.ChannelBinding{PrincipalID: owner}
		if err := rows.Scan(&value.ID, &value.Channel.Channel, &value.Channel.ID, &value.Issuer, &value.Subject); err != nil {
			return nil, storeError(err)
		}
		result = append(result, value)
	}
	return result, storeError(rows.Err())
}

// Every lifecycle write goes through this table, including operator import.
var linkTransitions = map[string]map[string]string{
	"pending":  {"verify": "verified", "reject": "gone", "expire": "gone"},
	"verified": {"revoke": "revoked", "delete": "gone", "prove_oidc": "verified"},
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
	if err := ensureSigningKey(ctx, tx, contract.SignLink); err != nil {
		return Challenge{}, err
	}
	proof, err := sign(ctx, tx, contract.SignLink, contract.LinkClaims{
		Issuer: contract.RunTokenIssuer, Audience: contract.ChannelLinkAudience, Subject: owner,
		Challenge: id, ChannelDigest: linkChannelDigest(c), Expires: expires.Unix(),
	})
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
	if refused == nil && strings.Contains(proof, ".") {
		keys, err := signingKeys(ctx, tx, r.now())
		if err != nil {
			return err
		}
		claims, err := contract.VerifyLinkToken(proof, keys, r.now())
		if err != nil || claims.Challenge != id || claims.Subject != owner ||
			claims.ChannelDigest != linkChannelDigest(observed) || claims.Expires != time.Unix(0, expires).Unix() {
			event, refused = "reject", contract.ErrForbidden
		}
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

func linkChannelDigest(c contract.ChannelIdentity) string {
	return contract.PolicyDigest([]byte(c.Channel + "\x00" + c.ID))
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

// ExpirePendingLinks bounds one control upkeep pass; unresolved proofs never
// authorize resolution while older expired rows await the next pass.
func (r *Registry) ExpirePendingLinks(ctx context.Context, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id,principal_id FROM links WHERE state='pending' AND expires_at<=? ORDER BY expires_at,id LIMIT 128", now.UnixNano())
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
		if err := transitionLink(ctx, tx, link[0], "expire"); err != nil {
			return err
		}
		if err := audit(ctx, tx, now, "link_expire", "system:identity-hub", link[1], "challenge_expired"); err != nil {
			return storeError(err)
		}
	}
	return storeError(tx.Commit())
}
