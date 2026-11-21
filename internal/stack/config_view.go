package stack

import (
	"os"
	"path/filepath"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

// ConfigViewDir is the runtime /config view target: one directory per
// environment the render materializes from the effective bundle. The v0
// publish mirror owned this path before S7d; now it is a render output the
// compose /config bind and the spawn-time claim materializer both fill.
func ConfigViewDir(spaceDir, environment string) string {
	return filepath.Join(spaceDir, "generated", "config-view", environment)
}

// MaterializeConfigView republishes the runtime /config directory from the
// effective bundle: the six passports plus prompt-facing files, each
// replaced atomically inside a stable directory inode (S1.4 - Docker
// Desktop single-file binds are not a refresh mechanism, and the directory
// bind must never see a half-swapped file). Files absent from the new
// revision are removed individually. Moved from passport.refreshConfigView
// in S7d - the view is materialization, not publish authority.
func MaterializeConfigView(spaceDir string, s Settings, eff spawnv1.Effective) error {
	viewDir := ConfigViewDir(spaceDir, s.Environment)
	if err := os.MkdirAll(viewDir, 0755); err != nil {
		return err
	}
	docs, err := spawnv1.EncodeDocuments(eff)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, name := range spawnv1.Names {
		want[name+".yaml"] = true
		if err := atomicMode(filepath.Join(viewDir, name+".yaml"), docs[name], 0640); err != nil {
			return err
		}
	}
	want["prompt-pointer.txt"] = true
	if err := atomicMode(filepath.Join(viewDir, "prompt-pointer.txt"), []byte(eff.PromptPointer()), 0640); err != nil {
		return err
	}
	want["revision"] = true
	if err := atomicMode(filepath.Join(viewDir, "revision"), []byte(eff.Header.Metadata.Revision+"\n"), 0640); err != nil {
		return err
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
	// they are not passport documents - render rewrites their contents.
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
