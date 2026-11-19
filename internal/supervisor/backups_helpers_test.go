package supervisor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupEnvParsing(t *testing.T) {
	const dname, iname = "TEST_BACKUP_DUR", "TEST_BACKUP_INT"
	if backupDurationEnv(dname, time.Hour) != time.Hour {
		t.Fatal("unset duration must use fallback")
	}
	t.Setenv(dname, "0")
	if backupDurationEnv(dname, time.Hour) != -1 {
		t.Fatal("0 must mean disabled")
	}
	t.Setenv(dname, "OFF")
	if backupDurationEnv(dname, time.Hour) != -1 {
		t.Fatal("off must mean disabled")
	}
	t.Setenv(dname, "15m")
	if backupDurationEnv(dname, time.Hour) != 15*time.Minute {
		t.Fatal("valid duration not parsed")
	}
	t.Setenv(dname, "bogus")
	if backupDurationEnv(dname, time.Hour) != time.Hour {
		t.Fatal("invalid duration must fall back")
	}
	t.Setenv(dname, "-5m")
	if backupDurationEnv(dname, time.Hour) != time.Hour {
		t.Fatal("negative duration must fall back")
	}
	if backupIntEnv(iname, 3) != 3 {
		t.Fatal("unset int must use fallback")
	}
	t.Setenv(iname, "7")
	if backupIntEnv(iname, 3) != 7 {
		t.Fatal("valid int not parsed")
	}
	t.Setenv(iname, "0")
	if backupIntEnv(iname, 3) != 3 {
		t.Fatal("zero int must fall back")
	}
	t.Setenv(iname, "nope")
	if backupIntEnv(iname, 3) != 3 {
		t.Fatal("invalid int must fall back")
	}
}

func TestBackupErrorClasses(t *testing.T) {
	if backupErrorClass(nil) != "" {
		t.Fatal("nil error must have no class")
	}
	if backupErrorClass(os.ErrNotExist) != "missing_source" {
		t.Fatal("missing source not classified")
	}
	if backupErrorClass(os.ErrPermission) != "unwritable_target" {
		t.Fatal("permission error not classified")
	}
	if backupErrorClass(errors.New("disk full")) != "backup_failed" {
		t.Fatal("generic error not classified")
	}
	if drillErrorClass(nil) != "ok" {
		t.Fatal("nil drill error must be ok")
	}
	if drillErrorClass(errors.New("hash mismatch on MEMORY.md")) != "hash_mismatch" {
		t.Fatal("hash mismatch not classified")
	}
	if drillErrorClass(errors.New("manifest broken")) != "verify_failed" {
		t.Fatal("manifest error not classified")
	}
	if drillErrorClass(errors.New("user mismatch")) != "verify_failed" {
		t.Fatal("user mismatch not classified")
	}
	if drillErrorClass(errors.New("tar exploded")) != "restore_failed" {
		t.Fatal("generic drill error not classified")
	}
	if spaceBackupDir("/spaces/alice") != filepath.Join("/spaces/alice", "archive", "backups") {
		t.Fatal("backup dir escaped the space")
	}
}

func TestBackupStateRoundTripAndFilter(t *testing.T) {
	m, _ := backupTestManager(t)
	if m.backupsPath() != filepath.Join(m.cfg.StateDir, backupsStateFile) {
		t.Fatal("backup state path drifted")
	}
	m.backups["alice"] = BackupRecord{User: "alice", Retained: 2, LastSuccessAt: time.Now().UTC()}
	m.mu.Lock()
	if err := m.persistBackupsLocked(); err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	m.mu.Unlock()
	m.backups = nil
	m.loadBackups()
	if m.backups["alice"].Retained != 2 {
		t.Fatalf("backup record not restored: %+v", m.backups)
	}
	// Corrupt and foreign records must be dropped on load.
	body, _ := json.Marshal([]BackupRecord{{User: "alice", Retained: 1}, {User: "../escape", Retained: 9}})
	if err := os.WriteFile(m.backupsPath(), body, 0600); err != nil {
		t.Fatal(err)
	}
	m.loadBackups()
	if _, ok := m.backups["../escape"]; ok {
		t.Fatal("invalid user record must not load")
	}
	if err := os.WriteFile(m.backupsPath(), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	m.backups = nil
	m.loadBackups()
	if len(m.backups) != 0 {
		t.Fatal("corrupt state must not load")
	}
}
