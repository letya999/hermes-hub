package contextlife

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupCannotExportForeignMemoryThroughSymlink(t *testing.T) {
	root := t.TempDir()
	alice := filepath.Join(root, "alice")
	bob := filepath.Join(root, "bob")
	os.MkdirAll(filepath.Join(alice, "hermes", "memories"), 0700)
	os.MkdirAll(bob, 0700)
	foreign := filepath.Join(bob, "memory.md")
	os.WriteFile(foreign, []byte("bob-private-memory-marker"), 0600)
	if err := os.Symlink(foreign, filepath.Join(alice, "hermes", "memories", "borrowed.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	out := filepath.Join(root, "backup.zip")
	err := Backup(alice, "alice", out, "")
	if err != nil {
		return
	}
	archive, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "bob-private-memory-marker") {
			t.Errorf("foreign memory leaked through export %s", file.Name)
		}
	}
}
