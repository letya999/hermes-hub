package devcheck

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCoverage(t *testing.T) {
	name := filepath.Join(t.TempDir(), "coverage.out")
	if _, err := Coverage(name, 85); err == nil {
		t.Fatal("missing coverage file accepted")
	}
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
	if err := Project(t.TempDir()); err == nil {
		t.Fatal("missing project files accepted")
	}
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
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Format(root); err != nil {
		t.Fatal(err)
	}
}

func TestDockerSmokeRunsRequiredChecksAndPropagatesFailure(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "docker.log")
	name := "docker"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\ncase \"$*\" in *\"$HUB_FAIL_MATCH\"*) [ -z \"$HUB_FAIL_MATCH\" ] || exit 7;; esac\ncase \"$*\" in *'%{http_code}'*) printf 409;; esac\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo %*\r\nif not \"%HUB_FAIL_MATCH%\"==\"\" (echo %* | findstr /C:\"%HUB_FAIL_MATCH%\" >nul && exit /b 7)\r\necho %* | findstr /C:\"%%{http_code}\" >nul && <nul set /p =409\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := DockerSmoke(context.Background(), "test-image"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(body), "--entrypoint hermes test-image --version") || !strings.Contains(string(body), "--entrypoint glab test-image --version") || !strings.Contains(string(body), "hub-runtime health") || !strings.Contains(string(body), "HUB_POLICY_VERSION=policy-smoke") {
		t.Fatal(string(body), err)
	}
	t.Setenv("HUB_FAIL_MATCH", "--entrypoint glab")
	if err := DockerSmoke(context.Background(), "test-image"); err == nil {
		t.Fatal("docker failure ignored")
	}
}
