package contextlife

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupCannotFollowAncestorIntoForeignMemory(t *testing.T) {
	root := t.TempDir()
	alice := filepath.Join(root, "alice")
	foreign := filepath.Join(root, "bob", "hermes", "memories", "toolhub")
	os.MkdirAll(alice, 0700)
	os.MkdirAll(foreign, 0700)
	os.WriteFile(filepath.Join(foreign, "store.json"), []byte(`{"note":"foreign-memory-marker"}`), 0600)
	if err := os.Symlink(filepath.Dir(foreign), filepath.Join(alice, "runtime")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	out := filepath.Join(root, "backup.zip")
	if err := Backup(alice, "alice", out, ""); err != nil {
		return
	}
	z, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "foreign-memory-marker") {
			t.Error("foreign memory exported through an ancestor alias")
		}
	}
}
