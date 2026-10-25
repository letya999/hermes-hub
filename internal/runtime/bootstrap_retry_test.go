package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrapExplicitRevisionStartsNewAttempt(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	state, workspace = t.TempDir(), t.TempDir()
	t.Cleanup(func() { state, workspace = oldState, oldWorkspace })
	t.Setenv("HUB_BOOTSTRAP_GOAL", "prepare")
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	t.Setenv("HUB_BOOTSTRAP_REVISION", "1")
	var keys []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			w.WriteHeader(201)
			return
		}
		if r.URL.Path == "/v1/runs" {
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			if len(keys) == 1 {
				w.WriteHeader(500)
				return
			}
			fmt.Fprintf(w, `{"run_id":"run-%d"}`, len(keys))
			return
		}
		fmt.Fprint(w, `{"status":"completed","output":"done"}`)
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	r := validExecuteRequest("alice", "personal", "ordinary-key", "ordinary")
	r.JobID = "ordinary-job"
	if _, err := (&runtimeHTTP{}).executePersistent(context.Background(), r); err == nil {
		t.Fatal("fixture did not fail bootstrap")
	}
	t.Setenv("HUB_BOOTSTRAP_REVISION", "2")
	result, err := (&runtimeHTTP{}).executePersistent(context.Background(), r)
	if err != nil || result.Status != "completed" || len(keys) != 3 {
		t.Errorf("explicit retry failed to unlock ordinary work: status=%s calls=%d error=%v", result.Status, len(keys), err)
	}
	if len(keys) < 2 || keys[0] == keys[1] || keys[1] == "" {
		t.Error("explicit retry reused the failed attempt's idempotency key")
	}
}
