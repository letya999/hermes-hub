package identityhub

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/iam"
	"github.com/letya999/hermes-hub/internal/identity"
)

func canonicalIssuer(issuer string) bool {
	value, err := iam.ValidateIssuer(issuer)
	u, parseErr := url.Parse(issuer)
	return err == nil && value == issuer && len(issuer) <= identity.MaxProvenanceBytes && parseErr == nil && u.User == nil
}

func validOIDCSubject(subject string) bool {
	return subject != "" && len(subject) <= identity.MaxProvenanceBytes && !strings.ContainsAny(subject, "\r\n")
}

func (r *Registry) VerifyOIDC(ctx context.Context, proof contract.OIDCLink) error {
	if !canonicalIssuer(proof.Issuer) || !validOIDCSubject(proof.Subject) || !canonicalChannel(proof.Channel) {
		return contract.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	if err := r.verifyOIDC(ctx, tx, proof, "verified_ingress"); err != nil {
		return err
	}
	return storeError(tx.Commit())
}

func (r *Registry) verifyOIDC(ctx context.Context, tx *sql.Tx, proof contract.OIDCLink, source string) error {
	var id, existingOwner, issuer, subject string
	err := tx.QueryRowContext(ctx, "SELECT id,principal_id,issuer,subject FROM links WHERE channel=? AND external_id=? AND state='verified'", proof.Channel.Channel, proof.Channel.ID).Scan(&id, &existingOwner, &issuer, &subject)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return storeError(err)
	}
	owner := proof.PrincipalID
	if owner == "" {
		if err := tx.QueryRowContext(ctx, "SELECT s.principal_id FROM subjects s JOIN principals p ON p.id=s.principal_id WHERE issuer=? AND subject=? AND p.kind='human' AND p.enabled=1", proof.Issuer, proof.Subject).Scan(&owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return contract.ErrForbidden
			}
			return storeError(err)
		}
	} else if source == "verified_ingress" && existingOwner != owner {
		return contract.ErrForbidden
	}
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=? AND kind='human' AND enabled=1", owner).Scan(&active); err != nil {
		return storeError(err)
	}
	if active != 1 {
		return contract.ErrForbidden
	}
	if existingOwner != "" && (existingOwner != owner || (issuer != "" && (issuer != proof.Issuer || subject != proof.Subject))) {
		return contract.ErrConflict
	}
	if err := bindSubject(ctx, tx, proof.Issuer, proof.Subject, owner); err != nil {
		return err
	}
	if id != "" && issuer == proof.Issuer && subject == proof.Subject {
		return nil
	}
	if id == "" {
		id, err = randomID()
		if err != nil {
			return contract.ErrUnavailable
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO links(id,channel,external_id,principal_id,state,expires_at) VALUES(?,?,?,?,'pending',0)", id, proof.Channel.Channel, proof.Channel.ID, owner); err != nil {
			return storeError(err)
		}
		if err := transitionLink(ctx, tx, id, "verify"); err != nil {
			return err
		}
	} else if err := transitionLink(ctx, tx, id, "prove_oidc"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE links SET issuer=?,subject=? WHERE id=?", proof.Issuer, proof.Subject, id); err != nil {
		return storeError(err)
	}
	if err := audit(ctx, tx, r.now(), "link_oidc_verified", owner, owner, source); err != nil {
		return storeError(err)
	}
	return nil
}

func (r *Registry) Unlink(ctx context.Context, query contract.UnlinkRequest) error {
	if !canonicalChannel(query.Channel) {
		return contract.ErrInvalid
	}
	var id string
	err := r.db.QueryRowContext(ctx, "SELECT id FROM links WHERE channel=? AND external_id=? AND principal_id=? AND state='verified'", query.Channel.Channel, query.Channel.ID, query.PrincipalID).Scan(&id)
	if err != nil {
		return storeError(err)
	}
	return r.RevokeLink(ctx, id, query.PrincipalID)
}
