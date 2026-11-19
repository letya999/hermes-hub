package passport

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Publish stages one immutable revision and activates it in a single pointer
// write. Apply uses Prepare and Activate separately to prepare other readers.
func (r Revision) Publish(spaceDir string) error {
	previous, err := Current(spaceDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := r.Prepare(spaceDir, nil); err != nil {
		return err
	}
	if err := r.Activate(spaceDir); err != nil {
		return errors.Join(err, RestoreCurrent(spaceDir, previous, r.Provenance.Environment))
	}
	_, _ = PruneRevisions(spaceDir, previous)
	return nil
}

// Prepare writes immutable revision files and refreshes the mounted view.
// While mutable view files are replaced, current is deliberately unreadable.
func (r Revision) Prepare(spaceDir string, after func(string)) error {
	if !filepath.IsLocal(r.ID) || r.ID == "pending" {
		return fmt.Errorf("passport publish: invalid revision id")
	}
	if len(r.Docs) != len(Names) {
		return fmt.Errorf("passport publish: revision carries %d passports, want %d", len(r.Docs), len(Names))
	}
	for _, name := range Names {
		if len(r.Docs[name]) == 0 {
			return fmt.Errorf("passport publish: revision is missing %s", name)
		}
	}
	previous, err := Current(spaceDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("passport publish: current revision: %w", err)
	}
	root := filepath.Join(spaceDir, "generated", "passports")
	// Publishers and the restricted supervisor share a controller group, not a UID.
	if err := os.MkdirAll(root, 0750); err != nil {
		return err
	}
	staging := filepath.Join(root, ".staging-"+r.ID)
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0750); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, name := range Names {
		path := filepath.Join(staging, name+".yaml")
		if err := os.WriteFile(path, r.Docs[name], 0440); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(staging, "prompt-pointer.txt"), []byte(r.PromptPointer()), 0440); err != nil {
		return err
	}
	// Provenance (source layers, templates, catalog pins, timestamps) is
	// revision authority too — it lives beside the passports, never in them.
	prov, err := yaml.Marshal(r.Provenance)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, "provenance.yaml"), prov, 0440); err != nil {
		return err
	}
	dest := filepath.Join(root, r.ID)
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(staging, dest); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		for _, name := range append(append([]string{}, Names...), "prompt-pointer", "provenance") {
			filename := name + ".yaml"
			if name == "prompt-pointer" {
				filename = "prompt-pointer.txt"
			}
			staged, err := os.ReadFile(filepath.Join(staging, filename))
			if err != nil {
				return err
			}
			published, err := os.ReadFile(filepath.Join(dest, filename))
			if err != nil || !bytes.Equal(staged, published) {
				return fmt.Errorf("passport publish: immutable revision %s differs at %s", r.ID, filename)
			}
		}
	}
	if after != nil {
		after("bundle")
	}
	if err := atomic(filepath.Join(root, "current"), []byte("pending\n")); err != nil {
		return err
	}
	if after != nil {
		after("pending")
	}
	if err := r.refreshConfigView(spaceDir, after); err != nil {
		return errors.Join(err, RestoreCurrent(spaceDir, previous, r.Provenance.Environment))
	}
	return nil
}

// Activate is the only commit point. The view and all other in-process
// authority must already be prepared for this exact revision.
func (r Revision) Activate(spaceDir string) error {
	root := filepath.Join(spaceDir, "generated", "passports")
	b, err := os.ReadFile(filepath.Join(root, "current"))
	if err != nil || strings.TrimSpace(string(b)) != "pending" {
		return fmt.Errorf("passport activate: revision is not prepared")
	}
	b, err = os.ReadFile(filepath.Join(spaceDir, "generated", "config-view", r.Provenance.Environment, "revision"))
	if err != nil || strings.TrimSpace(string(b)) != r.ID {
		return fmt.Errorf("passport activate: config view is not prepared")
	}
	return PointCurrent(spaceDir, r.ID)
}

// atomic writes body to path via a same-directory temp file + rename.
func atomic(path string, body []byte) error {
	return atomicMode(path, body, 0640)
}

