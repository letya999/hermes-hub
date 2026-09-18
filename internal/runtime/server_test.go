package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func validExecuteRequest(user, organization, key, text string) ExecuteRequest {
	return ExecuteRequest{Envelope: identity.TelegramEnvelope(user, 11, user, "policy-1"), OrganizationID: organization, UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", Trigger: "message", IdempotencyKey: key, Text: text}
}

func TestRuntimeHTTPContractBindsScope(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
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
	request = validExecuteRequest("bob", "personal", "telegram:2", "hello")
	if got := call("runtime-secret", request).Code; got != http.StatusConflict {
		t.Fatalf("scope mismatch status %d", got)
	}
}

func TestSessionIDChangesWithInstructionRevision(t *testing.T) {
	request := validExecuteRequest("alice", "personal", "telegram:1", "hello")
	t.Setenv("HUB_SESSION_REVISION", "instructions-v1")
	first := sessionIDFor(request)
	t.Setenv("HUB_SESSION_REVISION", "instructions-v2")
	if second := sessionIDFor(request); second == first {
		t.Fatal("session survived a system-instruction revision")
	}
}

func TestPersistentHermesExecutionUsesPinnedRunAPI(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_PERSISTENT_HERMES", "false")
	runCalls, statusCalls := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Errorf("Hermes API auth=%q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["id"] == nil || body["title"] != nil {
				t.Errorf("unexpected session body=%v", body)
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			runCalls++
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["input"] != "hello" || body["session_id"] == nil {
				t.Errorf("unexpected run body=%v", body)
			}
			if r.Header.Get("Idempotency-Key") != "run-key" {
				t.Errorf("missing idempotency key")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"run_id":"run-1","status":"started"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run-1":
			statusCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"completed","output":"persistent reply"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	port := api.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(port))
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(mustJSON(t, validExecuteRequest("alice", "personal", "run-key", "hello"))))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "persistent reply") || runCalls != 1 || statusCalls != 1 {
		t.Fatalf("persistent status=%d body=%s runs=%d polls=%d", rec.Code, rec.Body.String(), runCalls, statusCalls)
	}
}

func TestPersistentHermesCancellationStopsAdmittedRun(t *testing.T) {
	var stopped atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/runs/stop-me/stop" {
			stopped.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := stopHermesRun(context.Background(), server.Client(), server.URL, "secret", "stop-me"); err != nil || !stopped.Load() {
		t.Fatalf("stop err=%v stopped=%v", err, stopped.Load())
	}
}

func TestPersistentHermesPreservesFailedOutcomeMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions":
			w.WriteHeader(http.StatusConflict)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			_ = json.NewEncoder(w).Encode(map[string]string{"run_id": "failed-run"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/failed-run":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "failed", "error": "provider failed"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	result, err := (&runtimeHTTP{}).executePersistent(context.Background(), ExecuteRequest{ContextID: "alice", ConversationID: "telegram-1", Text: "hello"})
	if err == nil || result.Status != "failed" || result.RunID != "failed-run" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRuntimeHTTPIncludesPersistentFailureMetadata(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			w.WriteHeader(http.StatusOK)
		case "/v1/runs":
			_ = json.NewEncoder(w).Encode(map[string]string{"run_id": "failed-run"})
		case "/v1/runs/failed-run":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "failed", "error": "provider failed"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	port := api.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_RUNTIME_ID", "alice")
	t.Setenv("HUB_POLICY_VERSION", "policy-1")
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(port))
	t.Setenv("HUB_PERSISTENT_HERMES", "true")
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(mustJSON(t, validExecuteRequest("alice", "personal", "failed-key", "hello"))))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "failed-run") || !strings.Contains(rec.Body.String(), "\"status\":\"failed\"") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHermesRequestRejectsBadResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad-json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-json"))
		case "/failure":
			w.WriteHeader(http.StatusBadGateway)
		case "/api/sessions":
			w.WriteHeader(http.StatusConflict)
		}
	}))
	defer server.Close()
	var target map[string]any
	if err := hermesRequest(context.Background(), server.Client(), http.MethodGet, server.URL+"/bad-json", "", nil, &target); err == nil {
		t.Fatal("invalid Hermes JSON accepted")
	}
	if err := hermesRequest(context.Background(), server.Client(), http.MethodGet, server.URL+"/failure", "", nil, nil); err == nil {
		t.Fatal("failed Hermes response accepted")
	}
	if err := hermesRequest(context.Background(), server.Client(), http.MethodPost, server.URL+"/api/sessions", "", nil, nil); !errors.Is(err, errSessionExists) {
		t.Fatalf("session conflict=%v", err)
	}
}

