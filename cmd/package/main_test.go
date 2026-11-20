package main

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestVersionValidation(t *testing.T) {
	for _, version := range []string{"", "../bad", `bad\\name`} {
		if err := pack(version); err == nil {
			t.Fatalf("accepted %q", version)
		}
	}
}

func TestBuild(t *testing.T) {
	root := t.TempDir()
	source, output := filepath.Join(root, "main.go"), filepath.Join(root, "tool")
	if err := os.WriteFile(source, []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := build(output, source, goruntime.GOOS, goruntime.GOARCH); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}
}

func TestPack(t *testing.T) {
	root := t.TempDir()
	oldDir, _ := os.Getwd()
	oldSources, oldBuild := sourcePaths, buildBinary
	t.Cleanup(func() { os.Chdir(oldDir); sourcePaths, buildBinary = oldSources, oldBuild })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("README.md", []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	sourcePaths = []string{"README.md"}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "README.md"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	runtimeBuilds := map[string]bool{}
	buildBinary = func(output, pkg, _, arch string) error {
		if pkg == "./cmd/runtime" {
			runtimeBuilds[arch] = true
		}
		return os.WriteFile(output, []byte(filepath.Base(output)), 0600)
	}
	if err := pack("test"); err != nil {
		t.Fatal(err)
	}
	checksums, err := os.ReadFile(filepath.Join("dist", "SHA256SUMS"))
	if err != nil || strings.Count(string(checksums), "\n") != 9 || !runtimeBuilds["amd64"] || !runtimeBuilds["arm64"] {
		t.Fatalf("checksums: %q, %v", checksums, err)
	}
	archive, err := zip.OpenReader(filepath.Join("dist", "hermes-hub-test-source.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 1 || archive.File[0].Name != "README.md" {
		t.Fatalf("archive files: %#v", archive.File)
	}
}

func TestSourceArchiveContainsOnlyTrackedInputs(t *testing.T) {
	t.Chdir(t.TempDir())
	if out, err := exec.Command("git", "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	files := map[string]string{"README.md": "fixture", "contracts/task/v1/task.go": "package task", ".work/tmp/private.txt": "private-fixture", "config/local.txt": "untracked-fixture", "services/credential-broker/go.mod": "module fixture/broker", "go.work": "go 1.27.4", "platform/catalogs/fixture/manifest.yaml": "release: fixture", "services/README.template.md": "## Responsibility", "shared/httpapi/httpapi.go": "package httpapi"}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(".gitignore", []byte(".work/tmp/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "add", "README.md", "contracts/task/v1/task.go", "services/credential-broker/go.mod", "go.work", "platform/catalogs/fixture/manifest.yaml", "services/README.template.md", "shared/httpapi/httpapi.go").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	for _, root := range sourcePaths {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zipSources("source.zip"); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader("source.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	got := map[string]bool{}
	for _, file := range archive.File {
		got[file.Name] = true
	}
	if len(got) != 7 || !got["shared/httpapi/httpapi.go"] || !got["services/README.template.md"] || !got["README.md"] || !got["contracts/task/v1/task.go"] || !got["services/credential-broker/go.mod"] || !got["go.work"] || !got["platform/catalogs/fixture/manifest.yaml"] {
		t.Fatalf("source entries: %v", got)
	}
}

func TestSourceArchiveRefusesTrackedSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("private-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if out, err := exec.Command("git", "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	if err := os.Symlink(target, "README.md"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if out, err := exec.Command("git", "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if err := zipSources("source.zip"); err == nil {
		t.Fatal("tracked symlink entered the source archive")
	}
}
