package vault

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/memory"
)

type pendingRow struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

func readPending(t *testing.T, root string) []pendingRow {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "runtime", "memory", "truth_pending.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []pendingRow
	for s := bufio.NewScanner(f); s.Scan(); {
		var r pendingRow
		if err := json.Unmarshal(s.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	return rows
}

func writerFixture(t *testing.T) (*Writer, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "vault")
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	return NewWriter(root), root
}

func TestWriterReadConfined(t *testing.T) {
	w, _ := writerFixture(t)
	for _, path := range []string{"../outside", "runtime/memory/requests/x.json"} {
		if _, _, err := w.Read(path); err == nil {
			t.Fatalf("read escaped governed zones: %q", path)
		}
	}
	if _, digest, err := w.Read("memory/cards/notes/new.md"); err != nil || digest != "" {
		t.Fatalf("missing governed card: digest=%q err=%v", digest, err)
	}
}

// A stale WantSHA rejects the write and leaves the on-disk (owner) version
// untouched — the owner always wins (SPEC-0085 R1.4).
func TestWriterStaleHashRejected(t *testing.T) {
	w, root := writerFixture(t)
	path := "memory/daily/2026-10-31.md"
	if _, err := w.Apply(memory.WriteOp{Path: path, Content: []byte("# owner\n"), WantSHA: "", Actor: memory.ActorOwner}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Apply(memory.WriteOp{Path: path, Content: []byte("# clobber\n"), WantSHA: "deadbeef", Actor: memory.ActorAgent}); !errors.Is(err, memory.ErrStaleHash) {
		t.Fatalf("stale write must be rejected, got %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "# owner\n" {
		t.Fatalf("rejected write still clobbered the file: %q", body)
	}
}

// When an owner's direct edit wins over an agent write, the conflict is
// flagged in a writer-owned sidecar as truth_pending — never as user-visible
// markdown inside the vault content (SPEC-0085 R1.4).
func TestTruthPendingOnOwnerConflict(t *testing.T) {
	w, root := writerFixture(t)
	path := "memory/cards/notes/x.md"
	first, err := w.Apply(memory.WriteOp{Path: path, Content: []byte("# agent draft\n"), WantSHA: "", Actor: memory.ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	// The owner edits the file directly — the authoritative change.
	if err := os.WriteFile(filepath.Join(root, path), []byte("# owner correction\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The agent's retry against its stale hash loses and is flagged.
	if _, err := w.Apply(memory.WriteOp{Path: path, Content: []byte("# agent rewrite\n"), WantSHA: first.SHA256, Actor: memory.ActorAgent}); !errors.Is(err, memory.ErrStaleHash) {
		t.Fatalf("stale agent write must be rejected, got %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || string(body) != "# owner correction\n" {
		t.Fatalf("owner version lost: %q %v", body, err)
	}
	pending := readPending(t, root)
	if len(pending) != 1 || pending[0].Path != path || pending[0].Status != "truth_pending" {
		t.Fatalf("conflict not flagged truth_pending: %+v", pending)
	}
}

// Every governed write lands as a git commit authored by its actor class —
// owner, agent, rollup, UI or system — so the vault history attributes who
// changed what (SPEC-0085 R1.5).
func TestWriterScopedCommits(t *testing.T) {
	w, root := writerFixture(t)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/daily/2026-11-01.md", Content: []byte("# a\n"), WantSHA: "", Actor: memory.ActorRollup}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Apply(memory.WriteOp{Path: "memory/cards/notes/y.md", Content: []byte("# b\n"), WantSHA: "", Actor: memory.ActorAgent}); err != nil {
		t.Fatal(err)
	}
	authors := gitLogAuthors(t, root)
	if len(authors) < 2 || authors[0] != "hermes-agent" || authors[1] != "hermes-rollup" {
		t.Fatalf("commits not actor-attributed: %v", authors)
	}
	// A rejected write creates a runtime sidecar; it must never be committed.
	_, _ = w.Apply(memory.WriteOp{Path: "memory/daily/2026-11-01.md", Content: []byte("x"), WantSHA: "stale", Actor: memory.ActorAgent})
	if _, err := os.Stat(filepath.Join(root, "runtime")); err == nil {
		cmd := exec.Command("git", "ls-files", "runtime")
		cmd.Dir = root
		out, _ := cmd.Output()
		if len(out) != 0 {
			t.Fatalf("runtime sidecar committed into the vault repo: %s", out)
		}
	}
}

// Governed writes are confined to the vault root and to the whitelisted
// zones — daily/, cards/, summaries/ and CORE.md. Schema, sidecars, indexes
// and every path outside the whitelist are refused, as are traversal,
// absolute paths and symlink components (SPEC-0085 R1.4/R1.6).
func TestWriterZoneConfined(t *testing.T) {
	w, root := writerFixture(t)
	for _, path := range []string{
		"memory/daily/2026-11-02.md", "memory/cards/notes/y.md",
		"memory/summaries/daily/z.md",
	} {
		if _, err := w.Apply(memory.WriteOp{Path: path, Content: []byte("x"), WantSHA: "", Actor: memory.ActorAgent}); err != nil {
			t.Fatalf("governed write to %q rejected: %v", path, err)
		}
	}
	// CORE.md exists from the scaffold — a governed write supplies its hash.
	if _, err := w.Apply(memory.WriteOp{Path: "memory/CORE.md", Content: []byte("# core\n"), WantSHA: sha256Hex(nil), Actor: memory.ActorRollup}); err != nil {
		t.Fatalf("governed write to CORE.md rejected: %v", err)
	}
	for _, path := range []string{
		"memory/schema.json", "memory/CORE.history.md", ".gitignore", ".okignore",
		"runtime/memory/x", "memory/evil.md", "memory/attachments/a.bin", "memory/.graph/g",
	} {
		if _, err := w.Apply(memory.WriteOp{Path: path, Content: []byte("x"), WantSHA: "", Actor: memory.ActorAgent}); err == nil {
			t.Fatalf("write outside governed zones accepted: %q", path)
		}
	}
	for _, path := range []string{"../escape.md", "memory/../../escape.md", "/abs.md"} {
		if _, err := w.Apply(memory.WriteOp{Path: path, Content: []byte("x"), WantSHA: "", Actor: memory.ActorAgent}); err == nil {
			t.Fatalf("escaping write accepted: %q", path)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "memory", "daily", "link")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := w.Apply(memory.WriteOp{Path: "memory/daily/link/evil.md", Content: []byte("x"), WantSHA: "", Actor: memory.ActorAgent}); err == nil {
			t.Fatal("write through a symlink component accepted")
		}
	}
}

// A governed write into a vault whose git repo vanished surfaces the failure
// — history attribution is part of the write contract, never best-effort.
func TestWriterWithoutGitRepo(t *testing.T) {
	root := t.TempDir()
	w := NewWriter(root)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/daily/2026-11-03.md", Content: []byte("x"), WantSHA: "", Actor: memory.ActorAgent}); err == nil {
		t.Fatal("write without a git repo must fail")
	}
}

func gitLogAuthors(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%an")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		lines = append(lines, strings.TrimSpace(l))
	}
	return lines
}
