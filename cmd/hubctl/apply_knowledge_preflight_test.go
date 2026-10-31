package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgeApplyValidatesAllSnapshotsBeforeWrites(t *testing.T) {
	for _, mode := range []string{"count", "context", "state"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			folder := filepath.Join(root, "notes")
			os.Mkdir(folder, 0700)
			count := 1
			if mode == "count" {
				count = 129
			}
			for i := 0; i < count; i++ {
				os.WriteFile(filepath.Join(folder, fmt.Sprintf("%d.md", i)), []byte("note"), 0600)
			}
			scope, status := "project", "present"
			if mode == "context" {
				scope = "../foreign"
			}
			if mode == "state" {
				status = "invalid"
			}
			spec := filepath.Join(root, "apply.yaml")
			os.WriteFile(filepath.Join(root, "safe.md"), []byte("safe"), 0600)
			body := fmt.Sprintf("knowledge:\n  - name: safe.md\n    source: safe.md\n  - name: docs\n    context: %s\n    state: %s\n    source: notes\n", scope, status)
			os.WriteFile(spec, []byte(body), 0600)
			state := filepath.Join(root, "state")
			if _, err := applyFile(filepath.Join(root, "spool"), state, "alice", spec, ""); err == nil {
				t.Error("invalid knowledge apply accepted")
			}
			if _, err := os.Stat(filepath.Join(state, "knowledge", "alice", "safe.md")); !os.IsNotExist(err) {
				t.Error("validation failure partially wrote preceding source")
			}
		})
	}
}
