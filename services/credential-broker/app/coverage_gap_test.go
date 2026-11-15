package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// configuredInstall returns a valid dev Config over a real Init layout so the
// only variable under test is the mutation each case applies.
func configuredInstall(t *testing.T) Config {
	t.Helper()
	root := filepath.Join(t.TempDir(), "install")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	c, err := ReadConfig(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Every store maintenance entry point fails before touching the ledger when
// the master key is unreadable — no partial state, no silent skip.
func TestStoreOpsRefuseMissingMasterKey(t *testing.T) {
	c := configuredInstall(t)
	c.MasterKeyFile = filepath.Join(t.TempDir(), "missing.key")
	if err := MigrateStore(c); err == nil {
		t.Fatal("migrate accepted a missing master key")
	}
	if err := ActivateStore(c); err == nil {
		t.Fatal("activate accepted a missing master key")
	}
	if err := NormalizeStore(c); err == nil {
		t.Fatal("normalize accepted a missing master key")
	}
	if _, err := RetainAudits(c, false); err == nil {
		t.Fatal("audit retention accepted a missing master key")
	}
	if _, err := RetainRecords(c, false, 1024); err == nil {
		t.Fatal("records retention accepted a missing master key")
	}
	if _, err := SweepObjects(c); err == nil {
		t.Fatal("sweep accepted a missing master key")
	}
}

// A recovery layout that reuses the master key as the recovery key is refused
// at every entry point — the escrow is never a copy of the thing it protects.
func TestStoreOpsRefuseEqualRecoveryKey(t *testing.T) {
	c := configuredInstall(t)
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryKeyFile = c.MasterKeyFile
	c.RecoveryIdentity = "installation-one/dev"
	if err := MigrateStore(c); err == nil {
		t.Fatal("migrate accepted the master key as recovery key")
	}
	if err := ActivateStore(c); err == nil {
		t.Fatal("activate accepted the master key as recovery key")
	}
	if err := NormalizeStore(c); err == nil {
		t.Fatal("normalize accepted the master key as recovery key")
	}
	if _, err := RetainAudits(c, false); err == nil {
		t.Fatal("audit retention accepted the master key as recovery key")
	}
	if _, err := RetainRecords(c, false, 1024); err == nil {
		t.Fatal("records retention accepted the master key as recovery key")
	}
	if err := MigrateRecovery(c); err == nil {
		t.Fatal("recovery migration accepted the master key as recovery key")
	}
	if err := MigrateHead(c); err == nil {
		t.Fatal("head migration accepted the master key as recovery key")
	}
}

// Recovery maintenance validates its key inputs independently — a missing
// master key and a missing recovery key are both visible refusals.
func TestRecoveryMigrationsRefuseMissingKeys(t *testing.T) {
	c := configuredInstall(t)
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryIdentity = "installation-one/dev"
	c.RecoveryKeyFile = filepath.Join(t.TempDir(), "recovery.key")
	key := make([]byte, 32)
	for i := range key {
		key[i] = 9
	}
	if err := os.WriteFile(c.RecoveryKeyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	master := c.MasterKeyFile
	c.MasterKeyFile = filepath.Join(t.TempDir(), "missing.key")
	if err := MigrateRecovery(c); err == nil {
		t.Fatal("recovery migration accepted a missing master key")
	}
	if err := MigrateHead(c); err == nil {
		t.Fatal("head migration accepted a missing master key")
	}
	c.MasterKeyFile = master
	c.RecoveryKeyFile = filepath.Join(t.TempDir(), "missing-recovery.key")
	if err := MigrateRecovery(c); err == nil {
		t.Fatal("recovery migration accepted a missing recovery key")
	}
	if err := MigrateHead(c); err == nil {
		t.Fatal("head migration accepted a missing recovery key")
	}
}

// An incomplete or overlapping recovery layout refuses before any store file
// is read — the escrow triad must be three independent paths.
func TestRecoveryLayoutRefusals(t *testing.T) {
	c := configuredInstall(t)
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	if err := MigrateRecovery(c); err == nil {
		t.Fatal("recovery migration accepted an incomplete layout")
	}
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryKeyFile = filepath.Join(t.TempDir(), "recovery.key")
	c.RecoveryIdentity = "installation-one/dev"
	c.RecoveryDir = c.DataDir
	if err := MigrateRecovery(c); err == nil {
		t.Fatal("recovery migration accepted an overlapping layout")
	}
	c.RecoveryDir = "relative/recovery"
	if err := MigrateRecovery(c); err == nil {
		t.Fatal("recovery migration accepted a relative path")
	}
}

// The state witness refuses a directory it cannot trust — an occupied path is
// recovery-required, never silently initialized.
func TestStateWitnessRefusesOccupiedDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	ok, err := readStateWitness(p)
	if ok || !errors.Is(err, ErrStateRecoveryRequired) {
		t.Fatalf("witness over occupied path = %v %v, want ErrStateRecoveryRequired", ok, err)
	}
	ok, err = readStateWitness(filepath.Join(t.TempDir(), "witness"))
	if ok || err != nil {
		t.Fatalf("fresh witness dir = %v %v, want false nil", ok, err)
	}
}

// Init is explicit about its boundary: relative roots and occupied paths
// refuse; re-initialization never overwrites an existing key.
func TestInitRefusals(t *testing.T) {
	if err := Init("relative/install"); err == nil {
		t.Fatal("init accepted a relative root")
	}
	root := filepath.Join(t.TempDir(), "install")
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if err := Init(root); err == nil {
		t.Fatal("init overwrote an existing installation")
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.MkdirAll(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "keys"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(blocked); err == nil {
		t.Fatal("init accepted an occupied keys path")
	}
}
