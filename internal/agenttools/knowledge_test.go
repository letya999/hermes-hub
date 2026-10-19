package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// knowledge_list and knowledge_read expose the per-user staged knowledge
// directory to the model. The directory lives in the runtime's own state
// volume — no HTTP hop, no control token. Content returns as tool output
// only — nothing is written into the workspace.
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

	out, err := v.Call(ctx, "knowledge_list", Input{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	res, _ := out.(map[string]any)
	items, _ := res["items"].([]any)
	if len(items) != 1 || items[0].(knowledgeItem).Name != "notes.md" {
		t.Fatalf("list result: %v", res)
	}
	out, err = v.Call(ctx, "knowledge_read", Input{Name: "notes.md"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	res, _ = out.(map[string]any)
	if res["content"] != "# spec notes" {
		t.Fatalf("read result: %v", res)
	}
	if _, err := v.Call(ctx, "knowledge_read", Input{Name: "missing.md"}); err == nil {
		t.Fatal("missing source read as ok")
	}
	if _, err := v.Call(ctx, "knowledge_read", Input{Name: "../secret"}); err == nil {
		t.Fatal("path escape read as ok")
	}
	// Nothing lands in the workspace.
	if _, err := os.Lstat(filepath.Join(v.Workspace.Name(), "notes.md")); !os.IsNotExist(err) {
		t.Fatal("knowledge read wrote into the workspace")
	}
	// Catalog: both tools listed.
	found := map[string]bool{}
	for _, spec := range HubToolsDefinition().Tools {
		found[spec.Name] = true
	}
	if !found["knowledge_list"] || !found["knowledge_read"] || !found["knowledge_search"] {
		t.Fatal("knowledge tools missing from the hub catalog")
	}
	out, err = v.Call(ctx, "knowledge_search", Input{Query: "spec notes"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	res, _ = out.(map[string]any)
	fragments, _ := res["fragments"].([]knowledgeFragment)
	if len(fragments) == 0 {
		t.Fatalf("search result: %v", res)
	}
}
