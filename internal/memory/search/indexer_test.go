package search

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

func fixtureVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := vault.Ensure(root); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	daily, err := vault.OpenDaily(root, time.UTC)
	if err != nil {
		t.Fatalf("open daily: %v", err)
	}
	at := time.Date(2026, 4, 19, 15, 0, 0, 0, time.UTC)
	for _, e := range []memory.Entry{
		{ID: "turn-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate, Body: "we picked the earl grey supplier", ObservedAt: at},
		{ID: "turn-2", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudienceGroup, Body: "group chat about weekend plans", ObservedAt: at},
	} {
		if err := daily.Append(e, "telegram"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	return root
}

// The indexer turns prov.db entries into searchable docs and maps each to its
// daily-leaf evidence span.
func TestIndexerBuilds(t *testing.T) {
	root := fixtureVault(t)
	idxr := NewIndexer(root)
	idx, rebuilt, err := idxr.Ensure()
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !rebuilt {
		t.Fatal("first ensure must build")
	}
	hits := idx.Search("earl grey", Options{Audience: "private", Now: testNow})
	if len(hits) != 1 || hits[0].Doc.ID != "turn-1" {
		t.Fatalf("hits %+v want turn-1", hits)
	}
	if hits[0].Evidence == "" || hits[0].Evidence == ":0-0" {
		t.Fatalf("evidence %q must point at the leaf block", hits[0].Evidence)
	}
}

// mtime+size cache: a second Ensure with unchanged sources serves the built
// index without rebuilding; a new leaf entry forces a rebuild.
func TestIndexerLazyRebuild(t *testing.T) {
	root := fixtureVault(t)
	idxr := NewIndexer(root)
	if _, rebuilt, err := idxr.Ensure(); err != nil || !rebuilt {
		t.Fatalf("first ensure: rebuilt=%v err=%v", rebuilt, err)
	}
	if _, rebuilt, _ := idxr.Ensure(); rebuilt {
		t.Fatal("unchanged sources must not rebuild")
	}
	daily, _ := vault.OpenDaily(root, time.UTC)
	if err := daily.Append(memory.Entry{ID: "turn-3", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "the oolong follow-up", ObservedAt: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)}, "telegram"); err != nil {
		t.Fatalf("append: %v", err)
	}
	idx, rebuilt, err := idxr.Ensure()
	if err != nil || !rebuilt {
		t.Fatalf("post-append ensure: rebuilt=%v err=%v", rebuilt, err)
	}
	if hits := idx.Search("oolong", Options{Audience: "private", Now: testNow}); len(hits) != 1 {
		t.Fatalf("hits %+v want the new entry", hits)
	}
}

// A missing vault is an honest error — the tool surfaces "unavailable",
// never a silent empty result.
func TestIndexerUnavailable(t *testing.T) {
	idxr := NewIndexer(filepath.Join(t.TempDir(), "nope"))
	if _, _, err := idxr.Ensure(); err == nil {
		t.Fatal("missing vault must error")
	}
}

// An entry that reached prov.db but whose leaf write failed still indexes —
// its evidence falls back to the prov.db row, never orphan text.
func TestIndexerPartialFallback(t *testing.T) {
	root := fixtureVault(t)
	prov, err := memory.OpenProvDB(filepath.Join(root, "runtime", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prov.Append(&memory.Entry{ID: "orphan", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "captured but never leafed", ObservedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	idx, _, err := NewIndexer(root).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	hits := idx.Search("never leafed", Options{Audience: "private", Now: testNow})
	if len(hits) != 1 || !strings.HasPrefix(hits[0].Evidence, "runtime/memory/prov.db:") {
		t.Fatalf("hits %+v want prov.db fallback evidence", hits)
	}
}

// Cards and summaries index as private, kind-tagged whole-file docs; a
// captured turn [[linking]] a card pulls it into the result as a neighbor.
func TestIndexerMarkdownDocs(t *testing.T) {
	root := fixtureVault(t)
	daily, _ := vault.OpenDaily(root, time.UTC)
	if err := daily.Append(memory.Entry{ID: "turn-link", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "supplier shortlist — see [[earl-card]]", ObservedAt: testNow}, "telegram"); err != nil {
		t.Fatal(err)
	}
	card := "---\ntype: contact\nstatus: active\ntags: []\ncreated: 2026-04-01\nupdated: 2026-04-18\n---\n# Earl Grey Supplier\nsingle-origin assam vendor\n"
	w := vault.NewWriter(root)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/cards/contacts/earl-card.md", Content: []byte(card), Actor: memory.ActorOwner}); err != nil {
		t.Fatal(err)
	}
	idx, _, err := NewIndexer(root).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	hits := idx.Search("supplier", Options{Audience: "private", Now: testNow})
	ids := map[string]bool{}
	for _, h := range hits {
		ids[h.Doc.ID] = true
	}
	if !ids["earl-card"] {
		t.Fatalf("hits %+v want the linked card expanded in", hits)
	}
	for _, h := range hits {
		if h.Doc.Audience != "private" {
			t.Fatalf("markdown doc leaked audience %q", h.Doc.Audience)
		}
	}
}

// Summaries index as private kind=summary docs — the rollup output is
// searchable alongside raw leaves.
func TestIndexerSummaries(t *testing.T) {
	root := fixtureVault(t)
	w := vault.NewWriter(root)
	sum := "---\ntype: summary\n---\n# Week of 2026-04-13\ntea supplier decided: earl grey\n"
	if _, err := w.Apply(memory.WriteOp{Path: "memory/summaries/weekly/2026-W16.md",
		Content: []byte(sum), Actor: memory.ActorSystem}); err != nil {
		t.Fatal(err)
	}
	idx, _, err := NewIndexer(root).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	hits := idx.Search("assam oolong", Options{Audience: "private", Kinds: []string{"summary"}, Now: testNow})
	_ = hits
	hits = idx.Search("earl grey", Options{Audience: "private", Kinds: []string{"summary"}, Now: testNow})
	if len(hits) != 1 || hits[0].Doc.Kind != "summary" {
		t.Fatalf("hits %+v want the weekly summary", hits)
	}
}

// The built index persists under runtime/memory/index/ — a fresh process
// (new Indexer) serves from the snapshot without rebuilding.
func TestIndexerPersistsSnapshot(t *testing.T) {
	root := fixtureVault(t)
	if _, _, err := NewIndexer(root).Ensure(); err != nil {
		t.Fatal(err)
	}
	idx2 := NewIndexer(root)
	idx, rebuilt, err := idx2.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt {
		t.Fatal("snapshot load must not count as a rebuild")
	}
	if hits := idx.Search("earl grey", Options{Audience: "private", Now: testNow}); len(hits) != 1 {
		t.Fatalf("snapshot index missed: %+v", hits)
	}
}
