package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestConcurrentJobsWaitForOneBootstrap(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	state, workspace = t.TempDir(), t.TempDir()
	t.Cleanup(func() { state, workspace = oldState, oldWorkspace })
	t.Setenv("HUB_BOOTSTRAP_GOAL", "prepare")
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	started, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			w.WriteHeader(201)
			return
		}
		if r.URL.Path == "/v1/runs" {
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"run_id":"goal"}`)
			return
		}
		fmt.Fprint(w, `{"status":"completed","output":"done"}`)
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := validExecuteRequest("alice", "personal", "ordinary", "ordinary")
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- (&runtimeHTTP{}).ensureBootstrap(ctx, r) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("bootstrap never started")
	}
	go func() { second <- (&runtimeHTTP{}).ensureBootstrap(ctx, r) }()
	returned := false
	select {
	case err := <-second:
		returned = true
		t.Errorf("second job did not wait for preparation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	if err := <-first; err != nil {
		t.Error(err)
	}
	if !returned {
		if err := <-second; err != nil {
			t.Error(err)
		}
	}
	select {
	case <-started:
		t.Error("duplicate bootstrap admitted")
	default:
	}
}
