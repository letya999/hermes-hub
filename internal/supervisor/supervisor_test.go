package supervisor

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func testManager(t *testing.T, command func(context.Context, ...string) ([]byte, error), probe func(context.Context, string, string) error) (*Manager, string) {
	t.Helper()
	// Model labels created by docker run rather than pretending every named
	// container belongs to this manager.
	var labelsMu sync.Mutex
	labels := make(map[string]map[string]string)
	sources := make(map[string]string)
	wrappedCommand := func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 3 && args[0] == "inspect" && args[2] == "{{json .Mounts}}" {
			labelsMu.Lock()
			source, ok := sources[args[3]]
			labelsMu.Unlock()
			if ok {
				return json.Marshal([]map[string]string{{"Type": "bind", "Source": source, "Destination": "/scope"}})
			}
		}
		if len(args) > 3 && args[0] == "inspect" && args[2] == "{{.Id}}" {
			labelsMu.Lock()
			metadata, ok := labels[args[3]]
			labelsMu.Unlock()
			if ok {
				return []byte(hex.EncodeToString(hashBytes(args[3] + metadata["hermes-hub.generation"]))), nil
			}
		}
		if len(args) > 3 && args[0] == "inspect" && args[2] == "{{json .Config.Labels}}" {
			labelsMu.Lock()
			metadata, ok := labels[args[3]]
			b, _ := json.Marshal(metadata)
			labelsMu.Unlock()
			if ok {
				return b, nil
			}
		}
		out, err := command(ctx, args...)
		if err == nil && len(args) > 0 && args[0] == "run" {
			container := ""
			source := ""
			metadata := make(map[string]string)
			for i := 1; i+1 < len(args); i++ {
				if args[i] == "--name" {
					container = args[i+1]
				}
				if args[i] == "--label" {
					parts := strings.SplitN(args[i+1], "=", 2)
					if len(parts) == 2 {
						metadata[parts[0]] = parts[1]
					}
				}
				if args[i] == "--mount" && strings.HasPrefix(args[i+1], "type=bind,src=") {
					path, destination, _ := strings.Cut(strings.TrimPrefix(args[i+1], "type=bind,src="), ",dst=")
					if strings.Split(destination, ",")[0] == "/scope" {
						source = path
					}
				}
			}
			labelsMu.Lock()
			labels[container] = metadata
			sources[container] = source
			labels[hex.EncodeToString(hashBytes(container+metadata["hermes-hub.generation"]))] = metadata
			sources[hex.EncodeToString(hashBytes(container+metadata["hermes-hub.generation"]))] = source
			labelsMu.Unlock()
		}
		return out, err
	}
	root := t.TempDir()
	ctxRoot := filepath.Join(root, "alice")
	for _, name := range []string{"runtime", "hermes", "workspace"} {
		if err := os.MkdirAll(filepath.Join(ctxRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, ctxRoot)
	m, err := New(Config{SpacesRoot: root, RuntimeAuth: "secret", Image: "hermes:test", WarmTTL: time.Minute, Command: wrappedCommand, Probe: probe, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	return m, ctxRoot
}

func binding(contextRoot string) Binding {
	return Binding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", UserID: "alice", OrganizationID: "personal", PolicyVersion: "policy-1", ContextRoot: contextRoot}
}

func writeHermesSources(t *testing.T, ctxRoot string) {
	t.Helper()
	for _, env := range []string{"dev", "prod"} {
		if err := os.WriteFile(filepath.Join(ctxRoot, "hermes."+env+".yaml"), []byte("model: {}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProtectedSelfEnvIsProxiedToOwnedRuntime(t *testing.T) {
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/self-env" || r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request hubruntime.SelfEnvRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Values["GOOGLE_OAUTH_CLIENT_SECRET"] != "client-secret" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"updated": []string{"GOOGLE_OAUTH_CLIENT_SECRET"}, "restart_scheduled": true})
	}))
	defer runtimeAPI.Close()
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(normalized)] = &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "generation-1", Address: runtimeAPI.URL, State: Ready}, binding: normalized, auth: "secret", leases: map[string]Lease{}}
	m.mu.Unlock()
	request := hubruntime.SelfEnvRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Values: map[string]string{"GOOGLE_OAUTH_CLIENT_SECRET": "client-secret"}}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/self-env", bytes.NewReader(body))
	httpRequest.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "restart_scheduled") {
		t.Fatalf("self-env proxy status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRestartForwardsToOwnedRuntime(t *testing.T) {
	restarted := false
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		restarted = r.URL.Path == "/v1/restart" && r.Header.Get("Authorization") == "Bearer secret"
		w.WriteHeader(http.StatusOK)
	}))
	defer runtimeAPI.Close()
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(normalized)] = &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "generation-1", Container: "container-1", Address: runtimeAPI.URL, State: Ready}, binding: normalized, auth: "secret", leases: map[string]Lease{}}
	m.mu.Unlock()
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "communication", Trigger: "restart", JobID: "restart-job", IdempotencyKey: "restart-job"}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/restart", bytes.NewReader(body))
	httpRequest.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("restart status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !restarted {
		t.Fatal("runtime /v1/restart was not called")
	}
}

