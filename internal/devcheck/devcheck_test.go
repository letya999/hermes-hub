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
		{"mode: atomic\na.go:1.1,2.1 9 1\nb.go:1.1,2.1 1 0\n\n", 85, true},
		{"mode: atomic\na.go:1.1,2.1 9 1\n\nb.go:1.1,2.1 1 0\n", 85, false},
		{"mode: count\na:1 85 1\nb:1 15 0\n", 85, true},
		{"mode: set\na:1 84 1\nb:1 16 0\n", 85, false},
		{"garbage\n", 85, false},
		{"mode: set\nbad row\n", 85, false},
		{"mode: set\na:1 0 1\n", 85, false},
		{"mode: set\na:1 0 0\nb:1 1 1\n", 85, true},
		{"mode: set\na:1 84 0\nb:1 16 0\na:1 84 1\nb:1 16 1\n", 85, true},
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

func TestCoverageGate(t *testing.T) {
	name := filepath.Join(t.TempDir(), "coverage.out")
	body := "mode: count\n" +
		"github.com/letya999/hermes-hub/internal/a/x.go:1.1,2.1 90 1\n" +
		"github.com/letya999/hermes-hub/internal/a/y.go:1.1,2.1 10 0\n" +
		"github.com/letya999/hermes-hub/internal/b/z.go:1.1,2.1 5 0\n" +
		"github.com/letya999/hermes-hub/cmd/c/main.go:1.1,2.1 5 0\n"
	if err := os.WriteFile(name, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CoverageGate(name, 60, 0); err != nil {
		t.Fatalf("global gate: %v", err)
	}
	if _, err := CoverageGate(name, 60, 50); err == nil {
		t.Fatal("package at 0% accepted under 50% floor")
	}
	if _, err := CoverageGate(name, 60, 85); err == nil {
		t.Fatal("package at 0% accepted under 85% floor")
	}
	if _, err := CoverageGate(name, 95, 0); err == nil {
		t.Fatal("global shortfall accepted")
	}
	// cmd/ stays outside the floor: internal/a at 90% passes, cmd/c at 0% ignored.
	body = "mode: count\ngithub.com/letya999/hermes-hub/internal/a/x.go:1.1,2.1 9 1\ngithub.com/letya999/hermes-hub/internal/a/y.go:1.1,2.1 1 0\ngithub.com/letya999/hermes-hub/cmd/c/main.go:1.1,2.1 5 0\n"
	if err := os.WriteFile(name, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CoverageGate(name, 60, 85); err != nil {
		t.Fatalf("cmd/ main wrongly gated: %v", err)
	}
	// Replaced local modules carry their own stricter coverage gate; the root
	// floor must not police their internal packages, only this module's.
	body = "mode: count\n" +
		"github.com/letya999/hermes-hub/internal/a/x.go:1.1,2.1 30 1\n" +
		"github.com/letya999/credential-broker/internal/strictjson/j.go:1.1,2.1 10 0\n"
	if err := os.WriteFile(name, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CoverageGate(name, 60, 85); err != nil {
		t.Fatalf("foreign module internal package wrongly gated: %v", err)
	}
	body = "mode: count\n" +
		"github.com/letya999/hermes-hub/internal/a/x.go:1.1,2.1 30 1\n" +
		"github.com/letya999/hermes-hub/internal/b/y.go:1.1,2.1 10 0\n"
	if err := os.WriteFile(name, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CoverageGate(name, 60, 85); err == nil {
		t.Fatal("own internal package below floor accepted")
	}
}

func TestCoverAllPackageFloor(t *testing.T) {
	name := filepath.Join(t.TempDir(), "coverage.out")
	_, err := CoverAll(name, 0, 50,
		func() ([]string, error) { return []string{"pkg/a"}, nil },
		func(pkg, out string) error {
			return os.WriteFile(out, []byte("mode: count\ngithub.com/letya999/hermes-hub/internal/pa/x.go:1.1,2.1 9 1\ngithub.com/letya999/hermes-hub/internal/pb/y.go:1.1,2.1 5 0\n"), 0600)
		})
	if err == nil {
		t.Fatal("CoverAll ignored package floor")
	}
}

func TestCoverAll(t *testing.T) {
	name := filepath.Join(t.TempDir(), "coverage.out")
	packages := []string{"pkg/a", "pkg/b"}
	listed := 0
	tested := map[string]bool{}
	actual, err := CoverAll(name, 85, 0,
		func() ([]string, error) {
			listed++
			return packages, nil
		},
		func(pkg, out string) error {
			tested[pkg] = true
			body := "mode: count\na.go:1.1,2.1 84 0\nb.go:1.1,2.1 16 0\n"
			if pkg == "pkg/b" {
				body = "mode: count\na.go:1.1,2.1 84 1\nb.go:1.1,2.1 16 1\n"
			}
			return os.WriteFile(out, []byte(body), 0600)
		})
	if err != nil {
		t.Fatalf("CoverAll: %v", err)
	}
	if listed != 1 || len(tested) != 2 || !tested["pkg/a"] || !tested["pkg/b"] {
		t.Fatalf("packages not exercised: listed=%d tested=%v", listed, tested)
	}
	if actual != 100 {
		t.Fatalf("union coverage = %.2f, want 100", actual)
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
			body = "---\ndescription: test\nlast_verified: 2000-01-01\n---\n[readme](../README.md)\n"
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
