package main

import (
	"context"
	"encoding/json"
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
	spool := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(filepath.Join(spool, "schedules"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "schedules", "morning.json"), []byte(`{"schedule_id":"morning","principal_id":"owner","context_id":"owner"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context", "backup", "--dir", dir, "--user", "owner", "--out", out, "--spool", spool}); err != nil {
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
	spoolDest := filepath.Join(t.TempDir(), "spool-restore")
	if err := run(ctx, []string{"context", "restore", "--dir", dest, "--user", "owner", "--in", out, "--spool", spoolDest}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(spoolDest, "schedules", "morning.json")); err != nil {
		t.Fatal("hubctl restore did not write schedules into --spool")
	}
	if err := run(ctx, []string{"memory", "list", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"memory", "delete", "--dir", dir, "--user", "owner", "--name", "USER.md"}); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(skill, []byte("---\nname: demo\ndescription: demo skill\n---\n\n# Demo\nList files.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo", "--from-file", skill, "--consent"}); err != nil {
		t.Fatal(err)
	}
	listed := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "list", "--dir", dir, "--user", "owner"})
	})
	if !strings.Contains(listed, `"demo"`) {
		t.Fatalf("skill list lost the published skill: %s", listed)
	}
	if err := run(ctx, []string{"skill", "show", "--dir", dir, "--user", "owner", "--name", "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "archive", "--dir", dir, "--user", "owner", "--name", "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"skill", "restore", "--dir", dir, "--user", "owner", "--name", "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"context", "purge", "--dir", dest, "--user", "owner", "--confirm", "owner"}); err != nil {
		t.Fatal(err)
	}
	routineSpool := filepath.Join(t.TempDir(), "routine-spool")
	due := "once:2099-01-01T00:00:00Z"
	if err := run(ctx, []string{"routine", "create", "--spool", routineSpool, "--user", "owner", "--chat-id", "1", "--organization", "personal", "--runtime", "owner", "--policy", "policy-1", "--id", "morning", "--tz", "UTC", "--expr", due, "--input", "brief"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"routine", "list", "--spool", routineSpool, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"routine", "pause", "--spool", routineSpool, "--user", "owner", "--chat-id", "1", "--organization", "personal", "--runtime", "owner", "--policy", "policy-1", "--id", "morning"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"routine", "delete", "--spool", routineSpool, "--user", "owner", "--chat-id", "1", "--organization", "personal", "--runtime", "owner", "--policy", "policy-1", "--id", "morning"}); err != nil {
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
	// Without --consent the skill lands as an inactive draft, not an error.
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo", "--from-file", skill}); err != nil {
		t.Fatal(err)
	}
	drafts := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "drafts", "--dir", dir, "--user", "owner"})
	})
	if !strings.Contains(drafts, `"demo"`) {
		t.Fatalf("no-consent install did not land as a draft: %s", drafts)
	}
	var dlist []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(drafts), &dlist); err != nil || len(dlist) == 0 {
		t.Fatalf("drafts output not decodable: %v %s", err, drafts)
	}
	if err := run(ctx, []string{"skill", "approve", "--dir", dir, "--user", "owner", "--id", dlist[0].ID}); err != nil {
		t.Fatal(err)
	}
	shown := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "show", "--dir", dir, "--user", "owner", "--name", dlist[0].Name})
	})
	if !strings.Contains(shown, `"demo"`) {
		t.Fatalf("approved draft not active: %s", shown)
	}
	// A second unconsented install drafts again; reject drops it.
	if err := run(ctx, []string{"skill", "install", "--dir", dir, "--user", "owner", "--name", "demo", "--from-file", skill}); err != nil {
		t.Fatal(err)
	}
	drafts2 := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "drafts", "--dir", dir, "--user", "owner"})
	})
	var dlist2 []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(drafts2), &dlist2); err != nil || len(dlist2) == 0 {
		t.Fatalf("second draft missing: %v %s", err, drafts2)
	}
	if err := run(ctx, []string{"skill", "reject", "--dir", dir, "--user", "owner", "--id", dlist2[0].ID}); err != nil {
		t.Fatal(err)
	}
	drafts3 := captureOutput(t, func() error {
		return run(ctx, []string{"skill", "drafts", "--dir", dir, "--user", "owner"})
	})
	if strings.Contains(drafts3, dlist2[0].ID) {
		t.Fatalf("rejected draft still listed: %s", drafts3)
	}
	if err := run(ctx, []string{"skill", "rollback", "--dir", dir, "--user", "owner", "--name", "demo"}); err == nil {
		t.Fatal("rollback without --to")
	}
	if err := run(ctx, []string{"skill", "rollback", "--dir", dir, "--user", "owner", "--name", "demo", "--to", "deadbeef"}); err == nil || strings.Contains(err.Error(), "unknown skill command") {
		t.Fatalf("rollback to a missing revision must reach the store: %v", err)
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
	if err := run(ctx, []string{"routine", "unknown", "--spool", routineSpool, "--user", "owner"}); err == nil {
		t.Fatal("unknown routine command")
	}
	if err := run(ctx, []string{"context", "restore", "--dir", dest, "--user", "owner"}); err == nil {
		t.Fatal("restore without archive")
	}
	if err := run(ctx, []string{"memory", "list", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
}
