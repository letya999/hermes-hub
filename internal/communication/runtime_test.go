package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestHTTPRunnerContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/execute" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("runtime request was not authenticated")
		}
		var request hubruntime.ExecuteRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Text != "hello" || request.PrincipalID != "alice" || request.RuntimeID != "alice" || request.PolicyVersion != "policy-1" {
			t.Error("runtime request body was not forwarded")
		}
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{Text: "reply"})
	}))
	defer server.Close()
	runner := HTTPRunner{URL: server.URL, Auth: "secret", HTTP: server.Client(), Limit: time.Second}
	text, err := runner.Run(context.Background(), Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "telegram:1", Text: "hello"}, User{})
	if err != nil || text != "reply" {
		t.Fatal(text, err)
	}

	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	badRunner := HTTPRunner{URL: failed.URL, Auth: "secret", HTTP: failed.Client(), Limit: time.Second}
	_, err = badRunner.Run(context.Background(), Job{Text: "hello"}, User{})
	failed.Close()
	if err == nil || errors.Is(err, ErrUncertain) {
		t.Fatalf("deterministic runtime error classified incorrectly: %v", err)
	}
	terminal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{Status: "cancelled", LastEvent: "run.cancelled"})
	}))
	terminalRunner := HTTPRunner{URL: terminal.URL, Auth: "secret", HTTP: terminal.Client(), Limit: time.Second}
	outcome, err := terminalRunner.RunOutcome(context.Background(), Job{Text: "hello"})
	terminal.Close()
	if err == nil || outcome.Status != "cancelled" || outcome.LastEvent != "run.cancelled" {
		t.Fatalf("terminal outcome=%+v err=%v", outcome, err)
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := closed.URL
	closed.Close()
	_, err = (HTTPRunner{URL: url, Auth: "secret", Limit: time.Second}).Run(context.Background(), Job{Text: "hello"}, User{})
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("transport error was not uncertain: %v", err)
	}
}

func TestRuntimeRestartClient(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = r.Method == http.MethodPost && r.URL.Path == "/v1/restart" && r.Header.Get("Authorization") == "Bearer secret"
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := runtimeRestart(server.URL, "secret")(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("runtime restart was not requested")
	}
	if !strings.Contains(ErrUncertain.Error(), "uncertain") {
		t.Fatal("uncertain error lost its contract meaning")
	}
}

func TestRemoteGatewayConfigDoesNotNeedUserMounts(t *testing.T) {
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("TELEGRAM_BOT_TOKEN", "bot")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11")
	t.Setenv("HUB_FEATURES", "workspace")
	t.Setenv("HUB_RUNTIME_URL", "http://hermes-runtime:8080")
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	t.Setenv("HUB_COMMUNICATION_SPOOL", t.TempDir())
	config, err := ConfigFromEnv()
	if err != nil || config.RuntimeURL == "" || config.RuntimeAuth == "" {
		t.Fatal(config, err)
	}
	if _, err := New(config); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Config{{OrganizationID: "personal", SpoolDir: "spool", RuntimeURL: "ftp://runtime", RuntimeAuth: "secret"}, {OrganizationID: "personal", SpoolDir: "spool", RuntimeURL: "http://runtime"}} {
		invalid.Users = []User{{ID: "alice"}}
		if invalid.Validate() == nil {
			t.Fatal("invalid runtime contract accepted")
		}
	}
}

func TestSupervisorURLOverridesStaticRuntime(t *testing.T) {
	t.Setenv("HUB_RUNTIME_URL", "http://static:8080")
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://host.docker.internal:8765")
	t.Setenv("HUB_SUPERVISOR_AUTH", "supervisor-secret")
	if got := runtimeURLFromEnv(); got != "http://host.docker.internal:8765" {
		t.Fatalf("runtime URL=%q", got)
	}
	if got := runtimeAuthFromEnv(); got != "supervisor-secret" {
		t.Fatalf("runtime auth=%q", got)
	}
}
