package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrapRefusesUnmanagedHostAuthority(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	state, workspace = t.TempDir(), t.TempDir()
	t.Cleanup(func() { state, workspace = oldState, oldWorkspace })
	t.Setenv("HUB_BOOTSTRAP_GOAL", "prepare")
	t.Setenv("HUB_CAPABILITY_MODE", "")
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/api/sessions" {
			w.WriteHeader(201)
		} else if r.URL.Path == "/v1/runs" {
			fmt.Fprint(w, `{"run_id":"goal"}`)
		} else {
			fmt.Fprint(w, `{"status":"completed","output":"done"}`)
		}
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	r := validExecuteRequest("alice", "personal", "goal", "prepare")
	if err := (&runtimeHTTP{}).ensureBootstrap(context.Background(), r); err == nil || calls != 0 {
		t.Fatalf("bootstrap used unmanaged authority: calls=%d error=%v", calls, err)
	}
}
