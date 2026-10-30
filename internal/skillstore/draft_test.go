package skillstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPublishActionDrafts covers non-publish draft actions (R5.3): an
// archive draft moves live into .archive on approval, a restore draft
// brings the chosen archive stamp back. Both still require an approver.
func TestPublishActionDrafts(t *testing.T) {
	s := newStore(t, ScopeUser)
	if _, err := s.Publish("demo", skillTree("v1"), IndexMeta{Author: "u"}); err != nil {
		t.Fatal(err)
	}
	live, err := s.Tree("demo")
	if err != nil {
		t.Fatal(err)
	}
	// Archive draft: publish archives live, does not overwrite it.
	d, err := s.CreateDraft("demo", live, DraftMeta{Author: "agent", Reason: "retire", Action: "archive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishDraft(d.ID, "owner"); err != nil {
		t.Fatalf("publish archive draft: %v", err)
	}
	if _, err := s.Tree("demo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("live should be archived: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Root(), archiveDir, "demo"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("archive entries: %v %v", entries, err)
	}
	stamp := entries[0].Name()
	// Restore draft pinned to that stamp brings the tree back.
	rd, err := s.CreateDraft("demo", live, DraftMeta{Author: "agent", Reason: "bring back", Action: "restore", RestoreStamp: stamp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishDraft(rd.ID, "owner"); err != nil {
		t.Fatalf("publish restore draft: %v", err)
	}
	tree, err := s.Tree("demo")
	if err != nil || !strings.Contains(string(tree[SkillFile].Data), "v1") {
		t.Fatalf("restored tree: %v", err)
	}
	// Approver still mandatory for action drafts.
	d2, err := s.CreateDraft("demo", live, DraftMeta{Author: "agent", Action: "archive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishDraft(d2.ID, ""); !errors.Is(err, ErrNeedsReview) {
		t.Fatalf("empty approver must fail: %v", err)
	}
}

// Imported trees (git/archive/dir/file installs, R10.9) carry foreign link
// shapes the authored contract rejects: resource files may chain when the
// link resolves inside the tree — admitted with an audit note — while a
// dead SKILL.md link stays a hard failure. Rollback of the published
// foreign skill keeps the same leniency via the persisted flag.
func TestImportedDraftAllowsForeignChains(t *testing.T) {
	s := newStore(t, ScopeUser)
	head := "---\nname: demo\ndescription: \"d\"\n---\n\n"
	bin := append([]byte("png data\n[x]("), 0)
	bin = append(bin, []byte("zz)\n")...)
	tree := Tree{
		SkillFile:         File{Data: []byte(head + "[guide](references/a.md)")},
		"references/a.md": File{Data: []byte("see [proc](b.md), [readme](../README.md) and [ext](missing.md)")},
		"references/b.md": File{Data: []byte("leaf")},
		"README.md":       File{Data: []byte("repo readme")},
		// Opaque payloads and code files never carry markdown links —
		// linting them produces notes about bytes nobody wrote as links.
		"docs/images/hero.png": File{Data: bin},
		"scripts/bundle.cjs":   File{Data: []byte(`html.replace(/src=["']([^"']+)["']/gi, f)`)},
	}
	d, err := s.CreateDraft("demo", tree, DraftMeta{Imported: true})
	if err != nil {
		t.Fatalf("imported foreign chains refused: %v", err)
	}
	joined := strings.Join(d.Meta.ImportNotes, "\n")
	if len(d.Meta.ImportNotes) == 0 {
		t.Fatal("foreign links admitted without an audit note")
	}
	if !strings.Contains(joined, "admitted") || !strings.Contains(joined, "missing.md") {
		t.Fatalf("notes must name admitted count and dead links: %v", d.Meta.ImportNotes)
	}
	if strings.Contains(joined, "hero.png") || strings.Contains(joined, "bundle.cjs") {
		t.Fatalf("binary/code files must not produce link notes: %v", d.Meta.ImportNotes)
	}
	rev, err := s.PublishDraft(d.ID, "me")
	if err != nil {
		t.Fatalf("imported draft refused at publish: %v", err)
	}
	// A later strict publish retains the foreign revision; restoring it is
	// not re-authoring — the retained content was already admitted once.
	if _, err := s.Publish("demo", skillTree("v2"), IndexMeta{Author: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rollback("demo", rev); err != nil {
		t.Fatalf("rollback of a foreign revision refused: %v", err)
	}
	// The same tree without the import marker is still refused.
	tree2 := Tree{
		SkillFile:         File{Data: []byte(head + "[guide](references/a.md)")},
		"references/a.md": File{Data: []byte("see [proc](b.md)")},
		"references/b.md": File{Data: []byte("leaf")},
	}
	if _, err := s.CreateDraft("demo", tree2, DraftMeta{Author: "agent"}); err == nil {
		t.Fatal("authored chains must stay refused")
	}
}

// TestArchiveTree reads an archived entry without restoring it, so a
// restore draft can preview the exact content it will bring back.
func TestArchiveTree(t *testing.T) {
	s := newStore(t, ScopeUser)
	if _, err := s.Publish("demo", skillTree("v1"), IndexMeta{Author: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish("demo", skillTree("v2"), IndexMeta{Author: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive("demo", "retire", "", "u"); err != nil {
		t.Fatal(err)
	}
	tree, stamp, err := s.ArchiveTree("demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if stamp == "" || !strings.Contains(string(tree[SkillFile].Data), "v2") {
		t.Fatalf("archive tree: stamp=%q", stamp)
	}
	// Unknown name and unknown stamp are not-found, not existence leaks.
	if _, _, err := s.ArchiveTree("ghost", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ghost: %v", err)
	}
	if _, _, err := s.ArchiveTree("demo", "bogus-stamp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad stamp: %v", err)
	}
}
