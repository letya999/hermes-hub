package supervisor

import (
	"context"
	"encoding/json"
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
	m, err := New(Config{SpacesRoot: root, RuntimeAuth: "secret", Image: "hermes:test", WarmTTL: time.Minute, Command: command, Probe: probe, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	return m, ctxRoot
}

func binding(contextRoot string) Binding {
	return Binding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", UserID: "alice", OrganizationID: "personal", PolicyVersion: "policy-1", ContextRoot: contextRoot}
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
	if _, err := m.Ensure(context.Background(), binding(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), binding(root)); err != nil {
		t.Fatal(err)
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
	if _, err := m.normalize(Binding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", ContextRoot: root, OrganizationRoot: org, EnvFile: filepath.Join(root, "runtime.auth")}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.normalize(Binding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", ContextRoot: root, EnvFile: filepath.Join(filepath.Dir(root), "missing")}); err == nil {
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
	req = httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(`{"identity_schema":1,"principal_id":"alice","context_id":"alice","runtime_id":"alice","policy_version":"policy-1","organization_id":"personal","user_id":"alice","actor_id":"alice","scope_id":"user:alice","channel":"telegram_bot","trigger":"message","idempotency_key":"one","text":"hello"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("runtime failure status=%d", rec.Code)
	}
	_ = root
}

func TestRunArgsMountsExactContextAndLimits(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, _ ...string) ([]byte, error) { return nil, nil }, nil)
	if err := os.WriteFile(filepath.Join(root, "hermes.prod.yaml"), []byte("model: {}"), 0600); err != nil {
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
	args := m.runArgs(b, "hermes-context-test", 19000)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--read-only", "--cap-drop ALL", "--pids-limit 256", "--memory 1g", "--cpus 2", "127.0.0.1:19000:8080", "dst=/scope", "dst=/org", "dst=/config/config.yaml", "dst=/config/SOUL.md", "--env-file"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("run args missing %q: %s", want, joined)
		}
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
