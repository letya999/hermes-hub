package securefs

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func privateDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if e := os.Chmod(d, 0700); e != nil {
		t.Fatal(e)
	}
	return d
}
func TestFiles(t *testing.T) {
	d := privateDir(t)
	if Dir(d) != nil {
		t.Fatal("private directory")
	}
	if Dir(filepath.Join(d, "nested", "child")) != nil {
		t.Fatal("create nested")
	}
	if Dir("relative") == nil || Dir(d+"/../bad") == nil {
		t.Fatal("unsafe path")
	}
	for _, n := range []string{"", ".", "..", "a/b", "a\\b", "a\x00b"} {
		if Name(n) {
			t.Fatal(n)
		}
	}
	if e := Create(d, "blob", []byte("hello"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := Create(d, "blob", nil, 0600); e == nil {
		t.Fatal("overwrite")
	}
	v, e := Read(d, "blob", 5)
	if e != nil || string(v) != "hello" {
		t.Fatal(e)
	}
	if _, e = Read(d, "blob", 4); e == nil {
		t.Fatal("size limit")
	}
	if _, e = Read(d, "../blob", 9); e == nil {
		t.Fatal("traversal")
	}
	if _, e = Read(d, "missing", 9); !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if e = os.Symlink("blob", filepath.Join(d, "symlink")); e != nil {
		t.Fatal(e)
	}
	if _, e = Read(d, "symlink", 9); e == nil {
		t.Fatal("symlink")
	}
	if e = os.Link(filepath.Join(d, "blob"), filepath.Join(d, "hardlink")); e != nil {
		t.Fatal(e)
	}
	if _, e = Read(d, "hardlink", 9); e == nil {
		t.Fatal("hardlink")
	}
	if _, e = Read(d, "nested", 9); e == nil {
		t.Fatal("directory read")
	}
	if _, e = Open(filepath.Join(d, "missing-dir"), "file", syscall.O_RDONLY, 0); e == nil {
		t.Fatal("missing dir")
	}
	if e = syscall.Mkfifo(filepath.Join(d, "fifo"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Read(d, "fifo", 9); e == nil {
		t.Fatal("FIFO")
	}
	if e = os.Symlink(filepath.Join(d, "nested"), filepath.Join(d, "symdir")); e != nil {
		t.Fatal(e)
	}
	if Dir(filepath.Join(d, "symdir", "child")) == nil {
		t.Fatal("symlink component")
	}
	if e = os.Chmod(filepath.Join(d, "nested"), 0777); e != nil {
		t.Fatal(e)
	}
	if Dir(filepath.Join(d, "nested")) == nil {
		t.Fatal("unsafe permissions")
	}
	if SyncDir(filepath.Join(d, "absent")) == nil {
		t.Fatal("missing sync directory")
	}
}
func TestKeyAndTmpfs(t *testing.T) {
	d := privateDir(t)
	p := filepath.Join(d, "key")
	if _, e := ReadKey("relative"); e == nil {
		t.Fatal("relative")
	}
	if _, e := ReadKey(p); e == nil {
		t.Fatal("missing")
	}
	if e := Create(d, "key", bytes.Repeat([]byte{42}, 32), 0600); e != nil {
		t.Fatal(e)
	}
	key, e := ReadKey(p)
	if e != nil || len(key) != 32 {
		t.Fatal(e)
	}
	if e = os.Chmod(p, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadKey(p); e == nil {
		t.Fatal("permissions")
	}
	if e := Create(d, "short", []byte{1}, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadKey(filepath.Join(d, "short")); e == nil {
		t.Fatal("size")
	}
	if Tmpfs(filepath.Join(d, "absent")) == nil {
		t.Fatal("nonexistent tmpfs")
	}
	if e := Tmpfs("/dev/shm"); e != nil {
		t.Skip("tmpfs not mounted in this environment")
	}
	if Tmpfs(d) == nil {
		t.Log("test temporary directory itself is tmpfs")
	}
}
