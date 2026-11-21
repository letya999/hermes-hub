package passport

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func publishRev(t *testing.T, dir, id string) {
	t.Helper()
	rev := Revision{
		ID:         id,
		Provenance: Provenance{Space: "alice", Environment: "dev", UpdatedAt: time.Now(), GeneratedAt: time.Now()},
		Docs:       map[string][]byte{},
	}
	for _, name := range Names {
		rev.Docs[name] = []byte("apiVersion: six-passport/v2\nkind: " + name + "\n# " + id + "\n")
	}
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
}

func revisionDirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "generated", "passports"))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}

// Superseded revisions are bounded history: publish leaves the live pointer
// chain plus RevisionKeepSlack newest bundles, never the full past.
func TestPublishPrunesSupersededRevisions(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"r-0001", "r-0002", "r-0003", "r-0004", "r-0005", "r-0006", "r-0007"}
	for _, id := range ids {
		publishRev(t, dir, id)
	}
	remaining := revisionDirs(t, dir)
	if len(remaining) > 2+RevisionKeepSlack {
		t.Fatalf("unbounded revision history: %v", remaining)
	}
	for _, want := range []string{"r-0007", "r-0006"} {
		if !slices.Contains(remaining, want) {
			t.Fatalf("live revision %s pruned: %v", want, remaining)
		}
	}
}

// The sweep keeps exactly the pointer chain plus the newest slack: with
// deterministic mtimes the surviving set is fully specified.
func TestPruneRevisionsKeepsPointerChainAndNewestSlack(t *testing.T) {
	dir := t.TempDir()
	ids := []string{"r-0001", "r-0002", "r-0003", "r-0004", "r-0005", "r-0006", "r-0007"}
	base := time.Now().Add(-time.Hour)
	for i, id := range ids {
		revDir := filepath.Join(dir, "generated", "passports", id)
		if err := os.MkdirAll(revDir, 0700); err != nil {
			t.Fatal(err)
		}
		at := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(revDir, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := PointCurrent(dir, "r-0007"); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneRevisions(dir, "r-0006"); err != nil {
		t.Fatal(err)
	}
	remaining := revisionDirs(t, dir)
	slices.Sort(remaining)
	want := []string{"r-0003", "r-0004", "r-0005", "r-0006", "r-0007"}
	if !slices.Equal(remaining, want) {
		t.Fatalf("got %v, want %v", remaining, want)
	}
}

// An apply journal owns the tree until recovery settles it: sweeping then
// could delete the exact bundle a rollback must re-read.
func TestPruneRevisionsSkipsWhileJournalOpen(t *testing.T) {
	dir := t.TempDir()
	publishRev(t, dir, "r-0001")
	publishRev(t, dir, "r-0002")
	stale := filepath.Join(dir, "generated", "passports", "r-dead")
	if err := os.MkdirAll(stale, 0700); err != nil {
		t.Fatal(err)
	}
	// A mid-publish mirror pins current to pending — the same fence the
	// deleted apply journal provided.
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	removed, err := PruneRevisions(dir, "")
	if err != nil || len(removed) != 0 {
		t.Fatalf("pruned under an open journal: %v %v", removed, err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("journal-gated sweep still deleted")
	}
}

// A pending pointer means activation never committed: the tree belongs to the
// in-flight publish, not to the sweeper.
func TestPruneRevisionsSkipsPendingPointer(t *testing.T) {
	dir := t.TempDir()
	publishRev(t, dir, "r-0001")
	if err := PointCurrent(dir, "pending"); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "generated", "passports", "r-dead")
	if err := os.MkdirAll(stale, 0700); err != nil {
		t.Fatal(err)
	}
	if removed, err := PruneRevisions(dir, ""); err != nil || len(removed) != 0 {
		t.Fatalf("pruned under a pending pointer: %v %v", removed, err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("pending-gated sweep still deleted")
	}
}

// A crashed prepare leaves .staging-* and .publish-* behind; the sweep is the
// reconciler that collects those orphans.
func TestPruneRevisionsCollectsOrphanArtifacts(t *testing.T) {
	dir := t.TempDir()
	publishRev(t, dir, "r-0001")
	root := filepath.Join(dir, "generated", "passports")
	if err := os.MkdirAll(filepath.Join(root, ".staging-r-0002"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".publish-xyz", ".apply-journal-abc"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := PruneRevisions(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".staging-r-0002", ".publish-xyz", ".apply-journal-abc"} {
		if !slices.Contains(removed, want) {
			t.Fatalf("orphan %s not swept: %v", want, removed)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "current" && e.Name() != "r-0001" {
			t.Fatalf("unexpected survivor: %s", e.Name())
		}
	}
}

// The mounted config view is the runtime's actual /config: its pinned
// revision survives even when the pointer moved on and history is full.
func TestPruneRevisionsKeepsMountedViewRevision(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"r-0001", "r-0002", "r-0003", "r-0004", "r-0005", "r-0006", "r-0007"} {
		if err := os.MkdirAll(filepath.Join(dir, "generated", "passports", id), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := PointCurrent(dir, "r-0007"); err != nil {
		t.Fatal(err)
	}
	// A divergent view still mounts r-0001 — the runtime reads it, so the
	// bundle must survive even though it fell out of the slack window.
	viewDir := filepath.Join(dir, "generated", "config-view", "dev")
	if err := os.MkdirAll(viewDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewDir, "revision"), []byte("r-0001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneRevisions(dir, "r-0006"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(revisionDirs(t, dir), "r-0001") {
		t.Fatal("mounted view revision was swept")
	}
}
