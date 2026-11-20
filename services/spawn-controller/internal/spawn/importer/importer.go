// Package importer is the one-time v0 -> v1 move (D5, CHG-0336 S5):
// enabled through HUB_IMPORT_V0_DIR + HUB_IMPORT_SPACES_DIR, it runs on
// the spawn-controller's first start once and lands every v0 space's
// current passport revision as the active user layer, plus its backups,
// image pins and runtime-generation baselines in the durable store.
// HUB_IMPORT_DRY_RUN=true prints the same report without writing.
//
// Invariants the package keeps:
//   - source files are read-only — nothing under the v0 dirs is created,
//     modified or deleted;
//   - the report carries counts, ids and stage names only: no document
//     contents, no environment values, no container details;
//   - data the v1 schema has no reader for (signals/transitions journals,
//     v0 jobs, holds, pins, orphan records and live runtime rows) is
//     counted as dropped with a reason, never guessed into a table;
//   - every write is idempotent — a crashed import resumes, a second run
//     is a no-op.
package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/shared/auth"
	"github.com/letya999/hermes-hub/shared/store"
	"gopkg.in/yaml.v3"
)

// localOrg is the installation organization until identity-hub lands:
// the tenant of every row the importer writes.
const localOrg = "local"

// Config names the v0 sources. Empty V0Dir disables the importer; the
// spaces root falls back to <V0Dir>/spaces when unset.
type Config struct {
	V0Dir     string
	SpacesDir string
	DryRun    bool
}

// Importer binds the source readers to the durable services.
type Importer struct {
	Revisions *spawn.Revisions
	Store     *store.Store
	Now       func() time.Time
}

func (i *Importer) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

// Failure is one space's failed stage: the id and the stage are
// allowlisted — never a document body.
type Failure struct {
	Space string `json:"space"`
	Stage string `json:"stage"`
	Error string `json:"error"`
}

// Report is what the run did or would do. Every field is a count, an id
// or a fixed reason word — document contents never appear here.
type Report struct {
	Ran                 bool      `json:"ran"`
	DryRun              bool      `json:"dry_run"`
	Reason              string    `json:"reason,omitempty"`
	Spaces              []string  `json:"spaces,omitempty"`
	Revisions           int       `json:"revisions"`
	Backups             int       `json:"backups"`
	ImagePins           int       `json:"image_pins"`
	Generations         int       `json:"generations"`
	RuntimesDropped     int       `json:"runtimes_dropped"`
	JobsDropped         int       `json:"jobs_dropped"`
	HoldsDropped        int       `json:"holds_dropped"`
	PinsDropped         int       `json:"pins_dropped"`
	OrphansDropped      int       `json:"orphans_dropped"`
	SignalsDropped      int       `json:"signals_dropped"`
	TransitionsDropped  int       `json:"transitions_dropped"`
	UnmatchedPrincipals int       `json:"unmatched_principals"`
	UnmanagedSpaces     int       `json:"unmanaged_spaces"`
	Failed              []Failure `json:"failed,omitempty"`
}

// envelopeKeys are the v2 file envelope fields — never layer content.
var envelopeKeys = map[string]bool{"apiVersion": true, "kind": true, "metadata": true}