func TestRestartRejectsBadRequests(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	for _, body := range []string{`not-json`, `{"identity_schema":9}`} {
		httpRequest := httptest.NewRequest(http.MethodPost, "/v1/restart", strings.NewReader(body))
		httpRequest.Header.Set("Authorization", "Bearer secret")
		recorder := httptest.NewRecorder()
		m.Handler().ServeHTTP(recorder, httpRequest)
		if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusConflict {
			t.Fatalf("restart %q → %d, want 400/409", body, recorder.Code)
		}
	}
	httpRequest := httptest.NewRequest(http.MethodGet, "/v1/restart", nil)
	httpRequest.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/restart → %d, want 405", recorder.Code)
	}
}

func TestRestartSkipsAbsentRuntime(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "communication", Trigger: "restart", JobID: "restart-job", IdempotencyKey: "restart-job"}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/restart", bytes.NewReader(body))
	httpRequest.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("restart for absent runtime must be a no-op success, got %d", recorder.Code)
	}
	if len(m.items) != 0 {
		t.Fatal("restart spawned a runtime just to bounce it")
	}
}

func TestEnsureDeduplicatesAndReusesWarmRuntime(t *testing.T) {
	var mu sync.Mutex
	runs := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if len(args) > 0 && args[0] == "run" {
			runs++
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := m.Ensure(context.Background(), b)
			if err != nil || r.Generation == "" {
				t.Errorf("ensure: %v", err)
			}
			_ = m.Release("alice", "gateway")
		}()
	}
	wg.Wait()
	if runs != 1 {
		t.Fatalf("docker runs=%d, want one generation", runs)
	}
	changed := b
	changed.RuntimeID = "other"
	if _, err := m.Ensure(context.Background(), changed); err == nil {
		t.Fatal("runtime identity changed within one context")
	}
	r, ok, err := m.Status(b)
	if err != nil || !ok || r.State != Idle {
		t.Fatalf("status=%+v ok=%v err=%v", r, ok, err)
	}
	if err := m.Reap(context.Background(), time.Unix(100, 0).Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, _, _ = m.Status(b)
	if r.State != Stopped {
		t.Fatalf("reaped state=%s", r.State)
	}
	cold, err := m.Ensure(context.Background(), b)
	if err != nil || cold.RuntimeID != "alice" || cold.Generation == r.Generation {
		t.Fatalf("cold start lost logical identity: before=%+v after=%+v err=%v", r, cold, err)
	}
}

func TestSupervisorStateSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	ctxRoot := filepath.Join(root, "alice")
	for _, name := range []string{"runtime", "hermes", "workspace"} {
		if err := os.MkdirAll(filepath.Join(ctxRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, ctxRoot)
	started := false
	commands := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			started = true
		}
		if args[0] == "inspect" && !started {
			return nil, os.ErrNotExist
		}
		if len(args) > 0 && args[0] == "inspect" {
			return []byte("running"), nil
		}
		return []byte("running"), nil
	}
	cfg := Config{SpacesRoot: root, RuntimeAuth: "control", Image: "hermes:test", Command: commands, Probe: func(context.Context, string, string) error { return nil }, Now: func() time.Time { return time.Unix(100, 0) }}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := binding(ctxRoot)
	first, err := m.Ensure(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := m.Acquire(context.Background(), b, LeaseJob)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 3 && args[0] == "inspect" && args[2] == "{{json .Config.Labels}}" {
			return json.Marshal(map[string]string{"hermes-hub.owner": m.ownerID(), "hermes-hub.context": hex.EncodeToString(hashBytes(runtimeKey(b))), "hermes-hub.generation": first.Generation})
		}
		if len(args) > 3 && args[0] == "inspect" && args[2] == "{{json .Mounts}}" {
			return json.Marshal([]map[string]string{{"Type": "bind", "Source": ctxRoot, "Destination": "/scope"}})
		}
		return commands(ctx, args...)
	}
	restored, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := restored.Ensure(context.Background(), b)
	if err != nil || second.Generation != first.Generation {
		t.Fatalf("restored=%+v first=%+v err=%v", second, first, err)
	}
	if err := restored.ReleaseLease(lease.ID); err != nil {
		t.Fatalf("restored lease release: %v", err)
	}
}

