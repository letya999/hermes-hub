package provider

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverableLocalReadsEncryptedBackupAfterPrimaryLoss(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "primary")
	recovery := filepath.Join(t.TempDir(), "recovery")
	p, err := NewRecoverableLocal("local", primary, recovery, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secret := Bundle{"token": []byte("synthetic-provider-canary")}
	ref, err := p.Write(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(recovery, ref.Locator))
	if err != nil || bytes.Contains(backup, secret["token"]) {
		t.Fatal("recovery object absent or plaintext")
	}
	if err := os.Remove(filepath.Join(primary, ref.Locator)); err != nil {
		t.Fatal(err)
	}
	got, err := p.Read(context.Background(), ref)
	if err != nil || !bytes.Equal(got["token"], secret["token"]) {
		t.Fatal("original locator was not recovered")
	}
	got.Wipe()
}

func TestRecoverableLocalDeleteCannotReadBackup(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "primary")
	recovery := filepath.Join(t.TempDir(), "recovery")
	p, err := NewRecoverableLocal("local", primary, recovery, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := p.Write(context.Background(), Bundle{"token": []byte("synthetic-provider-canary")})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Read(context.Background(), ref); err != ErrNotFound {
		t.Fatalf("deleted object recovered from backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recovery, ref.Locator)); !os.IsNotExist(err) {
		t.Fatal("deleted recovery ciphertext remains")
	}
}

func TestRecoverableLocalBacksUpExistingObject(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "primary")
	recovery := filepath.Join(t.TempDir(), "recovery")
	key := bytes.Repeat([]byte{1}, 32)
	legacy, err := NewLocal("local", primary, key)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := legacy.Write(context.Background(), Bundle{"token": []byte("synthetic-provider-canary")})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewRecoverableLocal("local", primary, recovery, key, bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.BackupExisting(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err := p.BackupExisting(context.Background(), ref); err != nil {
		t.Fatalf("migration retry failed: %v", err)
	}
	if err := os.Remove(filepath.Join(primary, ref.Locator)); err != nil {
		t.Fatal(err)
	}
	got, err := p.Read(context.Background(), ref)
	if err != nil || !bytes.Equal(got["token"], []byte("synthetic-provider-canary")) {
		t.Fatal("migrated provider object unavailable by original ref")
	}
	got.Wipe()
}

func TestRecoverableLocalRejectsWrongKeyAndCorruptBackup(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "primary")
	recovery := filepath.Join(t.TempDir(), "recovery")
	key := bytes.Repeat([]byte{1}, 32)
	p, err := NewRecoverableLocal("local", primary, recovery, key, bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := p.Write(context.Background(), Bundle{"token": []byte("synthetic-provider-canary")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(primary, ref.Locator)); err != nil {
		t.Fatal(err)
	}
	wrong, err := NewRecoverableLocal("local", primary, recovery, key, bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Read(context.Background(), ref); err != ErrUnavailable {
		t.Fatal("wrong recovery key accepted")
	}
	if err := os.WriteFile(filepath.Join(recovery, ref.Locator), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Read(context.Background(), ref); err != ErrUnavailable {
		t.Fatal("corrupt recovery ciphertext accepted")
	}
}

func TestRecoverableLocalMigrationRejectsExistingCorruptBackup(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "primary")
	recovery := filepath.Join(t.TempDir(), "recovery")
	key := bytes.Repeat([]byte{1}, 32)
	legacy, err := NewLocal("local", primary, key)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := legacy.Write(context.Background(), Bundle{"token": []byte("synthetic-provider-canary")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRecoverableLocal("local", primary, recovery, key, []byte("short")); err == nil {
		t.Fatal("invalid recovery key accepted")
	}
	p, err := NewRecoverableLocal("local", primary, recovery, key, bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recovery, ref.Locator), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.BackupExisting(context.Background(), ref); err != ErrUnavailable {
		t.Fatal("corrupt preexisting backup silently replaced")
	}
}
