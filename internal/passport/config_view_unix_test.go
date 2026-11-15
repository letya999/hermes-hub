//go:build !windows

package passport

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func dirInode(t *testing.T, p string) uint64 {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func TestPublishedConfigViewReadableByRuntimeGroup(t *testing.T) {
	dir := t.TempDir()
	rev := Revision{ID: "r-0001", Provenance: Provenance{Space: "alice", Environment: "dev"}, Docs: map[string][]byte{}}
	for _, name := range Names {
		rev.Docs[name] = []byte("fixture")
	}
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(append([]string{}, Names...), "revision", "prompt-pointer.txt") {
		if name != "revision" && name != "prompt-pointer.txt" {
			name += ".yaml"
		}
		info, err := os.Stat(filepath.Join(dir, "generated", "config-view", "dev", name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0640 {
			t.Fatalf("runtime view %s mode=%o, want 640", name, info.Mode().Perm())
		}
	}
	for name, mode := range map[string]os.FileMode{"current": 0640, filepath.Join(rev.ID, "workspace.yaml"): 0440} {
		info, err := os.Stat(filepath.Join(dir, "generated", "passports", name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("controller publication %s mode=%o, want %o", name, info.Mode().Perm(), mode)
		}
	}
}

func TestPublishedRevisionReadableByControllerGroup(t *testing.T) {
	dir := t.TempDir()
	rev := Revision{ID: "r-0001", Provenance: Provenance{Space: "alice", Environment: "dev"}, Docs: map[string][]byte{}}
	for _, name := range Names {
		rev.Docs[name] = []byte("fixture")
	}
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "generated", "passports")
	for _, path := range []string{root, filepath.Join(root, rev.ID)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0750 {
			t.Fatalf("controller directory %s mode=%o, want 750", filepath.Base(path), info.Mode().Perm())
		}
	}
	for _, name := range append(append([]string{}, Names...), "prompt-pointer", "provenance") {
		filename := name + ".yaml"
		if name == "prompt-pointer" {
			filename = name + ".txt"
		}
		info, err := os.Stat(filepath.Join(root, rev.ID, filename))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0440 {
			t.Fatalf("immutable controller document %s mode=%o, want 440", name, info.Mode().Perm())
		}
	}
}
