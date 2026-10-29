package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeIndex(t *testing.T, dir, body string) {
	t.Helper()
	root := GeneratedEffectiveSkillsDir(dir)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillDoctorNotRendered(t *testing.T) {
	lines, err := SkillDoctor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "not rendered") {
		t.Fatalf("expected not-rendered line, got %v", lines)
	}
}

func TestSkillDoctorReportsIneligible(t *testing.T) {
	dir := t.TempDir()
	writeIndex(t, dir, `{"schema":1,"revision":"abcdef1234567890","skills":[
		{"name":"good-one","source":"user","eligible":true},
		{"name":"git-flow","source":"org","eligible":false,"missing":["connector:gitlab"],"creds_missing":["GITLAB_TOKEN"]},
		{"name":"quiet","source":"platform","eligible":true,"hidden":true},
		{"name":"scripty","source":"user","eligible":true,"has_scripts":true,"instructions_only":true,"deps_missing":["pip:torch"]}
	],"notices":[{"kind":"collision","name":"dup","hidden_by_source":"user","at":"t"}]}`)
	if err := os.MkdirAll(filepath.Join(dir, "skills", ".drafts", "d1"), 0755); err != nil {
		t.Fatal(err)
	}
	lines, err := SkillDoctor(dir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"user=2", "org=1", "platform=1", "abcdef123456",
		`git-flow [org] INELIGIBLE`, "connector:gitlab",
		"setup needed, credentials: GITLAB_TOKEN",
		"hidden from implicit catalog",
		"instructions only", "pip:torch",
		`notice collision: "dup"`, "pending draft",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}
