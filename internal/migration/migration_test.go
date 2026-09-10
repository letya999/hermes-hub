package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDryRunAndApplyKeepSourcesAndMoveRuntimeState(t *testing.T) {
	root := t.TempDir()
	legacyOrg := filepath.Join(root, "organizations", "acme")
	legacyUser := filepath.Join(root, "legacy-user")
	state := filepath.Join(root, "state")
	workspace := filepath.Join(root, "workspace")
	for _, path := range []string{legacyOrg, legacyUser, filepath.Join(state, "hermes", "sessions"), filepath.Join(state, "browser"), workspace} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacyOrg, "settings.yaml"), []byte("schema: 1\norganization: acme\nmembers:\n  alice: owner\nfeatures: [workspace]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyUser, "settings.yaml"), []byte("schema: 1\nuser: alice\ntimezone: UTC\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "hermes", "sessions", "one.json"), []byte("session"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "draft.md"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Root: root, User: "alice", Organization: "acme", UserSource: legacyUser, OrganizationSource: legacyOrg, StateSource: state, WorkspaceSource: workspace}
	report, err := Run(opts)
	if err != nil || report.Mode != "dry-run" {
		t.Fatal(report, err)
	}
	if _, err := os.Stat(filepath.Join(root, "spaces")); !os.IsNotExist(err) {
		t.Fatal("dry-run mutated destination")
	}
	if strings.Contains(string(mustJSON(t, report)), "session") {
		t.Fatal("report leaked session content")
	}
	report, err = Run(Options{Root: root, User: "alice", Organization: "acme", UserSource: legacyUser, OrganizationSource: legacyOrg, StateSource: state, WorkspaceSource: workspace, Apply: true})
	if err != nil || report.Mode != "applied" {
		t.Fatal(report, err)
	}
	if _, err := Run(Options{Root: root, User: "alice", Organization: "acme", UserSource: legacyUser, OrganizationSource: legacyOrg, StateSource: state, WorkspaceSource: workspace, Apply: true}); err != nil {
		t.Fatal("retry failed:", err)
	}
	for _, path := range []string{legacyOrg, legacyUser, state, workspace, filepath.Join(root, "spaces", "alice", "workspace", "draft.md"), filepath.Join(root, "spaces", "alice", "hermes", "sessions", "one.json"), filepath.Join(root, "spaces", "acme", "scope.yaml")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(path, err)
		}
	}
}

func TestMigrationInputAndDestinationValidation(t *testing.T) {
	root := t.TempDir()
	for _, opts := range []Options{{Root: root}, {Root: root, User: "../bad"}, {Root: root, User: "a", Organization: "a"}, {Root: root, User: "a", Environment: "test"}} {
		if _, err := Run(opts); err == nil {
			t.Fatalf("invalid migration accepted: %+v", opts)
		}
	}
	state := filepath.Join(root, "state")
	if err := os.WriteFile(state, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{Root: root, User: "alice", StateSource: state}); err == nil {
		t.Fatal("file state source accepted")
	}
	user := filepath.Join(root, "spaces", "alice")
	if err := os.MkdirAll(user, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(user, "scope.yaml"), []byte("kind: organization\nid: alice\norganization: alice\nschema: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{Root: root, User: "alice"}); err == nil {
		t.Fatal("kind collision accepted")
	}
}

func TestMigrationRejectsSymlinkActiveAndUnrelatedDestination(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(legacy, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Run(Options{Root: root, User: "alice", UserSource: legacy}); err == nil {
		t.Fatal("symlink migration accepted")
	}
	_ = os.Remove(filepath.Join(legacy, "link"))
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), []byte(`{"pids":[999999]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{Root: root, User: "alice", StateSource: state}); err == nil {
		t.Fatal("active runtime migration accepted")
	}
	destination := filepath.Join(root, "spaces", "alice")
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "unrelated.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(state, "runtime.json"))
	if _, err := Run(Options{Root: root, User: "alice", UserSource: legacy}); err == nil {
		t.Fatal("unrelated destination accepted")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
