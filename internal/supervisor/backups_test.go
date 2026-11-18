package supervisor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/contextlife"
)

func backupTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	control := filepath.Join(root, ".control")
	if err := os.MkdirAll(control, 0o700); err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		items:   map[string]*runtimeEntry{},
		jobs:    map[string]jobRecord{},
		backups: map[string]BackupRecord{},
		cfg: Config{
			SpacesRoot: root, StateDir: control,
			BackupEvery: time.Hour, BackupKeep: 2, BackupDrillEvery: 24 * time.Hour,
		},
		statePath: filepath.Join(control, "supervisor.json"),
	}
	return m, root
}

func writeSpaceFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBackupSpaceCapturesAndDrills(t *testing.T) {
	m, root := backupTestManager(t)
	writeSpaceFile(t, filepath.Join(root, "alice"), "hermes/memory/MEMORY.md", "# facts\n")
	writeSpaceFile(t, filepath.Join(root, "alice"), "secrets.prod.env", "TOKEN=must-not-leak\n")

	now := time.Now()
	m.backupSpace("alice", now)

	rec := m.backups["alice"]
	if rec.LastSuccessAt.IsZero() || rec.LastError != "" {
		t.Fatalf("backup record wrong: %+v", rec)
	}
	if rec.Retained != 1 || rec.LastArchive == "" {
		t.Fatalf("retention wrong: %+v", rec)
	}
	// Drill ran on the fresh archive and verified bytes.
	if rec.DrillResult != "ok" || rec.DrillChecked == 0 {
		t.Fatalf("drill wrong: %+v", rec)
	}
	// The secret file must never appear in the archive.
	manifest, err := contextlife.ReadManifest(filepath.Join(root, "alice", "archive", "backups", rec.LastArchive))
	if err != nil {
		t.Fatal(err)
	}
	for name := range manifest.SHA256 {
		if name == "secrets.prod.env" {
			t.Fatal("secret file leaked into scheduled backup")
		}
	}
	// backups.json persisted the auditable record.
	if _, err := os.Stat(filepath.Join(m.cfg.StateDir, backupsStateFile)); err != nil {
		t.Fatalf("backups.json not persisted: %v", err)
	}
}

func TestBackupSpaceRetentionAndCadence(t *testing.T) {
	m, root := backupTestManager(t)
	writeSpaceFile(t, filepath.Join(root, "alice"), "workspace/note.txt", "v")

	now := time.Now()
	for i := 0; i < 4; i++ {
		m.backups["alice"] = BackupRecord{} // force due
		m.backupSpace("alice", now.Add(time.Duration(i)*time.Minute))
	}
	rec := m.backups["alice"]
	if rec.Retained != 2 {
		t.Fatalf("keep bound violated: %+v", rec)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "alice", "archive", "backups"))
	if len(entries) != 2 {
		t.Fatalf("on-disk retention %d want 2", len(entries))
	}

	// Not due yet: LastSuccess fresh → no new capture.
	rec2 := m.backups["alice"]
	before := rec2.LastArchive
	m.backupSpace("alice", now.Add(30*time.Minute))
	if m.backups["alice"].LastArchive != before {
		t.Fatal("backup ran before its cadence")
	}
}

func TestBackupSweepDisabled(t *testing.T) {
	m, root := backupTestManager(t)
	m.cfg.BackupEvery = 0
	writeSpaceFile(t, filepath.Join(root, "alice"), "workspace/note.txt", "v")
	m.backupSweep(t.Context(), time.Now())
	if len(m.backups) != 0 {
		t.Fatal("disabled sweep captured a backup")
	}
}