func TestSupervisorReapsRestoredIdleRuntimeWithoutLocalSlot(t *testing.T) {
	root := t.TempDir()
	ctxRoot := filepath.Join(root, "alice")
	for _, name := range []string{"runtime", "hermes", "workspace"} {
		if err := os.MkdirAll(filepath.Join(ctxRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, ctxRoot)
	started := false
	commands := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			started = true
		}
		if args[0] == "inspect" && !started {
			return nil, os.ErrNotExist
		}
		if len(args) > 0 && args[0] == "inspect" {
			return []byte("running"), nil
		}
		return []byte("running"), nil
	}
	cfg := Config{SpacesRoot: root, RuntimeAuth: "control", Image: "hermes:test", Command: commands, Probe: func(context.Context, string, string) error { return nil }, Now: func() time.Time { return time.Unix(100, 0) }}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := binding(ctxRoot)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.Release("alice", "gateway"); err != nil {
		t.Fatal(err)
	}
	actual, _, _ := m.Status(b)
	cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 3 && args[0] == "inspect" && args[2] == "{{.Id}}" {
			return []byte(strings.Repeat("a", 64)), nil
		}
		if len(args) > 3 && args[0] == "inspect" && args[2] == "{{json .Config.Labels}}" {
			return json.Marshal(map[string]string{"hermes-hub.owner": m.ownerID(), "hermes-hub.context": hex.EncodeToString(hashBytes(runtimeKey(b))), "hermes-hub.generation": actual.Generation})
		}
		if len(args) > 3 && args[0] == "inspect" && args[2] == "{{json .Mounts}}" {
			return json.Marshal([]map[string]string{{"Type": "bind", "Source": ctxRoot, "Destination": "/scope"}})
		}
		return commands(ctx, args...)
	}
	restored, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Reap(context.Background(), time.Unix(100, 0).Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, _, _ := restored.Status(b)
	if status.State != Stopped {
		t.Fatalf("restored status=%+v", status)
	}
}

func TestSupervisorRejectsStaleLeaseAndCorruptState(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	lease, _, err := m.Acquire(context.Background(), b, LeaseJob)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(b)].Generation = "replacement"
	m.mu.Unlock()
	if err := m.ReleaseLease(lease.ID); err == nil {
		t.Fatal("stale lease released replacement")
	}
	bad := filepath.Join(t.TempDir(), "supervisor.json")
	if err := os.WriteFile(bad, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{SpacesRoot: root, StateDir: filepath.Dir(bad), RuntimeAuth: "control", Image: "hermes:test"}); err == nil {
		t.Fatal("corrupt supervisor state accepted")
	}
}

