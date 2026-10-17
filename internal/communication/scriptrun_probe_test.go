package communication

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunScriptProbeProvidesInput(t *testing.T) {
	emitted, err := RunScriptProbe(context.Background(), `emit(input().upper())`, time.Now(), "fetched-body", "")
	if err != nil || len(emitted) != 1 || emitted[0] != "FETCHED-BODY" {
		t.Fatalf("input() not provided: %v %v", emitted, err)
	}
}

func TestRunScriptProbeFileBuiltinsStayInWatchRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := RunScriptProbe(context.Background(), `emit(read_file("notes.txt"))`, time.Now(), "", root); err == nil {
		t.Fatal("missing file must fail")
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	emitted, err := RunScriptProbe(context.Background(), `emit(read_file("notes.txt"))`, time.Now(), "", root)
	if err != nil || len(emitted) != 1 || emitted[0] != "hello" {
		t.Fatalf("read_file: %v %v", emitted, err)
	}
	for _, src := range []string{`emit(read_file("../escape"))`, `emit(read_file("/etc/passwd"))`, `emit(list_dir(".."))`, `emit(hash_path("../escape"))`} {
		if _, err := RunScriptProbe(context.Background(), src, time.Now(), "", root); err == nil {
			t.Fatalf("traversal accepted: %s", src)
		}
	}
}

func TestRunScriptProbeListsAndHashesWatchRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("beta"), 0600); err != nil {
		t.Fatal(err)
	}
	emitted, err := RunScriptProbe(context.Background(), `emit(",".join(sorted(list_dir("."))))`, time.Now(), "", root)
	if err != nil || len(emitted) != 1 || emitted[0] != "a.txt,sub" {
		t.Fatalf("list_dir: %v %v", emitted, err)
	}
	emitted, err = RunScriptProbe(context.Background(), `emit(hash_path("."))`, time.Now(), "", root)
	if err != nil || len(emitted) != 1 || len(emitted[0]) != 64 {
		t.Fatalf("hash_path: %v %v", emitted, err)
	}
	first := emitted[0]
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	emitted, err = RunScriptProbe(context.Background(), `emit(hash_path("."))`, time.Now(), "", root)
	if err != nil || emitted[0] == first {
		t.Fatal("hash_path did not reflect file change")
	}
}
