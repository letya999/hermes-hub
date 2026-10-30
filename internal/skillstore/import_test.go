package skillstore

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// Foreign packages arrive as directories, zip archives or standalone
// files. The importers share one pipeline: bounded extraction, dot paths
// dropped with a report, skill roots discovered by SKILL.md presence and
// the caller selecting the subtree — never guessing (R10.9).
func TestImportForeignPackage(t *testing.T) {
	head := "---\nname: demo\ndescription: d\n---\n\nbody"

	// Zip: a GitHub-style download wraps the payload in one top dir and
	// carries repo plumbing under dot paths.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	put("repo-ref/SKILL.md", head)
	put("repo-ref/references/a.md", "ref")
	put("repo-ref/scripts/run.sh", "#!/bin/sh\n")
	put("repo-ref/.github/workflows/ci.yml", "ci")
	put("repo-ref/.gitignore", "dist")
	put("repo-ref/.origin.json", `{"commit":"spoof"}`)
	put("repo-ref/node_modules/pkg/index.js", "vendored")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	pkg, dropped, err := ImportZip(buf.Bytes())
	if err != nil {
		t.Fatalf("zip import: %v", err)
	}
	roots := SkillRoots(pkg)
	if len(roots) != 1 || roots[0] != "repo-ref" {
		t.Fatalf("skill roots: %v", roots)
	}
	tree := SelectTree(pkg, roots[0])
	if tree[SkillFile].Data == nil || tree["references/a.md"].Data == nil {
		t.Fatalf("selected tree: %v", mapsKeys(tree))
	}
	if len(dropped) == 0 {
		t.Fatal("dot plumbing dropped without a report")
	}
	for p := range tree {
		if p == ".gitignore" || p == ".origin.json" || p == ".github/workflows/ci.yml" {
			t.Fatalf("dot path %q must not enter the skill tree", p)
		}
	}
	if name := NameFromTree(tree, "fallback"); name != "demo" {
		t.Fatalf("frontmatter name: %q", name)
	}

	// Directory walks share the same shape (agent workspace, CLI --from).
	fsys := fstest.MapFS{
		"SKILL.md":            &fstest.MapFile{Data: []byte(head)},
		"references/a.md":     &fstest.MapFile{Data: []byte("ref")},
		".claude/hooks.json":  &fstest.MapFile{Data: []byte("{}")},
		"node_modules/x/y.js": &fstest.MapFile{Data: []byte("js")},
	}
	dtree, ddropped, err := ImportTree(fsys)
	if err != nil {
		t.Fatalf("dir import: %v", err)
	}
	if dtree[SkillFile].Data == nil || dtree["node_modules/x/y.js"].Data == nil {
		t.Fatalf("dir tree: %v", mapsKeys(dtree))
	}
	if len(ddropped) == 0 || dtree[".claude/hooks.json"].Data != nil {
		t.Fatalf("dot drop: %v %v", ddropped, mapsKeys(dtree))
	}

	// Traversal members refuse the whole archive — never silently skip.
	var bad bytes.Buffer
	bw := zip.NewWriter(&bad)
	w, err := bw.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportZip(bad.Bytes()); err == nil {
		t.Fatal("traversal member must refuse the archive")
	}
}

// Import bounds and hostile-member refusals: archives cannot smuggle
// quota escapes, duplicates, links or repository metadata; directory
// imports refuse symlinks and oversized files (R10.9 bounds).
func TestImportHardening(t *testing.T) {
	head := "---\nname: demo\ndescription: d\n---\n\nbody"
	build := func(files map[string][]byte, modes map[string]int64) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range files {
			h := &zip.FileHeader{Name: name, Method: zip.Deflate}
			if m, ok := modes[name]; ok {
				h.SetMode(fs.FileMode(m))
			}
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(body); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	good := map[string][]byte{"SKILL.md": []byte(head), "big.txt": bytes.Repeat([]byte("x"), MaxFileBytes+1)}
	if _, _, err := ImportZip(build(good, nil)); err == nil {
		t.Fatal("oversized member must refuse")
	}
	dup := map[string][]byte{"SKILL.md": []byte(head)}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 2; i++ {
		w, err := zw.Create("dup.md")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(dup["SKILL.md"]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportZip(buf.Bytes()); err == nil {
		t.Fatal("duplicate member must refuse")
	}
	_, dropped, err := ImportZip(build(map[string][]byte{"SKILL.md": []byte(head), ".git/config": []byte("c")}, nil))
	if err != nil || len(dropped) == 0 {
		t.Fatal("repository metadata must be dropped with a report")
	}
	if _, _, err := ImportZip(bytes.Repeat([]byte("x"), MaxImportBytes+1)); err == nil {
		t.Fatal("oversized archive must refuse")
	}
	link := build(map[string][]byte{"SKILL.md": []byte(head), "ev": []byte("x")}, map[string]int64{"ev": int64(fs.ModeSymlink | 0o777)})
	if _, _, err := ImportZip(link); err == nil {
		t.Fatal("symlink member must refuse")
	}
	// Exec bits survive extraction — scripts keep their mode.
	exec := build(map[string][]byte{"SKILL.md": []byte(head), "scripts/a.sh": []byte("#!/bin/sh\n")}, map[string]int64{"scripts/a.sh": 0o755})
	tree, _, err := ImportZip(exec)
	if err != nil || !tree["scripts/a.sh"].Exec {
		t.Fatalf("exec bit lost: %v", err)
	}
	// Directory import: symlink and oversized files refuse.
	fsys := fstest.MapFS{
		"SKILL.md":  &fstest.MapFile{Data: []byte(head)},
		"link.md":   &fstest.MapFile{Data: []byte("x"), Mode: fs.ModeSymlink},
		"plain.txt": &fstest.MapFile{Data: []byte("p")},
	}
	if _, _, err := ImportTree(fsys); err == nil {
		t.Fatal("dir symlink must refuse")
	}
	fsys2 := fstest.MapFS{
		"SKILL.md": &fstest.MapFile{Data: []byte(head)},
		"big.bin":  &fstest.MapFile{Data: bytes.Repeat([]byte("x"), MaxFileBytes+1)},
	}
	if _, _, err := ImportTree(fsys2); err == nil {
		t.Fatal("dir oversized file must refuse")
	}
	// A standalone SKILL.md becomes a one-file tree.
	if FileTree([]byte(head))[SkillFile].Data == nil {
		t.Fatal("file tree missing SKILL.md")
	}
}

// A repo can carry thousands of vendored files — the audit note must
// stay a summary, not an enumeration: examples, then a "+N more" tail.
func TestDropNotesBoundsListing(t *testing.T) {
	var paths []string
	for i := 0; i < 15; i++ {
		paths = append(paths, fmt.Sprintf("node_modules/pkg%d/index.js", i))
	}
	notes := DropNotes(paths)
	if len(notes) != 1 {
		t.Fatalf("notes: %v", notes)
	}
	if !strings.Contains(notes[0], "15") || !strings.Contains(notes[0], "more") {
		t.Fatalf("dropped listing must summarize: %q", notes[0])
	}
	if strings.Contains(notes[0], "pkg14") || len(notes[0]) > 400 {
		t.Fatalf("note must not enumerate every path: %q", notes[0])
	}
}

func mapsKeys(t Tree) []string {
	out := []string{}
	for k := range t {
		out = append(out, k)
	}
	return out
}
