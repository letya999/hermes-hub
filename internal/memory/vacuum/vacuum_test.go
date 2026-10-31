package vacuum

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// fixture builds a vault with one superseded card, one struck leaf entry and
// live content that must survive.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	daily, err := vault.OpenDaily(root, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 4, 19, 15, 0, 0, 0, time.UTC)
	for _, e := range []memory.Entry{
		{ID: "doomed-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate, Body: "obsolete supplier name", ObservedAt: at},
		{ID: "live-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate, Body: "current supplier name", ObservedAt: at},
	} {
		if err := daily.Append(e, "test"); err != nil {
			t.Fatal(err)
		}
	}
	w := vault.NewWriter(root)
	for _, c := range []struct{ rel, body string }{
		{"memory/cards/notes/old-card.md", "---\ntype: note\nstatus: superseded\ntags: []\n---\n\n## Facts\n\n- stale fact\n"},
		{"memory/cards/notes/live-card.md", "---\ntype: note\nstatus: active\ntags: []\n---\n\n## Facts\n\n- live fact\n"},
	} {
		if _, err := w.Apply(memory.WriteOp{Path: c.rel, Content: []byte(c.body), WantSHA: "", Actor: memory.ActorSystem}); err != nil {
			t.Fatal(err)
		}
	}
	if err := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Strike("doomed-1"); err != nil {
		t.Fatal(err)
	}
	return root
}

// R7.4: dry-run reports exactly what would be pruned and mutates nothing.
func TestVacuumDryRunAccurate(t *testing.T) {
	root := fixture(t)
	rep, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.SupersededCards) != 1 || rep.SupersededCards[0] != "memory/cards/notes/old-card.md" {
		t.Fatalf("superseded cards = %v", rep.SupersededCards)
	}
	if len(rep.StruckEntries) != 1 || rep.StruckEntries[0] != "doomed-1" {
		t.Fatalf("struck entries = %v", rep.StruckEntries)
	}
	if len(rep.LeafRewrites) != 1 || rep.LeafRewrites[0] != "memory/daily/2026-04-19.md" {
		t.Fatalf("leaf rewrites = %v", rep.LeafRewrites)
	}
	if rep.ProvDropped != 1 {
		t.Fatalf("prov dropped = %d, want 1", rep.ProvDropped)
	}
	// Nothing changed on disk.
	if _, err := os.Stat(filepath.Join(root, "memory", "cards", "notes", "old-card.md")); err != nil {
		t.Fatal("dry-run deleted the superseded card")
	}
	leaf, _ := os.ReadFile(filepath.Join(root, "memory", "daily", "2026-04-19.md"))
	if !strings.Contains(string(leaf), "mem:id=doomed-1") {
		t.Fatal("dry-run stripped the struck block")
	}
}

// R7.4: apply purges only superseded/struck content — the live card, the live
// leaf block and its prov row survive; struck content is gone from the working
// tree (recoverable in git history).
func TestVacuumKeepsLiveContent(t *testing.T) {
	root := fixture(t)
	rep, err := Apply(root)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Applied {
		t.Fatal("apply report must mark applied")
	}
	// Purged: superseded card, struck block, struck prov row.
	if _, err := os.Stat(filepath.Join(root, "memory", "cards", "notes", "old-card.md")); !os.IsNotExist(err) {
		t.Fatal("superseded card survived vacuum")
	}
	leaf, _ := os.ReadFile(filepath.Join(root, "memory", "daily", "2026-04-19.md"))
	if strings.Contains(string(leaf), "mem:id=doomed-1") || strings.Contains(string(leaf), "obsolete supplier") {
		t.Fatal("struck block survived vacuum")
	}
	prov, err := memory.OpenProvDB(filepath.Join(root, "runtime", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := prov.Entries()
	if len(entries) != 1 || entries[0].ID != "live-1" {
		t.Fatalf("prov.db after vacuum: %v", entries)
	}
	// Untouched: live card and live leaf block.
	if _, err := os.Stat(filepath.Join(root, "memory", "cards", "notes", "live-card.md")); err != nil {
		t.Fatal("live card deleted")
	}
	if !strings.Contains(string(leaf), "mem:id=live-1") || !strings.Contains(string(leaf), "current supplier") {
		t.Fatal("live block stripped")
	}
	// Forget sidecar retains the strike record — audit trail survives.
	if !memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Set()["doomed-1"] {
		t.Fatal("forget sidecar lost the strike record")
	}
}

// R7.4: vacuum re-reads the leaf at apply time — an owner edit that lands
// before the purge survives inside the rewrite (the governed write is
// hash-checked against that fresh read).
func TestVacuumKeepsConcurrentEdit(t *testing.T) {
	root := fixture(t)
	leaf := filepath.Join(root, "memory", "daily", "2026-04-19.md")
	cur, err := os.ReadFile(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaf, append(cur, []byte("\n## owner note\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(root); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(leaf)
	if !strings.Contains(string(body), "owner note") {
		t.Fatal("pre-purge owner edit was clobbered")
	}
	if strings.Contains(string(body), "mem:id=doomed-1") {
		t.Fatal("struck block survived")
	}
}

// R7.4: a card with unterminated or missing frontmatter is not "superseded"
// — the purge set stays exact and the malformed file is left alone.
func TestVacuumSkipsMalformedCards(t *testing.T) {
	root := fixture(t)
	w := vault.NewWriter(root)
	for _, c := range []struct{ rel, body string }{
		{"memory/cards/notes/broken.md", "---\ntype: note\nstatus: superseded\nno closing marker\n"},
		{"memory/cards/notes/plain.md", "just a body, no frontmatter\n"},
	} {
		if _, err := w.Apply(memory.WriteOp{Path: c.rel, Content: []byte(c.body), Actor: memory.ActorSystem}); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := Apply(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.SupersededCards) != 1 || rep.SupersededCards[0] != "memory/cards/notes/old-card.md" {
		t.Fatalf("purge set drifted to malformed cards: %v", rep.SupersededCards)
	}
	for _, rel := range []string{"broken.md", "plain.md"} {
		if _, err := os.Stat(filepath.Join(root, "memory", "cards", "notes", rel)); err != nil {
			t.Fatalf("malformed card %s removed", rel)
		}
	}
}

// R7.4 red-team: a card symlink escaping the vault must never feed the
// purge set — reads resolve inside the vault root, so outside content
// cannot forge a superseded entry.
func TestVacuumSkipsSymlinkedCards(t *testing.T) {
	root := fixture(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("---\ntype: note\nstatus: superseded\ntags: []\n---\n\n## Facts\n\n- forged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "memory", "cards", "notes", "evil.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable on this host: %v", err)
	}
	rep, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range rep.SupersededCards {
		if strings.Contains(rel, "evil") {
			t.Fatalf("symlinked outside file forged a purge entry: %v", rep.SupersededCards)
		}
	}
}

// R7.4: a purged file's deletion is committed to vault history — nothing is
// left half-staged, and `git log` still shows the file's earlier content.
func TestVacuumDeletionCommitted(t *testing.T) {
	root := fixture(t)
	if _, err := Apply(root); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "status", "--porcelain", "--", "memory/cards/notes/old-card.md")
	cmd.Dir = root
	out, _ := cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("deletion left uncommitted: %s", out)
	}
	cmd = exec.Command("git", "log", "--format=%s", "--", "memory/cards/notes/old-card.md")
	cmd.Dir = root
	out, _ = cmd.CombinedOutput()
	if len(strings.TrimSpace(string(out))) == 0 {
		t.Fatal("card history missing from git log")
	}
}