func atomicMode(path string, body []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".publish-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// PromptPointer renders the short per-turn inventory line R9.1 injects: the
// revision id, the bounded core tool names and the extended count, plus the
// pinned platform pointer. It never embeds catalog bodies — tool_search keeps
// discovery lazy inside the turn.
func (r Revision) PromptPointer() string {
	core, _ := r.value("tools", "core").(map[string]any)
	extended, _ := r.value("tools", "extended").(map[string]any)
	names := make([]string, 0, len(core))
	for k := range core {
		if k != "platform" && k != "discovery" {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	return fmt.Sprintf("revision=%s core=%s extended=+%d platform=tool_search+tool_call\n",
		r.ID, strings.Join(names, ","), len(extended))
}

// PointCurrent repoints the active revision pointer — the rollback half of
// Publish's commit point, used when post-publish reconciliation fails.
func PointCurrent(spaceDir, revisionID string) error {
	path := filepath.Join(spaceDir, "generated", "passports", "current")
	if revisionID == "" {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	digest, err := RevisionPolicyDigest(spaceDir, revisionID)
	if err != nil {
		return err
	}
	if digest != "" {
		revisionID = digest
	}
	return atomic(path, []byte(revisionID+"\n"))
}

// RestoreCurrent puts the pointer and mounted config view back on the same
// revision after a post-publish failure. Generated revision files remain
// immutable, so the old view can be reconstructed without editable inputs.
func RestoreCurrent(spaceDir, revisionID, environment string) error {
	viewDir := filepath.Join(spaceDir, "generated", "config-view", environment)
	if err := atomic(filepath.Join(spaceDir, "generated", "passports", "current"), []byte("pending\n")); err != nil {
		return err
	}
	if revisionID == "" {
		for _, name := range append(append([]string{}, Names...), "prompt-pointer", "revision") {
			filename := name + ".yaml"
			if name == "prompt-pointer" {
				filename = "prompt-pointer.txt"
			} else if name == "revision" {
				filename = "revision"
			}
			if err := os.Remove(filepath.Join(viewDir, filename)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return PointCurrent(spaceDir, "")
	}
	if !filepath.IsLocal(revisionID) {
		return fmt.Errorf("passport restore: invalid revision id")
	}
	rev := Revision{ID: revisionID, Provenance: Provenance{Environment: environment}, Docs: map[string][]byte{}}
	for _, name := range Names {
		body, err := os.ReadFile(filepath.Join(spaceDir, "generated", "passports", revisionID, name+".yaml"))
		if err != nil {
			return err
		}
		rev.Docs[name] = body
	}
	if err := rev.refreshConfigView(spaceDir, nil); err != nil {
		return err
	}
	return PointCurrent(spaceDir, revisionID)
}

// refreshConfigView republishes the runtime /config directory: the six
// passports plus prompt-facing files, each replaced atomically inside a
// stable directory inode (S1.4 - Docker Desktop single-file binds are not a
// refresh mechanism, and the directory bind must never see a half-swapped
// file). Files absent from the new revision are removed individually.
func (r Revision) refreshConfigView(spaceDir string, after func(string)) error {
	viewDir := filepath.Join(spaceDir, "generated", "config-view", r.Provenance.Environment)
	if err := os.MkdirAll(viewDir, 0755); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, name := range Names {
		want[name+".yaml"] = true
		if err := atomicMode(filepath.Join(viewDir, name+".yaml"), r.Docs[name], 0640); err != nil {
			return err
		}
		if after != nil {
			after("view:" + name)
		}
	}
	want["prompt-pointer.txt"] = true
	if err := atomicMode(filepath.Join(viewDir, "prompt-pointer.txt"), []byte(r.PromptPointer()), 0640); err != nil {
		return err
	}
	want["revision"] = true
	if err := atomicMode(filepath.Join(viewDir, "revision"), []byte(r.ID+"\n"), 0640); err != nil {
		return err
	}
	if after != nil {
		after("view:revision")
	}
	// The persona file is authored per space, not generated per revision -
	// the view copies the current one so /config shows what the agent sees.
	if soul, err := os.ReadFile(filepath.Join(spaceDir, "SOUL.md")); err == nil { // #nosec G304 -- operator-owned space file
		want["SOUL.md"] = true
		if err := atomicMode(filepath.Join(viewDir, "SOUL.md"), soul, 0640); err != nil {
			return err
		}
	}
	// Executor contracts (tool policy snapshot, tool surface manifest)
	// materialize inside the view so the read-only /config bind covers them;
	// they are not passport documents — publish preserves them while
	// render/spawn rewrites their contents.
	want["tool-policy.json"] = true
	want["tool-manifest.json"] = true
	entries, err := os.ReadDir(viewDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || want[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(viewDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
