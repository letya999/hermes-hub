//go:build integration

package supervisor

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The driver retains finished helpers only until inspection, then the actual
// supervisor cleanup removes them. Limits and executable are production arguments.
func TestDockerM10ScriptMemoryIsolation(t *testing.T) {
	image := os.Getenv("HUB_SCRIPT_IMAGE")
	if image == "" {
		t.Skip("HUB_SCRIPT_IMAGE required")
	}
	oom := false
	names := []string{}
	m, _ := testManager(t, func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			retained := []string{}
			name := ""
			for i, arg := range args {
				if arg == "--name" && i+1 < len(args) {
					name = args[i+1]
				}
				if arg != "--rm" {
					retained = append(retained, arg)
				}
			}
			names = append(names, name)
			out, err := exec.CommandContext(ctx, "docker", retained...).CombinedOutput()
			inspect, stop := context.WithTimeout(context.Background(), 5*time.Second)
			status, inspectErr := exec.CommandContext(inspect, "docker", "inspect", "--format", "{{.State.OOMKilled}}", name).CombinedOutput()
			stop()
			if inspectErr == nil && strings.TrimSpace(string(status)) == "true" {
				oom = true
			}
			return out, err
		}
		return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	}, nil)
	m.cfg.Image = image
	request := hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron"}, DueAt: time.Now()}
	for _, test := range []struct {
		source string
		status int
	}{{`emit("ok")`, 200}, {`emit("x" * 1000000000)`, 502}, {`load("/etc/passwd", "secret")`, 502}, {`http.get("https://example.com")`, 502}} {
		request.Text = test.source
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/v1/script", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		start := time.Now()
		m.Handler().ServeHTTP(w, r)
		if w.Code != test.status || time.Since(start) > 22*time.Second {
			t.Fatalf("script %q: status=%d duration=%v body=%s", test.source, w.Code, time.Since(start), w.Body.String())
		}
		if test.status == 200 && strings.TrimSpace(w.Body.String()) != `["ok"]` {
			t.Fatalf("helper output=%s", w.Body.String())
		}
	}
	if !oom {
		t.Fatal("large native allocation was not killed by the Docker memory limit")
	}
	for _, name := range names {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := exec.CommandContext(ctx, "docker", "inspect", name).Run()
		cancel()
		if err == nil {
			t.Errorf("helper leaked: %s", name)
		}
	}
}
