package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #440 S1: the consolidated files verbs — surgical edit with CAS,
// mkdir, generalized remove, archive action routing, chunked reads and
// snippet search — all bound to the writable root.
func TestFilesR5Verbs(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	ws := v.Workspace.Name()

	write := func(path, text string) map[string]any {
		out, err := v.Call(ctx, "file_write", Input{Path: path, Text: text})
		if err != nil {
			t.Fatalf("seed write %s: %v", path, err)
		}
		return out.(map[string]any)
	}

	// file_mkdir: creates a directory, parents honored, evidence returned.
	out, err := v.Call(ctx, "file_mkdir", Input{Path: "a/b/c", Parents: true})
	if err != nil {
		t.Fatalf("file_mkdir: %v", err)
	}
	res := out.(map[string]any)
	if res["path"] != "a/b/c" {
		t.Fatalf("mkdir result: %v", res)
	}
	if info, err := os.Stat(filepath.Join(ws, "a/b/c")); err != nil || !info.IsDir() {
		t.Fatal("mkdir did not create the directory")
	}
	if _, err := v.Call(ctx, "file_mkdir", Input{Path: "x/y"}); err == nil {
		t.Fatal("file_mkdir without parents created nested dirs")
	}

	// file_edit: surgical replace with revision CAS; stale revision refuses.
	rev, _ := write("note.txt", "hello world")["revision"].(string)
	out, err = v.Call(ctx, "file_edit", Input{Path: "note.txt", OldString: "world", NewString: "there", Revision: rev})
	if err != nil {
		t.Fatalf("file_edit: %v", err)
	}
	res = out.(map[string]any)
	newRev, _ := res["revision"].(string)
	if newRev == "" || newRev == rev {
		t.Fatalf("edit result: %v", res)
	}
	body, _ := os.ReadFile(filepath.Join(ws, "note.txt"))
	if string(body) != "hello there" {
		t.Fatalf("edited content: %q", body)
	}
	if _, err := v.Call(ctx, "file_edit", Input{Path: "note.txt", OldString: "there", NewString: "x", Revision: rev}); err == nil {
		t.Fatal("file_edit accepted a stale revision")
	}
	if _, err := v.Call(ctx, "file_edit", Input{Path: "note.txt", OldString: "absent", NewString: "x", Revision: newRev}); err == nil {
		t.Fatal("file_edit replaced a missing string")
	}

	// file_remove: deletes one file inside the writable root.
	if _, err := v.Call(ctx, "file_remove", Input{Path: "note.txt"}); err != nil {
		t.Fatalf("file_remove: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(ws, "note.txt")); !os.IsNotExist(err) {
		t.Fatal("file_remove left the file")
	}
	if _, err := v.Call(ctx, "file_remove", Input{Path: "note.txt"}); err == nil {
		t.Fatal("file_remove deleted a missing file")
	}

	// file_archive: action routes create/unpack; the zip output stays under
	// the artifacts/ boundary the capability admits.
	write("src/one.txt", "1")
	if _, err = v.Call(ctx, "file_archive", Input{Action: "create", Path: "artifacts/packed.zip", Paths: []string{"src/one.txt"}}); err != nil {
		t.Fatalf("file_archive create: %v", err)
	}
	if _, err := v.Call(ctx, "file_archive", Input{Action: "unpack", Path: "artifacts/packed.zip", Name: "unpacked-one"}); err != nil {
		t.Fatalf("file_archive unpack: %v", err)
	}
	if _, err := v.Call(ctx, "file_archive", Input{Path: "x.zip"}); err == nil {
		t.Fatal("file_archive accepted an empty action")
	}

	// file_read: offset/limit chunking reports next_offset.
	write("big.txt", strings.Repeat("0123456789", 600))
	out, err = v.Call(ctx, "file_read", Input{Path: "big.txt", Limit: 1000})
	if err != nil {
		t.Fatalf("file_read chunk: %v", err)
	}
	res = out.(map[string]any)
	text, _ := res["text"].(string)
	next, _ := res["next_offset"].(float64)
	if len(text) != 1000 || next != 1000 {
		t.Fatalf("chunked read: %v", res)
	}
	out, err = v.Call(ctx, "file_read", Input{Path: "big.txt", Offset: 1000})
	if err != nil {
		t.Fatalf("file_read offset: %v", err)
	}
	res = out.(map[string]any)
	if txt, _ := res["text"].(string); len(txt) != 5000 {
		t.Fatalf("offset read: %v", res)
	}

	// file_search: snippets carry line numbers; no extension filter.
	write("code.bin", "junk\nneedle on line two\nmore junk")
	out, err = v.Call(ctx, "file_search", Input{Query: "needle"})
	if err != nil {
		t.Fatalf("file_search: %v", err)
	}
	res = out.(map[string]any)
	items, _ := res["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("search items: %v", res)
	}
	if items[0]["path"] != "code.bin" {
		t.Fatalf("search skipped a non-list extension: %v", items)
	}
	snips, _ := items[0]["snippets"].([]map[string]any)
	if len(snips) == 0 || snips[0]["line"] != 2 || !strings.Contains(snips[0]["text"].(string), "needle") {
		t.Fatalf("search snippets: %v", items[0])
	}
}
