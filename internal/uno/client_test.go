package uno

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Client.call is the single HTTP contract every Uno verb shares: JSON
// body+auth out, bounded response in, status classes mapped to Error.
func TestClientCallContract(t *testing.T) {
	var sawAuth, sawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		sawBody = string(buf[:n])
		switch r.URL.Path {
		case "/v1/boxes":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"boxes": []any{}})
		case "/v1/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/v1/boom":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("server exploded"))
		case "/v1/teapot":
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("no"))
		case "/v1/badjson":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{oops"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok", nil)

	var out map[string]any
	if err := c.call(context.Background(), "list", http.MethodGet, "/v1/boxes", nil, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if _, ok := out["boxes"]; !ok {
		t.Fatalf("response: %v", out)
	}
	if err := c.call(context.Background(), "launch", http.MethodPost, "/v1/boxes", map[string]string{"name": "b"}, nil); err != nil {
		t.Fatalf("post: %v", err)
	}
	if sawAuth != "Bearer tok" || !strings.Contains(sawBody, `"name":"b"`) {
		t.Fatalf("request contract: auth=%q body=%q", sawAuth, sawBody)
	}
	if err := c.call(context.Background(), "get", http.MethodGet, "/v1/missing", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	err := c.call(context.Background(), "x", http.MethodGet, "/v1/boom", nil, nil)
	var uerr *Error
	if !errors.As(err, &uerr) || uerr.Status != 500 || !uerr.Ambiguous || !uerr.Retryable() {
		t.Fatalf("500: %v", err)
	}
	err = c.call(context.Background(), "x", http.MethodGet, "/v1/teapot", nil, nil)
	if !errors.As(err, &uerr) || uerr.Status != 418 || uerr.Ambiguous || uerr.Retryable() {
		t.Fatalf("4xx: %v", err)
	}
	if err := c.call(context.Background(), "x", http.MethodGet, "/v1/badjson", nil, &out); err == nil {
		t.Fatal("bad JSON accepted")
	}
	// Unreachable host is a transport-level failure: no status, and the
	// refusal is not a timeout so Ambiguous stays false — Retryable still
	// covers status-0 failures.
	dead := NewClient("http://127.0.0.1:1", "", &http.Client{Timeout: 200 * time.Millisecond})
	err = dead.call(context.Background(), "x", http.MethodGet, "/v1/boxes", nil, nil)
	if !errors.As(err, &uerr) || uerr.Status != 0 || uerr.Ambiguous || !uerr.Retryable() {
		t.Fatalf("transport: %v", err)
	}
}

// Error renders op, message and status into the operator-visible string.
func TestErrorString(t *testing.T) {
	err := (&Error{Op: "run", Status: 502, Message: "bad gateway"}).Error()
	if !strings.Contains(err, "run") || !strings.Contains(err, "502") || !strings.Contains(err, "bad gateway") {
		t.Fatalf("render: %q", err)
	}
}

// Verb wrappers share the call contract; each maps its path and envelope.
func TestClientVerbs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/boxes":
			_ = json.NewEncoder(w).Encode(map[string]any{"boxes": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/boxes":
			_ = json.NewEncoder(w).Encode(map[string]any{"box": map[string]any{"id": "new"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/boxes/ghost":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/boxes/b1/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"id": "r0", "status": "pending"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/boxes/b1":
			_ = json.NewEncoder(w).Encode(map[string]any{"box": map[string]any{"id": "b1"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/boxes/b1/runs/r9":
			if r.URL.Query().Get("wait_s") != "5" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"id": "r9", "status": "done"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/boxes/b1/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []map[string]any{
				{"id": "r1", "idempotency_key": "k1"},
				{"id": "r2", "idempotency_key": "k2"},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", nil)
	ctx := context.Background()

	boxes, err := c.ListBoxes(ctx)
	if err != nil || len(boxes) != 0 {
		t.Fatalf("ListBoxes: %v %+v", err, boxes)
	}
	launched, err := c.LaunchBox(ctx, LaunchRequest{Image: "img"})
	if err != nil {
		t.Fatalf("LaunchBox: %v", err)
	}
	_ = launched
	if err := c.DeleteBox(ctx, "ghost"); err != nil {
		t.Fatalf("DeleteBox swallows 404: %v", err)
	}
	submitted, err := c.Run(ctx, "b1", RunRequest{Cmd: "true"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = submitted

	box, err := c.GetBox(ctx, "b1")
	if err != nil || box.ID != "b1" {
		t.Fatalf("GetBox: %v %+v", err, box)
	}
	if _, err := c.GetBox(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBox 404: %v", err)
	}
	run, err := c.GetRun(ctx, "b1", "r9", 5)
	if err != nil || run.ID != "r9" {
		t.Fatalf("GetRun: %v %+v", err, run)
	}
	run, ok, err := c.FindRunByKey(ctx, "b1", "k2")
	if err != nil || !ok || run.ID != "r2" {
		t.Fatalf("FindRunByKey hit: %v %v %+v", err, ok, run)
	}
	if _, ok, err := c.FindRunByKey(ctx, "b1", "ghost"); err != nil || ok {
		t.Fatalf("FindRunByKey miss: %v %v", err, ok)
	}
}
