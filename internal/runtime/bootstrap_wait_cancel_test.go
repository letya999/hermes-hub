package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBootstrapWaitHonorsCallerCancellation(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	state, workspace = t.TempDir(), t.TempDir()
	t.Cleanup(func() { state, workspace = oldState, oldWorkspace })
	t.Setenv("HUB_BOOTSTRAP_GOAL", "prepare")
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	started, release := make(chan struct{}, 1), make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			w.WriteHeader(201)
			return
		}
		if r.URL.Path == "/v1/runs" {
			started <- struct{}{}
			<-release
			fmt.Fprint(w, `{"run_id":"goal"}`)
			return
		}
		fmt.Fprint(w, `{"status":"completed","output":"done"}`)
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	first := make(chan error, 1)
	request := validExecuteRequest("alice", "personal", "key", "ordinary")
	go func() { first <- (&runtimeHTTP{}).ensureBootstrap(context.Background(), request) }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	second := make(chan error, 1)
	go func() { second <- (&runtimeHTTP{}).ensureBootstrap(ctx, request) }()
	returned := false
	select {
	case err := <-second:
		returned = true
		if err == nil {
			t.Error("cancelled wait succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("cancelled caller remained blocked on bootstrap mutex")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if !returned {
		<-second
	}
}
