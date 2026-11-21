package spawn

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/shared/store"
)

// The v0 mirror is transitional (S7b, deleted in S7d): every activation
// republishes the space's generated passport tree and points `current` at
// the effective digest so v0 readers — render, passportview, the rights
// ledger — follow the DB truth while they are being cut over. The
// activation transaction leaves a durable mirror_pending row; the mirror
// clears it once the files carry the digest. A crash between commit and
// publish leaves the row for the sweeper — the DB is the truth and a
// mirror failure never un-activates.

// SpacesRoot is the spaces root the v0 mirror writes under; empty means
// no mirror (unit tests, spaces not mounted). BeforeMirror is the test
// seam for mirror-failure injection.
//
//	SpacesRoot   string
//	BeforeMirror func() error
// — declared on Revisions in revisions.go.

// mirror republishes rev's documents under the space's generated tree and
// points `current` at digest, then clears the pending row. rev.Prepare
// pins `current` to "pending" while it writes, so a partial mirror is
// never readable as a finished revision — readers refuse until Activate
// stamps the digest.
func (r *Revisions) mirror(ctx context.Context, t store.Tenant, spaceID string, rev passport.Revision, digest string) error {
	if r.SpacesRoot == "" {
		return nil
	}
	if r.BeforeMirror != nil {
		if err := r.BeforeMirror(); err != nil {
			return err
		}
	}
	dir := filepath.Join(r.SpacesRoot, spaceID)
	if err := rev.Prepare(dir, nil); err != nil {
		return err
	}
	if err := rev.LinkPolicyDigest(dir, digest); err != nil {
		return err
	}
	if err := rev.Activate(dir); err != nil {
		return err
	}
	return r.Store.Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.Scoped(t).Exec("DELETE FROM mirror_pending WHERE {tenant} AND id = ?", spaceID)
		return err
	})
}

// mirrorLog is where non-fatal mirror failures are reported — the pending
// row is the durable retry marker; the log line is the operator's signal.
var mirrorLog = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
}

// mirrorFailed reports a committed activation whose file publish failed.
func (r *Revisions) mirrorFailed(spaceID string, err error) {
	mirrorLog("spawn-controller: mirror %s: %v\n", spaceID, err)
}

// MirrorPending republishes every activation whose mirror a crash (or a
// failed attempt) left pending. The sweeper calls it on every tick; each
// space's mirror is re-derived from its CURRENT active set so a stale
// revision is never written back. Failures leave the row for the next
// tick; one bad space never starves the rest.
func (r *Revisions) MirrorPending(ctx context.Context) error {
	if r.SpacesRoot == "" {
		return nil
	}
	var due []store.DueRow
	if err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		var err error
		due, err = tx.DueRows("mirror_pending", math.MaxInt64, 1000)
		return err
	}); err != nil {
		return err
	}
	var firstErr error
	for _, d := range due {
		t := store.Tenant{Org: d.Org, Owner: d.Owner}
		rev, digest, err := r.activeMirrorSource(ctx, t, d.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			r.mirrorFailed(d.ID, err)
			continue
		}
		if err := r.mirror(ctx, t, d.ID, rev, digest); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			r.mirrorFailed(d.ID, err)
		}
	}
	return firstErr
}

// activeMirrorSource compiles the space's active layer set exactly as
// activation did — the mirror's revision tree and digest must be the
// committed spec, never a remembered candidate.
func (r *Revisions) activeMirrorSource(ctx context.Context, t store.Tenant, spaceID string) (passport.Revision, string, error) {
	var rev passport.Revision
	var digest string
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		env, err := spaceEnvironment(q, spaceID)
		if err != nil {
			return err
		}
		var storedDigest string
		if err := q.QueryRow("SELECT COALESCE(effective_digest, '') FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&storedDigest); err != nil {
			return err
		}
		if storedDigest == "" {
			return fmt.Errorf("space %q has no active spec to mirror", spaceID)
		}
		byLayer, atByLayer, err := activeLayerDocs(q, spaceID)
		if err != nil {
			return err
		}
		var latest int64
		for _, at := range atByLayer {
			if at > latest {
				latest = at
			}
		}
		reserved, err := reservedPorts(q, spaceID)
		if err != nil {
			return err
		}
		cat, err := r.catalogDocs()
		if err != nil {
			return err
		}
		var dig string
		rev, _, _, dig, err = compilePolicy(spaceID, env, byLayer, cat, time.Unix(latest, 0), reserved)
		if err != nil {
			return err
		}
		if dig != storedDigest {
			return fmt.Errorf("active set compiles to %s, stored digest is %s", dig, storedDigest)
		}
		digest = dig
		return nil
	})
	return rev, digest, err
}
