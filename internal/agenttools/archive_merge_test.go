package agenttools

import (
	"os"
	"path/filepath"
	"testing"
)

// archiveCreate bounds inputs to distinct regular files under a safe
// artifacts/*.zip output; archiveUnpack refuses existing targets and
// non-zip bodies and round-trips a created archive.
func TestArchiveCreateUnpack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	ws, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	tools := &Tools{Workspace: ws}

	out, err := tools.archiveCreate(ws, Input{Path: "artifacts/pack.zip", Paths: []string{"a.txt", "b.txt"}})
	if err != nil || out["files"] != 2 {
		t.Fatalf("create: %v %v", out, err)
	}
	if _, err := tools.archiveCreate(ws, Input{Path: "bad.zip", Paths: []string{"a.txt"}}); err == nil {
		t.Fatal("non-artifacts output accepted")
	}
	if _, err := tools.archiveCreate(ws, Input{Path: "artifacts/x.zip"}); err == nil {
		t.Fatal("empty sources accepted")
	}
	if _, err := tools.archiveCreate(ws, Input{Path: "artifacts/x.zip", Paths: []string{"a.txt", "a.txt"}}); err == nil {
		t.Fatal("duplicate sources accepted")
	}
	if _, err := tools.archiveCreate(ws, Input{Path: "artifacts/x.zip", Paths: []string{"missing"}}); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := tools.archiveCreate(ws, Input{Path: "artifacts/x.zip", Paths: []string{"sub"}}); err == nil {
		t.Fatal("directory source accepted")
	}
	if _, err := tools.archiveCreate(ws, Input{Path: "artifacts/pack.zip", Paths: []string{"b.txt"}}); err == nil {
		t.Fatal("existing output overwritten")
	}

	// Unpack the created archive into a new target.
	unpacked, err := tools.archiveUnpack(ws, Input{Path: "artifacts/pack.zip", Name: "box"})
	if err != nil {
		t.Fatal(err)
	}
	if unpacked["files"] != 2 {
		t.Fatalf("unpacked: %v", unpacked)
	}
	if _, err := ws.ReadFile("artifacts/unpacked/box/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.archiveUnpack(ws, Input{Path: "artifacts/pack.zip", Name: "box"}); err == nil {
		t.Fatal("existing unpack target accepted")
	}
	for _, r := range []Input{
		{Path: "../x.zip", Name: "y"},
		{Path: "artifacts/pack.zip"},
		{Path: "artifacts/pack.zip", Name: "a/b"},
		{Path: "missing.zip", Name: "y"},
		{Path: "a.txt", Name: "y"},
	} {
		if _, err := tools.archiveUnpack(ws, r); err == nil {
			t.Fatalf("unpack input accepted: %+v", r)
		}
	}
}
