package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapFailurePersistsNotReadyWithoutAutomaticRetry(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	state, workspace = t.TempDir(), t.TempDir()
	t.Cleanup(func() { state, workspace = oldState, oldWorkspace })
	t.Setenv("HUB_BOOTSTRAP_GOAL", "prepare")
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			w.WriteHeader(201)
			return
		}
		if r.URL.Path == "/v1/runs" {
			body, err := os.ReadFile(filepath.Join(state, "bootstrap.json"))
			if err != nil || !strings.Contains(string(body), `"status":"running"`) {
				t.Errorf("bootstrap admission not durable before launch: %s %v", body, err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "partial.txt"), []byte("partial install"), 0600); err != nil {
				t.Error(err)
			}
			calls++
			w.WriteHeader(500)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	r := validExecuteRequest("alice", "personal", "ordinary-key", "do not admit me")
	r.JobID = "ordinary-job"
	for i := 0; i < 2; i++ {
		result, err := (&runtimeHTTP{}).executePersistent(context.Background(), r)
		if err == nil || result.LastEvent != "bootstrap.not_ready" {
			t.Errorf("ordinary job not blocked after bootstrap failure: %+v %v", result, err)
		}
	}
	if calls != 1 {
		t.Errorf("bootstrap automatically retried: calls=%d", calls)
	}
	body, err := os.ReadFile(filepath.Join(state, "bootstrap.json"))
	if err != nil || !strings.Contains(string(body), `"status":"failed"`) {
		t.Errorf("named not-ready state missing: %s %v", body, err)
	}
	if !strings.Contains(string(body), "partial.txt") || !strings.Contains(string(body), `"changes_available":true`) {
		t.Errorf("failed bootstrap omitted inspectable partial changes: %s", body)
	}
}
