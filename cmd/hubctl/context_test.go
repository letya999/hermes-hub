package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextMemorySkillRoutineCLI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hermes", "memories", "USER.md"), []byte("remember tea"), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "owner.zip")
	if err := run(ctx, []string{"context", "backup", "--dir", dir, "--user", "owner", "--out", out}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	if strings.Contains(string(raw), "OPENAI_API_KEY=") && strings.Contains(string(raw), "super") {
		t.Fatal("backup leaked secrets")
	}
	other := t.TempDir()
	if err := run(ctx, []string{"context", "restore", "--dir", other, "--user", "intruder", "--in", out}); err == nil {
		t.Fatal("restore to another user")
	}
	dest := t.TempDir()
	exported := filepath.Join(t.TempDir(), "owner-export.zip")
	if err := run(ctx, []string{"context", "export", "--dir", dir, "--user", "owner", "--out", exported}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context", "restore", "--dir", dest, "--user", "owner", "--in", out}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"memory", "list", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"memory", "delete", "--dir", dir, "--user", "owner", "--name", "USER.md"}); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(skill, []byte("# Demo\nList files."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo", "--from-file", skill, "--consent"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "list", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "revoke", "--dir", dir, "--user", "owner", "--name", "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context", "purge", "--dir", dest, "--user", "owner", "--confirm", "owner"}); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(t.TempDir(), "spool")
	due := "once:2099-01-01T00:00:00Z"
	if err := run(ctx, []string{"routine", "create", "--spool", spool, "--user", "owner", "--id", "morning", "--tz", "UTC", "--expr", due, "--input", "brief"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"routine", "list", "--spool", spool, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"routine", "pause", "--spool", spool, "--user", "owner", "--id", "morning"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"routine", "delete", "--spool", spool, "--user", "owner", "--id", "morning"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context"}); err == nil {
		t.Fatal("context without action")
	}
	if err := run(ctx, []string{"context", "backup", "--dir", dir, "--user", "owner"}); err == nil {
		t.Fatal("backup without out")
	}
	if err := run(ctx, []string{"memory", "delete", "--dir", dir, "--user", "owner"}); err == nil {
		t.Fatal("memory delete without name")
	}
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo"}); err == nil {
		t.Fatal("skill install without file")
	}
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo", "--from-file", skill}); err == nil {
		t.Fatal("skill install without consent")
	}
	if err := run(ctx, []string{"routine", "list"}); err == nil {
		t.Fatal("routine without spool")
	}
	if err := run(ctx, []string{"context", "stop-runtime", "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context", "delete-runtime", "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"memory", "unknown", "--dir", dir, "--user", "owner"}); err == nil {
		t.Fatal("unknown memory command")
	}
	if err := run(ctx, []string{"skill", "unknown", "--dir", dir, "--user", "owner"}); err == nil {
		t.Fatal("unknown skill command")
	}
	if err := run(ctx, []string{"routine", "unknown", "--spool", spool, "--user", "owner"}); err == nil {
		t.Fatal("unknown routine command")
	}
	if err := run(ctx, []string{"context", "restore", "--dir", dest, "--user", "owner"}); err == nil {
		t.Fatal("restore without archive")
	}
	if err := run(ctx, []string{"memory", "list", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
}
