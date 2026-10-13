package agenttools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// knowledge_list and knowledge_read expose the hub's per-user staged
// knowledge directory to the model. Content returns as tool output only —
// nothing is written into the workspace.
func TestKnowledgeToolsReadOnly(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/knowledge":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"notes.md","size":12}]`))
		case "/v1/knowledge/notes.md":
			_, _ = w.Write([]byte("# spec notes"))
		default:
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	v.CommunicationAuth = "control-token"
	ctx := context.Background()

	out, err := v.Call(ctx, "knowledge_list", Input{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	res, _ := out.(map[string]any)
	items, _ := res["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "notes.md" {
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
	// Nothing lands in the workspace.
	if _, err := os.Lstat(filepath.Join(v.Workspace.Name(), "notes.md")); !os.IsNotExist(err) {
		t.Fatal("knowledge read wrote into the workspace")
	}
	// Catalog: both tools listed.
	found := map[string]bool{}
	for _, spec := range HubToolsDefinition().Tools {
		found[spec.Name] = true
	}
	if !found["knowledge_list"] || !found["knowledge_read"] {
		t.Fatal("knowledge tools missing from the hub catalog")
	}
}
