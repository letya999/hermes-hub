package vault

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// OpenScopeMaterial loads the vault's scope-mint material from
// runtime/memory/scope.key, generating 32 random bytes on first open. The
// file lives in the hub-owned sidecar zone — the space never sees it, and
// MintScope(material, env, principal, audience) is the only bank-id input a
// replay uses, so provider scopes are opaque and non-derivable
// (SPEC-0085 R11.4, ADR-0086 D15).
func OpenScopeMaterial(root string) ([]byte, error) {
	path := filepath.Join(root, "runtime", "memory", "scope.key")
	if b, err := os.ReadFile(path); err == nil {
		return b, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return b, nil
}
