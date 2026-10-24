package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyKnowledgeScopedFolderLifecycle(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "notes")
	if err := os.MkdirAll(filepath.Join(folder, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "sub", "guide.md"), []byte("version-one"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "apply.yaml")
	state := filepath.Join(root, "state")
	apply := func(status string) {
		t.Helper()
		body := fmt.Sprintf("knowledge:\n  - name: docs\n    context: project\n    state: %s\n", status)
		if status == "present" {
			body += "    source: notes\n"
		}
		if err := os.WriteFile(cfg, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := applyFile(filepath.Join(root, "spool"), state, "alice", cfg); err != nil {
			t.Fatal(err)
		}
	}
	inspect := func(want string) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(state, "knowledge", "alice", "project", ".bindings.json"))
		if err != nil {
			t.Fatal(err)
		}
		var bindings map[string]struct {
			State string
			Files map[string][]byte
		}
		if err := json.Unmarshal(body, &bindings); err != nil {
			t.Fatal(err)
		}
		b := bindings["docs"]
		if b.State != want {
			t.Fatalf("state %q want %q", b.State, want)
		}
		if want == "present" && string(b.Files["sub/guide.md"]) != "version-one" {
			t.Fatal("folder content not staged")
		}
		if want == "absent" && len(b.Files) != 0 {
			t.Fatal("revoked derived content retained")
		}
	}
	apply("present")
	inspect("present")
	apply("stale")
	inspect("stale")
	apply("unavailable")
	inspect("unavailable")
	apply("absent")
	inspect("absent")
}
