package passport

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// S1.4: publishing refreshes generated/config-view/<env>/ per file inside a
// stable directory inode — never a wholesale replace, stale files are
// dropped individually.
func TestPublishRefreshesConfigViewWithStableInode(t *testing.T) {
	dir := t.TempDir()
	rev := Revision{
		ID:         "r-0001",
		Provenance: Provenance{Space: "alice", Environment: "dev", UpdatedAt: time.Now(), GeneratedAt: time.Now()},
		Docs:       map[string][]byte{},
	}
	for _, name := range Names {
		rev.Docs[name] = []byte("apiVersion: six-passport/v2\nkind: " + name + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
	viewDir := filepath.Join(dir, "generated", "config-view", "dev")
	inode1 := dirInode(t, viewDir)
	stale := filepath.Join(viewDir, "stale.yaml")
	if err := os.WriteFile(stale, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	rev.ID = "r-0002"
	rev.Docs["tools"] = []byte("apiVersion: six-passport/v2\nkind: tools\n# updated\n")
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
	inode2 := dirInode(t, viewDir)
	if inode1 != inode2 {
		t.Fatal("config-view directory inode changed — wholesale replace, not per-file refresh")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale file survived the refresh")
	}
	b, err := os.ReadFile(filepath.Join(viewDir, "tools.yaml"))
	if err != nil || len(b) == 0 {
		t.Fatal("tools.yaml not refreshed")
	}
	if got := string(b); got != "apiVersion: six-passport/v2\nkind: tools\n# updated\n" {
		t.Fatalf("tools.yaml not the new revision body: %q", got)
	}
	if _, err := os.Stat(filepath.Join(viewDir, "SOUL.md")); err != nil {
		t.Fatal("space SOUL.md not published into the view")
	}
}
