package agenttools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// fileScoped enforces the admitted root, safe paths and revision-guarded
// writes; the governance snapshot denies families and caps read-only tools
// mid-session.
func TestFileScopedAndPolicyDeny(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	ws, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	tools := &Tools{Workspace: ws}

	list, err := tools.fileScoped("list", Input{Path: "."}, ws, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := list["items"].([]map[string]any)
	if len(items) != 1 || items[0]["name"] != "a.md" {
		t.Fatalf("list: %v", items)
	}
	found, err := tools.fileScoped("search", Input{Query: "WORLD"}, ws, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found["items"].([]map[string]any)) != 1 {
		t.Fatalf("search: %v", found)
	}
	read, err := tools.fileScoped("read", Input{Path: "a.md"}, ws, nil, nil)
	if err != nil || read["text"] != "hello world" {
		t.Fatalf("read: %v %v", read, err)
	}
	if _, err := tools.fileScoped("read", Input{Path: "a.md", Root: "bogus"}, ws, nil, nil); err == nil {
		t.Fatal("bogus root accepted")
	}
	if _, err := tools.fileScoped("read", Input{Path: "x", Root: "archive"}, ws, nil, nil); err == nil {
		t.Fatal("missing archive root accepted")
	}
	if _, err := tools.fileScoped("read", Input{Path: "..\\evil"}, ws, nil, nil); err == nil {
		t.Fatal("unsafe path accepted")
	}
	if _, err := tools.fileScoped("read", Input{Path: ".hub-writer.lock"}, ws, nil, nil); err == nil {
		t.Fatal("reserved lock read")
	}

	// Writes: first create, then revision-guarded update and conflicts.
	if _, err := tools.fileScoped("write", Input{Path: "n/b.txt", Text: "v1"}, ws, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.fileScoped("write", Input{Path: "n/b.txt", Text: "v2"}, ws, nil, nil); err == nil {
		t.Fatal("write without revision accepted")
	}
	if _, err := tools.fileScoped("write", Input{Path: "n/b.txt", Text: "v2", Revision: "bogus"}, ws, nil, nil); err == nil {
		t.Fatal("wrong revision accepted")
	}
	body, _ := ws.ReadFile("n/b.txt")
	rev := hash(body)
	if _, err := tools.fileScoped("write", Input{Path: "n/b.txt", Text: "v2", Revision: rev}, ws, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.fileScoped("write", Input{Path: "n/c.txt", Text: "x", Root: "archive"}, ws, nil, nil); err == nil {
		t.Fatal("archive write accepted")
	}
	if _, err := tools.fileScoped("bogus", Input{Path: "a.md"}, ws, nil, nil); err == nil {
		t.Fatal("unknown op accepted")
	}

	// Governance: deny-all, family deny, and read-only caps on write tools.
	if reason := tools.toolPolicyDeny("file_read"); reason != "" {
		t.Fatalf("nil policy denied: %q", reason)
	}
	tools.ToolPolicy = &api.ToolPolicySnapshot{DenyAll: true}
	if tools.toolPolicyDeny("file_read") == "" {
		t.Fatal("deny-all did not deny")
	}
	tools.ToolPolicy = &api.ToolPolicySnapshot{Deny: []string{"files"}}
	if tools.toolPolicyDeny("file_read") == "" {
		t.Fatal("family deny did not deny")
	}
	tools.ToolPolicy = &api.ToolPolicySnapshot{ReadOnly: []string{"files"}}
	if tools.toolPolicyDeny("file_write") == "" {
		t.Fatal("read-only cap allowed a write tool")
	}
	if tools.toolPolicyDeny("file_read") != "" {
		t.Fatal("read-only cap denied a read tool")
	}
	tools.ReadOnlyTools = map[string]bool{"file_write": true}
	tools.ToolPolicy = nil
	if _, err := tools.call(t.Context(), "file_write", Input{}, ws, nil, nil); err == nil {
		t.Fatal("runtime read-only tool wrote")
	}
}
