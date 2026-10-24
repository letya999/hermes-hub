package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgeFolderByteLimitIsPreflighted(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "notes")
	os.Mkdir(folder, 0700)
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("%d.md", i)), make([]byte, (8<<20)+1), 0600); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "safe.md"), []byte("safe"), 0600)
	spec := filepath.Join(root, "apply.yaml")
	os.WriteFile(spec, []byte("knowledge:\n  - name: safe.md\n    source: safe.md\n  - name: docs\n    context: project\n    state: present\n    source: notes\n"), 0600)
	state := filepath.Join(root, "state")
	if _, err := applyFile(filepath.Join(root, "spool"), state, "alice", spec); err == nil {
		t.Error("oversized folder accepted")
	}
	if _, err := os.Stat(filepath.Join(state, "knowledge", "alice", "safe.md")); !os.IsNotExist(err) {
		t.Error("oversized folder partially applied preceding entry")
	}
}
