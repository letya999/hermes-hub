package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dispatch guards: every subcommand validates its flags before touching the
// filesystem; an empty or malformed invocation must produce a usage error.
func TestContextCommandGuards(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", nil, "requires"},
		{"unknown subcommand", []string{"bogus"}, "unknown context command"},
		{"flag parse error", []string{"backup", "--bogus"}, ""},
		{"backup missing out", []string{"backup", "--dir", dir}, "requires --out"},
		{"export missing out", []string{"export", "--dir", dir}, "requires --out"},
		{"restore missing in", []string{"restore", "--dir", dir}, "requires --in"},
		{"restore-drill no archives", []string{"restore-drill", "--dir", dir}, "no scheduled backups"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runContext(ctx, tc.args)
			if err == nil {
				t.Fatalf("%s must fail", tc.name)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: %v lacks %q", tc.name, err, tc.want)
			}
		})
	}
	// Advisory commands always succeed — they print the modern equivalent.
	for _, sub := range []string{"stop-runtime", "delete-runtime"} {
		if err := runContext(ctx, []string{sub, "--dir", dir, "--user", "alice"}); err != nil {
			t.Fatalf("%s must not fail: %v", sub, err)
		}
	}
	// Purge without --confirm refuses inside contextlife, not silently.
	if err := runContext(ctx, []string{"purge", "--dir", dir, "--user", "alice"}); err == nil {
		t.Fatal("purge without confirm must refuse")
	}
}

func TestMemoryCommandGuards(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", nil, "requires"},
		{"unknown subcommand", []string{"bogus"}, "unknown memory command"},
		{"search without reindex", []string{"search", "--dir", dir}, "requires --reindex"},
		{"delete without name", []string{"delete", "--dir", dir}, "requires --name"},
		{"eval without questions", []string{"eval", "--dir", dir}, "requires --questions"},
		{"export-honcho without file", []string{"export-honcho", "--dir", dir}, "requires --file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runMemory(ctx, tc.args)
			if err == nil {
				t.Fatalf("%s must fail", tc.name)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: %v lacks %q", tc.name, err, tc.want)
			}
		})
	}
}

func TestSkillCommandGuards(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", nil, "requires"},
		{"unknown subcommand", []string{"bogus"}, "unknown skill command"},
		{"show without name", []string{"show", "--dir", dir}, "requires --name"},
		{"install without args", []string{"install", "--dir", dir}, "requires --name"},
		{"update without name", []string{"update", "--dir", dir}, "requires --name or --all"},
		{"archive without name", []string{"archive", "--dir", dir}, "requires --name"},
		{"restore without name", []string{"restore", "--dir", dir}, "requires --name"},
		{"sources without sub", []string{"sources"}, "requires list or remove"},
		{"approve without id", []string{"approve", "--dir", dir}, "requires --id"},
		{"reject without id", []string{"reject", "--dir", dir}, "requires --id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runSkill(ctx, tc.args)
			if err == nil {
				t.Fatalf("%s must fail", tc.name)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: %v lacks %q", tc.name, err, tc.want)
			}
		})
	}
	// Read-only inventory on a fresh store must answer, not crash.
	if err := runSkill(ctx, []string{"list", "--dir", dir}); err != nil {
		t.Fatalf("skill list on fresh store: %v", err)
	}
	if err := runSkill(ctx, []string{"drafts", "--dir", dir}); err != nil {
		t.Fatalf("skill drafts on fresh store: %v", err)
	}
	if err := runSkill(ctx, []string{"sources", "list", "--dir", dir}); err != nil {
		t.Fatalf("skill sources list: %v", err)
	}
	if err := runSkill(ctx, []string{"sources", "unknown", "--dir", dir}); err == nil {
		t.Fatal("unknown sources subcommand must fail")
	}
}

// A published backup file is picked up by restore-drill's archive scan.
func TestContextRestoreDrillFindsNewestArchive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	backups := filepath.Join(dir, "archive", "backups")
	if err := os.MkdirAll(backups, 0700); err != nil {
		t.Fatal(err)
	}
	// A non-archive file must not be mistaken for a scheduled backup.
	if err := os.WriteFile(filepath.Join(backups, "notes.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runContext(ctx, []string{"restore-drill", "--dir", dir}); err == nil || !strings.Contains(err.Error(), "no scheduled backups") {
		t.Fatalf("only backup-*.zip names count: %v", err)
	}
}
