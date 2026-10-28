package gitsource

import (
	"errors"
	"testing"
)

func TestParseSourceAcceptsGitHubAndGitLab(t *testing.T) {
	tests := []struct {
		raw              string
		wantRepo         string
		wantRef, wantDir string
	}{
		{"https://github.com/anthropics/skills", "https://github.com/anthropics/skills", "", ""},
		{"https://github.com/anthropics/skills/tree/main/skills/pdf", "https://github.com/anthropics/skills", "main", "skills/pdf"},
		{"https://gitlab.com/acme/tools", "https://gitlab.com/acme/tools", "", ""},
		{"https://gitlab.com/acme/tools/-/tree/release/skill/x", "https://gitlab.com/acme/tools", "release", "skill/x"},
	}
	for _, tc := range tests {
		src, err := ParseSource(tc.raw, "")
		if err != nil {
			t.Fatalf("ParseSource(%q): %v", tc.raw, err)
		}
		if src.Repo != tc.wantRepo || src.Ref != tc.wantRef || src.Path != tc.wantDir {
			t.Fatalf("ParseSource(%q) = %+v", tc.raw, src)
		}
	}
}

func TestSkillSourceRejectsNonHTTPSAndUserinfo(t *testing.T) {
	bad := []string{
		"ssh://git@github.com/a/b", "git://github.com/a/b", "file:///tmp/r",
		"https://user:tok@github.com/a/b", "https://github.com/a/b?x=1",
		"https://github.com/a/b#frag", "https://github.com/a/b.git",
		"https://github.com/a", "https://github.com//b", "https://github.com/a/../x",
		"/local/path/repo", "github.com/a/b", "https://evil.com/a/b",
		"https://gitlab.acme.corp/team/skills",
	}
	for _, raw := range bad {
		if _, err := ParseSource(raw, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ParseSource(%q) must fail with ErrInvalid", raw)
		}
	}
}

func TestParseSourceAcceptsConfiguredGitLabHost(t *testing.T) {
	src, err := ParseSource("https://gitlab.acme.corp/team/skills/-/tree/v1/x", "gitlab.acme.corp")
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	if src.Repo != "https://gitlab.acme.corp/team/skills" || src.Ref != "v1" || src.Path != "x" {
		t.Fatalf("unexpected %+v", src)
	}
}

func TestParseSourceRejectsUnsafeRefAndPath(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/a/b/tree/-x/dir",   // ref shaped like a git flag
		"https://github.com/a/b/tree/main/../", // traversal in the subpath
		"https://github.com/a/b/tree/main/secret/x",
		"https://github.com/a/b/tree/main/.git/x",
	} {
		if _, err := ParseSource(raw, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ParseSource(%q) must fail", raw)
		}
	}
	src, err := ParseSource("https://github.com/a/b/tree/main/skills/pdf", "")
	if err != nil || src.Path != "skills/pdf" {
		t.Fatalf("nested safe path must pass: %+v %v", src, err)
	}
}
