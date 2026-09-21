package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/credential-broker/app"
)

func TestControlCLIAndRuntimeRefusal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "install")
	if e := app.Init(root); e != nil {
		t.Fatal(e)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("no signed assertion")
		}
		if r.URL.Path == "/v1/error" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"code":"denied"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer up.Close()
	args := []string{"--url", up.URL, "--key-file", filepath.Join(root, "keys/toolhub.private"), "--actor-file", filepath.Join(root, "actor.json")}
	var out bytes.Buffer
	if e := run(context.Background(), args, &out); e != nil || !strings.Contains(out.String(), "ok") {
		t.Fatal(e, out.String())
	}
	if e := run(context.Background(), append(args, "--path", "/v1/runtime/leases/anything/materialize"), io.Discard); e == nil {
		t.Fatal("runtime print prohibited")
	}
	if e := run(context.Background(), append(args, "--audience", "broker:runtime"), io.Discard); e == nil {
		t.Fatal("runtime audience")
	}
	if e := run(context.Background(), append(args, "--path", "/v1/error"), io.Discard); e == nil {
		t.Fatal("API error")
	}
	body := filepath.Join(root, "request.json")
	if e := app.WriteJSON(body, map[string]string{"idempotency_key": "job_once"}); e != nil {
		t.Fatal(e)
	}
	if e := run(context.Background(), append(args, "--method", "POST", "--body-file", body), io.Discard); e != nil {
		t.Fatal(e)
	}
	_ = os.WriteFile(body, []byte(`{"x":1,"x":2}`), 0600)
	if e := run(context.Background(), append(args, "--body-file", body), io.Discard); e == nil {
		t.Fatal("duplicate JSON")
	}
	if e := run(context.Background(), append(args, "--body-file", "/missing"), io.Discard); e == nil {
		t.Fatal("missing body")
	}
	if e := run(context.Background(), append(args, "--url", "http://evil.example"), io.Discard); e == nil {
		t.Fatal("HTTPS")
	}
	if e := run(context.Background(), append(args, "--actor-file", "/missing"), io.Discard); e == nil {
		t.Fatal("actor")
	}
	_ = os.WriteFile(filepath.Join(root, "actor.json"), []byte(`invalid`), 0600)
	if e := run(context.Background(), args, io.Discard); e == nil {
		t.Fatal("actor JSON")
	}
	private := filepath.Join(root, "keys/toolhub.private")
	_ = os.Chmod(private, 0644)
	if e := run(context.Background(), args, io.Discard); e == nil {
		t.Fatal("key permissions")
	}
	_ = os.Chmod(private, 0600)
	_ = os.WriteFile(private, []byte("bad"), 0600)
	if e := run(context.Background(), args, io.Discard); e == nil {
		t.Fatal("key size")
	}
	for _, args := range [][]string{nil, {"--bad"}, {"extra"}} {
		if e := run(context.Background(), args, io.Discard); e == nil {
			t.Fatal(args)
		}
	}
}
