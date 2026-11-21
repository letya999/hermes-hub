package spawn_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/shared/store/storetest"
)

// TestMirrorRemoved: activation never writes the v0 mirror — no
// generated/passports tree, no config-view, no .policy indirection under
// the spaces root. A pre-existing stale tree is left untouched (deleting
// old files is out of scope for the cutover).
func TestMirrorRemoved(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		root := t.TempDir()
		revs := testRevisions(t, url)
		revs.SpacesRoot = root
		setupSpace(t, revs)
		ctx := context.Background()

		// Fresh activation: nothing materializes under the space dir.
		second := submitValidated(t, revs, "user", userDocs("UTC"))
		if _, err := revs.Activate(ctx, tenant, "alice", []string{second.ID}, nil); err != nil {
			t.Fatalf("activate: %v", err)
		}
		for _, gone := range []string{
			filepath.Join(root, "alice", "generated", "passports"),
			filepath.Join(root, "alice", "generated", "config-view"),
			filepath.Join(root, "alice", "generated", "passports", ".policy"),
		} {
			if _, err := os.Stat(gone); !os.IsNotExist(err) {
				t.Fatalf("activation wrote the v0 mirror path %s", gone)
			}
		}

		// A stale tree from before the cutover is preserved as-is.
		stale := filepath.Join(root, "alice", "generated", "passports", "r-old", "grants.yaml")
		if err := os.MkdirAll(filepath.Dir(stale), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stale, []byte("kind: grants\nallow: {}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		third := submitValidated(t, revs, "user", userDocs("Asia/Tokyo"))
		if _, err := revs.Activate(ctx, tenant, "alice", []string{third.ID}, nil); err != nil {
			t.Fatalf("second activate: %v", err)
		}
		if _, err := os.Stat(stale); err != nil {
			t.Fatalf("activation touched the pre-existing stale tree: %v", err)
		}
	})
}

// TestStaleLinksPrunedWithRevisions: the DB pruner bounds superseded
// history (the .policy file indirection is gone — nothing writes link
// files, so they can never accumulate), while the revision rows respect
// the keep-slack retention.
func TestStaleLinksPrunedWithRevisions(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		root := t.TempDir()
		revs := testRevisions(t, url)
		revs.SpacesRoot = root
		setupSpace(t, revs)
		ctx := context.Background()

		// Supersede the user layer enough times to exhaust the keep slack.
		for i := 0; i < 6; i++ {
			rev := submitValidated(t, revs, "user", userDocs([]string{"UTC", "Asia/Tokyo", "America/New_York", "Europe/Paris", "Australia/Sydney", "Africa/Cairo"}[i]))
			if _, err := revs.Activate(ctx, tenant, "alice", []string{rev.ID}, nil); err != nil {
				t.Fatalf("activate %d: %v", i, err)
			}
		}
		list, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil {
			t.Fatal(err)
		}
		active, superseded := 0, 0
		for _, rev := range list {
			switch rev.State {
			case "active":
				active++
			case "superseded":
				superseded++
			}
		}
		if active != 1 {
			t.Fatalf("active revisions = %d, want 1", active)
		}
		if superseded > 3 {
			t.Fatalf("superseded history = %d, want <= keep slack", superseded)
		}
		if _, err := os.Stat(filepath.Join(root, "alice", "generated", "passports", ".policy")); !os.IsNotExist(err) {
			t.Fatal("stale .policy link machinery exists after S7d")
		}
	})
}
