package credstore

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The local backend keeps records owner-scoped and key-bound: revisions bump,
// vault locators carry their prefix, rotation re-encrypts and rejects the old
// key, and backup/restore move ciphertext only.
func TestLocalBackendBranches(t *testing.T) {
	dir := t.TempDir()
	key, err := GenerateKey()
	if err != nil || len(key) != KeySize {
		t.Fatalf("GenerateKey: %v", err)
	}
	path := filepath.Join(dir, "store", "creds.json")
	store, err := Open(Options{Path: path, Key: key, Backend: BackendVault})
	if err != nil {
		t.Fatal(err)
	}
	if store.Name() != BackendVault {
		t.Fatalf("backend: %q", store.Name())
	}
	locator, err := store.NewLocator()
	if err != nil || !strings.HasPrefix(locator, "vault-") {
		t.Fatalf("vault locator: %q %v", locator, err)
	}
	if err := store.Put(locator, "alice", map[string]string{"TOKEN": "one"}); err != nil {
		t.Fatal(err)
	}
	info, err := store.Info(locator, "alice")
	if err != nil || info.Revision != 1 || info.Status != StatusActive {
		t.Fatalf("info: %+v %v", info, err)
	}
	if err := store.Put(locator, "alice", map[string]string{"TOKEN": "two"}); err != nil {
		t.Fatal(err)
	}
	info, _ = store.Info(locator, "alice")
	if info.Revision != 2 {
		t.Fatalf("revision not bumped: %+v", info)
	}
	if err := store.Put(locator, "bob", map[string]string{"TOKEN": "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign overwrite: %v", err)
	}
	if _, err := store.Get(locator, "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign read: %v", err)
	}
	if _, err := store.Get("loc-missing", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing locator: %v", err)
	}
	if err := store.SetStatus(locator, "alice", "bogus"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: %v", err)
	}
	if err := store.SetStatus(locator, "alice", StatusRevoked); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(locator, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked read: %v", err)
	}
	if err := store.SetStatus(locator, "alice", StatusActive); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTerminalExposure(locator, "alice", true); err != nil {
		t.Fatal(err)
	}
	info, _ = store.Info(locator, "alice")
	if !info.TerminalExposure {
		t.Fatal("terminal exposure not recorded")
	}
	list, err := store.List("alice")
	if err != nil || len(list) != 1 || list[0].Locator != locator {
		t.Fatalf("list: %+v %v", list, err)
	}
	list, err = store.List("nobody")
	if err != nil || len(list) != 0 {
		t.Fatalf("foreign list: %+v %v", list, err)
	}

	var backup bytes.Buffer
	if err := store.Backup(&backup); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(backup.Bytes(), []byte("two")) {
		t.Fatal("backup leaked plaintext")
	}
	path2 := filepath.Join(dir, "store2", "creds.json")
	restored, err := Open(Options{Path: path2, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(bytes.NewReader(backup.Bytes())); err != nil {
		t.Fatal(err)
	}
	values, err := restored.Get(locator, "alice")
	if err != nil || values["TOKEN"] != "two" {
		t.Fatalf("restored values: %v %v", values, err)
	}
	if err := restored.Restore(strings.NewReader("{")); err == nil {
		t.Fatal("corrupt backup restored")
	}
	if err := restored.Restore(bytes.NewReader(bytes.Repeat([]byte("x"), 8<<20+8))); err == nil {
		t.Fatal("oversized backup restored")
	}

	// Rotation re-encrypts under the new key and binds it durably.
	newKey, _ := GenerateKey()
	if err := restored.RotateKey(newKey); err != nil {
		t.Fatal(err)
	}
	if values, err := restored.Get(locator, "alice"); err != nil || values["TOKEN"] != "two" {
		t.Fatalf("post-rotate read: %v %v", values, err)
	}
	if err := restored.RotateKey([]byte("short")); !errors.Is(err, ErrKey) {
		t.Fatalf("short rotate key: %v", err)
	}
	if _, err := Open(Options{Path: path2, Key: key}); !errors.Is(err, ErrStale) {
		t.Fatalf("old key after rotation: %v", err)
	}

	// Key material may come from a key file outside the store directory.
	keyFile := filepath.Join(dir, "key", "cred.key")
	if err := WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	byFile, err := Open(Options{Path: filepath.Join(dir, "store3", "creds.json"), KeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	if l, err := byFile.NewLocator(); err != nil || !strings.HasPrefix(l, "loc-") {
		t.Fatalf("local locator: %q %v", l, err)
	}
	if _, err := Open(Options{Path: filepath.Join(dir, "s4", "creds.json"), Key: key, Backend: "memcached"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown backend: %v", err)
	}
	if _, err := Open(Options{Path: "relative/creds.json", Key: key}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative path: %v", err)
	}
	if _, err := Open(Options{Path: filepath.Join(dir, "s5", "creds.json")}); !errors.Is(err, ErrKey) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := Open(Options{Path: filepath.Join(dir, "s6", "creds.json"), Key: []byte("short")}); !errors.Is(err, ErrKey) {
		t.Fatalf("short key: %v", err)
	}
	if err := store.Delete(locator, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Info(locator, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted locator: %v", err)
	}
}
