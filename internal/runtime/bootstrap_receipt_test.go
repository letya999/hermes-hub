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
)

func TestBootstrapRetainsInspectableBoundedAttemptReceipts(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	state, workspace = t.TempDir(), t.TempDir()
	t.Cleanup(func() { state, workspace = oldState, oldWorkspace })
	t.Setenv("HUB_BOOTSTRAP_GOAL", "prepare")
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	t.Setenv("HUB_BOOTSTRAP_REVISION", "1")
	secret := "sk-proj-abcdef1234567890secret"
	runs := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/sessions":
			w.WriteHeader(201)
		case r.URL.Path == "/v1/runs":
			runs++
			os.WriteFile(filepath.Join(workspace, fmt.Sprintf("installed-%d.txt", runs)), []byte("toolchain"), 0600)
			fmt.Fprintf(w, `{"run_id":"goal-%d"}`, runs)
		case r.URL.Path == "/v1/runs/goal-1":
			json.NewEncoder(w).Encode(map[string]string{"status": "failed", "error": "bootstrap-failure-marker " + secret + strings.Repeat("z", 6000)})
		case r.URL.Path == "/v1/runs/goal-2":
			json.NewEncoder(w).Encode(map[string]string{"status": "completed", "output": "bootstrap-success-marker " + strings.Repeat("z", 6000)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(api.Listener.Addr().(*net.TCPAddr).Port))
	r := validExecuteRequest("alice", "personal", "ordinary", "ordinary")
	if err := (&runtimeHTTP{}).ensureBootstrap(context.Background(), r); err == nil {
		t.Fatal("expected failed preparation")
	}
	inspect := func(want, marker string) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(state, "bootstrap.json"))
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal(body, &record); err != nil {
			t.Fatal(err)
		}
		output, _ := record["output"].(string)
		if record["status"] != want || record["session_id"] == nil || record["run_id"] == nil || !strings.Contains(output, marker) || len([]rune(output)) > 4096 || strings.Contains(string(body), secret) {
			t.Errorf("uninspectable/unbounded/unredacted bootstrap receipt: status=%v output bytes=%d session=%v run=%v", record["status"], len(output), record["session_id"], record["run_id"])
		}
	}
	inspect("failed", "bootstrap-failure-marker")
	t.Setenv("HUB_BOOTSTRAP_REVISION", "2")
	if err := (&runtimeHTTP{}).ensureBootstrap(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	inspect("ready", "bootstrap-success-marker")
	files, err := os.ReadDir(filepath.Join(state, "bootstrap-history"))
	if err != nil {
		t.Error("bootstrap attempt history absent", err)
	} else {
		if len(files) != 2 {
			t.Errorf("attempt receipts lost: %d", len(files))
		}
		found := false
		for _, file := range files {
			body, err := os.ReadFile(filepath.Join(state, "bootstrap-history", file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "bootstrap-failure-marker") && strings.Contains(string(body), "installed-1.txt") {
				found = true
			}
		}
		if !found {
			t.Error("explicit retry discarded previous partial diff and output")
		}
	}
}
