package agenttools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileBoundsAndDiscovery(t *testing.T) {
	d := t.TempDir()
	v, err := Open(d, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err = Open("/missing", d); err == nil {
		t.Fatal("workspace")
	}
	if _, err = Open(d, "/missing"); err == nil {
		t.Fatal("archive")
	}
	_ = os.WriteFile(filepath.Join(d, "binary.txt"), []byte{255}, 0600)
	_ = os.WriteFile(filepath.Join(d, "huge.md"), []byte(strings.Repeat("a", Limit+1)), 0600)
	_ = os.Mkdir(filepath.Join(d, "folder"), 0700)
	_ = os.WriteFile(filepath.Join(d, "noext"), []byte("ignored"), 0600)
	for _, in := range []Input{{Path: "binary.txt"}, {Path: "huge.md"}, {Path: "folder"}, {Path: "missing"}, {Path: ".hub-writer.lock"}, {Root: "other"}} {
		if _, err = v.File("read", in); err == nil {
			t.Fatal(in)
		}
	}
	for _, in := range []Input{{Path: "new", Revision: "stale", Text: "x"}, {Path: "new", Text: strings.Repeat("x", Limit+1)}, {Path: "new", Text: string([]byte{255})}, {Path: "folder", Text: "x"}, {Path: "binary.txt", Text: "x"}} {
		if _, err = v.File("write", in); err == nil {
			t.Fatal("invalid write accepted")
		}
	}
	if _, err = v.File("unknown", Input{}); err == nil {
		t.Fatal("operation")
	}
	if _, err = v.File("search", Input{}); err == nil {
		t.Fatal("empty search")
	}
	if _, err = v.File("search", Input{Path: "missing", Query: "x"}); err == nil {
		t.Fatal("missing search root")
	}
	if _, err = v.File("list", Input{Path: "missing"}); err == nil {
		t.Fatal("missing list root")
	}
	for i := range 210 {
		_ = os.WriteFile(filepath.Join(d, fmt.Sprintf("match-%03d.md", i)), []byte("match"), 0600)
	}
	out, err := v.File("list", Input{})
	if err != nil || out["truncated"] != true {
		t.Fatal(out, err)
	}
	out, err = v.File("search", Input{Query: "match"})
	if err != nil || len(out["items"].([]map[string]any)) != 50 {
		t.Fatal(out, err)
	}
	out, err = v.File("read", Input{Path: "match-000.md"})
	if err != nil || out["text"] != "match" {
		t.Fatal(out, err)
	}
}
func TestHHReadContractsAndFailures(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	if _, err := v.API(ctx, "hh_search", Input{}); err == nil {
		t.Fatal("disabled")
	}
	v.HHEnabled = true
	for _, op := range []string{"hh_vacancy", "hh_resumes", "hh_apply", "unknown"} {
		if _, err := v.API(ctx, op, Input{Authorized: true}); err == nil {
			t.Fatal(op)
		}
	}
	v.HHKey = "fake"
	v.UserAgent = "test-client"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "test-client" {
			t.Error("user agent")
		}
		if r.URL.Path == "/vacancies" && r.URL.Query().Get("text") != "Go & AI" {
			t.Error("query encoding")
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	v.HHURL = s.URL
	for _, op := range []string{"hh_search", "hh_vacancy", "hh_resumes"} {
		if _, err := v.API(ctx, op, Input{ID: "123", Query: "Go & AI"}); err != nil {
			t.Fatal(op, err)
		}
	}
	s.Close()
	if _, err := v.API(ctx, "hh_search", Input{}); err == nil {
		t.Fatal("network failure")
	}
	for _, tc := range []struct {
		status int
		body   string
	}{{http.StatusTooManyRequests, `{"errors":[{"type":"limit"}]}`}, {http.StatusOK, "not json"}, {http.StatusOK, strings.Repeat("x", 16*1024*1024+1)}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		v.HHURL = server.URL
		if _, err := v.API(ctx, "hh_search", Input{}); err == nil {
			t.Fatal("upstream error accepted")
		}
		server.Close()
	}
	v.HHURL = "http://[invalid"
	if _, err := v.API(ctx, "hh_search", Input{}); err == nil {
		t.Fatal("invalid address")
	}
}
