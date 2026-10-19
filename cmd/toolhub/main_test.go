package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunRequiresStore(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_STORE", "")
	if err := run(); err == nil {
		t.Fatal("missing ToolHub store accepted")
	}
}

func TestRunRejectsInvalidListenAddressAfterLoadingStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(storePath, []byte(`{"schema":1,"definitions":[],"connections":[],"credential_references":[],"bindings":[],"workloads":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "invalid-listen-address")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	if err := run(); err == nil {
		t.Fatal("invalid listen address accepted")
	}
}

// CHG-0129 S2.2: the public listener is opt-in — no HUB_PUBLIC_LISTEN means
// no second server; with it, the handler's public surface is served and
// control paths are absent.
func TestRunFailsOnUnusablePublicListen(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(storePath, []byte(`{"schema":1,"definitions":[],"connections":[],"credential_references":[],"bindings":[],"workloads":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:0")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	t.Setenv("HUB_PUBLIC_LISTEN", "no-such-host.invalid:80")
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unusable public listen address accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() ignored an unusable public listen address")
	}
}

func TestRunServesPublicSurface(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(storePath, []byte(`{"schema":1,"definitions":[],"connections":[],"credential_references":[],"bindings":[],"workloads":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	publicAddr := free.Addr().String()
	free.Close()
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:0")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	t.Setenv("HUB_PUBLIC_LISTEN", publicAddr)
	go func() { _ = run() }()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get("http://" + publicAddr + "/confirm/x?t=t")
		if err == nil {
			resp.Body.Close()
			// Reaching the confirm handler with an unknown onboarding is a 401;
			// a refused connection means the public listener never started.
			if resp.StatusCode == http.StatusUnauthorized {
				return
			}
			t.Fatalf("public surface status=%d", resp.StatusCode)
		}
		if time.Now().After(deadline) {
			t.Fatalf("public listener never answered: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPublicServerOptIn(t *testing.T) {
	t.Setenv("HUB_PUBLIC_LISTEN", "")
	if srv, ln, err := publicServerFor(handlerFunc{}); err != nil || srv != nil || ln != nil {
		t.Fatal("public listener should be absent without HUB_PUBLIC_LISTEN")
	}
	t.Setenv("HUB_PUBLIC_LISTEN", "127.0.0.1:0")
	if _, _, err := publicServerFor(handlerFunc{}); err == nil {
		t.Fatal("handler without a public surface accepted")
	}
	srv, ln, err := publicServerFor(withPublic{})
	if err != nil || srv == nil || ln == nil {
		t.Fatalf("public server=%v err=%v", srv, err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()
	base := "http://" + ln.Addr().String()
	resp, err := http.Get(base + "/confirm/x?t=t")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("public surface status=%d", resp.StatusCode)
	}
	blocked, err := http.Get(base + "/v1/connectors")
	if err != nil {
		t.Fatal(err)
	}
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusNotFound {
		t.Fatalf("control path on public surface status=%d", blocked.StatusCode)
	}
}

type handlerFunc struct{}

func (handlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

type withPublic struct{ handlerFunc }

func (withPublic) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/confirm/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	return mux
}
