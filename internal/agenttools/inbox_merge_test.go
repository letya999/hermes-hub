package agenttools

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inboxFetch stages inbound files under the admitted root with a durable
// receipt: plain bytes land as a single file, zips unpack bounded, unsafe
// names/ids are refused, and a completed receipt replays without refetch.
func TestInboxFetchStagingAndReplay(t *testing.T) {
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	var zipBody bytes.Buffer
	zw := zip.NewWriter(&zipBody)
	fw, _ := zw.Create("inner.txt")
	_, _ = fw.Write([]byte("zipped"))
	_ = zw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Hub-Principal") != "alice" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound/plain1":
			w.Header().Set("X-Hub-Name", "notes.txt")
			_, _ = w.Write([]byte("payload"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound/zip1":
			w.Header().Set("X-Hub-Name", "bundle.zip")
			_, _ = w.Write(zipBody.Bytes())
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound/badname":
			w.Header().Set("X-Hub-Name", "../evil")
			_, _ = w.Write([]byte("x"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound/missing":
			http.Error(w, "gone", http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/consume"):
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	ws, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	tools := &Tools{Workspace: ws, CommunicationURL: srv.URL, CommunicationAuth: "tok", HTTP: srv.Client()}

	got, err := tools.inboxFetch(t.Context(), ws, Input{ID: "plain1"})
	if err != nil {
		t.Fatal(err)
	}
	if got["size"].(int) != 7 || got["acknowledged"] != true {
		t.Fatalf("plain fetch: %v", got)
	}
	if b, err := ws.ReadFile("inbox/plain1/files/notes.txt"); err != nil || string(b) != "payload" {
		t.Fatalf("staged file: %v", err)
	}
	again, err := tools.inboxFetch(t.Context(), ws, Input{ID: "plain1"})
	if err != nil || again["acknowledged"] != true || again["size"].(float64) != 7 {
		t.Fatalf("receipt replay: %v %v", again, err)
	}
	got, err = tools.inboxFetch(t.Context(), ws, Input{ID: "zip1"})
	if err != nil || got["path"] != "inbox/zip1/files" {
		t.Fatalf("zip fetch: %v %v", got, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "inbox", "zip1", "files", "inner.txt")); err != nil || string(b) != "zipped" {
		t.Fatalf("unpacked file: %v", err)
	}
	for _, id := range []string{"missing", "../escape", "plain1/extra"} {
		if _, err := tools.inboxFetch(t.Context(), ws, Input{ID: id}); err == nil {
			t.Fatalf("unsafe or missing inbound accepted: %s", id)
		}
	}
	noHub := &Tools{Workspace: ws}
	if _, err := noHub.inboxFetch(t.Context(), ws, Input{ID: "plain1"}); err == nil {
		t.Fatal("inbox fetch without hub url accepted")
	}
}
