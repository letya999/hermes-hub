package agenttools

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInboxFetch(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	consumed := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound/in-zip":
			w.Header().Set("X-Hub-Name", "pack.zip")
			_, _ = w.Write(zipBytes(t, map[string]string{"a.txt": "one", "sub/b.txt": "two"}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound/in-doc":
			w.Header().Set("X-Hub-Name", "note.txt")
			_, _ = w.Write([]byte("hello"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/inbound/in-zip/consume":
			consumed["in-zip"] = true
		case r.Method == http.MethodPost && r.URL.Path == "/v1/inbound/in-doc/consume":
			consumed["in-doc"] = true
		default:
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	v.CommunicationAuth = "control-token"
	ctx := context.Background()

	if _, err := v.inboxFetch(ctx, v.Workspace, Input{}); err == nil {
		t.Fatal("missing hub url accepted")
	}
	res, err := v.inboxFetch(ctx, v.Workspace, Input{ID: "in-zip"})
	if err != nil {
		t.Fatalf("zip fetch: %v", err)
	}
	if res["files"] != 2 {
		t.Fatalf("zip manifest: %v", res)
	}
	ws := v.Workspace
	root := ws.Name()
	if body, err := os.ReadFile(filepath.Join(root, "inbox", "pack", "a.txt")); err != nil || string(body) != "one" {
		t.Fatalf("unpacked file missing: %v %q", err, body)
	}
	if body, err := os.ReadFile(filepath.Join(root, "inbox", "pack", "sub", "b.txt")); err != nil || string(body) != "two" {
		t.Fatalf("nested unpacked file missing: %v %q", err, body)
	}
	if _, err := os.Lstat(filepath.Join(root, "inbox", "pack.zip")); !os.IsNotExist(err) {
		t.Fatal("zip persisted on the workspace")
	}
	if !consumed["in-zip"] {
		t.Fatal("staged zip not consumed")
	}

	res, err = v.inboxFetch(ctx, v.Workspace, Input{ID: "in-doc"})
	if err != nil {
		t.Fatalf("doc fetch: %v", err)
	}
	if res["path"] != "inbox/note.txt" {
		t.Fatalf("doc path: %v", res)
	}
	if body, err := os.ReadFile(filepath.Join(root, "inbox", "note.txt")); err != nil || string(body) != "hello" {
		t.Fatalf("fetched file missing: %v %q", err, body)
	}
	if !consumed["in-doc"] {
		t.Fatal("staged doc not consumed")
	}

	// A second file sharing the same name must not overwrite the first.
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("X-Hub-Name", "note.txt")
			_, _ = w.Write([]byte("other"))
			return
		}
	}))
	defer server2.Close()
	v.CommunicationURL = server2.URL
	res, err = v.inboxFetch(ctx, v.Workspace, Input{ID: "in-9"})
	if err != nil {
		t.Fatalf("name collision fetch: %v", err)
	}
	if res["path"] == "inbox/note.txt" {
		t.Fatal("second file overwrote the first")
	}
	if body, err := os.ReadFile(filepath.Join(root, "inbox", "note.txt")); err != nil || string(body) != "hello" {
		t.Fatalf("first file clobbered: %v %q", err, body)
	}

	// Dispatch and catalog: the tool must be callable by name and listed.
	if _, err := v.Call(ctx, "inbox_fetch", Input{ID: "in-missing"}); err != nil && strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("inbox_fetch not dispatched: %v", err)
	}
	found := false
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name == "inbox_fetch" {
			found = true
		}
	}
	if !found {
		t.Fatal("inbox_fetch missing from the hub catalog")
	}
}
