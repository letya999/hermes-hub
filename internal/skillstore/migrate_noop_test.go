package skillstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyBundledNoopDoesNotReadUnrelatedDrafts(t *testing.T) {
	space, platform := t.TempDir(), t.TempDir()
	for _, root := range []string{platform, filepath.Join(space, "hermes", "skills")} {
		dir := filepath.Join(root, "bundled")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte("---\nname: bundled\ndescription: shipped copy\n---\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	draft := filepath.Join(space, "skills", draftsDir, "invalid")
	if err := os.MkdirAll(draft, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySpaceWithPlatform(space, platform, true); err != nil {
		t.Fatalf("unrelated draft blocked a bundled-copy noop: %v", err)
	}
	// New candidates must still validate the draft inventory before migration.
	if err := os.WriteFile(filepath.Join(space, "hermes", "skills", "bundled", SkillFile), []byte("---\nname: bundled\ndescription: owner modification\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySpaceWithPlatform(space, platform, true); err == nil {
		t.Fatal("new migration ignored an invalid draft inventory")
	}
	if _, err := os.Stat(draft); err != nil {
		t.Fatal("unrelated draft was removed")
	}
}
