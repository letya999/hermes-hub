package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSpoolMigrateKnowledge drives CHG-0126: knowledge staged under the
// legacy <spool>/knowledge tree is copied into <state>/knowledge, dry-run
// writes nothing, conflicts are reported not overwritten, and unsafe entries
// are skipped.
func TestSpoolMigrateKnowledge(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "spool")
	state := filepath.Join(t.TempDir(), "state")

	seed := func(rel, body string) {
		t.Helper()
		path := filepath.Join(spool, "knowledge", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	seed("alice/notes.md", "hello")
	seed("alice/org/ctx.md", "scoped")
	seed("bob/facts.md", "b")
	if err := os.Symlink("notes.md", filepath.Join(spool, "knowledge", "alice", "link.md")); err != nil {
		t.Fatal(err)
	}
	seed(".hidden/evil.md", "x")
	if err := os.WriteFile(filepath.Join(spool, "knowledge", "stray.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	// Pre-existing conflicting destination: same name, different body.
	dest := filepath.Join(state, "knowledge", "alice", "notes.md")
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("newer"), 0600); err != nil {
		t.Fatal(err)
	}

	dry, err := spoolMigrate(spool, state, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !dry.DryRun || len(dry.Principals) != 2 {
		t.Fatalf("dry run report: %+v", dry)
	}
	alice := dry.Principals[0]
	if alice.ID != "alice" {
		t.Fatalf("first principal: %+v", alice)
	}
	if len(alice.Copied) != 1 || len(alice.Conflicts) != 1 || len(alice.Skipped) != 1 {
		t.Fatalf("alice report: %+v", alice)
	}
	if _, err := os.Lstat(filepath.Join(state, "knowledge", "alice", "org", "ctx.md")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote to state")
	}

	live, err := spoolMigrate(spool, state, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, rel := range []string{"alice/org/ctx.md"} {
		got, err := os.ReadFile(filepath.Join(state, "knowledge", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("missing migrated %s: %v", rel, err)
		}
		if string(got) != "scoped" {
			t.Fatalf("migrated %s body: %q", rel, got)
		}
	}
	if got, _ := os.ReadFile(dest); string(got) != "newer" {
		t.Fatal("conflict destination overwritten")
	}
	if len(live.Principals[0].Conflicts) != 1 {
		t.Fatalf("conflicts after apply: %+v", live.Principals[0])
	}
	// Source kept for rollback.
	if _, err := os.Stat(filepath.Join(spool, "knowledge", "alice", "org", "ctx.md")); err != nil {
		t.Fatal("source removed; rollback path lost")
	}
	// Idempotent re-run: everything already staged reports unchanged/conflict.
	again, err := spoolMigrate(spool, state, true)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if len(again.Principals[0].Copied) != 0 {
		t.Fatalf("re-run copied again: %+v", again.Principals[0])
	}
}

// TestSpoolMigrateCLI drives the `hubctl spool migrate` command path.
func TestSpoolMigrateCLI(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "spool")
	state := filepath.Join(t.TempDir(), "state")
	dir := filepath.Join(spool, "knowledge", "carol")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"spool", "migrate", "--spool", spool, "--state", state}); err != nil {
		t.Fatalf("dry-run via run(): %v", err)
	}
	if err := run(t.Context(), []string{"spool", "migrate", "--spool", spool, "--state", state, "--apply"}); err != nil {
		t.Fatalf("apply via run(): %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "knowledge", "carol", "notes.md")); err != nil {
		t.Fatalf("file not migrated: %v", err)
	}
	if err := run(t.Context(), []string{"spool"}); err == nil {
		t.Fatal("expected usage error without subcommand")
	}
}

// TestSpoolMigrateGuards drives the fail-closed edges: absent knowledge dir,
// oversized sources and invalid context directory names.
func TestSpoolMigrateGuards(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "spool")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	rep, err := spoolMigrate(spool, state, false)
	if err != nil {
		t.Fatalf("empty spool must not error: %v", err)
	}
	if len(rep.Notes) == 0 {
		t.Fatal("expected a note for the missing knowledge dir")
	}

	big := filepath.Join(spool, "knowledge", "dave")
	if err := os.MkdirAll(filepath.Join(big, "..ctx"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(big, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(big, "..ctx", "x.md"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	oversize := filepath.Join(big, "huge.md")
	f, err := os.Create(oversize)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate((9 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	rep, err = spoolMigrate(spool, state, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Principals) != 1 || len(rep.Principals[0].Skipped) != 2 {
		t.Fatalf("expected 2 skipped entries: %+v", rep.Principals)
	}
	if _, err := os.Stat(filepath.Join(state, "knowledge", "dave", "huge.md")); !os.IsNotExist(err) {
		t.Fatal("oversized file migrated")
	}
}
