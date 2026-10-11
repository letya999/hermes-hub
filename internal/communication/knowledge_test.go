package communication

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Knowledge sources are read-only files staged per user in the hub spool by
// the operator (docs, saved pages, connector exports). The tools plane lists
// and reads them through the control API; nothing lands in the workspace and
// the agent can never write here.
func TestKnowledgeEndpointScopedReadOnly(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(g.spool.root, "knowledge", "alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# spec notes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(g.spool.root, "knowledge", "bob"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.spool.root, "knowledge", "bob", "secret.txt"), []byte("bob only"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	call := func(path, principal string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+c.ControlAuth)
		req.Header.Set("X-Hub-Principal", principal)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, _ := call("/v1/knowledge", "mallory"); code != http.StatusUnauthorized {
		t.Fatalf("unknown principal got %d", code)
	}
	code, body := call("/v1/knowledge", "alice")
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	var items []KnowledgeItem
	if err := json.Unmarshal([]byte(body), &items); err != nil || len(items) != 1 || items[0].Name != "notes.md" {
		t.Fatalf("list: %d %s", code, body)
	}
	code, body = call("/v1/knowledge/notes.md", "alice")
	if code != http.StatusOK || body != "# spec notes" {
		t.Fatalf("read: %d %q", code, body)
	}
	// Scope: bob's files are invisible to alice — her listing never sees them.
	if code, _ = call("/v1/knowledge/secret.txt", "alice"); code != http.StatusNotFound {
		t.Fatalf("foreign knowledge served: %d", code)
	}
	// Traversal and non-file names are rejected.
	if code, _ = call("/v1/knowledge/..%2F..%2Fschedules", "alice"); code == http.StatusOK {
		t.Fatal("traversal served")
	}
	// Writes are not part of the contract.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/knowledge/new.md", nil)
	req.Header.Set("Authorization", "Bearer "+c.ControlAuth)
	req.Header.Set("X-Hub-Principal", "alice")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode == http.StatusOK {
		t.Fatalf("knowledge write accepted")
	}
}