func TestSupervisorRejectsInvalidPersistedRuntime(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "supervisor.json"), []byte(`{"items":[{"runtime":{"context_id":""}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{SpacesRoot: root, StateDir: root, RuntimeAuth: "control", Image: "hermes:test"}); err == nil {
		t.Fatal("invalid persisted runtime accepted")
	}
}

func TestSupervisorJobOutcomeReplaysAfterRestart(t *testing.T) {
	root := t.TempDir()
	ctxRoot := filepath.Join(root, "alice")
	for _, name := range []string{"runtime", "hermes", "workspace"} {
		if err := os.MkdirAll(filepath.Join(ctxRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, ctxRoot)
	runs := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/execute" {
			runs++
			_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{Text: "answer", Status: "completed", RunID: "run-1"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer backend.Close()
	command := func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}
	cfg := Config{SpacesRoot: root, RuntimeAuth: "control", Image: "hermes:test", Command: command, Probe: func(context.Context, string, string) error { return nil }}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := binding(ctxRoot)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = backend.URL
	m.mu.Unlock()
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "job-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-1", Text: "hello"}
	body, _ := json.Marshal(request)
	call := func(manager *Manager) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer control")
		rec := httptest.NewRecorder()
		manager.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := call(m); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "answer") {
		t.Fatalf("first response=%d %s", rec.Code, rec.Body.String())
	}
	if runs != 1 {
		t.Fatalf("backend runs=%d", runs)
	}
	restored, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rec := call(restored); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "answer") {
		t.Fatalf("replay response=%d %s", rec.Code, rec.Body.String())
	}
	if runs != 1 {
		t.Fatalf("replay created another run: %d", runs)
	}
}

func TestSupervisorMarksInFlightJobUncertainOnRestart(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	request := hubruntime.ExecuteRequest{JobID: "job-running", IdempotencyKey: "idem-running", Text: "hello"}
	if _, replay, err := m.beginJob(request); err != nil || replay {
		t.Fatalf("begin replay=%v err=%v", replay, err)
	}
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	response, replay, err := restarted.beginJob(request)
	if err != nil || !replay || response.Status != "uncertain" || response.LastEvent != "run.unknown" {
		t.Fatalf("restart replay=%+v replay=%v err=%v", response, replay, err)
	}
}

func TestSupervisorIgnoresStaleJobCompletion(t *testing.T) {
	m, _ := testManager(t, func(_ context.Context, _ ...string) ([]byte, error) { return nil, os.ErrNotExist }, nil)
	request := hubruntime.ExecuteRequest{JobID: "job-stale", IdempotencyKey: "idem-stale"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	record.Generation = "old-generation"
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.finishJobGeneration(request, hubruntime.ExecuteResponse{Text: "wrong"}, "completed", "new-generation")
	m.mu.Lock()
	status := m.jobs[request.JobID].Status
	m.mu.Unlock()
	if status != "running" {
		t.Fatalf("stale completion changed status to %q", status)
	}
	m.mu.Lock()
	record = m.jobs[request.JobID]
	record.Generation = ""
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.finishJobGeneration(request, hubruntime.ExecuteResponse{Text: "wrong"}, "completed", "new-generation")
	m.mu.Lock()
	status = m.jobs[request.JobID].Status
	m.mu.Unlock()
	if status != "running" {
		t.Fatalf("unbound stale completion changed status to %q", status)
	}
}

func TestSupervisorMarksTransportOutcomeUncertain(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := dead.URL
	dead.Close()
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = address
	m.mu.Unlock()
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "job-uncertain", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-uncertain", Text: "hello"}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"status":"uncertain"`) {
		t.Fatalf("uncertain response=%d %s", rec.Code, rec.Body.String())
	}
	m.mu.Lock()
	status := m.jobs[request.JobID].Status
	m.mu.Unlock()
	if status != "uncertain" {
		t.Fatalf("job status=%q", status)
	}
	// Release the setup hold: only the uncertain execution must now prevent reap.
	if err := m.ReleaseBinding(b); err != nil {
		t.Fatal(err)
	}
	if err := m.Reap(context.Background(), time.Unix(100, 0).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	runtime, _, _ := m.Status(b)
	if runtime.State != Busy || runtime.Leases != 1 {
		t.Fatalf("uncertain execution was reaped: %+v", runtime)
	}
	restored, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Reap(context.Background(), time.Unix(100, 0).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	runtime, _, _ = restored.Status(b)
	if runtime.State != Busy || runtime.Leases != 1 {
		t.Fatalf("restart lost uncertain hold: %+v", runtime)
	}
}

func TestConcurrentNamedLeaseReleaseIsAtomic(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	lease, _, err := m.Acquire(context.Background(), b, LeaseStream)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- m.ReleaseLease(lease.ID) }()
	}
	success := 0
	for range 2 {
		if <-results == nil {
			success++
		}
	}
	runtime, _, _ := m.Status(b)
	if success != 1 || runtime.Leases != 0 || runtime.State != Idle {
		t.Fatalf("success=%d runtime=%+v", success, runtime)
	}
}

func TestNamedLeaseReleaseRollsBackOnPersistenceFailure(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	lease, _, err := m.Acquire(context.Background(), b, LeaseApproval)
	if err != nil {
		t.Fatal(err)
	}
	path := m.statePath
	m.statePath = filepath.Join(t.TempDir(), "missing", "state.json")
	if err := m.ReleaseLease(lease.ID); err == nil {
		t.Fatal("persistence failure ignored")
	}
	runtime, _, _ := m.Status(b)
	if runtime.Leases != 1 || runtime.State != Busy {
		t.Fatalf("failed release changed hold: %+v", runtime)
	}
	m.statePath = path
	if err := m.ReleaseLease(lease.ID); err != nil {
		t.Fatalf("retry release: %v", err)
	}
}

