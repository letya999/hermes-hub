package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CoreBlock re-reads memory/CORE.md on every call — no caching layer may
// serve a stale snapshot (SPEC-0085 R1.6).
func TestCoreBlockRereads(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	corePath := filepath.Join(root, "memory", "CORE.md")
	body := "# CORE\n\n## Directives\n- prefer mornings\n\n## Facts\n- owner is left-handed\n"
	if err := os.WriteFile(corePath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	block := CoreBlock(root)
	for _, want := range []string{"## Directives", "prefer mornings", "## Facts", "left-handed"} {
		if !strings.Contains(block, want) {
			t.Fatalf("core block missing %q: %q", want, block)
		}
	}
	// An owner edit is visible on the very next read — nothing is cached.
	if err := os.WriteFile(corePath, []byte("# CORE\n\n## Directives\n- evenings now\n\n## Facts\n"), 0600); err != nil {
		t.Fatal(err)
	}
	block = CoreBlock(root)
	if !strings.Contains(block, "evenings now") || strings.Contains(block, "prefer mornings") {
		t.Fatalf("stale or missing re-read: %q", block)
	}
}

// The injected CORE block is capped at 3600 characters regardless of how much
// curated content accrues (SPEC-0085 R1.6).
func TestCoreCapTruncates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	big := "# CORE\n\n## Directives\n" + strings.Repeat("- d\n", 2000) + "\n## Facts\n" + strings.Repeat("- f\n", 2000)
	if err := os.WriteFile(filepath.Join(root, "memory", "CORE.md"), []byte(big), 0600); err != nil {
		t.Fatal(err)
	}
	if got := len(CoreBlock(root)); got > 3600 {
		t.Fatalf("core block exceeds the 3600-char cap: %d", got)
	}
}
