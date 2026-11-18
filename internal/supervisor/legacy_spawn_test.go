package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dangling `generated/passports/current` pointer claims managed status but
// fails to load: runArgsWithGeneration takes the non-managed spawn assembly
// and dies at the config materialization gate instead of producing a runtime
// from empty settings (SPEC-0095).
func TestRunArgsDanglingRevision(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	if err := os.MkdirAll(filepath.Join(root, "generated", "passports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generated", "passports", "current"), []byte("rev-dangling"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(binding(root), "hub-legacy-test", 0, "gen-1")
	if err == nil {
		t.Fatal("dangling revision produced spawn args")
	}
	t.Logf("spawn failure: %v", err)
	if args != nil {
		t.Fatal("args returned alongside error")
	}
	if !strings.Contains(err.Error(), "revision") {
		t.Fatalf("unexpected failure: %v", err)
	}
}