func TestSupervisorJobAdmissionFencesDuplicates(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	if _, _, err := m.beginJob(hubruntime.ExecuteRequest{}); err == nil {
		t.Fatal("missing job identity accepted")
	}
	request := hubruntime.ExecuteRequest{JobID: "job-admission", IdempotencyKey: "idem-admission", Text: "hello"}
	if _, replay, err := m.beginJob(request); err != nil || replay {
		t.Fatalf("first admission replay=%v err=%v", replay, err)
	}
	if _, _, err := m.beginJob(request); !errors.Is(err, errJobInProgress) {
		t.Fatalf("in-progress admission err=%v", err)
	}
	changed := request
	changed.Text = "different"
	if _, _, err := m.beginJob(changed); err == nil {
		t.Fatal("payload collision accepted")
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	record.Status, record.Response = "completed", hubruntime.ExecuteResponse{Text: "answer"}
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	if response, replay, err := m.beginJob(request); err != nil || !replay || response.Text != "answer" {
		t.Fatalf("completed replay=%+v replay=%v err=%v", response, replay, err)
	}
	m.mu.Lock()
	record.Status = "uncertain"
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	if response, replay, err := m.beginJob(request); err != nil || !replay || response.Text != "answer" {
		t.Fatalf("uncertain replay=%+v replay=%v err=%v", response, replay, err)
	}
}

func TestReaperNeverStopsLeasedRuntime(t *testing.T) {
	stops := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if len(args) > 0 && args[0] == "rm" {
			stops++
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.Reap(context.Background(), time.Unix(100, 0).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if stops != 0 {
		t.Fatal("active lease was reaped")
	}
	if err := m.Release("alice", "gateway"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reap(context.Background(), time.Unix(100, 0).Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if stops != 1 {
		t.Fatalf("stops=%d", stops)
	}
}

func TestNormalizeRejectsEscapesAndSymlinks(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, _ ...string) ([]byte, error) { return nil, nil }, nil)
	if _, err := m.Ensure(context.Background(), Binding{PrincipalID: "alice", ContextID: "..", RuntimeID: "alice", ContextRoot: root}); err == nil {
		t.Fatal("invalid id accepted")
	}
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err == nil {
		if _, err = m.Ensure(context.Background(), Binding{PrincipalID: "alice", ContextID: "link", RuntimeID: "alice", ContextRoot: link}); err == nil {
			t.Fatal("symlink context accepted")
		}
	}
	if _, err := m.Ensure(context.Background(), Binding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", ContextRoot: outside}); err == nil {
		t.Fatal("outside context accepted")
	}
}

func TestSupervisorHandlerRoutesAndAuthorizes(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{Text: "reply"})
	}))
	defer backend.Close()
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	key := "alice\x00alice\x00gateway"
	m.mu.Lock()
	m.items[key].Address = backend.URL
	m.mu.Unlock()
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "one", Text: "hello"}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "reply") {
		t.Fatalf("proxy response %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/runtimes", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatal("unauthorized supervisor request accepted")
	}
}

