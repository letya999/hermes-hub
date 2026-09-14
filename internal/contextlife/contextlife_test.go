package contextlife

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupRestorePurgeIsolation(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{"hermes/memories", "workspace", "connections/google", "skills"} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "hermes", "memories", "USER.md"), []byte("alice-memory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hermes", "secret-note.md"), []byte("OPENAI_API_KEY=hidden\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "workspace", "note.md"), []byte("work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "connections", "google", "meta.json"), []byte(`{"status":"linked"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "secrets.prod.env"), []byte("OPENAI_API_KEY=super-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "runtime", "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "runtime", "credentials", "store.enc"), []byte("ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "runtime", "toolhub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "runtime", "toolhub", "store.json"), []byte(`{"bindings":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	spool := t.TempDir()
	if err := os.MkdirAll(filepath.Join(spool, "schedules"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "schedules", "morning.json"), []byte(`{"schedule_id":"morning"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "alice.zip")
	if err := Backup(home, "alice", out, spool); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Fatal("plaintext secret in general archive")
	}
	if err := Restore(t.TempDir(), "bob", out); err == nil {
		t.Fatal("restore overwrote another user")
	}
	dest := t.TempDir()
	if err := Restore(dest, "alice", out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "hermes", "memories", "USER.md"))
	if err != nil || string(got) != "alice-memory" {
		t.Fatalf("restore memory=%s err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "secrets.prod.env")); err == nil {
		t.Fatal("restore wrote secrets")
	}
	if _, err := os.Stat(filepath.Join(dest, "spool", "schedules", "morning.json")); err == nil {
		t.Fatal("restore duplicated cron deliveries")
	}
	if _, err := os.Stat(filepath.Join(dest, "runtime", "toolhub", "store.json")); err != nil {
		t.Fatal("toolhub bindings missing from restore")
	}
	if err := Verify(out, "bob"); err == nil {
		t.Fatal("verify accepted another user")
	}
	info := dummyInfo("x", 1)
	if info.IsDir() || info.Sys() != nil {
		t.Fatal("file info")
	}
	corrupt := filepath.Join(t.TempDir(), "bad.zip")
	if err := os.WriteFile(corrupt, []byte("not-a-zip"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(corrupt, "alice"); err == nil {
		t.Fatal("corrupt archive verified")
	}
	if _, err := Purge(dest, "alice", "bob"); err == nil {
		t.Fatal("purge without matching confirm")
	}
	report, err := Purge(dest, "alice", "alice")
	if err != nil || len(report.Removed) == 0 {
		t.Fatal(report, err)
	}
	if strings.Contains(strings.Join(report.Removed, ","), "super-secret") {
		t.Fatal("purge reported secrets")
	}
	if names, err := MemoryFiles(home); err != nil || len(names) != 1 || names[0] != "USER.md" {
		t.Fatal(names, err)
	}
	if err := DeleteMemory(home, "USER.md"); err != nil {
		t.Fatal(err)
	}
	if names, _ := MemoryFiles(home); len(names) != 0 {
		t.Fatal(names)
	}
	if err := Backup("/no/such/home", "alice", out, ""); err == nil {
		t.Fatal("missing home backed up")
	}
	if err := Backup(home, "bad id", out, ""); err == nil {
		t.Fatal("invalid user backed up")
	}
	if err := DeleteMemory(home, "../escape"); err == nil {
		t.Fatal("memory path escape")
	}
	if names, err := MemoryFiles(t.TempDir()); err != nil || names != nil {
		t.Fatal(names, err)
	}
}

func TestRestoreSkipsUnsafeAndDirectoryEntries(t *testing.T) {
	memory := []byte("keep-memory")
	sum := sha256.Sum256(memory)
	manifest := Manifest{Schema: Schema, User: "alice", ContextID: "alice", CreatedAt: time.Now().UTC(), Includes: []string{"hermes"}, Excludes: []string{"plaintext-secrets"}, SHA256: map[string]string{"hermes/memories/USER.md": hex.EncodeToString(sum[:])}}
	manBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "manual.zip")
	file, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	dh := &zip.FileHeader{Name: "hermes/plugins/"}
	dh.SetMode(os.ModeDir | 0755)
	if _, err := zw.CreateHeader(dh); err != nil {
		t.Fatal(err)
	}
	w, err := zw.Create("hermes/memories/USER.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(memory); err != nil {
		t.Fatal(err)
	}
	w, err = zw.Create("secrets.prod.env")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("OPENAI_API_KEY=nope")); err != nil {
		t.Fatal(err)
	}
	w, err = zw.Create("hermes/../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	w, err = zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(append(manBody, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := Restore(dest, "alice", out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "hermes", "memories", "USER.md"))
	if err != nil || string(got) != "keep-memory" {
		t.Fatalf("memory=%s err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "escape.txt")); err == nil {
		t.Fatal("path escape restored")
	}
	if _, err := os.Stat(filepath.Join(dest, "secrets.prod.env")); err == nil {
		t.Fatal("excluded secret restored")
	}
}
