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

func TestBootstrapProviderCallHonorsHardRunBudget(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	state, workspace = t.TempDir(), t.TempDir()
	t.Cleanup(func() { state, workspace = oldState, oldWorkspace })
	t.Setenv("HUB_BOOTSTRAP_GOAL", "prepare")
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	t.Setenv("HUB_RUN_BUDGET", "50ms")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			w.WriteHeader(201)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			w.WriteHeader(500)
		}
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	r := validExecuteRequest("alice", "personal", "goal", "prepare")
	start := time.Now()
	err := (&runtimeHTTP{}).ensureBootstrap(context.Background(), r)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("bootstrap escaped 50ms run budget: elapsed=%v error=%v", time.Since(start), err)
	}
}
