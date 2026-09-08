package main

import (
	"archive/zip"
	"os"
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
	buildBinary = func(output, _, _, _ string) error { return os.WriteFile(output, []byte(filepath.Base(output)), 0600) }
	if err := pack("test"); err != nil {
		t.Fatal(err)
	}
	checksums, err := os.ReadFile(filepath.Join("dist", "SHA256SUMS"))
	if err != nil || strings.Count(string(checksums), "\n") != 7 {
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
