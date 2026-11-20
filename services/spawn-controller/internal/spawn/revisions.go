package spawn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/shared/auth"
	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/locale"
	"github.com/letya999/hermes-hub/shared/store"
)

// Layer slots in compile order (ADR-0106): ceilings and templates bound
// the user layer, and the agent layer merges last — it may only narrow
// what the layers above it produced.
var LayerOrder = []string{
	"platform-ceiling",
	"platform-template",
	"org-ceiling",
	"org-template",
	"user",
	"agent",
}

// ValidLayer reports whether name is one of the six passport layer slots.
func ValidLayer(name string) bool {
	for _, l := range LayerOrder {
		if l == name {
			return true
		}
	}
	return false
}

// Revision is one submitted layer document set and its lifecycle state.
// BaseRevision is the activation CAS anchor: the active revision the row
// was validated against, or — on a superseded row — the revision that
// superseded it (the live pointer chain, like v0's prev pointer).
type Revision struct {
	ID           string `json:"id"`
	Space        string `json:"space"`
	Layer        string `json:"layer"`
	State        string `json:"state"`
	Digest       string `json:"digest"`
	Error        string `json:"error,omitempty"`
	Detail       string `json:"detail,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	BaseRevision string `json:"base_revision,omitempty"`
}

// Space is one tenant-owned managed space.
type Space struct {
	Org         string `json:"org"`
	Owner       string `json:"owner"`
	ID          string `json:"id"`
	Environment string `json:"environment"`
	CreatedAt   int64  `json:"created_at"`
}

// EffectiveSpec is the compiled policy of the space's active layers:
// digest is "sha256:" + hex(sha256(PolicyBundle().Canonical())).
type EffectiveSpec struct {
	Space     string                     `json:"space"`
	Digest    string                     `json:"digest"`
	Documents map[string]json.RawMessage `json:"documents"`
}

// Revisions is the durable revision service: spaces, layered passport
// submissions, compile validation, atomic activation with rollback, the
// effective spec and per-consumer policy slices. BeforeCommit is the
// crash-window seam tests inject — it fires inside the activation
// transaction after the state moves and before the effective row lands.
type Revisions struct {
	Store   *store.Store
	Catalog catalog.Docs
	// LoadCatalog resolves the pinned catalog lazily when set — services
	// boot without the baked catalog tree reachable, and a compile that
	// still cannot resolve it fails unavailable, never silently.
	LoadCatalog  func() (catalog.Docs, error)
	Now          func() time.Time
	BeforeCommit func() error
}

func (r *Revisions) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Revisions) catalogDocs() (catalog.Docs, error) {
	if r.LoadCatalog != nil {
		return r.LoadCatalog()
	}
	return r.Catalog, nil
}

// CreateSpace registers one tenant-owned space; re-creating the same id
// with the same environment returns the existing row (the write is
// naturally idempotent — the idempotency key travels the contract, the
// (org, owner, id) key dedups).
func (r *Revisions) CreateSpace(ctx context.Context, t store.Tenant, id, environment string) (Space, error) {
	if !auth.ValidID(id) {
		return Space{}, httpapi.Errorf(httpapi.ErrInvalid, "invalid space id")
	}
	if environment != "dev" && environment != "prod" {
		return Space{}, httpapi.Errorf(httpapi.ErrInvalid, "environment must be dev or prod")
	}
	var out Space
	err := r.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var env string
		var at int64
		err := q.QueryRow("SELECT environment, created_at FROM spaces WHERE {tenant} AND id = ?", id).Scan(&env, &at)
		switch {
		case err == nil && env == environment:
			out = Space{Org: t.Org, Owner: t.Owner, ID: id, Environment: env, CreatedAt: at}
			return nil
		case err == nil:
			return fmt.Errorf("%w: space %q exists with environment %q", store.ErrConflict, id, env)
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		now := r.now().Unix()
		if err := q.Insert("spaces", []string{"id", "created_at", "environment"}, id, now, environment); err != nil {
			return err
		}
		out = Space{Org: t.Org, Owner: t.Owner, ID: id, Environment: environment, CreatedAt: now}
		return nil
	})
	return out, err
}

func (r *Revisions) space(ctx context.Context, t store.Tenant, spaceID string) (Space, error) {
	var out Space
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		var env string
		var at int64
		err := tx.Scoped(t).QueryRow("SELECT environment, created_at FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&env, &at)
		if err != nil {
			return err
		}
		out = Space{Org: t.Org, Owner: t.Owner, ID: spaceID, Environment: env, CreatedAt: at}
		return nil
	})
	return out, err
}

// Submit stores one layer's documents as a draft revision. It is
// idempotent on (space, layer, layer digest): resubmitting identical
// documents returns the existing row.
func (r *Revisions) Submit(ctx context.Context, t store.Tenant, spaceID, layer string, docs map[string]map[string]any) (Revision, error) {
	if !ValidLayer(layer) {
		return Revision{}, httpapi.Errorf(httpapi.ErrInvalid, "unknown layer %q", layer)
	}
	if len(docs) == 0 {
		return Revision{}, httpapi.Errorf(httpapi.ErrInvalid, "empty layer documents")
	}
	for name, body := range docs {
		if !validDocName(name) {
			return Revision{}, httpapi.Errorf(httpapi.ErrInvalid, "unknown passport document %q", name)
		}
		if body == nil {
			return Revision{}, httpapi.Errorf(httpapi.ErrInvalid, "document %q must be an object", name)
		}
	}
	canonical, err := json.Marshal(docs)
	if err != nil {
		return Revision{}, httpapi.Errorf(httpapi.ErrInvalid, "documents: %v", err)
	}
	digest := "sha256:" + hex.EncodeToString(sum256(canonical))
	var out Revision
	err = r.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		if _, err := spaceRow(q, spaceID); err != nil {
			return err
		}
		// Identical resubmissions return the newest matching row — except a
		// pruned one: pruned content can be submitted fresh again.
		var id, state string
		err := q.QueryRow("SELECT id, state FROM passport_revisions WHERE {tenant} AND space_id = ? AND layer = ? AND digest = ? ORDER BY created_at DESC, id DESC LIMIT 1", spaceID, layer, digest).Scan(&id, &state)
		if err == nil && state != "pruned" {
			got, err := loadRevision(q, spaceID, id)
			out = got.Revision
			return err
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		// The row id carries the same-digest row count so content that was
		// pruned can be submitted again under a fresh id.
		var n int
		if err := q.QueryRow("SELECT count(*) FROM passport_revisions WHERE {tenant} AND space_id = ? AND layer = ? AND digest = ?", spaceID, layer, digest).Scan(&n); err != nil {
			return err
		}
		now := r.now()
		revID := revisionID(t, spaceID, layer, digest, n)
		if err := fsm.Create(q, PassportRevisionMachine, revID, now); err != nil {
			// A concurrent identical submit owns the row.
			var existing string
			selErr := q.QueryRow("SELECT id FROM passport_revisions WHERE {tenant} AND space_id = ? AND layer = ? AND digest = ? AND state != 'pruned' ORDER BY created_at DESC, id DESC LIMIT 1", spaceID, layer, digest).Scan(&existing)
			if selErr != nil {
				return err
			}
			got, err := loadRevision(q, spaceID, existing)
			out = got.Revision
			return err
		}
		if _, err := q.Exec("UPDATE passport_revisions SET space_id = ?, layer = ?, digest = ?, documents = ?, created_at = ? WHERE {tenant} AND id = ?", spaceID, layer, digest, string(canonical), now.Unix(), revID); err != nil {
			return err
		}
		got, err := loadRevision(q, spaceID, revID)
		out = got.Revision
		return err
	})
	return out, err
}

// Validate compiles the revision's layer merged with the freshest known
// documents of the other layers — the active row where one exists, else
// the newest validated, else the newest draft — so a space can submit its
// full layer set before any layer has activated. Success moves draft ->
// validated and anchors the CAS base to the active revision of the same
// layer; a compile error moves draft -> rejected with a localized error
// key plus the compiler detail. A validated row re-validates in place,
// refreshing its base anchor.
func (r *Revisions) Validate(ctx context.Context, t store.Tenant, spaceID, id string) (Revision, error) {
	var out Revision
	err := r.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		rev, err := loadRevision(q, spaceID, id)
		if err != nil {
			return err
		}
		if rev.State != "draft" && rev.State != "validated" {
			return fmt.Errorf("%w: revision %q is %s", store.ErrConflict, id, rev.State)
		}
		env, err := spaceEnvironment(q, spaceID)
		if err != nil {
			return err
		}
		byLayer, latest, err := knownLayerDocs(q, spaceID, rev.Layer)
		if err != nil {
			return err
		}
		var docs map[string]map[string]any
		if err := json.Unmarshal(rev.documents, &docs); err != nil {
			return err
		}
		byLayer[rev.Layer] = docs
		if rev.CreatedAt > latest {
			latest = rev.CreatedAt
		}
		reserved, err := reservedPorts(q, spaceID)
		if err != nil {
			return err
		}
		cat, err := r.catalogDocs()
		if err != nil {
			return fmt.Errorf("%w: catalog: %v", httpapi.ErrUnavailable, err)
		}
		_, _, _, compileErr := compilePolicy(spaceID, env, byLayer, cat, time.Unix(latest, 0), reserved)
		if compileErr != nil {
			if rev.State != "draft" {
				return fmt.Errorf("%w: %v", store.ErrConflict, compileErr)
			}
			if err := fsm.Transition(q, PassportRevisionMachine, id, "reject", r.now()); err != nil {
				return err
			}
			if _, err := q.Exec("UPDATE passport_revisions SET error = ?, detail = ? WHERE {tenant} AND id = ?", locale.T(locale.Default, "spawn.revision-rejected"), compileErr.Error(), id); err != nil {
				return err
			}
			got, err := loadRevision(q, spaceID, id)
			out = got.Revision
			return err
		}
		base := ""
		var curID string
		err = q.QueryRow("SELECT id FROM passport_revisions WHERE {tenant} AND space_id = ? AND layer = ? AND state = 'active'", spaceID, rev.Layer).Scan(&curID)
		if err == nil {
			base = curID
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if rev.State == "draft" {
			if err := fsm.Transition(q, PassportRevisionMachine, id, "validate", r.now()); err != nil {
				return err
			}
		}
		if _, err := q.Exec("UPDATE passport_revisions SET base_revision = ?, error = '', detail = '' WHERE {tenant} AND id = ?", base, id); err != nil {
			return err
		}
		got, err := loadRevision(q, spaceID, id)
		out = got.Revision
		return err
	})
	return out, err
}

// Activate is the one-transaction revision cut: the current active row of
// the layer is CAS'd to superseded (re-anchoring its base to the target so
// rollback stays reachable), the target moves to active, the effective
// spec is recomputed from every active layer and inserted if absent, and
// superseded rows beyond the retention slack are pruned. The target must
// have been validated or superseded against the still-current active —
// any drift is a conflict. A crash anywhere before commit leaves the
// previous active serving.
func (r *Revisions) Activate(ctx context.Context, t store.Tenant, spaceID, id string) (Revision, error) {
	var out Revision
	err := r.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		rev, err := loadRevision(q, spaceID, id)
		if err != nil {
			return err
		}
		switch rev.State {
		case "active":
			out = rev.Revision
			return nil
		case "validated", "superseded":
		default:
			return fmt.Errorf("%w: revision %q is %s", store.ErrConflict, id, rev.State)
		}
		var curID string
		err = q.QueryRow("SELECT id FROM passport_revisions WHERE {tenant} AND space_id = ? AND layer = ? AND state = 'active'", spaceID, rev.Layer).Scan(&curID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if curID != rev.BaseRevision {
			return fmt.Errorf("%w: activation base moved to %q", store.ErrConflict, curID)
		}
		now := r.now()
		if curID != "" {
			if err := fsm.Transition(q, PassportRevisionMachine, curID, "supersede", now); err != nil {
				return err
			}
			if _, err := q.Exec("UPDATE passport_revisions SET base_revision = ? WHERE {tenant} AND id = ?", id, curID); err != nil {
				return err
			}
		}
		if err := fsm.Transition(q, PassportRevisionMachine, id, "activate", now); err != nil {
			return err
		}
		if r.BeforeCommit != nil {
			if err := r.BeforeCommit(); err != nil {
				return err
			}
		}
		env, err := spaceEnvironment(q, spaceID)
		if err != nil {
			return err
		}
		byLayer, latest, err := activeLayerDocs(q, spaceID)
		if err != nil {
			return err
		}
		reserved, err := reservedPorts(q, spaceID)
		if err != nil {
			return err
		}
		cat, err := r.catalogDocs()
		if err != nil {
			return fmt.Errorf("%w: catalog: %v", httpapi.ErrUnavailable, err)
		}
		_, canonical, digest, compileErr := compilePolicy(spaceID, env, byLayer, cat, time.Unix(latest, 0), reserved)
		if compileErr == nil {
			var have int
			if err := q.QueryRow("SELECT count(*) FROM effective_specs WHERE {tenant} AND id = ?", digest).Scan(&have); err != nil {
				return err
			}
			if have == 0 {
				if err := q.Insert("effective_specs", []string{"id", "space_id", "documents", "created_at"}, digest, spaceID, string(canonical), now.Unix()); err != nil {
					return err
				}
			}
		}
		// A compile failure never aborts the cut: during bootstrap the
		// active set fills in layer by layer and may not yet be coherent,
		// and Validate already gates quality. The effective spec simply
		// materializes on the first activation whose set compiles; until
		// then Effective reports the active set's compile error.
		if err := pruneSuperseded(q, spaceID, rev.Layer, now); err != nil {
			return err
		}
		got, err := loadRevision(q, spaceID, id)
		out = got.Revision
		return err
	})
	return out, err
}

// Effective recomputes the spec from the space's active layers and answers
// the stored immutable bundle for its digest.
func (r *Revisions) Effective(ctx context.Context, t store.Tenant, spaceID string) (EffectiveSpec, error) {
	var out EffectiveSpec
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		env, err := spaceEnvironment(q, spaceID)
		if err != nil {
			return err
		}
		byLayer, latest, err := activeLayerDocs(q, spaceID)
		if err != nil {
			return err
		}
		if len(byLayer) == 0 {
			return store.ErrNotFound
		}
		reserved, err := reservedPorts(q, spaceID)
		if err != nil {
			return err
		}
		cat, err := r.catalogDocs()
		if err != nil {
			return fmt.Errorf("%w: catalog: %v", httpapi.ErrUnavailable, err)
		}
		_, canonical, digest, err := compilePolicy(spaceID, env, byLayer, cat, time.Unix(latest, 0), reserved)
		if err != nil {
			// The active set does not compile — the space has no servable
			// effective spec until activation restores a coherent set.
			return fmt.Errorf("%w: effective spec: %v", httpapi.ErrUnavailable, err)
		}
		var stored string
		err = q.QueryRow("SELECT documents FROM effective_specs WHERE {tenant} AND space_id = ? AND id = ?", spaceID, digest).Scan(&stored)
		switch {
		case err == nil:
			canonical = []byte(stored)
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		docs, err := bundlePassports(canonical)
		if err != nil {
			return err
		}
		out = EffectiveSpec{Space: spaceID, Digest: digest, Documents: docs}
		return nil
	})
	return out, err
}

// PolicySlice is the subset of the stored effective spec the consumers
// registry attributes to service: a service or a digest the registry and
// this space do not know is not_found.
func (r *Revisions) PolicySlice(ctx context.Context, t store.Tenant, spaceID, service, digest string) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var raw string
		err := q.QueryRow("SELECT documents FROM effective_specs WHERE {tenant} AND space_id = ? AND id = ?", spaceID, digest).Scan(&raw)
		if err != nil {
			return err
		}
		docs, err := bundlePassports([]byte(raw))
		if err != nil {
			return err
		}
		slice, err := projectSlice(docs, service)
		if err != nil {
			return err
		}
		out = slice
		return nil
	})
	return out, err
}

// ListRevisions returns the space's revision rows, optionally narrowed to
// one layer, newest first.
func (r *Revisions) ListRevisions(ctx context.Context, t store.Tenant, spaceID, layer string) ([]Revision, error) {
	var out []Revision
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		if _, err := spaceRow(q, spaceID); err != nil {
			return err
		}
		query := "SELECT id, layer, state, digest, base_revision, error, detail, created_at FROM passport_revisions WHERE {tenant} AND space_id = ?"
		args := []any{spaceID}
		if layer != "" {
			query += " AND layer = ?"
			args = append(args, layer)
		}
		query += " ORDER BY created_at DESC, id"
		rowsQ, err := q.Query(query, args...)
		if err != nil {
			return err
		}
		defer rowsQ.Close()
		for rowsQ.Next() {
			var rev Revision
			if err := rowsQ.Scan(&rev.ID, &rev.Layer, &rev.State, &rev.Digest, &rev.BaseRevision, &rev.Error, &rev.Detail, &rev.CreatedAt); err != nil {
				return err
			}
			rev.Space = spaceID
			out = append(out, rev)
		}
		return rowsQ.Err()
	})
	return out, err
}

// revision is the full row shape including the stored document set.
type revision struct {
	Revision
	documents []byte
}

func loadRevision(q store.Scoped, spaceID, id string) (revision, error) {
	var rev revision
	err := q.QueryRow("SELECT id, COALESCE(layer, ''), state, COALESCE(digest, ''), base_revision, error, detail, created_at, documents FROM passport_revisions WHERE {tenant} AND space_id = ? AND id = ?", spaceID, id).
		Scan(&rev.ID, &rev.Layer, &rev.State, &rev.Digest, &rev.BaseRevision, &rev.Error, &rev.Detail, &rev.CreatedAt, &rev.documents)
	rev.Space = spaceID
	return rev, err
}

func spaceRow(q store.Scoped, spaceID string) (Space, error) {
	var env string
	var at int64
	err := q.QueryRow("SELECT environment, created_at FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&env, &at)
	return Space{Org: q.Tenant().Org, Owner: q.Tenant().Owner, ID: spaceID, Environment: env, CreatedAt: at}, err
}

func spaceEnvironment(q store.Scoped, spaceID string) (string, error) {
	var env string
	err := q.QueryRow("SELECT environment FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&env)
	return env, err
}

// activeLayerDocs reads every active layer's stored documents plus the
// newest created_at — the compile timestamp input that keeps the effective
// digest content-addressed.
func activeLayerDocs(q store.Scoped, spaceID string) (map[string]map[string]map[string]any, int64, error) {
	rows, err := q.Query("SELECT COALESCE(layer, ''), documents, created_at FROM passport_revisions WHERE {tenant} AND space_id = ? AND state = 'active'", spaceID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := map[string]map[string]map[string]any{}
	var latest int64
	for rows.Next() {
		var layer string
		var raw []byte
		var at int64
		if err := rows.Scan(&layer, &raw, &at); err != nil {
			return nil, 0, err
		}
		var docs map[string]map[string]any
		if len(raw) != 0 {
			if err := json.Unmarshal(raw, &docs); err != nil {
				return nil, 0, err
			}
		}
		out[layer] = docs
		if at > latest {
			latest = at
		}
	}
	return out, latest, rows.Err()
}

// knownLayerDocs reads, per layer slot other than skipLayer, the freshest
// usable documents: the active row if one exists, else the newest
// validated, else the newest draft — a layer validates against the spec
// the space's pending submissions intend. Rejected and pruned rows never
// participate.
func knownLayerDocs(q store.Scoped, spaceID, skipLayer string) (map[string]map[string]map[string]any, int64, error) {
	rows, err := q.Query("SELECT COALESCE(layer, ''), state, documents, created_at FROM passport_revisions WHERE {tenant} AND space_id = ? ORDER BY created_at DESC, id DESC", spaceID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	type pick struct {
		docs map[string]map[string]any
		at   int64
		rank int
	}
	rankOf := map[string]int{"draft": 1, "validated": 2, "active": 3}
	picks := map[string]pick{}
	var latest int64
	for rows.Next() {
		var layer, state string
		var raw []byte
		var at int64
		if err := rows.Scan(&layer, &state, &raw, &at); err != nil {
			return nil, 0, err
		}
		rank := rankOf[state]
		if layer == "" || layer == skipLayer || rank == 0 {
			continue
		}
		// Rows arrive newest-first: within one rank keep the first seen,
		// and a better rank always replaces a worse one.
		if cur, ok := picks[layer]; ok && cur.rank >= rank {
			continue
		}
		var docs map[string]map[string]any
		if len(raw) != 0 {
			if err := json.Unmarshal(raw, &docs); err != nil {
				return nil, 0, err
			}
		}
		picks[layer] = pick{docs: docs, at: at, rank: rank}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	out := map[string]map[string]map[string]any{}
	for layer, p := range picks {
		out[layer] = p.docs
		if p.at > latest {
			latest = p.at
		}
	}
	return out, latest, nil
}

// reservedPorts collects the host ports sibling spaces already published —
// the cross-space collision input Compile checks (SPEC-0092 R4.1).
func reservedPorts(q store.Scoped, spaceID string) (map[int]string, error) {
	rows, err := q.Query("SELECT space_id, documents FROM effective_specs WHERE {tenant} AND space_id != ?", spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var space, raw string
		if err := rows.Scan(&space, &raw); err != nil {
			return nil, err
		}
		var bundle struct {
			Passports map[string]struct {
				Ports map[string]int `json:"ports"`
			} `json:"passports"`
		}
		if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
			return nil, err
		}
		for _, p := range bundle.Passports["config"].Ports {
			out[p] = space
		}
	}
	return out, rows.Err()
}

// pruneSuperseded keeps the newest RevisionKeepSlack superseded rows of
// the (space, layer); older ones move to the final pruned state through
// the explicit prune edge.
func pruneSuperseded(q store.Scoped, spaceID, layer string, now time.Time) error {
	rows, err := q.Query("SELECT id FROM passport_revisions WHERE {tenant} AND space_id = ? AND layer = ? AND state = 'superseded' ORDER BY created_at DESC, id DESC", spaceID, layer)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for i, id := range ids {
		if i < passport.RevisionKeepSlack {
			continue
		}
		if err := fsm.Transition(q, PassportRevisionMachine, id, "prune", now); err != nil {
			return err
		}
	}
	return nil
}

// compilePolicy runs the layered compile: grants docs fold into the
// capability lists and ledger LayeredGrants derives, then Compile merges
// the six layers, and the policy bundle yields the canonical document set
// and its content digest.
func compilePolicy(space, env string, byLayer map[string]map[string]map[string]any, cat catalog.Docs, now time.Time, reserved map[int]string) (passport.Revision, []byte, string, error) {
	var empty passport.Revision
	layers := make([]passport.Layer, len(LayerOrder))
	for i, name := range LayerOrder {
		layers[i] = passport.Layer{Name: name, Version: 1, Docs: byLayer[name]}
	}
	folded, ledger, err := passport.LayeredGrants(layers)
	if err != nil {
		return empty, nil, "", err
	}
	rev, err := passport.Compile(passport.Inputs{
		Space:            space,
		Environment:      env,
		PlatformCeiling:  folded[0],
		PlatformTemplate: folded[1],
		OrgCeiling:       folded[2],
		OrgTemplate:      folded[3],
		User:             folded[4],
		Agent:            folded[5],
		Grants:           ledger,
		Catalog:          cat,
		Now:              now,
		RendersInfra:     rendersInfra(folded),
		ReservedPorts:    reserved,
	})
	if err != nil {
		return empty, nil, "", err
	}
	bundle, err := rev.PolicyBundle()
	if err != nil {
		return empty, nil, "", err
	}
	canonical, digest, err := bundle.Canonical()
	if err != nil {
		return empty, nil, "", err
	}
	return rev, canonical, digest, nil
}

// rendersInfra resolves stack.identity.infra the way the invariant merge
// does: the first layer declaring it fixes it.
func rendersInfra(layers []passport.Layer) bool {
	for _, l := range layers {
		if identity, ok := l.Docs["stack"]["identity"].(map[string]any); ok {
			if v, isBool := identity["infra"].(bool); isBool {
				return v
			}
		}
	}
	return false
}

func revisionID(t store.Tenant, spaceID, layer, digest string, n int) string {
	sum := sha256.Sum256([]byte(t.Org + "\x00" + t.Owner + "\x00" + spaceID + "\x00" + layer + "\x00" + digest + "\x00" + fmt.Sprint(n)))
	return "rev-" + hex.EncodeToString(sum[:])[:16]
}

func sum256(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func validDocName(name string) bool {
	for _, n := range passport.Names {
		if n == name {
			return true
		}
	}
	return false
}
