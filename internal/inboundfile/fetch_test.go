package inboundfile

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stageServer emulates the communication /v1/inbound surface: GET returns the
// staged body with name/source headers, POST .../consume records an ack.
type stageServer struct {
	srv       *httptest.Server
	consumed  []string
	status    int
	body      []byte
	name      string
	source    string
	consumeOK int
}

func newStage(t *testing.T) *stageServer {
	t.Helper()
	s := &stageServer{status: http.StatusOK, name: "report.txt", source: "people", consumeOK: http.StatusOK}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/inbound/")
		if r.Method == http.MethodPost && strings.HasSuffix(id, "/consume") {
			s.consumed = append(s.consumed, strings.TrimSuffix(id, "/consume"))
			w.WriteHeader(s.consumeOK)
			return
		}
		if s.status != http.StatusOK {
			w.WriteHeader(s.status)
			return
		}
		w.Header().Set("X-Hub-Name", s.name)
		if s.source != "" {
			w.Header().Set("X-Hub-Source", s.source)
		}
		w.Write(s.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func fetchInto(t *testing.T, ws *os.Root, s *stageServer, id string) (map[string]any, error) {
	t.Helper()
	return Fetch(context.Background(), ws, "", id, s.srv.URL, "tok", "principal", s.srv.Client(), nil, SafeUnpack)
}

func openWS(t *testing.T) *os.Root {
	t.Helper()
	ws, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

func TestFetchRejectsUnsafeIDs(t *testing.T) {
	s := newStage(t)
	ws := openWS(t)
	for _, id := range []string{"a/b", "..", ".", `a\b`, "a\x00b"} {
		if _, err := fetchInto(t, ws, s, id); err == nil {
			t.Fatalf("id %q accepted", id)
		}
	}
	if _, err := Fetch(context.Background(), ws, "", "x", "", "tok", "p", nil, nil, nil); err == nil {
		t.Fatal("empty communication URL accepted")
	}
}

func TestFetchStoresSingleFileAndConsumes(t *testing.T) {
	s := newStage(t)
	s.body = []byte("payload-bytes")
	ws := openWS(t)
	result, err := fetchInto(t, ws, s, "id-1")
	if err != nil {
		t.Fatal(err)
	}
	if result["path"] != "inbox/people/id-1/files/report.txt" || result["size"] != len(s.body) || result["acknowledged"] != true {
		t.Fatalf("unexpected result: %v", result)
	}
	if len(s.consumed) != 1 || s.consumed[0] != "id-1" {
		t.Fatalf("consume not called: %v", s.consumed)
	}
	body, err := ws.ReadFile("people/id-1/files/report.txt")
	if err != nil || string(body) != "payload-bytes" {
		t.Fatalf("file not materialized: %v %q", err, body)
	}
	if _, err := ws.ReadFile("people/id-1/receipt.json"); err != nil {
		t.Fatalf("receipt missing: %v", err)
	}
	// A second fetch serves the recorded receipt without another GET.
	again, err := fetchInto(t, ws, s, "id-1")
	if err != nil || again["name"] != "report.txt" {
		t.Fatalf("receipt read failed: %v %v", again, err)
	}
}

func TestFetchStoresZipEntries(t *testing.T) {
	s := newStage(t)
	s.name = "bundle.zip"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{"a.txt": "one", "dir/b.txt": "two"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	s.body = buf.Bytes()
	ws := openWS(t)
	result, err := fetchInto(t, ws, s, "z-1")
	if err != nil {
		t.Fatal(err)
	}
	if result["path"] != "inbox/people/z-1/files" || result["files"] != 2 {
		t.Fatalf("unexpected zip result: %v", result)
	}
	if body, err := ws.ReadFile("people/z-1/files/dir/b.txt"); err != nil || string(body) != "two" {
		t.Fatalf("zip entry missing: %v %q", err, body)
	}
}

func TestFetchServiceSource(t *testing.T) {
	s := newStage(t)
	s.source = "services"
	s.body = []byte("svc")
	ws := openWS(t)
	result, err := fetchInto(t, ws, s, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if result["source"] != "services" || result["path"] != "inbox/services/s-1/files/report.txt" {
		t.Fatalf("unexpected services result: %v", result)
	}
}

func TestFetchRefusals(t *testing.T) {
	ws := openWS(t)
	// Unknown source header.
	s := newStage(t)
	s.source = "elsewhere"
	s.body = []byte("x")
	if _, err := fetchInto(t, ws, s, "e-1"); err == nil {
		t.Fatal("unknown source accepted")
	}
	// Unsafe base name.
	s = newStage(t)
	s.name = `a\b`
	s.body = []byte("x")
	if _, err := fetchInto(t, ws, s, "e-2"); err == nil {
		t.Fatal("unsafe name accepted")
	}
	// Rejected GET.
	s = newStage(t)
	s.status = http.StatusForbidden
	if _, err := fetchInto(t, ws, s, "e-3"); err == nil {
		t.Fatal("rejected GET accepted")
	}
	// Existing target without a completed receipt refuses, leaving the dir.
	s = newStage(t)
	s.body = []byte("x")
	if err := ws.MkdirAll("people/e-4", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchInto(t, ws, s, "e-4"); err == nil {
		t.Fatal("pre-existing target accepted")
	}
	// Consume failure flips acknowledged, the write still lands.
	s = newStage(t)
	s.body = []byte("x")
	s.consumeOK = http.StatusBadGateway
	result, err := fetchInto(t, ws, s, "e-5")
	if err != nil || result["acknowledged"] != false {
		t.Fatalf("consume failure mishandled: %v %v", result, err)
	}
}

func TestFetchCorruptReceiptRefused(t *testing.T) {
	s := newStage(t)
	ws := openWS(t)
	if err := ws.MkdirAll("people/c-1", 0700); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile("people/c-1/receipt.json", []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchInto(t, ws, s, "c-1"); err == nil {
		t.Fatal("corrupt receipt accepted")
	}
}

func TestMaterializeInboundValidation(t *testing.T) {
	if _, err := MaterializeInbound(context.Background(), "relative/path", "principal", "http://x", "tok", "id"); err == nil {
		t.Fatal("relative inbox root accepted")
	}
	if _, err := MaterializeInbound(context.Background(), t.TempDir(), "bad principal!", "http://x", "tok", "id"); err == nil {
		t.Fatal("invalid principal accepted")
	}
	if _, err := MaterializeInbound(context.Background(), t.TempDir(), "principal", "", "tok", "id"); err == nil {
		t.Fatal("empty URL accepted")
	}
	if _, err := MaterializeInbound(context.Background(), t.TempDir(), "principal", "http://x", "", "id"); err == nil {
		t.Fatal("empty auth accepted")
	}
	s := newStage(t)
	s.body = []byte("inbox")
	dir := t.TempDir()
	result, err := MaterializeInbound(context.Background(), dir, "principal-1", s.srv.URL, "tok", "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if result["path"] != "inbox/people/m-1/files/report.txt" {
		t.Fatalf("materialized to wrong path: %v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "people", "m-1", "receipt.json")); err != nil {
		t.Fatalf("receipt not under inbox root: %v", err)
	}
}

func TestSafeUnpackGuards(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("x.txt")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("y"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	ws := openWS(t)
	// Target dir already exists → exclusive mkdir refuses.
	if err := ws.MkdirAll("dup/target", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeUnpack(ws, zr, "dup/target"); err == nil {
		t.Fatal("existing unpack target accepted")
	}
}

func TestCleanZipName(t *testing.T) {
	bad := []string{"", "/", "../x", "/etc/passwd", `a\b`, "a:b", "a//b", "./x", "a/../x", "x\x00"}
	for _, name := range bad {
		if _, err := cleanZipName(name); err == nil {
			t.Fatalf("unsafe name %q accepted", name)
		}
	}
	for _, name := range []string{"a", "dir/file.txt", "a.b/c-d_e"} {
		if got, err := cleanZipName(name); err != nil || got != name {
			t.Fatalf("safe name %q rejected: %v %q", name, err, got)
		}
	}
}

func TestSafeUnpackRefusesTraversalEntry(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../evil")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("x"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	ws := openWS(t)
	if _, err := SafeUnpack(ws, zr, "people/x/files"); err == nil {
		t.Fatal("traversal entry unpacked")
	}
	// The partial target is removed on failure.
	if _, err := ws.Stat("people/x/files"); !os.IsNotExist(err) {
		t.Fatalf("partial target left behind: %v", err)
	}
}

func TestReceiptShapeRoundTrip(t *testing.T) {
	s := newStage(t)
	s.body = []byte("x")
	ws := openWS(t)
	if _, err := fetchInto(t, ws, s, "r-1"); err != nil {
		t.Fatal(err)
	}
	body, err := ws.ReadFile("people/r-1/receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil || decoded["name"] != "report.txt" {
		t.Fatalf("receipt unreadable: %v %v", decoded, err)
	}
}
