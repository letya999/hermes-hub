package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyKnowledgeRejectsUnsafeSnapshotBeforeWriting(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "private.md")
	os.WriteFile(outside, []byte("foreign-marker"), 0600)
	folder := filepath.Join(root, "notes")
	os.Mkdir(folder, 0700)
	t.Chdir(folder)
	if err := os.Symlink("../private.md", "leak.md"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg := filepath.Join(root, "apply.yaml")
	state := filepath.Join(root, "state")
	body := "knowledge:\n  - name: safe.md\n    source: private.md\n  - name: docs\n    context: project\n    state: present\n    source: notes\n"
	os.WriteFile(cfg, []byte(body), 0600)
	if _, err := applyFile(filepath.Join(root, "spool"), state, "alice", cfg); err == nil {
		t.Error("folder symlink accepted")
	}
	if _, err := os.Stat(filepath.Join(state, "knowledge", "alice", "safe.md")); !os.IsNotExist(err) {
		t.Error("invalid snapshot partially applied preceding entry")
	}
}
