package contextlife

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Backup packs the context home minus secret-shaped files, carries spool
// schedule/mapping state, and Verify/Restore reject tampered or off-user
// archives before anything lands.
func TestBackupVerifyRestoreBranches(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	for _, rel := range []string{"hermes/memories", "workspace", "connections", "skills", "runtime/toolhub"} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(home, filepath.FromSlash(rel)), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("hermes/notes.md", "notes")
	write("hermes/memories/first.md", "memory one")
	write("workspace/README.md", "ws")
	write("connections/conn.json", "{}")
	write("skills/list.txt", "skill")
	write("hermes/secrets.dev.env", "TOKEN=leak") // excluded by name
	write("hermes/store.enc", "ciphertext")       // excluded by suffix
	write("runtime/toolhub/store.json", "{}")     // included explicitly
	spool := filepath.Join(base, "spool")
	for _, rel := range []string{"schedules", "mappings", "outbox"} {
		if err := os.MkdirAll(filepath.Join(spool, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(spool, "schedules", "s1.json"), []byte(`{"principal_id":"alice","context_id":"alice"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "mappings", "m1.json"), []byte(`{"principal_id":"alice","context_id":"alice"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "outbox", "d1.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(base, "backup.zip")
	if err := Backup(home, "alice", archive, spool); err != nil {
		t.Fatal(err)
	}
	if err := Verify(archive, "alice"); err != nil {
		t.Fatalf("own backup rejected: %v", err)
	}
	if err := Verify(archive, "bob"); err == nil {
		t.Fatal("foreign user accepted own backup")
	}
	if err := Backup(home, "Bad User", filepath.Join(base, "x.zip"), ""); err == nil {
		t.Fatal("invalid user backed up")
	}
	if err := Backup(filepath.Join(base, "missing"), "alice", filepath.Join(base, "x.zip"), ""); err == nil {
		t.Fatal("missing home backed up")
	}

	// Secret-shaped files never enter the archive.
	r, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		if f.Name == "hermes/secrets.dev.env" || f.Name == "hermes/store.enc" {
			t.Fatalf("secret file archived: %s", f.Name)
		}
	}
	r.Close()

	// Restore lands workspace dirs and spool schedules/mappings.
	home2 := filepath.Join(base, "home2")
	spool2 := filepath.Join(base, "spool2")
	if err := os.MkdirAll(home2, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Restore(home2, "alice", archive, spool2); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home2, "workspace", "README.md")); err != nil || string(b) != "ws" {
		t.Fatalf("restored workspace: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(spool2, "schedules", "s1.json")); err != nil || string(b) != `{"principal_id":"alice","context_id":"alice"}` {
		t.Fatalf("restored spool: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spool2, "outbox", "d1.json")); !os.IsNotExist(err) {
		t.Fatal("outbox restored — only schedules/mappings may cross")
	}

	// Tampered archives fail hash verification.
	evil := filepath.Join(base, "evil.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mw, _ := zw.Create("manifest.json")
	manifest := `{"schema":1,"user":"alice","sha256":{"workspace/README.md":"deadbeef"}}`
	_, _ = mw.Write([]byte(manifest))
	fw, _ := zw.Create("workspace/README.md")
	_, _ = fw.Write([]byte("tampered"))
	_ = zw.Close()
	if err := os.WriteFile(evil, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(evil, "alice"); err == nil {
		t.Fatal("tampered hash accepted")
	}
	// Manifest-less and schema-mismatched archives fail.
	var buf2 bytes.Buffer
	zw2 := zip.NewWriter(&buf2)
	fw2, _ := zw2.Create("other.txt")
	_, _ = fw2.Write([]byte("x"))
	_ = zw2.Close()
	if err := os.WriteFile(filepath.Join(base, "nomanifest.zip"), buf2.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(filepath.Join(base, "nomanifest.zip"), "alice"); err == nil {
		t.Fatal("manifest-less archive accepted")
	}

	// Purge needs a matching confirm; MemoryFiles/DeleteMemory bound names.
	if _, err := Purge(home, "alice", "bob"); err == nil {
		t.Fatal("purge without matching confirm")
	}
	names, err := MemoryFiles(home)
	if err != nil || len(names) != 1 || names[0] != "first.md" {
		t.Fatalf("memory files: %v %v", names, err)
	}
	if err := DeleteMemory(home, "../escape"); err == nil {
		t.Fatal("path traversal memory delete")
	}
	if err := DeleteMemory(home, "first.md"); err != nil {
		t.Fatal(err)
	}
	report, err := Purge(home, "alice", "alice")
	if err != nil || len(report.Removed) == 0 {
		t.Fatalf("purge: %+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace")); !os.IsNotExist(err) {
		t.Fatal("workspace survived purge")
	}
}
