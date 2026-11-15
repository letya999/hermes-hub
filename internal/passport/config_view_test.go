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

// R5.2: the executor contracts materialize inside the view so the single
// read-only /config bind covers them; publishing a revision must preserve
// them (render/spawn rewrites them).
func TestPublishPreservesExecutorContractsInConfigView(t *testing.T) {
	dir := t.TempDir()
	rev := Revision{
		ID:         "r-0001",
		Provenance: Provenance{Space: "alice", Environment: "dev", UpdatedAt: time.Now(), GeneratedAt: time.Now()},
		Docs:       map[string][]byte{},
	}
	for _, name := range Names {
		rev.Docs[name] = []byte("apiVersion: six-passport/v2\nkind: " + name + "\n")
	}
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
	viewDir := filepath.Join(dir, "generated", "config-view", "dev")
	for _, name := range []string{"tool-policy.json", "tool-manifest.json"} {
		if err := os.WriteFile(filepath.Join(viewDir, name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rev.ID = "r-0002"
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tool-policy.json", "tool-manifest.json"} {
		if _, err := os.Stat(filepath.Join(viewDir, name)); err != nil {
			t.Fatalf("publish dropped executor contract %s", name)
		}
	}
}

func TestPublishViewFailureDoesNotActivateRevision(t *testing.T) {
	dir := t.TempDir()
	rev := Revision{ID: "r-0003", Provenance: Provenance{Environment: "dev"}, Docs: map[string][]byte{}}
	for _, name := range Names {
		rev.Docs[name] = []byte("apiVersion: six-passport/v2\nkind: " + name + "\n")
	}
	viewRoot := filepath.Join(dir, "generated", "config-view")
	if err := os.MkdirAll(viewRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewRoot, "dev"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(dir); err == nil {
		t.Fatal("view refresh failure was ignored")
	}
	if active, err := Current(dir); err == nil || active != "" {
		t.Fatalf("failed publish activated %q: %v", active, err)
	}
}

func TestPublishRejectsImmutableRevisionCollision(t *testing.T) {
	dir := t.TempDir()
	rev := Revision{ID: "r-0004", Provenance: Provenance{Environment: "dev"}, Docs: map[string][]byte{}}
	for _, name := range Names {
		rev.Docs[name] = []byte("apiVersion: six-passport/v2\nkind: " + name + "\n")
	}
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
	rev.Docs["tools"] = []byte("apiVersion: six-passport/v2\nkind: tools\n# collision\n")
	if err := rev.Publish(dir); err == nil {
		t.Fatal("same revision ID accepted different tools")
	}
	if current, err := Current(dir); err != nil || current != rev.ID {
		t.Fatalf("collision displaced active revision: %q %v", current, err)
	}
}
