package vault

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTracked(t *testing.T, root string) map[string]bool {
	t.Helper()
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	tracked := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			tracked[l] = true
		}
	}
	return tracked
}

// R8.4: an owner edit landing on disk outside the governed writer — the OK
// UI path — is swept into vault history as an owner-authored commit so a
// later rollup commit can never absorb it under a system author. Hub runtime
// sidecars and OK's own state never enter history.
func TestUIWriteOwnerProvenance(t *testing.T) {
	_, root := writerFixture(t)
	write := func(rel, body string) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("memory/cards/notes/tea.md", "# owner correction\n")
	write("library/reading.md", "# shelf\n")
	write("runtime/memory/prov.db", "junk")
	write(".ok/state", "junk")
	write(".graph/index", "junk")

	n, err := CommitExternalEdits(root)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 swept paths, got %d", n)
	}
	authors := gitLogAuthors(t, root)
	if authors[0] != "hermes-owner" {
		t.Fatalf("external edits must commit as hermes-owner, got %v", authors)
	}
	tracked := gitTracked(t, root)
	for _, rel := range []string{"memory/cards/notes/tea.md", "library/reading.md"} {
		if !tracked[rel] {
			t.Fatalf("owner file %s not in vault history", rel)
		}
	}
	for _, rel := range []string{"runtime/memory/prov.db", ".ok/state", ".graph/index"} {
		if tracked[rel] {
			t.Fatalf("sidecar %s swept into vault history", rel)
		}
	}
}