func TestSupervisorLeaseHTTPContract(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	if err := m.ReleaseLease(""); err == nil {
		t.Fatal("empty lease ID accepted")
	}
	if err := m.ReleaseLease("lease-missing"); err == nil {
		t.Fatal("unknown lease accepted")
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "lease", Text: "hello"}
	body, _ := json.Marshal(struct {
		hubruntime.ExecuteRequest
		Kind LeaseKind `json:"kind"`
	}{request, LeaseStream})
	req := httptest.NewRequest(http.MethodPost, "/v1/leases", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("lease create=%d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Lease Lease `json:"lease"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &created) != nil || created.Lease.ID == "" {
		t.Fatalf("invalid lease response=%s", rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodDelete, "/v1/leases/"+created.Lease.ID, nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("lease release=%d %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{{http.MethodGet, "/v1/leases", "", http.StatusMethodNotAllowed}, {http.MethodPost, "/v1/leases", "bad", http.StatusBadRequest}, {http.MethodGet, "/v1/leases/missing", "", http.StatusMethodNotAllowed}, {http.MethodDelete, "/v1/leases/missing", "", http.StatusNotFound}, {http.MethodPost, "/v1/runtimes", "", http.StatusMethodNotAllowed}} {
		req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer secret")
		rec = httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s %s status=%d want=%d", tc.method, tc.path, rec.Code, tc.status)
		}
	}
	_ = root
}

func TestTypedLeasesProtectStreamsApprovalsAndPrincipalIsolation(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	stream, _, err := m.Acquire(context.Background(), b, LeaseStream)
	if err != nil {
		t.Fatal(err)
	}
	approval, _, err := m.Acquire(context.Background(), b, LeaseApproval)
	if err != nil {
		t.Fatal(err)
	}
	if stream.Kind != LeaseStream || approval.Kind != LeaseApproval || stream.ID == approval.ID {
		t.Fatalf("leases=%+v %+v", stream, approval)
	}
	if err := m.Reap(context.Background(), time.Unix(100, 0).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	status, _, _ := m.Status(b)
	if status.State != Busy || status.Leases != 2 {
		t.Fatalf("leased status=%+v", status)
	}
	if err := m.ReleaseLease(stream.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseLease(approval.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Reap(context.Background(), time.Unix(100, 0).Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, _, _ = m.Status(b)
	if status.State != Stopped {
		t.Fatalf("released status=%+v", status)
	}
	if err := m.ReleaseBinding(b); err == nil {
		t.Fatal("stopped runtime released")
	}
	if _, _, err := m.Acquire(context.Background(), b, LeaseKind("bogus")); err == nil {
		t.Fatal("invalid lease kind accepted")
	}
	otherRoot := filepath.Join(filepath.Dir(root), "bob")
	if err := os.MkdirAll(otherRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherRoot, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, otherRoot)
	other := b
	other.PrincipalID, other.ContextID, other.RuntimeID, other.ContextRoot = "bob", "bob", "bob", otherRoot
	if _, err := m.Ensure(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if m.items["alice\x00alice\x00gateway"].Container == m.items["bob\x00bob\x00gateway"].Container {
		t.Fatal("principal runtimes share container")
	}
}

func TestNewDefaultsAndServeStopsOnContext(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty supervisor config accepted")
	}
	m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	if m.cfg.WarmTTL != time.Minute || m.cfg.MaxConcurrent != 8 || m.cfg.RuntimePort != 8080 || m.cfg.PIDs != 256 {
		t.Fatalf("defaults not applied: %+v", m.cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, m, "127.0.0.1:0") }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestEnsureExistingAndFailurePaths(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return []byte("running"), nil
		}
		return nil, nil
	}, func(_ context.Context, _, auth string) error {
		if auth != "secret" {
			return os.ErrPermission
		}
		return nil
	})
	if _, err := m.Ensure(context.Background(), binding(root)); err == nil {
		t.Fatal("unverified existing container adopted")
	}
	bad, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return nil, os.ErrPermission
	}, func(context.Context, string, string) error { return nil })
	if _, err := bad.Ensure(context.Background(), binding(root)); err == nil {
		t.Fatal("start failure accepted")
	}
	if err := bad.Release("alice", "gateway"); err == nil {
		t.Fatal("degraded runtime lease released")
	}
	failing, froot := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return nil, nil
	}, func(context.Context, string, string) error { return os.ErrDeadlineExceeded })
	if _, err := failing.Ensure(context.Background(), binding(froot)); err == nil {
		t.Fatal("readiness failure accepted")
	}
}

func TestReleaseStatusListAndBindingBoundaries(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	if _, ok, err := m.Status(b); err != nil || ok {
		t.Fatalf("empty status=%v %v", ok, err)
	}
	if err := m.Release("alice", "gateway"); err == nil {
		t.Fatal("unknown release accepted")
	}
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.Release("alice", "gateway"); err != nil {
		t.Fatal(err)
	}
	if err := m.Release("alice", "gateway"); err == nil {
		t.Fatal("lease underflow accepted")
	}
	req := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "unknown:alice"}
	if _, err := m.bindingFor(req); err == nil {
		t.Fatal("unknown scope accepted")
	}
	req.ScopeID = "user:bob"
	if _, err := m.bindingFor(req); err == nil {
		t.Fatal("cross-user scope accepted")
	}
	org := filepath.Join(filepath.Dir(root), "acme")
	if err := os.MkdirAll(org, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.normalize(Binding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", UserID: "alice", ContextRoot: root, OrganizationRoot: org, EnvFile: filepath.Join(root, "runtime.auth")}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.normalize(Binding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", UserID: "alice", ContextRoot: root, EnvFile: filepath.Join(filepath.Dir(root), "missing")}); err == nil {
		t.Fatal("external env file accepted")
	}
}

