package migration

import (
	"os"
	"path/filepath"
	"testing"
)

// The spool substrate migration imports record files into spool.db and
// leaves a verified manifest; activation flips the marker and retires the
// files into substrate-backup/.
func TestMigrateSpoolSubstrateImportsRecords(t *testing.T) {
	spool := t.TempDir()
	pending := filepath.Join(spool, "pending")
	if err := os.MkdirAll(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, "job-m-1.json"), []byte(`{"id":"job-m-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := MigrateSpoolSubstrate(spool)
	if err != nil {
		t.Fatal(err)
	}
	if report.Records != 1 {
		t.Fatalf("imported %d records", report.Records)
	}
	if _, err := os.Lstat(filepath.Join(spool, "spool.db")); err != nil {
		t.Fatal("spool.db missing after import")
	}
	if _, err := os.Lstat(filepath.Join(spool, "spool.db.manifest.json")); err != nil {
		t.Fatal("verified migration manifest missing")
	}
}

// Activation requires the verified manifest, then flips the marker and
// retires record files so the gateway serves them from spool.db.
func TestActivateSpoolSubstrateFlipsAndRetires(t *testing.T) {
	spool := t.TempDir()
	if _, err := ActivateSpoolSubstrate(spool); err == nil {
		t.Fatal("activation ran without the verified manifest")
	}
	pending := filepath.Join(spool, "pending")
	if err := os.MkdirAll(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, "job-a-1.json"), []byte(`{"id":"job-a-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateSpoolSubstrate(spool); err != nil {
		t.Fatal(err)
	}
	report, err := ActivateSpoolSubstrate(spool)
	if err != nil {
		t.Fatal(err)
	}
	if report.Retired != 1 {
		t.Fatalf("retired %d files", report.Retired)
	}
	if _, err := os.Lstat(filepath.Join(spool, "records.activated")); err != nil {
		t.Fatal("activation marker missing")
	}
	if _, err := os.Lstat(filepath.Join(spool, "substrate-backup", "pending", "job-a-1.json")); err != nil {
		t.Fatal("retired record missing from substrate-backup")
	}
}
