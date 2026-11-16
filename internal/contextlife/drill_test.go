package contextlife

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestDrillRestoresAndVerifies(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "hermes", "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hermes", "memory", "MEMORY.md"), []byte("# facts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "b.zip")
	if err := Backup(home, "alice", archive, ""); err != nil {
		t.Fatal(err)
	}

	scratch := t.TempDir()
	report, err := Drill("alice", archive, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if report.Checked == 0 {
		t.Fatal("drill checked nothing")
	}
	// Restored bytes actually landed in scratch.
	if _, err := os.Stat(filepath.Join(scratch, "hermes", "memory", "MEMORY.md")); err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	// The live home is untouched by the drill (restore went to scratch).
	if _, err := os.Stat(filepath.Join(home, ".restore-partial")); !os.IsNotExist(err) {
		t.Fatal("staging leaked")
	}
}

func TestDrillWrongOwnerDenied(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "workspace", "note.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "b.zip")
	if err := Backup(home, "alice", archive, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Drill("bob", archive, t.TempDir()); err == nil {
		t.Fatal("cross-owner restore drill must stay denied")
	}
}

func TestDrillDetectsTamperedBytes(t *testing.T) {
	// An archive whose manifest hash does not match its bytes must fail
	// the drill at Verify — the integrity contract is checked before a
	// single restore write.
	tmp := filepath.Join(t.TempDir(), "tampered.zip")
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	w, err := zw.Create("workspace/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("tampered"))
	mw, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":1,"user":"alice","context_id":"alice","sha256":{"workspace/note.txt":"0000000000000000000000000000000000000000000000000000000000000000"}}`
	_, _ = mw.Write([]byte(manifest))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Drill("alice", tmp, t.TempDir()); err == nil {
		t.Fatal("tampered archive must fail the drill")
	}
}
