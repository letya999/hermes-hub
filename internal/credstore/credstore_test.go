package credstore

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func testValue(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 24)
	for i, b := range raw {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return "v-" + string(out)
}

func openStore(t *testing.T, backend string) (Backend, string, []byte) {
	t.Helper()
	key := testKey(t)
	root := t.TempDir()
	path := filepath.Join(root, "store.enc")
	store, err := Open(Options{Path: path, Key: key, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	return store, path, key
}

func TestPutGetRoundTripAndOmitsPlaintext(t *testing.T) {
	store, path, _ := openStore(t, BackendLocal)
	secret := testValue(t)
	locator, err := store.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(locator, "alice")
	if err != nil || got["GOOGLE_TOKEN"] != secret {
		t.Fatalf("round trip=%v err=%v", got, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatal("plaintext written to ciphertext store")
	}
	var snap map[string]any
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if _, ok := snap["records"]; !ok || strings.Contains(strings.ToLower(string(body)), secret) {
		t.Fatal("store snapshot missing records or leaked value")
	}
}

func TestCrossUserStaleRemovedAndRestart(t *testing.T) {
	store, path, key := openStore(t, BackendLocal)
	secret := testValue(t)
	locator, err := store.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(locator, "alice", map[string]string{"SLACK_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(locator, "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user get: %v", err)
	}
	if _, err := store.Get("loc-missing000000000000000000000000", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing locator: %v", err)
	}
	if err := store.SetStatus(locator, "alice", StatusRevoked); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(locator, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked locator still readable: %v", err)
	}
	if err := store.Delete(locator, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(locator, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed locator still readable: %v", err)
	}
	reopened, err := Open(Options{Path: path, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Get(locator, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restart revived removed locator: %v", err)
	}
	locator2, err := reopened.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Put(locator2, "alice", map[string]string{"SLACK_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	again, err := Open(Options{Path: path, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Get(locator2, "alice")
	if err != nil || got["SLACK_TOKEN"] != secret {
		t.Fatalf("post-restart get=%v err=%v", got, err)
	}
}

func TestBackupRestoreAndKeyRotation(t *testing.T) {
	store, path, key := openStore(t, BackendLocal)
	secret := testValue(t)
	locator, err := store.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(locator, "alice", map[string]string{"JIRA_API_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	var backup bytes.Buffer
	if err := store.Backup(&backup); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(backup.String(), secret) || strings.Contains(backup.String(), hexKey(key)) {
		t.Fatal("backup contained plaintext or encryption key")
	}
	if err := store.Delete(locator, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(bytes.NewReader(backup.Bytes())); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(locator, "alice")
	if err != nil || got["JIRA_API_TOKEN"] != secret {
		t.Fatalf("restore get=%v err=%v", got, err)
	}
	newKey := testKey(t)
	if err := store.RotateKey(newKey); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Path: path, Key: key}); !errors.Is(err, ErrStale) {
		t.Fatalf("old key still opened store: %v", err)
	}
	rotated, err := Open(Options{Path: path, Key: newKey})
	if err != nil {
		t.Fatal(err)
	}
	got, err = rotated.Get(locator, "alice")
	if err != nil || got["JIRA_API_TOKEN"] != secret {
		t.Fatalf("after rotation get=%v err=%v", got, err)
	}
}

func TestVaultSeamDoesNotChangeGetContract(t *testing.T) {
	local, _, _ := openStore(t, BackendLocal)
	vault, _, _ := openStore(t, BackendVault)
	if local.Name() != BackendLocal || vault.Name() != BackendVault {
		t.Fatal(local.Name(), vault.Name())
	}
	secret := testValue(t)
	for _, store := range []Backend{local, vault} {
		locator, err := store.NewLocator()
		if err != nil {
			t.Fatal(err)
		}
		if store.Name() == BackendVault && !strings.HasPrefix(locator, "vault-") {
			t.Fatalf("vault locator=%s", locator)
		}
		if err := store.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": secret}); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(locator, "alice")
		if err != nil || got["GOOGLE_TOKEN"] != secret {
			t.Fatalf("backend %s get=%v err=%v", store.Name(), got, err)
		}
		if _, err := store.Get(locator, "bob"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("backend %s cross-user: %v", store.Name(), err)
		}
	}
}

func TestListOmitsValuesAndTerminalExposureReverses(t *testing.T) {
	store, path, _ := openStore(t, "")
	secret := testValue(t)
	locator, err := store.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(locator, "alice", map[string]string{"GITHUB_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTerminalExposure(locator, "alice", true); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List("alice")
	if err != nil || len(listed) != 1 || listed[0].Names[0] != "GITHUB_TOKEN" || !listed[0].TerminalExposure {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	bob, err := store.List("bob")
	if err != nil || len(bob) != 0 {
		t.Fatalf("cross-user list=%+v err=%v", bob, err)
	}
	if err := store.SetTerminalExposure(locator, "alice", false); err != nil {
		t.Fatal(err)
	}
	info, err := store.Info(locator, "alice")
	if err != nil || info.TerminalExposure {
		t.Fatalf("terminal exposure was not reversed: %+v err=%v", info, err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), secret) {
		t.Fatal("list path leaked plaintext into store")
	}
}

func TestRejectsKeyInsideStoreAndInvalidInputs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.enc")
	keyPath := filepath.Join(root, "key")
	key := testKey(t)
	if err := WriteKeyFile(keyPath, key); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Path: path, KeyFile: keyPath}); err == nil {
		t.Fatal("key inside store directory accepted")
	}
	outside := filepath.Join(t.TempDir(), "key")
	if err := WriteKeyFile(outside, key); err != nil {
		t.Fatal(err)
	}
	store, err := Open(Options{Path: path, KeyFile: outside})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Path: path, Key: []byte("short")}); !errors.Is(err, ErrKey) {
		t.Fatalf("short key: %v", err)
	}
	if err := store.Put("bad=locator", "alice", map[string]string{"GOOGLE_TOKEN": "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("locator with = accepted: %v", err)
	}
	if err := store.Put("loc-ok", "", map[string]string{"GOOGLE_TOKEN": "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty owner accepted: %v", err)
	}
	locator, err := store.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(locator, "alice", map[string]string{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty values accepted: %v", err)
	}
	if _, err := Open(Options{Path: "relative.enc", Key: key}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative path accepted: %v", err)
	}
}

func TestEnvKeyCorruptFileAndDegradedGet(t *testing.T) {
	key := testKey(t)
	t.Setenv("HUB_CREDENTIAL_KEY", hexKey(key))
	t.Setenv("HUB_CREDENTIAL_KEY_FILE", "")
	path := filepath.Join(t.TempDir(), "store.enc")
	store, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	secret := testValue(t)
	locator, err := store.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(locator, "alice", StatusDegraded); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(locator, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("degraded get: %v", err)
	}
	if err := store.SetStatus(locator, "alice", "nope"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid status: %v", err)
	}
	if _, err := Open(Options{Path: filepath.Join(t.TempDir(), "x.enc"), Key: key, Backend: "s3"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown backend: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":1,"key_id":"x"} extra`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Path: path, Key: key}); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
}

func TestParseRawKeyAndOwnerMismatch(t *testing.T) {
	key := testKey(t)
	keyFile := filepath.Join(t.TempDir(), "raw.key")
	if err := os.MkdirAll(filepath.Dir(keyFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "store.enc"), KeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	locator, err := store.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": testValue(t)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(locator, "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user delete: %v", err)
	}
	if err := store.SetTerminalExposure(locator, "bob", true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user expose: %v", err)
	}
	if err := store.Put("loc-ok", "alice", map[string]string{"bad-name": "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid name: %v", err)
	}
}

func TestGenerateKeyAndEnvKeyFile(t *testing.T) {
	key, err := GenerateKey()
	if err != nil || validateKey(key) != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "k")
	if err := WriteKeyFile(path, key); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_CREDENTIAL_KEY_FILE", path)
	t.Setenv("HUB_CREDENTIAL_KEY", "")
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "store.enc")})
	if err != nil {
		t.Fatal(err)
	}
	locator, err := store.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	secret := testValue(t)
	if err := store.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(locator, "alice")
	if err != nil || got["GOOGLE_TOKEN"] != secret {
		t.Fatal(got, err)
	}
}

func hexKey(key []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(key)*2)
	for i, b := range key {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out)
}

func TestReloadMissingFileTamperAndInvalidInputs(t *testing.T) {
	store, path, key := openStore(t, BackendLocal)
	secret := testValue(t)
	locator, err := store.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": secret}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(locator, "alice")
	if err != nil || got["GOOGLE_TOKEN"] != secret {
		t.Fatalf("recreated store get=%v err=%v", got, err)
	}
	local := store.(*Local)
	local.mu.Lock()
	record := local.state.Records[locator]
	nonce, ciphertext, err := encrypt(local.key, locator, "alice", []byte("[]"))
	if err != nil {
		local.mu.Unlock()
		t.Fatal(err)
	}
	record.Nonce = nonce
	record.Ciphertext = ciphertext
	local.state.Records[locator] = record
	if err := local.persistLocked(); err != nil {
		local.mu.Unlock()
		t.Fatal(err)
	}
	local.mu.Unlock()
	if _, err := store.Get(locator, "alice"); !errors.Is(err, ErrStale) {
		t.Fatalf("non-object payload: %v", err)
	}
	local.mu.Lock()
	record = local.state.Records[locator]
	record.Nonce = "!!!"
	local.state.Records[locator] = record
	if err := local.persistLocked(); err != nil {
		local.mu.Unlock()
		t.Fatal(err)
	}
	local.mu.Unlock()
	if _, err := store.Get(locator, "alice"); !errors.Is(err, ErrStale) {
		t.Fatalf("bad nonce: %v", err)
	}
	if err := store.SetStatus("loc-missing00000000000000000000", "alice", StatusActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing status: %v", err)
	}
	if err := store.SetStatus(locator, "bob", StatusActive); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user status: %v", err)
	}
	if err := store.SetTerminalExposure("loc-missing00000000000000000000", "alice", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing expose: %v", err)
	}
	if _, err := store.List(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty owner list: %v", err)
	}
	if err := store.Put(locator, "alice", map[string]string{"1BAD": "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("name starting with digit: %v", err)
	}
	if err := store.Put(locator, "alice", map[string]string{"BAD-NAME": "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("name with hyphen: %v", err)
	}
	tooBig := make([]byte, 8<<20+1)
	if err := store.Restore(bytes.NewReader(tooBig)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("huge restore: %v", err)
	}
	if err := store.Restore(bytes.NewReader([]byte(`{"schema":0,"key_id":"x"}`))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad schema restore: %v", err)
	}
	if _, err := parseKey([]byte(strings.Repeat("gg", KeySize))); !errors.Is(err, ErrKey) {
		t.Fatalf("invalid hex key: %v", err)
	}
	if _, err := parseKey([]byte("abcd")); !errors.Is(err, ErrKey) {
		t.Fatalf("short key: %v", err)
	}
	if err := WriteKeyFile(filepath.Join(t.TempDir(), "k"), []byte("short")); !errors.Is(err, ErrKey) {
		t.Fatalf("short write key: %v", err)
	}
	t.Setenv("HUB_CREDENTIAL_KEY", "")
	t.Setenv("HUB_CREDENTIAL_KEY_FILE", "")
	if _, err := Open(Options{Path: filepath.Join(t.TempDir(), "empty.enc")}); !errors.Is(err, ErrKey) {
		t.Fatalf("missing key: %v", err)
	}
	_ = key
}
