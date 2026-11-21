package spawn

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/locale"
	"github.com/letya999/hermes-hub/shared/store"
	"gopkg.in/yaml.v3"
)

// ApplyResult is the apply receipt the contract carries: the submitted
// layer rows, the would-be (dry run) or new effective digest, the digest
// and layer set it replaces, the classified diff and the action plan the
// caller executes.
type ApplyResult struct {
	Revisions         []Revision
	EffectiveDigest   string
	PreviousDigest    string
	PreviousRevisions []string
	Changes           []passport.Change
	Actions           []reconcile.Action
	Documents         map[string]json.RawMessage
}

// Apply submits every listed layer's documents, validates them, compiles
// the would-be set and — without dryRun — activates it in one transaction
// on the base-digest CAS (Decision 11/13: the space's current effective
// digest is the stale-base anchor). dryRun submits and validates but
// activates nothing; the rows stay validated for the confirmed call to
// dedup against.
func (r *Revisions) Apply(ctx context.Context, t store.Tenant, spaceID string, layers map[string]map[string]map[string]any, baseDigest string, dryRun bool) (ApplyResult, error) {
	var out ApplyResult
	if len(layers) == 0 {
		return out, httpapi.Errorf(httpapi.ErrInvalid, "empty layer set")
	}
	if _, err := r.space(ctx, t, spaceID); err != nil {
		return out, err
	}
	var previousDigest string
	if err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		return tx.Scoped(t).QueryRow("SELECT COALESCE(effective_digest, '') FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&previousDigest)
	}); err != nil {
		return out, err
	}
	if previousDigest != baseDigest {
		return out, fmt.Errorf("%w: %s", store.ErrConflict, locale.T(locale.Default, "spawn.apply-stale-base"))
	}
	names := make([]string, 0, len(layers))
	for layer := range layers {
		if !ValidLayer(layer) {
			return out, httpapi.Errorf(httpapi.ErrInvalid, "unknown layer %q", layer)
		}
		names = append(names, layer)
	}
	sort.Strings(names)
	var ids []string
	for _, layer := range names {
		rev, err := r.Submit(ctx, t, spaceID, layer, layers[layer])
		if err != nil {
			return out, err
		}
		if rev.State == "draft" || rev.State == "validated" {
			if rev, err = r.Validate(ctx, t, spaceID, rev.ID); err != nil {
				return out, err
			}
		}
		ids = append(ids, rev.ID)
		out.Revisions = append(out.Revisions, rev)
	}
	out.PreviousDigest = previousDigest

	// Assemble the would-be set read-only: active layer docs with the
	// submitted layers replaced, compiled exactly as the activation tx
	// will — the compile is deterministic over the same rows, so the
	// receipt's digest is the digest activation writes.
	var candidate passport.Revision
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		byLayer, atByLayer, err := activeLayerDocs(q, spaceID)
		if err != nil {
			return err
		}
		prevIDs, err := q.Query("SELECT id FROM passport_revisions WHERE {tenant} AND space_id = ? AND state = 'active' ORDER BY layer", spaceID)
		if err != nil {
			return err
		}
		for prevIDs.Next() {
			var id string
			if err := prevIDs.Scan(&id); err != nil {
				prevIDs.Close()
				return err
			}
			out.PreviousRevisions = append(out.PreviousRevisions, id)
		}
		prevIDs.Close()
		for i, layer := range names {
			docs, err := decodeRevisionDocs(mustDocs(q, spaceID, out.Revisions[i].ID))
			if err != nil {
				return err
			}
			byLayer[layer] = docs
			atByLayer[layer] = out.Revisions[i].CreatedAt
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
			return fmt.Errorf("%w: catalog: %v", httpapi.ErrUnavailable, err)
		}
		var canonical []byte
		var digest string
		var compileErr error
		candidate, _, canonical, digest, compileErr = compilePolicy(spaceID, envOf(q, spaceID), byLayer, cat, time.Unix(latest, 0), reserved)
		if compileErr != nil {
			return fmt.Errorf("%w: %s: %v", store.ErrConflict, locale.T(locale.Default, "spawn.revision-rejected"), compileErr)
		}
		out.EffectiveDigest = digest
		out.Documents, err = bundlePassports(canonical)
		return err
	})
	if err != nil {
		return ApplyResult{}, err
	}
	prevEff, err := effectiveOf(ctx, r, t, spaceID, previousDigest)
	if err != nil {
		return ApplyResult{}, err
	}
	nextEff, err := candidate.Effective()
	if err != nil {
		return ApplyResult{}, err
	}
	out.Changes = passport.Diff(prevEff, nextEff)
	if previousDigest != "" {
		out.Actions = reconcile.Plan(out.Changes, nextEff.Stack.Services)
	}
	if dryRun {
		return out, nil
	}
	if _, err := r.Activate(ctx, t, spaceID, ids, &baseDigest); err != nil {
		return ApplyResult{}, err
	}
	eff, err := r.Effective(ctx, t, spaceID)
	if err != nil {
		return ApplyResult{}, err
	}
	out.EffectiveDigest = eff.Digest
	out.Documents = eff.Documents
	return out, nil
}

// envOf resolves the space's environment inside an open read; the space
// row's existence was already checked on the apply path.
func envOf(q store.Scoped, spaceID string) string {
	env, _ := spaceEnvironment(q, spaceID)
	return env
}

func mustDocs(q store.Scoped, spaceID, id string) []byte {
	var raw []byte
	if err := q.QueryRow("SELECT documents FROM passport_revisions WHERE {tenant} AND space_id = ? AND id = ?", spaceID, id).Scan(&raw); err != nil {
		return nil
	}
	return raw
}

// effectiveOf rebuilds the passport.Effective a stored digest carries —
// the diff baseline for apply and settings. An empty digest is the first
// apply: the baseline is the zero effective.
func effectiveOf(ctx context.Context, r *Revisions, t store.Tenant, spaceID, digest string) (passport.Effective, error) {
	if digest == "" {
		return passport.Effective{}, nil
	}
	var eff passport.Effective
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		var stored string
		err := tx.Scoped(t).QueryRow("SELECT documents FROM effective_specs WHERE {tenant} AND space_id = ? AND id = ?", spaceID, digest).Scan(&stored)
		if err != nil {
			return err
		}
		docs, err := bundlePassports([]byte(stored))
		if err != nil {
			return err
		}
		rev := passport.Revision{Docs: map[string][]byte{}}
		for name, raw := range docs {
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				return err
			}
			body, err := yaml.Marshal(doc)
			if err != nil {
				return err
			}
			rev.Docs[name] = body
		}
		got, err := rev.Effective()
		if err != nil {
			return err
		}
		eff = got
		return nil
	})
	return eff, err
}
