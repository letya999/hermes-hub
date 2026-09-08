package devcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverage(t *testing.T) {
	name := filepath.Join(t.TempDir(), "coverage.out")
	for _, tc := range []struct {
		body string
		min  float64
		ok   bool
	}{
		{"mode: atomic\na.go:1.1,2.1 9 1\nb.go:1.1,2.1 1 0\n", 85, true},
		{"mode: count\na:1 85 1\nb:1 15 0\n", 85, true},
		{"mode: set\na:1 84 1\nb:1 16 0\n", 85, false},
		{"garbage\n", 85, false},
		{"mode: set\nbad row\n", 85, false},
		{"mode: set\na:1 0 1\n", 85, false},
		{"mode: set\na:1 0 0\nb:1 1 1\n", 85, true},
	} {
		if err := os.WriteFile(name, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Coverage(name, tc.min)
		if (err == nil) != tc.ok {
			t.Fatalf("Coverage(%q): %v", tc.body, err)
		}
	}
}

func TestProjectAndFormat(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"AGENTS.md", "README.md", "SETUP.md", "CONTRIBUTING.md", "SECURITY.md", "LICENSE", "docs/index.md", "specs/index.md", ".work/index.md"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		body := "ok\n"
		if strings.HasPrefix(name, "docs/") {
			body = "---\ndescription: test\n---\n[readme](../README.md)\n"
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Project(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "bad.md"), []byte("{{missing}}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Project(root); err == nil {
		t.Fatal("bad docs accepted")
	}
	for _, folder := range []string{"cmd", "internal"} {
		if err := os.MkdirAll(filepath.Join(root, folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	goFile := filepath.Join(root, "cmd", "main.go")
	if err := os.WriteFile(goFile, []byte("package main\nfunc main(){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Format(root); err == nil {
		t.Fatal("unformatted Go accepted")
	}
}
