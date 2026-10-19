package api

import (
	"os"
	"path/filepath"
	"testing"
)

// restoreAuthorizedFile replaces or removes the injected credentials.env.
func TestRestoreAuthorizedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	restoreAuthorizedFile(dir, []byte("A=1\n"), true)
	body, err := os.ReadFile(filepath.Join(dir, "credentials.env"))
	if err != nil || string(body) != "A=1\n" {
		t.Fatalf("restore write: %q %v", body, err)
	}
	restoreAuthorizedFile(dir, nil, false)
	if _, err := os.Lstat(filepath.Join(dir, "credentials.env")); !os.IsNotExist(err) {
		t.Fatal("restore must remove the file")
	}
	restoreAuthorizedFile("", nil, true) // no workspace: no-op
}