// Run executes the import (or its dry run) and returns the report.
func (i *Importer) Run(ctx context.Context, cfg Config) (Report, error) {
	rep := Report{DryRun: cfg.DryRun}
	if cfg.V0Dir == "" {
		rep.Reason = "disabled"
		return rep, nil
	}
	if cfg.SpacesDir == "" {
		cfg.SpacesDir = filepath.Join(cfg.V0Dir, "spaces")
	}
	manager, err := readManager(cfg.V0Dir)
	if err != nil {
		return rep, fmt.Errorf("supervisor.json: %w", err)
	}
	backups, err := readBackups(cfg.V0Dir)
	if err != nil {
		return rep, fmt.Errorf("backups.json: %w", err)
	}
	if rep.SignalsDropped, err = countLines(filepath.Join(cfg.V0Dir, "signals.jsonl")); err != nil {
		return rep, fmt.Errorf("signals.jsonl: %w", err)
	}
	if rep.TransitionsDropped, err = countLines(filepath.Join(cfg.V0Dir, "transitions.jsonl")); err != nil {
		return rep, fmt.Errorf("transitions.jsonl: %w", err)
	}
	rep.RuntimesDropped = len(manager.Items)
	rep.JobsDropped = manager.Jobs
	rep.HoldsDropped = manager.Holds
	rep.PinsDropped = manager.Pins
	rep.OrphansDropped = manager.Orphans

	spaces, err := discoverSpaces(cfg.SpacesDir)
	if err != nil {
		return rep, err
	}
	if len(spaces) == 0 {
		rep.Reason = "no spaces"
		return rep, nil
	}
	existing, err := i.existingSpaces(ctx, spaces)
	if err != nil {
		return rep, err
	}
	pending := 0
	for _, name := range spaces {
		if !existing[name] {
			pending++
		}
	}
	if pending == 0 {
		rep.Reason = "already imported"
		return rep, nil
	}
	rep.Ran = true
	if cfg.DryRun {
		// Dry run parses every source but writes nothing: each space's
		// passport and pin files are validated for shape, and failures
		// land in the report exactly where a real run would hit them.
		for _, name := range spaces {
			if existing[name] {
				continue
			}
			if _, _, err := readRevisionDocs(cfg.SpacesDir, name); err != nil && !errors.Is(err, errNoRevision) {
				rep.Failed = append(rep.Failed, Failure{Space: name, Stage: "passport", Error: err.Error()})
				continue
			}
			if _, err := readPins(cfg.SpacesDir, name); err != nil {
				rep.Failed = append(rep.Failed, Failure{Space: name, Stage: "image-pins", Error: err.Error()})
				continue
			}
			rep.Spaces = append(rep.Spaces, name)
		}
		return rep, nil
	}

	// Generation baselines keyed by the v0 principal: only pairs whose
	// principal maps to an imported space land; the rest are counted.
	genByPair := map[string]map[string]int64{}
	for _, item := range manager.Items {
		n, ok := generationNumber(item.Runtime.Generation)
		if !ok || item.Runtime.PrincipalID == "" || item.Runtime.ContextID == "" {
			continue
		}
		pair := genByPair[item.Runtime.PrincipalID]
		if pair == nil {
			pair = map[string]int64{}
			genByPair[item.Runtime.PrincipalID] = pair
		}
		if n > pair[item.Runtime.ContextID] {
			pair[item.Runtime.ContextID] = n
		}
	}

	for _, name := range spaces {
		if existing[name] {
			continue
		}
		if err := i.importSpace(ctx, cfg.SpacesDir, name, backups, genByPair[name], &rep); err != nil {
			rep.Failed = append(rep.Failed, Failure{Space: name, Stage: failureStage(err), Error: err.Error()})
			continue
		}
		rep.Spaces = append(rep.Spaces, name)
	}
	for principal := range genByPair {
		if !containsString(spaces, principal) {
			rep.UnmatchedPrincipals++
		}
	}
	sort.Strings(rep.Spaces)
	return rep, nil
}

// stageError marks which import stage failed for the report.
type stageError struct {
	stage string
	err   error
}

func (s stageError) Error() string { return s.err.Error() }
func (s stageError) Unwrap() error { return s.err }

func failureStage(err error) string {
	var se stageError
	if errors.As(err, &se) {
		return se.stage
	}
	return "write"
}

