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