func TestPersistentHermesTerminalStatuses(t *testing.T) {
	for _, want := range []string{"cancelled", "interrupted"} {
		want := want
		t.Run(want, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/sessions":
					w.WriteHeader(http.StatusOK)
				case "/v1/runs":
					_ = json.NewEncoder(w).Encode(map[string]string{"run_id": "terminal-run"})
				case "/v1/runs/terminal-run":
					_ = json.NewEncoder(w).Encode(map[string]string{"status": want})
				}
			}))
			defer server.Close()
			host, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
			t.Setenv("HUB_HERMES_API_HOST", host)
			t.Setenv("HUB_HERMES_API_PORT", port)
			result, err := (&runtimeHTTP{}).executePersistent(context.Background(), ExecuteRequest{ContextID: "alice", ConversationID: "telegram-1", Text: "hello"})
			if err == nil || result.Status != want || result.RunID != "terminal-run" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestPersistentHermesAdmissionFailures(t *testing.T) {
	for _, mode := range []string{"session", "run", "id"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/sessions" {
					if mode == "session" {
						w.WriteHeader(http.StatusBadGateway)
					} else {
						w.WriteHeader(http.StatusOK)
					}
					return
				}
				if r.URL.Path == "/v1/runs" {
					if mode == "run" {
						w.WriteHeader(http.StatusBadGateway)
					} else if mode == "id" {
						_ = json.NewEncoder(w).Encode(map[string]string{})
					}
				}
			}))
			defer server.Close()
			host, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
			t.Setenv("HUB_HERMES_API_HOST", host)
			t.Setenv("HUB_HERMES_API_PORT", port)
			if _, err := (&runtimeHTTP{}).executePersistent(context.Background(), ExecuteRequest{ContextID: "alice", ConversationID: "telegram-1", Text: "hello"}); err == nil {
				t.Fatal("admission failure accepted")
			}
		})
	}
}

func TestEmptyRunOutputDoesNotReusePreviousAssistantMessage(t *testing.T) {
	messageReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			messageReads++
			_, _ = w.Write([]byte(`{"data":[{"role":"assistant","content":"previous answer"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed"}`))
	}))
	defer server.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	result, err := (&runtimeHTTP{}).observeHermesRun(context.Background(), ExecuteResponse{RunID: "original", SessionID: "session"}, nil)
	if err == nil || result.Status != "uncertain" || result.RunID != "original" || result.Text != "" || messageReads != 0 {
		t.Fatalf("old reply reused or recovery lost: %+v %v reads=%d", result, err, messageReads)
	}
}

func TestPersistentHermesHandlesSessionReplayRunFailuresAndMessageFallback(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_PERSISTENT_HERMES", "true")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if r.URL.Path == "/v1/runs" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body["input"] {
			case "missing":
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"status":"started"}`))
				return
			case "failed":
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"run_id":"run-failed"}`))
				return
			case "bad":
				w.WriteHeader(http.StatusBadGateway)
				return
			default:
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"run_id":"run-empty"}`))
				return
			}
		}
		if r.URL.Path == "/v1/runs/run-empty" {
			_, _ = w.Write([]byte(`{"status":"completed"}`))
			return
		}
		if r.URL.Path == "/v1/runs/run-failed" {
			_, _ = w.Write([]byte(`{"status":"failed","error":"denied"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/messages") {
			_, _ = w.Write([]byte(`{"data":[{"role":"assistant","content":"from session"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	port := api.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(port))
	call := func(text, key string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(mustJSON(t, validExecuteRequest("alice", "personal", key, text))))
		req.Header.Set("Authorization", "Bearer runtime-secret")
		rec := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(rec, req)
		return rec.Code
	}
	if got := call("empty", "empty-key"); got != http.StatusInternalServerError {
		t.Fatalf("empty final incorrectly completed: status=%d", got)
	}
	if got := call("failed", "failed-key"); got != http.StatusInternalServerError {
		t.Fatalf("failed run status=%d", got)
	}
	if got := call("missing", "missing-key"); got != http.StatusInternalServerError {
		t.Fatalf("missing run id status=%d", got)
	}
	if got := call("bad", "bad-key"); got != http.StatusInternalServerError {
		t.Fatalf("bad run status=%d", got)
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
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	port := api.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", fmt.Sprint(port))
	ready := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	ready.Header.Set("Authorization", "Bearer runtime-secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, ready)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready status %d", recorder.Code)
	}
	ready.Header.Set("Authorization", "Bearer wrong")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, ready)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("ready auth status %d", recorder.Code)
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
	t.Setenv("HUB_HERMES_API_HOST", "invalid host")
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

func TestValidateExecuteRequestAcceptsOrganizationScope(t *testing.T) {
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "acme")
	request := validExecuteRequest("alice", "acme", "org-run", "hello")
	request.ScopeID = "organization:acme"
	request.Envelope.ContextID = "acme"
	if err := validateExecuteRequest(request); err != nil {
		t.Fatalf("organization scope rejected: %v", err)
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

func TestHermesGatewayEnvironmentPinsAuthenticatedAPI(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_HERMES_API_PORT", "9000")
	env := hermesGatewayEnvironment()
	if env["HERMES_EXEC_ASK"] != "true" || env["API_SERVER_ENABLED"] != "true" || env["API_SERVER_KEY"] != "runtime-secret" || env["API_SERVER_PORT"] != "9000" || env["API_SERVER_HOST"] != "127.0.0.1" {
		t.Fatalf("unexpected gateway environment: %#v", env)
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

func TestRuntimeReadyReportsUnavailableHermes(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", "1")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.Header.Set("Authorization", "Bearer runtime-secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable readiness status=%d", rec.Code)
	}
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
