package spawn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/selfsettings"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/locale"
	"github.com/letya999/hermes-hub/shared/store"
	"gopkg.in/yaml.v3"
)

// SettingsResult is the self-settings read: the reviewed config leaves of
// the space's active spec, keyed by tool-facing names. Secrets never
// appear — the allowlist only covers reviewed non-secret leaves.
type SettingsResult struct {
	PublishedRevision    string
	EffectiveRevision    string
	PublishedValues      map[string]any
	ManageableKeys       []string
	SecretValuesIncluded bool
}

// SettingsEditResult is one allowlisted edit's outcome: the new effective
// digest (or the unchanged base) and whether anything changed.
type SettingsEditResult struct {
	PublishedRevision string
	PreviousRevision  string
	Changed           bool
}

// GetSettings reads the reviewed self-settings leaves out of the space's
// stored effective spec — the digest fields carry the effective digest
// (the tool's published_revision keeps its name, its value is the digest).
func (r *Revisions) GetSettings(ctx context.Context, t store.Tenant, spaceID, key string) (SettingsResult, error) {
	var out SettingsResult
	if key != "" {
		if _, ok := selfsettings.PublishedPath(key); !ok {
			return out, httpapi.Errorf(httpapi.ErrInvalid, "%s", locale.T(locale.Default, "spawn.settings-key"))
		}
	}
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		if _, err := spaceRow(q, spaceID); err != nil {
			return err
		}
		var digest string
		err := q.QueryRow("SELECT COALESCE(effective_digest, '') FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&digest)
		if err != nil {
			return err
		}
		if digest == "" {
			return store.ErrNotFound
		}
		config, err := effectiveConfigDoc(q, spaceID, digest)
		if err != nil {
			return err
		}
		values := map[string]any{}
		for _, candidate := range selfsettings.AllowedKeys() {
			if key != "" && key != candidate {
				continue
			}
			path, _ := selfsettings.PublishedPath(candidate)
			if value, present := leafAt(config, path); present {
				values[candidate] = value
			}
		}
		out = SettingsResult{
			PublishedRevision: digest,
			EffectiveRevision: digest,
			PublishedValues:   values,
			ManageableKeys:    selfsettings.AllowedKeys(),
		}
		return nil
	})
	return out, err
}

