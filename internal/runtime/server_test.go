package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func validExecuteRequest(user, organization, key, text string) ExecuteRequest {
	return ExecuteRequest{Envelope: identity.TelegramEnvelope(user, 11, user, "policy-1"), OrganizationID: organization, UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", Trigger: "message", IdempotencyKey: key, Text: text}
}

func TestRuntimeHTTPContractBindsScopeAndDeduplicates(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	oldExecute := executeHermes
	defer func() { executeHermes = oldExecute }()
	calls := 0
	executeHermes = func(context.Context, string) (string, error) {
		calls++
		return "reply", nil
	}
	handler := runtimeHandler()
	request := validExecuteRequest("alice", "personal", "telegram:1", "hello")
	call := func(auth string, body ExecuteRequest) *httptest.ResponseRecorder {
		encoded := mustJSON(t, body)
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer "+auth)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	if got := call("wrong", request).Code; got != http.StatusUnauthorized {
		t.Fatalf("bad auth status %d", got)
	}
	if got := call("runtime-secret", request).Code; got != http.StatusOK {
		t.Fatalf("execute status %d", got)
	}
	if got := call("runtime-secret", request).Code; got != http.StatusOK || calls != 1 {
		t.Fatalf("replay status=%d calls=%d", got, calls)
	}
	request.Text = "different"
	if got := call("runtime-secret", request).Code; got != http.StatusConflict {
		t.Fatalf("idempotency collision status %d", got)
	}
	request = validExecuteRequest("bob", "personal", "telegram:2", "hello")
	if got := call("runtime-secret", request).Code; got != http.StatusConflict {
		t.Fatalf("scope mismatch status %d", got)
	}
}

func TestRuntimeHTTPValidationAndHealth(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	handler := runtimeHandler()
	for _, path := range []string{"/healthz", "/missing"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		want := http.StatusOK
		if path == "/missing" {
			want = http.StatusNotFound
		}
		if recorder.Code != want {
			t.Fatalf("%s status %d", path, recorder.Code)
		}
	}
	method := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, method)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("health method status %d", recorder.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer runtime-secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("invalid identity status %d", recorder.Code)
	}
	invalidJSON := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader("{"))
	invalidJSON.Header.Set("Authorization", "Bearer runtime-secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, invalidJSON)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status %d", recorder.Code)
	}
	oldExecute := executeHermes
	executeHermes = func(context.Context, string) (string, error) { return "", errors.New("failed") }
	defer func() { executeHermes = oldExecute }()
	valid := validExecuteRequest("me", "personal", "one", "hello")
	request = httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(mustJSON(t, valid)))
	request.Header.Set("Authorization", "Bearer runtime-secret")
	recorder = httptest.NewRecorder()
	t.Setenv("HUB_USER_ID", "me")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("runtime error status %d", recorder.Code)
	}
}

func TestValidateExecuteRequestRejectsEachBoundary(t *testing.T) {
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "acme")
	base := validExecuteRequest("alice", "acme", "one", "hello")
	for name, request := range map[string]ExecuteRequest{
		"schema":       func() ExecuteRequest { r := base; r.Envelope.Schema = 2; return r }(),
		"principal_id": func() ExecuteRequest { r := base; r.PrincipalID = "bob"; return r }(),
		"context_id":   func() ExecuteRequest { r := base; r.ContextID = "other"; return r }(),
		"runtime_id":   func() ExecuteRequest { r := base; r.RuntimeID = "stale"; return r }(),
		"policy":       func() ExecuteRequest { r := base; r.PolicyVersion = "policy-0"; return r }(),
		"conversation": func() ExecuteRequest { r := base; r.ConversationID = "bad:id"; return r }(),
		"user":         func() ExecuteRequest { r := base; r.UserID = "bob"; return r }(),
		"actor":        func() ExecuteRequest { r := base; r.ActorID = "bob"; return r }(),
		"organization": func() ExecuteRequest { r := base; r.OrganizationID = "other"; return r }(),
		"scope":        func() ExecuteRequest { r := base; r.ScopeID = "organization:acme"; return r }(),
		"identity":     func() ExecuteRequest { r := base; r.Channel = ""; return r }(),
		"key":          func() ExecuteRequest { r := base; r.IdempotencyKey = strings.Repeat("x", 257); return r }(),
		"empty":        func() ExecuteRequest { r := base; r.Text = ""; return r }(),
		"large":        func() ExecuteRequest { r := base; r.Text = strings.Repeat("x", maxPromptBytes+1); return r }(),
	} {
		if validateExecuteRequest(request) == nil {
			t.Fatalf("accepted invalid %s request", name)
		}
	}
}