func TestSupervisorHTTPRejectsMalformedRequestsAndLists(t *testing.T) {
	m, _ := testManager(t, func(_ context.Context, _ ...string) ([]byte, error) { return nil, os.ErrNotExist }, nil)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(method, "/healthz", nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("health method=%s status=%d", method, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/runtimes", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "[]") {
		t.Fatalf("list=%d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed=%d", rec.Code)
	}
}

func TestEnvFileAuthRejectsMissingOrInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.auth")
	if err := os.WriteFile(path, []byte("OPENAI_API_KEY=x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := envFileAuth(path); err == nil {
		t.Fatal("missing runtime auth accepted")
	}
	if err := os.WriteFile(path, []byte("HUB_RUNTIME_AUTH=\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := envFileAuth(path); err == nil {
		t.Fatal("empty runtime auth accepted")
	}
}

func TestReadyPollsAuthenticatedEndpoint(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("readiness auth missing")
		}
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	m, _ := testManager(t, func(_ context.Context, _ ...string) ([]byte, error) { return nil, nil }, nil)
	if err := m.ready(context.Background(), server.URL, "secret"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("readiness calls=%d", calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.ready(ctx, "http://127.0.0.1:1", "secret"); err == nil {
		t.Fatal("cancelled readiness accepted")
	}
}

func TestSupervisorServeAndHTTPErrorBranches(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return nil, os.ErrPermission
	}, nil)
	if err := Serve(context.Background(), nil, "127.0.0.1:0"); err == nil {
		t.Fatal("nil manager accepted")
	}
	if err := Serve(context.Background(), m, ""); err == nil {
		t.Fatal("empty listen accepted")
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req := httptest.NewRequest(method, "/v1/execute", nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("execute method status=%d", rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/runtimes", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal("runtime list failed")
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(`{"identity_schema":1,"principal_id":"alice","external_identity_id":"telegram-1","conversation_id":"telegram-1","delivery_target_id":"telegram-1","context_id":"alice","runtime_id":"alice","policy_version":"policy-1","organization_id":"personal","user_id":"alice","actor_id":"alice","scope_id":"user:alice","channel":"telegram_bot","trigger":"message","idempotency_key":"one","text":"hello"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("runtime failure status=%d", rec.Code)
	}
	_ = root
}

func TestRunArgsMountsExactContextAndLimits(t *testing.T) {
	t.Setenv("HUB_ENV", "dev")
	m, root := testManager(t, func(_ context.Context, _ ...string) ([]byte, error) { return nil, nil }, nil)
	if err := os.WriteFile(filepath.Join(root, "hermes.dev.yaml"), []byte("model: {}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	org := filepath.Join(filepath.Dir(root), "acme")
	if err := os.MkdirAll(org, 0700); err != nil {
		t.Fatal(err)
	}
	b := binding(root)
	b.OrganizationRoot = org
	args, err := m.runArgs(b, "hermes-context-test", 19000)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--network hermes-hub-runtime", "src=hermes-hub-alice-dev_broker-secrets-runtime,dst=/run/broker-secrets,readonly", "--read-only", "--cap-drop ALL", "--pids-limit 256", "--memory 1g", "--cpus 2", "127.0.0.1:19000:8080", "dst=/scope", "dst=/org", "dst=/config/config.yaml", "dst=/config/SOUL.md", "dst=/state/hermes/config.yaml,readonly", "--env-file"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("run args missing %q: %s", want, joined)
		}
	}
	effective, err := os.ReadFile(filepath.Join(root, "generated", "hermes-effective.dev.yaml"))
	if err != nil || !strings.Contains(string(effective), "http://toolhub:8090/mcp") {
		t.Fatalf("effective config not materialized: %v %q", err, effective)
	}
	other := b
	other.UserID = "bob"
	rawOther, err := m.runArgs(other, "hermes-context-bob", 19001)
	if err != nil {
		t.Fatal(err)
	}
	otherArgs := strings.Join(rawOther, " ")
	if !strings.Contains(otherArgs, "--network hermes-hub-runtime") || !strings.Contains(otherArgs, "src=hermes-hub-bob-dev_broker-secrets-runtime") || strings.Contains(otherArgs, "hermes-hub-alice-dev") {
		t.Fatalf("runtime secrets are not owner-scoped: %s", otherArgs)
	}
}

func TestRunArgsMountsHubSkills(t *testing.T) {
	t.Setenv("HUB_ENV", "dev")
	m, root := testManager(t, func(_ context.Context, _ ...string) ([]byte, error) { return nil, nil }, nil)
	if err := os.WriteFile(filepath.Join(root, "hermes.dev.yaml"), []byte("model: {}"), 0600); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(root, "hub-skills")
	if err := os.MkdirAll(skills, 0700); err != nil {
		t.Fatal(err)
	}
	settings := "schema: 1\nuser: alice\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\nglobal_skills_dir: " + skills + "\n"
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	rawArgs, err := m.runArgs(binding(root), "hermes-context-test", 19000)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rawArgs, " ")
	if !strings.Contains(joined, "src="+skills+",dst=/opt/hub/skills,readonly") {
		t.Fatalf("run args missing skills mount: %s", joined)
	}
	ctx := filepath.Join(root, "spaces", "bob")
	for _, name := range []string{"runtime", "hermes", "workspace"} {
		if err := os.MkdirAll(filepath.Join(ctx, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"runtime.auth": "HUB_RUNTIME_AUTH=secret\n", "hermes.dev.yaml": "model: {}"} {
		if err := os.WriteFile(filepath.Join(ctx, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repoSkills := filepath.Join(root, "config", "skills")
	if err := os.MkdirAll(repoSkills, 0700); err != nil {
		t.Fatal(err)
	}
	rawBob, err := m.runArgs(binding(ctx), "hermes-context-bob", 19001)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(rawBob, " ")
	if !strings.Contains(joined, "src="+repoSkills+",dst=/opt/hub/skills,readonly") {
		t.Fatalf("run args missing default skills mount: %s", joined)
	}
}

func TestReapSweepsOrphanLeases(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, _ ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	key := runtimeKey(b)
	m.mu.Lock()
	entry := m.items[key]
	entry.State = Busy
	entry.leases = map[string]Lease{
		"lease-job":      {ID: "lease-job", Kind: LeaseJob, Owner: "job:ghost", Generation: entry.Generation},
		"lease-gen":      {ID: "lease-gen", Kind: LeaseJob, Owner: "job:still-running", Generation: "old-generation"},
		"lease-super":    {ID: "lease-super", Kind: LeaseStream, Owner: "supervisor", Generation: entry.Generation, ExpiresAt: m.cfg.Now().Add(-time.Minute)},
		"lease-live-job": {ID: "lease-live-job", Kind: LeaseJob, Owner: "job:live", Generation: entry.Generation},
	}
	entry.Leases = 4
	m.jobs["live"] = jobRecord{Status: "running"}
	m.jobs["ghost"] = jobRecord{Status: "completed"}
	m.mu.Unlock()
	if err := m.Reap(context.Background(), m.cfg.Now()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.Leases != 2 || len(entry.leases) != 2 {
		t.Fatalf("orphan leases not swept: leases=%v map=%v", entry.Leases, entry.leases)
	}
	for _, id := range []string{"lease-live-job", "lease-super"} {
		if _, ok := entry.leases[id]; !ok {
			t.Fatalf("live lease %s swept: %v", id, entry.leases)
		}
	}
	if entry.State != Busy {
		t.Fatalf("entry with live leases must stay busy: %s", entry.State)
	}
	m.jobs["live"] = jobRecord{Status: "failed"}
	m.mu.Unlock()
	if err := m.Reap(context.Background(), m.cfg.Now()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	if entry.Leases != 1 || len(entry.leases) != 1 || entry.State != Busy {
		t.Fatalf("caller-held lease must survive the job sweep: leases=%v state=%s", entry.Leases, entry.State)
	}
	if _, ok := entry.leases["lease-super"]; !ok {
		t.Fatalf("caller-held lease swept: %v", entry.leases)
	}
}

func TestReapMarksDegradedOnDockerFailure(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if len(args) > 0 && args[0] == "rm" {
			return nil, os.ErrPermission
		}
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.Release("alice", "gateway"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reap(context.Background(), time.Unix(100, 0).Add(time.Minute)); err == nil {
		t.Fatal("docker stop failure ignored")
	}
	r, _, _ := m.Status(b)
	if r.State != Degraded {
		t.Fatalf("state=%s", r.State)
	}
}

func TestExecuteAbandonsJobWhenRuntimeNeverRan(t *testing.T) {
	m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return nil, os.ErrPermission
	}, nil)
	body := `{"identity_schema":1,"principal_id":"alice","external_identity_id":"telegram-1","conversation_id":"telegram-1","delivery_target_id":"telegram-1","context_id":"alice","runtime_id":"alice","policy_version":"policy-1","organization_id":"personal","user_id":"alice","actor_id":"alice","scope_id":"user:alice","channel":"telegram_bot","trigger":"message","idempotency_key":"one","job_id":"job-1","text":"hello"}`
	post := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(); code != http.StatusServiceUnavailable {
		t.Fatalf("spawn failure status=%d", code)
	}
	m.mu.Lock()
	_, kept := m.jobs["job-1"]
	m.mu.Unlock()
	if kept {
		t.Fatal("undispatched job recorded as terminal; a retry would replay instead of re-admitting")
	}
	if code := post(); code != http.StatusServiceUnavailable {
		t.Fatalf("retried wake job status=%d", code)
	}
}