// EditSettings applies one reviewed config leaf change: the authored
// config.yaml on disk is patched (the next `space apply` must not revert
// it — Decision 13), the patched doc is submitted as a new user-layer
// revision, the config-only rule is enforced against the compiled diff,
// and the set activates on the base-digest CAS. Non-config changes,
// unpublished authored drift and stale bases all refuse before any write.
func (r *Revisions) EditSettings(ctx context.Context, t store.Tenant, spaceID, key string, value *string, baseDigest string) (SettingsEditResult, error) {
	var out SettingsEditResult
	path, ok := selfsettings.PublishedPath(key)
	if !ok || baseDigest == "" {
		return out, httpapi.Errorf(httpapi.ErrInvalid, "%s", locale.T(locale.Default, "spawn.settings-key"))
	}
	var canonical any
	if value != nil && strings.TrimSpace(*value) != "" {
		var err error
		canonical, err = selfsettings.ValidatePath(key, *value)
		if err != nil {
			return out, httpapi.Errorf(httpapi.ErrInvalid, "%s", locale.T(locale.Default, "spawn.settings-value"))
		}
	}
	if r.SpacesRoot == "" {
		return out, httpapi.Errorf(httpapi.ErrUnavailable, "%s", locale.T(locale.Default, "spawn.settings-unavailable"))
	}
	dir := filepath.Join(r.SpacesRoot, spaceID)
	var before, after []byte
	var edited map[string]any
	var candidateDocs map[string]map[string]any
	var env string
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var err error
		env, err = spaceEnvironment(q, spaceID)
		if err != nil {
			return err
		}
		var digest string
		if err := q.QueryRow("SELECT COALESCE(effective_digest, '') FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&digest); err != nil {
			return err
		}
		if digest != baseDigest {
			return fmt.Errorf("%w: %s", store.ErrConflict, locale.T(locale.Default, "spawn.settings-stale-base"))
		}
		grants, err := effectiveGrantsDoc(q, spaceID, digest)
		if err != nil {
			return err
		}
		if !slices.Contains(grantsAllow(grants, "config"), "edit_own") {
			return httpapi.Errorf(httpapi.ErrForbidden, "%s", locale.T(locale.Default, "spawn.settings-denied"))
		}
		userDocs, err := activeLayerDocSet(q, spaceID, "user")
		if err != nil {
			return err
		}
		// Unpublished authored drift refuses the edit — same rule as v0's
		// baseline compile: the files are the layer's source of truth and
		// a divergent file would publish something else on the next apply.
		before, err = authoredBaseline(dir, userDocs)
		if err != nil {
			return err
		}
		after, edited, err = patchAuthoredConfig(before, path, canonical)
		if err != nil || len(after) > 1<<20 {
			return httpapi.Errorf(httpapi.ErrInvalid, "%s", locale.T(locale.Default, "spawn.settings-key"))
		}
		candidateDocs = map[string]map[string]any{}
		for name, doc := range userDocs {
			candidateDocs[name] = doc
		}
		candidateDocs["config"] = edited
		return nil
	})
	if err != nil {
		return out, err
	}

	// Compile the would-be set and enforce the config-only rule before
	// anything writes: the diff against the stored spec may only move
	// config.* leaves.
	var candidate passport.Revision
	var digest string
	err = r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		byLayer, atByLayer, err := activeLayerDocs(q, spaceID)
		if err != nil {
			return err
		}
		byLayer["user"] = candidateDocs
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
		var dig string
		var compileErr error
		candidate, _, _, dig, compileErr = compilePolicy(spaceID, env, byLayer, cat, time.Unix(latest, 0), reserved)
		if compileErr != nil {
			return fmt.Errorf("%w: %s: %v", store.ErrConflict, locale.T(locale.Default, "spawn.revision-rejected"), compileErr)
		}
		digest = dig
		return nil
	})
	if err != nil {
		return out, err
	}
	prevEff, err := effectiveOf(ctx, r, t, spaceID, baseDigest)
	if err != nil {
		return out, err
	}
	nextEff, err := candidate.Effective()
	if err != nil {
		return out, err
	}
	for _, change := range passport.Diff(prevEff, nextEff) {
		if !strings.HasPrefix(change.Path, "config.") {
			return out, httpapi.Errorf(httpapi.ErrConflict, "%s", locale.T(locale.Default, "spawn.settings-non-config"))
		}
	}
	out = SettingsEditResult{PublishedRevision: baseDigest, PreviousRevision: baseDigest}
	if digest == baseDigest {
		return out, nil
	}

	// The authored file moves first; a refused activation restores it so a
	// rejected edit never leaves unpublished content in the file (the next
	// apply would publish it).
	if err := writeAuthoredFile(dir, after); err != nil {
		return out, err
	}
	rev, err := r.Submit(ctx, t, spaceID, "user", candidateDocs)
	if err == nil && (rev.State == "draft" || rev.State == "validated") {
		rev, err = r.Validate(ctx, t, spaceID, rev.ID)
	}
	if err == nil {
		_, err = r.Activate(ctx, t, spaceID, []string{rev.ID}, &baseDigest)
	}
	if err != nil {
		// Best-effort restore: the edit did not commit, so the file keeps
		// the published content.
		_ = writeAuthoredFile(dir, before)
		return out, err
	}
	// The tool's published_revision is the ACTIVATED digest — the candidate
	// compile's timestamp differs from the committed revision's, so read the
	// pointer activation wrote rather than reporting the candidate digest.
	eff, err := r.Effective(ctx, t, spaceID)
	if err != nil {
		return out, err
	}
	out.PublishedRevision, out.Changed = eff.Digest, eff.Digest != baseDigest
	return out, nil
}

