package spawn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

// ExplainField is one effective leaf: its value and the layer that set it
// (one of LayerOrder, or "derived" for compiler-synthesized content).
type ExplainField struct {
	Doc   string `json:"doc"`
	Path  string `json:"path"`
	Value any    `json:"value"`
	Layer string `json:"layer"`
}

// ExplainReport is the per-leaf provenance of the space's active spec.
type ExplainReport struct {
	Space  string         `json:"space"`
	Digest string         `json:"digest"`
	Fields []ExplainField `json:"fields"`
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
	LoadCatalog func() (catalog.Docs, error)
	Now         func() time.Time
	// BeforeCommit fires inside the activation transaction after the
	// revision moves and before the effective row lands — the crash
	// seam for TestApplyCrashLeavesPreviousActive.
	BeforeCommit func() error
	// SpacesRoot is the operator's spaces root the transitional v0
	// mirror republishes under after every committed activation; empty
	// disables the mirror (unit tests, no mount). BeforeMirror is the
	// mirror-failure seam for TestMirrorFailureNeverUnactivates.
	SpacesRoot   string
	BeforeMirror func() error
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
		docs, err := decodeRevisionDocs(rev.documents)
		if err != nil {
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
		_, _, _, _, compileErr := compilePolicy(spaceID, env, byLayer, cat, time.Unix(latest, 0), reserved)
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

// Activate is the one-transaction revision cut over a SET of revisions —
// at most one per layer; single-revision activation and rollback are the
// one-element case. The transaction first compiles the would-be active set
// (every current active layer with the listed layers replaced): if it does
// not compile, a conflict is returned and NOTHING is written — no state
// change, no supersede, no prune — so a space keeps serving the policy it
// had. If it compiles, each listed revision's layer is CAS'd (the listed
// row's base must still be the active row), the current rows move to
// superseded re-anchored to their replacement, the targets move to active,
// the effective spec is inserted if absent and superseded rows past the
// keep slack are pruned. Activate is the authoritative gate — Validate is
// only a pre-check against the newest known documents.
// When base is non-nil it is the digest CAS anchor: the space's current
// effective digest must equal *base or nothing moves (apply and rollback
// pass it; importers pass nil). After commit, the transitional v0 mirror
// republishes the space's generated tree — the pending row the
// transaction left makes the sweeper retry a failed or lost publish.
func (r *Revisions) Activate(ctx context.Context, t store.Tenant, spaceID string, ids []string, base *string) ([]Revision, error) {
	var out []Revision
	var mirrorRev passport.Revision
	var mirrorDigest string
	err := r.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		if len(ids) == 0 {
			return httpapi.Errorf(httpapi.ErrInvalid, "empty activation set")
		}
		targets := map[string]revision{}
		for _, id := range ids {
			rev, err := loadRevision(q, spaceID, id)
			if err != nil {
				return err
			}
			switch rev.State {
			case "validated", "superseded", "active":
			default:
				return fmt.Errorf("%w: revision %q is %s", store.ErrConflict, id, rev.State)
			}
			if _, dup := targets[rev.Layer]; dup {
				return httpapi.Errorf(httpapi.ErrInvalid, "two revisions for layer %q", rev.Layer)
			}
			targets[rev.Layer] = rev
		}
		env, err := spaceEnvironment(q, spaceID)
		if err != nil {
			return err
		}
		if base != nil {
			// The digest CAS: the caller compiled against base; if the
			// pointer moved since, this activation is stale — nothing writes.
			var currentDigest string
			err = q.QueryRow("SELECT COALESCE(effective_digest, '') FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&currentDigest)
			if err != nil {
				return err
			}
			if currentDigest != *base {
				return fmt.Errorf("%w: %s", store.ErrConflict, locale.T(locale.Default, "spawn.apply-stale-base"))
			}
		}
		byLayer, atByLayer, err := activeLayerDocs(q, spaceID)
		if err != nil {
			return err
		}
		for layer, rev := range targets {
			if rev.State == "active" {
				continue
			}
			docs, err := decodeRevisionDocs(rev.documents)
			if err != nil {
				return err
			}
			byLayer[layer] = docs
			atByLayer[layer] = rev.CreatedAt
		}
		// The spec stamps the would-be set's freshest revision — the
		// replaced layers' targets, not whatever the superseded rows
		// carried. A rollback to an older revision must reproduce the
		// older set's digest exactly.
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
		compiled, attr, canonical, digest, compileErr := compilePolicy(spaceID, env, byLayer, cat, time.Unix(latest, 0), reserved)
		if compileErr != nil {
			return fmt.Errorf("%w: %s: %v", store.ErrConflict, locale.T(locale.Default, "spawn.revision-rejected"), compileErr)
		}
		mirrorRev, mirrorDigest = compiled, digest
		now := r.now()
		moved := map[string]string{}
		for layer, rev := range targets {
			if rev.State == "active" {
				continue
			}
			var curID string
			err = q.QueryRow("SELECT id FROM passport_revisions WHERE {tenant} AND space_id = ? AND layer = ? AND state = 'active'", spaceID, layer).Scan(&curID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if curID != rev.BaseRevision {
				return fmt.Errorf("%w: activation base moved to %q", store.ErrConflict, curID)
			}
			if curID != "" {
				if err := fsm.Transition(q, PassportRevisionMachine, curID, "supersede", now); err != nil {
					return err
				}
				if _, err := q.Exec("UPDATE passport_revisions SET base_revision = ? WHERE {tenant} AND id = ?", rev.ID, curID); err != nil {
					return err
				}
			}
			if err := fsm.Transition(q, PassportRevisionMachine, rev.ID, "activate", now); err != nil {
				return err
			}
			moved[layer] = rev.ID
		}
		if r.BeforeCommit != nil {
			if err := r.BeforeCommit(); err != nil {
				return err
			}
		}
		var have int
		if err := q.QueryRow("SELECT count(*) FROM effective_specs WHERE {tenant} AND id = ?", digest).Scan(&have); err != nil {
			return err
		}
		if have == 0 {
			attrJSON, err := json.Marshal(attr)
			if err != nil {
				return err
			}
			if err := q.Insert("effective_specs", []string{"id", "space_id", "documents", "attribution", "created_at"}, digest, spaceID, string(canonical), string(attrJSON), now.Unix()); err != nil {
				return err
			}
		}
		// Decision 11: the stored pointer is the readers' and claims' only
		// view of the active policy — it lands in the same transaction as
		// the revision moves, so a space never points at a spec it does
		// not serve. Runtime rows bound to another digest drain in the
		// same cut (Decision 10): a warm or busy runtime on the old policy
		// never answers under the new one.
		if _, err := q.Exec("UPDATE spaces SET effective_digest = ? WHERE {tenant} AND id = ?", digest, spaceID); err != nil {
			return err
		}
		rows, err := q.Query("SELECT id FROM runtimes WHERE {tenant} AND space_id = ? AND policy_digest != ? AND state IN ('warm','busy')", spaceID, digest)
		if err != nil {
			return err
		}
		var stale []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			stale = append(stale, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range stale {
			if err := runtimeTransition(q, id, "drain", now); err != nil {
				return err
			}
		}
		for layer := range moved {
			if err := pruneSuperseded(q, spaceID, layer, now); err != nil {
				return err
			}
		}
		if r.SpacesRoot != "" {
			// The v0 mirror publish is a durable post-commit effect: the
			// pending row commits with the activation so a crash between
			// here and the file write can never lose it (S7b).
			if _, err := q.Exec("DELETE FROM mirror_pending WHERE {tenant} AND id = ?", spaceID); err != nil {
				return err
			}
			if err := q.Insert("mirror_pending", []string{"id", "state", "deadline"}, spaceID, "pending", now.Unix()); err != nil {
				return err
			}
		}
		out = make([]Revision, 0, len(targets))
		for _, id := range ids {
			got, err := loadRevision(q, spaceID, id)
			if err != nil {
				return err
			}
			out = append(out, got.Revision)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if mErr := r.mirror(context.WithoutCancel(ctx), t, spaceID, mirrorRev, mirrorDigest); mErr != nil {
		// Mirror failure never un-activates: the pending row makes the
		// sweeper retry; the DB is the truth, files are a view.
		r.mirrorFailed(spaceID, mErr)
	}
	return out, nil
}

// Effective answers the space's stored effective spec: the digest
// activation wrote on the space row plus the immutable effective_specs
// bundle for it (Decision 11 — the read path never recompiles). A space
// that never activated answers not_found, like before.
func (r *Revisions) Effective(ctx context.Context, t store.Tenant, spaceID string) (EffectiveSpec, error) {
	var out EffectiveSpec
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var digest string
		err := q.QueryRow("SELECT effective_digest FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&digest)
		if err != nil {
			return err
		}
		if digest == "" {
			return store.ErrNotFound
		}
		var stored string
		err = q.QueryRow("SELECT documents FROM effective_specs WHERE {tenant} AND space_id = ? AND id = ?", spaceID, digest).Scan(&stored)
		if errors.Is(err, store.ErrNotFound) {
			// The pointer names a bundle the space does not carry — an
			// internal inconsistency, not a client miss.
			return fmt.Errorf("%w: effective spec %s is gone", httpapi.ErrUnavailable, digest)
		}
		if err != nil {
			return err
		}
		docs, err := bundlePassports([]byte(stored))
		if err != nil {
			return err
		}
		out = EffectiveSpec{Space: spaceID, Digest: digest, Documents: docs}
		return nil
	})
	return out, err
}

// Explain answers the space's per-leaf provenance: every leaf of the
// stored effective bundle with the layer the activating compile recorded
// for it. The read never recompiles — attribution lands on the spec row
// in the same transaction as the bundle itself.
func (r *Revisions) Explain(ctx context.Context, t store.Tenant, spaceID string) (ExplainReport, error) {
	var out ExplainReport
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var digest string
		err := q.QueryRow("SELECT effective_digest FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&digest)
		if err != nil {
			return err
		}
		if digest == "" {
			return store.ErrNotFound
		}
		var stored, attrRaw string
		err = q.QueryRow("SELECT documents, attribution FROM effective_specs WHERE {tenant} AND space_id = ? AND id = ?", spaceID, digest).Scan(&stored, &attrRaw)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: effective spec %s is gone", httpapi.ErrUnavailable, digest)
		}
		if err != nil {
			return err
		}
		docs, err := bundlePassports([]byte(stored))
		if err != nil {
			return err
		}
		var attr map[string]map[string]string
		if err := json.Unmarshal([]byte(attrRaw), &attr); err != nil {
			return err
		}
		fields := []ExplainField{}
		for _, doc := range passport.Names {
			raw, ok := docs[doc]
			if !ok {
				continue
			}
			var full map[string]any
			if err := json.Unmarshal(raw, &full); err != nil {
				return err
			}
			body := map[string]any{}
			for k, v := range full {
				if k == "apiVersion" || k == "kind" || k == "metadata" {
					continue
				}
				body[k] = v
			}
			flat := map[string]any{}
			flattenJSON(body, "", flat)
			paths := make([]string, 0, len(flat))
			for p := range flat {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			for _, p := range paths {
				layer := attr[doc][p]
				if layer == "" {
					layer = passport.DerivedLayer
				}
				fields = append(fields, ExplainField{Doc: doc, Path: p, Value: flat[p], Layer: layer})
			}
		}
		out = ExplainReport{Space: spaceID, Digest: digest, Fields: fields}
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
func activeLayerDocs(q store.Scoped, spaceID string) (map[string]map[string]map[string]any, map[string]int64, error) {
	rows, err := q.Query("SELECT COALESCE(layer, ''), documents, created_at FROM passport_revisions WHERE {tenant} AND space_id = ? AND state = 'active'", spaceID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[string]map[string]map[string]any{}
	at := map[string]int64{}
	for rows.Next() {
		var layer string
		var raw []byte
		var created int64
		if err := rows.Scan(&layer, &raw, &created); err != nil {
			return nil, nil, err
		}
		docs, err := decodeRevisionDocs(raw)
		if err != nil {
			return nil, nil, err
		}
		out[layer] = docs
		at[layer] = created
	}
	return out, at, rows.Err()
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
		docs, err := decodeRevisionDocs(raw)
		if err != nil {
			return nil, 0, err
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
func compilePolicy(space, env string, byLayer map[string]map[string]map[string]any, cat catalog.Docs, now time.Time, reserved map[int]string) (passport.Revision, passport.Attribution, []byte, string, error) {
	var empty passport.Revision
	layers := make([]passport.Layer, len(LayerOrder))
	for i, name := range LayerOrder {
		layers[i] = passport.Layer{Name: name, Version: 1, Docs: byLayer[name]}
	}
	folded, ledger, err := passport.LayeredGrants(layers)
	if err != nil {
		return empty, nil, nil, "", err
	}
	rev, attr, err := passport.CompileAttribution(passport.Inputs{
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
		return empty, nil, nil, "", err
	}
	bundle, err := rev.PolicyBundle()
	if err != nil {
		return empty, nil, nil, "", err
	}
	canonical, digest, err := bundle.Canonical()
	if err != nil {
		return empty, nil, nil, "", err
	}
	return rev, attr, canonical, digest, nil
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

// decodeRevisionDocs unmarshals a stored revision document set back into
// compiler-shaped values. Passport docs arrive YAML-shaped (Go ints), but
// the store round-trips them through JSON, where every number decodes as
// float64 — the compiler's integer-leaf assertions then reject valid
// content (the v0 import failed on tools.binding.generation). Decoding
// with UseNumber plus yamlShape restores the YAML types.
func decodeRevisionDocs(raw []byte) (map[string]map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var docs map[string]map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&docs); err != nil {
		return nil, err
	}
	for name, body := range docs {
		if shaped, ok := yamlShape(body).(map[string]any); ok {
			docs[name] = shaped
		}
	}
	return docs, nil
}

// yamlShape rewrites decoded JSON values to the Go types a YAML decode
// would have produced, in place: integral numbers become int, fractional
// ones stay float64.
func yamlShape(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = yamlShape(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = yamlShape(e)
		}
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t
	default:
		return v
	}
}
