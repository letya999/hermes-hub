package passport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Publish stages one revision under generated/passports/<id>/ and flips the
// single `current` pointer atomically. Every file lands read-only — the
// passports are generated authority, not editable config. Any failure before
// the pointer write leaves the previously published revision active.
func (r Revision) Publish(spaceDir string) error {
	if len(r.Docs) != len(Names) {
		return fmt.Errorf("passport publish: revision carries %d passports, want %d", len(r.Docs), len(Names))
	}
	for _, name := range Names {
		if len(r.Docs[name]) == 0 {
			return fmt.Errorf("passport publish: revision is missing %s", name)
		}
	}
	root := filepath.Join(spaceDir, "generated", "passports")
	staging := filepath.Join(root, ".staging-"+r.ID)
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, name := range Names {
		path := filepath.Join(staging, name+".yaml")
		if err := os.WriteFile(path, r.Docs[name], 0400); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(staging, "prompt-pointer.txt"), []byte(r.PromptPointer()), 0400); err != nil {
		return err
	}
	// Provenance (source layers, templates, catalog pins, timestamps) is
	// revision authority too — it lives beside the passports, never in them.
	prov, err := yaml.Marshal(r.Provenance)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, "provenance.yaml"), prov, 0400); err != nil {
		return err
	}
	dest := filepath.Join(root, r.ID)
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(staging, dest); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// The pointer flip is the commit point: the previous revision stays
	// active until this write lands.
	if err := atomic(filepath.Join(root, "current"), []byte(r.ID+"\n")); err != nil {
		return err
	}
	return r.refreshConfigView(spaceDir)
}

// atomic writes body to path via a same-directory temp file + rename.
func atomic(path string, body []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".publish-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
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
	return atomic(filepath.Join(spaceDir, "generated", "passports", "current"), []byte(revisionID+"\n"))
}

// refreshConfigView republishes the runtime /config directory: the six
// passports plus prompt-facing files, each replaced atomically inside a
// stable directory inode (S1.4 - Docker Desktop single-file binds are not a
// refresh mechanism, and the directory bind must never see a half-swapped
// file). Files absent from the new revision are removed individually.
func (r Revision) refreshConfigView(spaceDir string) error {
	viewDir := filepath.Join(spaceDir, "generated", "config-view", r.Provenance.Environment)
	if err := os.MkdirAll(viewDir, 0755); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, name := range Names {
		want[name+".yaml"] = true
		if err := atomic(filepath.Join(viewDir, name+".yaml"), r.Docs[name]); err != nil {
			return err
		}
	}
	want["prompt-pointer.txt"] = true
	if err := atomic(filepath.Join(viewDir, "prompt-pointer.txt"), []byte(r.PromptPointer())); err != nil {
		return err
	}
	// The persona file is authored per space, not generated per revision -
	// the view copies the current one so /config shows what the agent sees.
	if soul, err := os.ReadFile(filepath.Join(spaceDir, "SOUL.md")); err == nil { // #nosec G304 -- operator-owned space file
		want["SOUL.md"] = true
		if err := atomic(filepath.Join(viewDir, "SOUL.md"), soul); err != nil {
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