// authoredBaseline reads the space's authored passport files and refuses
// the edit when they diverge from the stored user layer — the settings
// path edits authored config, and an unpublished drift would hide inside
// the same commit. Returns config.yaml's raw bytes for the patch step.
func authoredBaseline(dir string, userDocs map[string]map[string]any) ([]byte, error) {
	var config []byte
	for _, name := range passport.Names {
		path := filepath.Join(dir, "passports", name+".yaml")
		body, err := os.ReadFile(path) // #nosec G703 -- fixed passport filename under the operator's spaces root
		if errors.Is(err, os.ErrNotExist) {
			if _, have := userDocs[name]; have {
				return nil, httpapi.Errorf(httpapi.ErrConflict, "%s", locale.T(locale.Default, "spawn.settings-drift"))
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		stored, have := userDocs[name]
		if !have {
			return nil, httpapi.Errorf(httpapi.ErrConflict, "%s", locale.T(locale.Default, "spawn.settings-drift"))
		}
		var doc map[string]any
		if err := yaml.Unmarshal(body, &doc); err != nil {
			return nil, httpapi.Errorf(httpapi.ErrConflict, "%s", locale.T(locale.Default, "spawn.settings-drift"))
		}
		storedJSON, _ := json.Marshal(stored)
		fileJSON, _ := json.Marshal(doc)
		if string(storedJSON) != string(fileJSON) {
			return nil, httpapi.Errorf(httpapi.ErrConflict, "%s", locale.T(locale.Default, "spawn.settings-drift"))
		}
		if name == "config" {
			config = body
		}
	}
	if config == nil {
		return nil, httpapi.Errorf(httpapi.ErrConflict, "%s", locale.T(locale.Default, "spawn.settings-drift"))
	}
	return config, nil
}

// writeAuthoredFile atomically replaces config.yaml — temp file, fsync,
// rename, same permission bits the file carried.
func writeAuthoredFile(dir string, next []byte) error {
	path := filepath.Join(dir, "passports", "config.yaml")
	info, err := os.Lstat(path) // #nosec G703 -- fixed authored config path in an operator-owned space
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("authored config is unavailable or not a regular file")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-publish-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(next); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path) // #nosec G703 -- temp and destination share one validated passport directory
}

// patchAuthoredConfig edits one nested config leaf in the authored
// config.yaml, preserving the file's comments and layout — the tool's
// change lands where the operator reads it. Moved verbatim from the
// deleted hubctl settings path.
func patchAuthoredConfig(before []byte, path string, value any) ([]byte, map[string]any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(before, &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("invalid authored config")
	}
	parts := strings.Split(path, ".")
	node := doc.Content[0]
	find := func(section *yaml.Node, name string) int {
		for i := 0; i+1 < len(section.Content); i += 2 {
			if section.Content[i].Value == name {
				return i
			}
		}
		return -1
	}
	for _, part := range parts[:len(parts)-1] {
		i := find(node, part)
		if i < 0 {
			if value == nil {
				var unchanged map[string]any
				if err := yaml.Unmarshal(before, &unchanged); err != nil {
					return nil, nil, err
				}
				return before, unchanged, nil
			}
			child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, child)
			node = child
			continue
		}
		if node.Content[i+1].Kind != yaml.MappingNode {
			return nil, nil, errors.New("authored config setting parent is not a map")
		}
		node = node.Content[i+1]
	}
	i := find(node, parts[len(parts)-1])
	if value == nil {
		if i >= 0 {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
		}
	} else {
		leaf := &yaml.Node{}
		if err := leaf.Encode(value); err != nil {
			return nil, nil, err
		}
		if i >= 0 {
			leaf.HeadComment = node.Content[i+1].HeadComment
			leaf.LineComment = node.Content[i+1].LineComment
			leaf.FootComment = node.Content[i+1].FootComment
			node.Content[i+1] = leaf
		} else {
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: parts[len(parts)-1]}, leaf)
		}
	}
	after, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, nil, err
	}
	var edited map[string]any
	if err := yaml.Unmarshal(after, &edited); err != nil {
		return nil, nil, err
	}
	return after, edited, nil
}

// effectiveConfigDoc reads the config passport of a stored spec; the
// apiVersion/kind/metadata envelope is stripped so leaf lookups hit the
// document body directly.
func effectiveConfigDoc(q store.Scoped, spaceID, digest string) (map[string]any, error) {
	return effectiveDoc(q, spaceID, digest, "config")
}

// effectiveGrantsDoc is the same read for the grants passport — the
// published allow the settings edit checks config.edit_own against.
func effectiveGrantsDoc(q store.Scoped, spaceID, digest string) (map[string]any, error) {
	return effectiveDoc(q, spaceID, digest, "grants")
}

func effectiveDoc(q store.Scoped, spaceID, digest, name string) (map[string]any, error) {
	var stored string
	err := q.QueryRow("SELECT documents FROM effective_specs WHERE {tenant} AND space_id = ? AND id = ?", spaceID, digest).Scan(&stored)
	if err != nil {
		return nil, err
	}
	docs, err := bundlePassports([]byte(stored))
	if err != nil {
		return nil, err
	}
	raw, ok := docs[name]
	if !ok {
		return map[string]any{}, nil
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, err
	}
	body := map[string]any{}
	for k, v := range full {
		if k == "apiVersion" || k == "kind" || k == "metadata" {
			continue
		}
		body[k] = v
	}
	return body, nil
}

// grantsAllow extracts one right family's granted verbs from the
// effective grants doc — {allow: {config: [edit_own, ...]}}.
func grantsAllow(grants map[string]any, family string) []string {
	allow, _ := grants["allow"].(map[string]any)
	raw, _ := allow[family].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// leafAt walks a dotted path through a decoded passport doc.
func leafAt(doc map[string]any, path string) (any, bool) {
	var value any = doc
	for _, part := range strings.Split(path, ".") {
		section, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = section[part]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

// activeLayerDocSet reads one active layer's stored docs.
func activeLayerDocSet(q store.Scoped, spaceID, layer string) (map[string]map[string]any, error) {
	var raw []byte
	err := q.QueryRow("SELECT documents FROM passport_revisions WHERE {tenant} AND space_id = ? AND layer = ? AND state = 'active'", spaceID, layer).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return decodeRevisionDocs(raw)
}
