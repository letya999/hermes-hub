package communication

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Offline import copies every record-classified file into spool.db rows
// without touching payloads, and preserves file mtimes so retention aging
// keeps working after the switch.
func TestSpoolImportCopiesRecordFilesToRows(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job-imp-1", UserID: "alice", ActorID: "alice", ScopeID: "user:alice"}
	if _, err := s.enqueueLocked(job); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(Job{ID: "job-imp-1", ScopeID: "user:alice", ActorID: "alice", ContextID: "alice"}, "run-1", hubruntime.SessionUsage{SessionID: "s1", SessionFound: true, Model: "m", FetchedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "inbound", "blob-1.bin")
	if err := os.WriteFile(payload, []byte("payload-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(root, "pending", "job-imp-1.json")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(pendingPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.fsys.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := ImportSpoolRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Records < 3 {
		t.Fatalf("import counted %d records, want pending+mapping+usage", report.Records)
	}
	fsys, err := openSpoolDB(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	want, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fsys.ReadFile(pendingPath)
	if err != nil {
		t.Fatalf("pending row missing: %v", err)
	}
	if string(got) != string(want) {
		t.Fatal("row body diverged from source file")
	}
	info, err := fsys.Lstat(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Unix() != old.Unix() {
		t.Fatalf("row mtime %v, want file mtime %v", info.ModTime(), old)
	}
	if _, err := fsys.ReadFile(filepath.Join(root, "usage.jsonl")); err != nil {
		t.Fatalf("usage journal row missing: %v", err)
	}
	if _, err := fsys.ReadFile(payload); err != nil {
		t.Fatalf("payload file must stay readable: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "records.activated")); !os.IsNotExist(err) {
		t.Fatal("import flipped the activation marker")
	}
}

// The import takes the writer fence: an online gateway must block it rather
// than race a half-imported database.
func TestSpoolImportRejectsActiveWriter(t *testing.T) {
	root := t.TempDir()
	release, err := LockSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := ImportSpoolRecords(root); err == nil {
		t.Fatal("import ran beside an active writer")
	}
}

// Activation flips the marker and retires record files into
// substrate-backup/: the gateway then serves everything from spool.db.
func TestSpoolActivateRetiresFilesAndFlips(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.enqueueLocked(Job{ID: "job-act-1", UserID: "alice", ActorID: "alice", ScopeID: "user:alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.fsys.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSpoolRecords(root); err != nil {
		t.Fatal(err)
	}
	report, err := ActivateSpoolRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Retired < 1 {
		t.Fatalf("activation retired %d files", report.Retired)
	}
	if _, err := os.Lstat(filepath.Join(root, "records.activated")); err != nil {
		t.Fatal("activation marker missing")
	}
	if _, err := os.Lstat(filepath.Join(root, "pending", "job-act-1.json")); !os.IsNotExist(err) {
		t.Fatal("record file left in place after retirement")
	}
	retired := filepath.Join(root, "substrate-backup", "pending", "job-act-1.json")
	if _, err := os.Lstat(retired); err != nil {
		t.Fatalf("retired copy missing: %v", err)
	}
	s2, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.fsys.Close()
	claimed, err := s2.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != "job-act-1" {
		t.Fatalf("activated spool lost the job: %+v", claimed)
	}
}

// Activation must refuse when spool.db was never imported or no longer
// matches the files — flipping early would silently drop records.
func TestSpoolActivateRefusesDivergedDB(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.enqueueLocked(Job{ID: "job-div-1", UserID: "alice", ActorID: "alice", ScopeID: "user:alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.fsys.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivateSpoolRecords(root); err == nil {
		t.Fatal("activation flipped without an imported spool.db")
	}
	if _, err := os.Lstat(filepath.Join(root, "records.activated")); !os.IsNotExist(err) {
		t.Fatal("refused activation still wrote the marker")
	}
	empty, err := openSpoolDB(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivateSpoolRecords(root); err == nil {
		t.Fatal("activation flipped on a spool.db missing the record rows")
	}
	if _, err := os.Lstat(filepath.Join(root, "records.activated")); !os.IsNotExist(err) {
		t.Fatal("refused activation still wrote the marker")
	}
}