// discoverSpaces lists the v0 space dirs: subdirectories whose name is a
// valid id. A dir without a valid name is not a space — it is skipped,
// never probed deeper.
func discoverSpaces(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("spaces root: %w", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || !auth.ValidID(name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// existingSpaces answers which candidate ids have already landed their
// effective digest — the import-once gate. A row without a digest (a
// crash between CreateSpace and Activate, or a space that never
// published) stays pending: the import path is idempotent per stage, so
// the resume retries exactly the missing stages.
func (i *Importer) existingSpaces(ctx context.Context, names []string) (map[string]bool, error) {
	out := map[string]bool{}
	err := i.Store.ReadTx(ctx, func(tx *store.Tx) error {
		for _, name := range names {
			var digest string
			err := tx.Scoped(store.Tenant{Org: localOrg, Owner: name}).QueryRow(
				"SELECT effective_digest FROM spaces WHERE {tenant} AND id = ?", name).Scan(&digest)
			switch {
			case err == nil:
				out[name] = digest != ""
			case errors.Is(err, store.ErrNotFound):
			default:
				return err
			}
		}
		return nil
	})
	return out, err
}

// errNoRevision marks a space that never published — it still gets its
// spaces row and auxiliaries, just no revision.
var errNoRevision = errors.New("no current revision")

// readRevisionDocs loads the space's current passport revision as the
// user-layer document set: envelope keys and the compiler-derived
// workspace.tree are stripped; every other leaf round-trips through the
// layer vocabulary it was emitted under.
func readRevisionDocs(spacesDir, space string) (docs map[string]map[string]any, environment string, err error) {
	genDir := filepath.Join(spacesDir, space, "generated")
	pointer, err := os.ReadFile(filepath.Join(genDir, "passports", "current"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", errNoRevision
	}
	if err != nil {
		return nil, "", err
	}
	reference := strings.TrimSpace(string(pointer))
	revision := reference
	if strings.HasPrefix(reference, "sha256:") {
		b, err := os.ReadFile(filepath.Join(genDir, "passports", ".policy", strings.TrimPrefix(reference, "sha256:")))
		if err != nil {
			return nil, "", fmt.Errorf("current policy cache: %w", err)
		}
		revision = strings.TrimSpace(string(b))
	}
	if revision == "pending" || revision == "" || !filepath.IsLocal(revision) {
		return nil, "", fmt.Errorf("current names unusable revision %q", revision)
	}
	docs = map[string]map[string]any{}
	environment = "dev"
	for _, name := range passport.Names {
		b, err := os.ReadFile(filepath.Join(genDir, "passports", revision, name+".yaml"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		var full map[string]any
		if err := yaml.Unmarshal(b, &full); err != nil {
			return nil, "", fmt.Errorf("%s.yaml: %w", name, err)
		}
		body := map[string]any{}
		for k, v := range full {
			if envelopeKeys[k] {
				if name == "stack" && k == "metadata" {
					if m, ok := v.(map[string]any); ok {
						if env, ok := m["environment"].(string); ok && env != "" {
							environment = env
						}
					}
				}
				continue
			}
			// workspace.tree is derived from layout — a layer that
			// re-declared it would read as authored content.
			if name == "workspace" && k == "tree" {
				continue
			}
			if name == "tools" && k == "core" {
				// tools.core.platform is the pinned platform family the
				// compiler injects — never authored content, never a
				// catalog toolset a layer may write.
				if core, ok := v.(map[string]any); ok {
					core = maps.Clone(core)
					delete(core, "platform")
					body[k] = core
					continue
				}
			}
			body[k] = v
		}
		docs[name] = body
	}
	return docs, environment, nil
}

// readPins parses the space's image-pins.json; a missing file is normal.
func readPins(spacesDir, space string) (v0RolloutPins, error) {
	var pins v0RolloutPins
	err := readJSON(filepath.Join(spacesDir, space, "generated", "image-pins.json"), &pins)
	if errors.Is(err, os.ErrNotExist) {
		return pins, nil
	}
	return pins, err
}

// importSpace moves one v0 space: the spaces row, its current revision
// through Submit/Validate/Activate, then its auxiliaries (pins, backup,
// generation baselines) in one transaction.
func (i *Importer) importSpace(ctx context.Context, spacesDir, name string, backups []v0Backup, gens map[string]int64, rep *Report) error {
	tenant := store.Tenant{Org: localOrg, Owner: name}
	docs, environment, err := readRevisionDocs(spacesDir, name)
	if err != nil && !errors.Is(err, errNoRevision) {
		return stageError{"passport", err}
	}
	if environment != "dev" && environment != "prod" {
		return stageError{"passport", fmt.Errorf("unsupported environment %q", environment)}
	}
	if _, err := i.Revisions.CreateSpace(ctx, tenant, name, environment); err != nil {
		return stageError{"space", err}
	}
	// Auxiliaries land before the revision chain: every write is idempotent,
	// so a crash mid-chain resumes at the revision stage with the aux rows
	// already in place instead of stranding them behind the digest gate.
	pins, err := readPins(spacesDir, name)
	if err != nil {
		return stageError{"image-pins", err}
	}
	now := i.now().Unix()
	err = i.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(tenant)
		for env, e := range pins.Envs {
			for _, pin := range []struct {
				slot, image string
				at          time.Time
			}{
				{"current", e.Current.Image, e.Current.SmokedAt},
			} {
				if pin.image == "" {
					continue
				}
				if err := insertPin(q, env+":"+pin.slot, pin.image, pin.at.Unix()); err != nil {
					return err
				}
				rep.ImagePins++
			}
			if e.Previous != nil && e.Previous.Image != "" {
				if err := insertPin(q, env+":previous", e.Previous.Image, e.Previous.SmokedAt.Unix()); err != nil {
					return err
				}
				rep.ImagePins++
			}
			if e.Canary != nil && e.Canary.Image != "" {
				if err := insertPin(q, env+":canary:"+e.Canary.ContextID, e.Canary.Image, e.Canary.SetAt.Unix()); err != nil {
					return err
				}
				rep.ImagePins++
			}
		}
		for _, b := range backups {
			if b.User != name {
				continue
			}
			var have int
			if err := q.QueryRow("SELECT count(*) FROM backups WHERE {tenant} AND id = ?", name).Scan(&have); err != nil {
				return err
			}
			if have > 0 {
				break
			}
			if err := q.Insert("backups", []string{"id", "last_success_at", "last_error", "last_archive", "retained", "last_drill_at", "drill_result", "drill_checked"},
				name, b.LastSuccessAt.Unix(), b.LastError, b.LastArchive, b.Retained, b.LastDrillAt.Unix(), b.DrillResult, b.DrillChecked); err != nil {
				return err
			}
			rep.Backups++
			break
		}
		// One synthetic absent row per (space, agent) keeps the imported
		// generation floor: the next claim continues past every gen-N the
		// v0 supervisor issued, and the row itself claims nothing.
		for agent, gen := range gens {
			if !auth.ValidID(agent) {
				continue
			}
			id := baselineRowID(tenant, name, agent, gen)
			var have int
			if err := q.QueryRow("SELECT count(*) FROM runtimes WHERE {tenant} AND id = ?", id).Scan(&have); err != nil {
				return err
			}
			if have > 0 {
				continue
			}
			if err := q.Insert("runtimes", []string{"id", "space_id", "agent", "generation", "state", "deadline", "policy_digest", "image_digest", "backend_id"},
				id, name, agent, gen, "absent", nil, "", "", ""); err != nil {
				return err
			}
			if err := q.Insert("runtimes_history", []string{"id", "seq", "at", "from_state", "event", "to_state"},
				id, 1, now, "", "import", "absent"); err != nil {
				return err
			}
			rep.Generations++
		}
		return nil
	})
	if err != nil {
		return stageError{"write", err}
	}
	if docs != nil {
		rev, err := i.Revisions.Submit(ctx, tenant, name, "user", docs)
		if err != nil {
			return stageError{"revision-submit", err}
		}
		if rev.State != "active" {
			if _, err := i.Revisions.Validate(ctx, tenant, name, rev.ID); err != nil {
				return stageError{"revision-validate", err}
			}
			if _, err := i.Revisions.Activate(ctx, tenant, name, []string{rev.ID}); err != nil {
				return stageError{"revision-activate", err}
			}
		}
		rep.Revisions++
	}
	return nil
}

// insertPin writes one image-pin row unless it already exists — pins are
// content-addressed by their env/slot id so a re-run is a no-op.
func insertPin(q store.Scoped, id, image string, at int64) error {
	var have int
	if err := q.QueryRow("SELECT count(*) FROM image_pins WHERE {tenant} AND id = ?", id).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return nil
	}
	return q.Insert("image_pins", []string{"id", "digest", "created_at"}, id, image, at)
}

// baselineRowID is content-derived like the claim path's runtimeRowID:
// a retried import names the same row.
func baselineRowID(t store.Tenant, space, agent string, gen int64) string {
	sum := sha256.Sum256([]byte(t.Org + "\x00" + t.Owner + "\x00" + space + "\x00" + agent + "\x00" + "import" + "\x00" + fmt.Sprint(gen)))
	return "rt-" + hex.EncodeToString(sum[:])[:16]
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