func TestRuntimeRestartOnlySchedulesWithPendingRequest(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	dir := t.TempDir()
	oldState := state
	state = dir
	defer func() { state = oldState }()
	handler := runtimeHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/restart", nil)
	req.Header.Set("Authorization", "Bearer runtime-secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"scheduled":false`) {
		t.Fatalf("unexpected restart response: %s", recorder.Body.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "restart.request"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/restart", nil)
	request.Header.Set("Authorization", "Bearer runtime-secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong restart method status %d", recorder.Code)
	}
	state = "\x00"
	request = httptest.NewRequest(http.MethodPost, "/v1/restart", nil)
	request.Header.Set("Authorization", "Bearer runtime-secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("invalid restart state status %d", recorder.Code)
	}
	state = dir
	called := make(chan struct{}, 1)
	oldSignal := signalRuntimeProcess
	signalRuntimeProcess = func() { called <- struct{}{} }
	defer func() { signalRuntimeProcess = oldSignal }()
	request = httptest.NewRequest(http.MethodPost, "/v1/restart", nil)
	request.Header.Set("Authorization", "Bearer runtime-secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"scheduled":true`) {
		t.Fatalf("unexpected scheduled restart response: %s", recorder.Body.String())
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("runtime restart was not signaled")
	}
}

func TestHermesEnvironmentFiltersGatewayCredentials(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "bot")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11")
	t.Setenv("HUB_RUNTIME_AUTH", "runtime")
	t.Setenv("OPENAI_API_KEY", "model")
	env := strings.Join(hermesEnvironment(), "\n")
	if !strings.Contains(env, "OPENAI_API_KEY=model") || strings.Contains(env, "TELEGRAM_BOT_TOKEN") || strings.Contains(env, "TELEGRAM_ALLOWED_USERS") || strings.Contains(env, "HUB_RUNTIME_AUTH") {
		t.Fatalf("filtered environment is unsafe: %s", env)
	}
}

func TestRunHermesReportsMissingCommand(t *testing.T) {
	oldWorkspace := workspace
	workspace = t.TempDir()
	defer func() { workspace = oldWorkspace }()
	t.Setenv("PATH", t.TempDir())
	if _, err := runHermes(context.Background(), "hello"); err == nil {
		t.Fatal("missing Hermes command accepted")
	}
}

func TestRunHermesSuccess(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "helper.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Print(\"reply\")}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "helper")
	if goruntime.GOOS == "windows" {
		bin += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Skipf("cannot build helper: %v (%s)", err, output)
	}
	oldCommand, oldWorkspace := hermesExecutable, workspace
	hermesExecutable, workspace = bin, dir
	defer func() { hermesExecutable, workspace = oldCommand, oldWorkspace }()
	if text, err := runHermes(context.Background(), "hello"); err != nil || text != "reply" {
		t.Fatal(text, err)
	}
	emptySource := filepath.Join(dir, "empty.go")
	if err := os.WriteFile(emptySource, []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	emptyBin := filepath.Join(dir, "empty")
	if goruntime.GOOS == "windows" {
		emptyBin += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", emptyBin, emptySource).CombinedOutput(); err != nil {
		t.Skipf("cannot build empty helper: %v (%s)", err, output)
	}
	hermesExecutable = emptyBin
	if _, err := runHermes(context.Background(), "hello"); err == nil {
		t.Fatal("empty Hermes response accepted")
	}
}

func TestRuntimeServerHelpersBoundInputAndShutdown(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(strings.Repeat("x", maxPromptBytes+1)))
	if _, err := decodeExecuteRequest(recorder, request); err == nil {
		t.Fatal("oversized runtime request accepted")
	}
	shutdownRuntimeServer(&http.Server{})
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	return mustJSONBytes(t, value)
}

func mustJSONBytes(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
