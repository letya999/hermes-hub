package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootstrapGoalFinishesBeforeOrdinaryJobAndRecordsChanges(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	state, workspace = t.TempDir(), t.TempDir()
	t.Cleanup(func() { state, workspace = oldState, oldWorkspace })
	t.Setenv("HUB_BOOTSTRAP_GOAL", "prepare-toolchain-marker")
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	stamp := time.Unix(1000, 0)
	for _, name := range []string{"modified.txt", "deleted.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(workspace, name), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	var inputs []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/sessions":
			w.WriteHeader(201)
		case r.Method == "POST" && r.URL.Path == "/v1/runs":
			var payload hermesRunBody
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			inputs = append(inputs, payload.Input)
			if strings.Contains(payload.Input, "prepare-toolchain-marker") {
				if err := os.WriteFile(filepath.Join(workspace, "modified.txt"), []byte("new"), 0600); err != nil {
					t.Error(err)
				}
				if err := os.Chtimes(filepath.Join(workspace, "modified.txt"), stamp, stamp); err != nil {
					t.Error(err)
				}
				if err := os.Remove(filepath.Join(workspace, "deleted.txt")); err != nil {
					t.Error(err)
				}
				if err := os.WriteFile(filepath.Join(workspace, "toolchain.txt"), []byte("installed"), 0600); err != nil {
					t.Error(err)
				}
			}
			_, _ = fmt.Fprintf(w, `{"run_id":"run-%d"}`, len(inputs))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/runs/"):
			_, _ = fmt.Fprint(w, `{"status":"completed","output":"done"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	r := validExecuteRequest("alice", "personal", "ordinary-key", "ordinary-job-marker")
	r.JobID = "ordinary-job"
	if _, err := (&runtimeHTTP{}).executePersistent(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.JobID, r.IdempotencyKey = "next-job", "next-key"
	if _, err := (&runtimeHTTP{}).executePersistent(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 3 || !strings.Contains(inputs[0], "prepare-toolchain-marker") || !strings.Contains(inputs[1], "ordinary-job-marker") || !strings.Contains(inputs[len(inputs)-1], "ordinary-job-marker") {
		t.Errorf("once-only bootstrap did not precede ordinary admission: %v", inputs)
	}
	body, err := os.ReadFile(filepath.Join(state, "bootstrap.json"))
	if err != nil || !strings.Contains(string(body), `"status":"ready"`) || !strings.Contains(string(body), "toolchain.txt") {
		t.Errorf("bootstrap state/change receipt missing: %s %v", body, err)
	}
	if !strings.Contains(string(body), "modified.txt") || !strings.Contains(string(body), "deleted.txt") {
		t.Errorf("bootstrap diff omitted modification/deletion: %s", body)
	}
}
