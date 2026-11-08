package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The staged knowledge directory is served through the files family under
// root=knowledge (SPEC-0093 R3.1, issue #440 S1): file_list/file_read/
// file_search reach it read-only; no knowledge_* names remain on the model
// surface and nothing lands in the workspace.
func TestKnowledgeToolsReadOnly(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	v := fixture(t)
	dir := filepath.Join(stateDir, "knowledge", "alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# spec notes"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	out, err := v.Call(ctx, "file_list", Input{Root: "knowledge"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	res, _ := out.(map[string]any)
	items, _ := res["items"].([]map[string]any)
	if len(items) != 1 || items[0]["name"] != "notes.md" {
		t.Fatalf("list result: %v", res)
	}
	out, err = v.Call(ctx, "file_read", Input{Root: "knowledge", Path: "notes.md"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	res, _ = out.(map[string]any)
	if res["text"] != "# spec notes" {
		t.Fatalf("read result: %v", res)
	}
	if _, err := v.Call(ctx, "file_read", Input{Root: "knowledge", Path: "missing.md"}); err == nil {
		t.Fatal("missing source read as ok")
	}
	if _, err := v.Call(ctx, "file_read", Input{Root: "knowledge", Path: "../secret"}); err == nil {
		t.Fatal("path escape read as ok")
	}
	// Nothing lands in the workspace.
	if _, err := os.Lstat(filepath.Join(v.Workspace.Name(), "notes.md")); !os.IsNotExist(err) {
		t.Fatal("knowledge read wrote into the workspace")
	}
	// The knowledge root is read-only through every write verb.
	for _, name := range []string{"file_write", "file_edit", "file_mkdir", "file_remove", "file_archive"} {
		if _, err := v.Call(ctx, name, Input{Root: "knowledge", Path: "notes.md", Text: "x", Action: "unpack"}); err == nil {
			t.Fatalf("%s wrote into the read-only knowledge root", name)
		}
	}
	// Catalog: the file knowledge root remains inside file tools; no
	// knowledge_* spec is reintroduced.
	for _, spec := range HubToolsDefinition().Tools {
		if len(spec.Name) > 9 && spec.Name[:9] == "knowledge" {
			t.Fatalf("knowledge tool %s survived the r5 merge", spec.Name)
		}
	}
	out, err = v.Call(ctx, "file_search", Input{Root: "knowledge", Query: "spec notes"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	res, _ = out.(map[string]any)
	found, _ := res["items"].([]map[string]any)
	if len(found) == 0 {
		t.Fatalf("search result: %v", res)
	}
}
