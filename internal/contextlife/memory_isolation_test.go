package contextlife

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryOperationsCannotTraverseForeignHome(t *testing.T) {
	root := t.TempDir()
	alice := filepath.Join(root, "alice")
	bob := filepath.Join(root, "bob", "hermes", "memories")
	if err := os.MkdirAll(filepath.Join(alice, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bob, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bob, "private.md"), []byte("bob-private-memory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bob, filepath.Join(alice, "hermes", "memories")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if names, err := MemoryFiles(alice); err == nil && len(names) > 0 {
		t.Errorf("foreign memory names exposed: %v", names)
	}
	if err := DeleteMemory(alice, "private.md"); err == nil {
		t.Error("foreign memory delete accepted")
	}
	if body, err := os.ReadFile(filepath.Join(bob, "private.md")); err != nil || string(body) != "bob-private-memory" {
		t.Errorf("foreign memory changed: error=%v", err)
	}
}
