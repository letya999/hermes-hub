package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type executorFake struct {
	running              bool
	ensureErr, removeErr bool
	starts, removes      int
	response             *http.Response
}

func (e *executorFake) Ensure(context.Context, Workload) error {
	e.starts++
	if e.ensureErr {
		return ErrUnavailable
	}
	e.running = true
	return nil
}
func (e *executorFake) Remove(context.Context, Workload) error {
	e.removes++
	if e.removeErr {
		return ErrUnavailable
	}
	e.running = false
	return nil
}
func (e *executorFake) Check(context.Context, Workload) error {
	if !e.running {
		return ErrUnavailable
	}
	return nil
}
func (e *executorFake) RoundTrip(context.Context, Workload, *http.Request) (*http.Response, error) {
	if e.response != nil {
		return e.response, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("dynamic-result"))}, nil
}
func dynamicRequest(id string) PublishRequest {
	r := request(id)
	r.Mode = "dynamic"
	r.Profile = "go-http"
	return r
}

func TestHostingDynamicLifecycleAndRestartBudget(t *testing.T) {
	s, cfg := fixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	executor := &executorFake{}
	s.executor = executor
	first, err := s.Publish(context.Background(), s.Binding(), dynamicRequest("create"))
	if err != nil {
		t.Fatal(err)
	}
	if executor.starts != 1 || first.Mode != "dynamic" {
		t.Fatal("dynamic profile not prepared")
	}
	executor.running = false
	executor.ensureErr = true
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.state.Sites["page"].Restarts != 1 {
		t.Fatal("restart attempt not persisted")
	}
	_ = s.Close()
	restored, err := Open(cfg, Options{Executor: executor, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.state.Sites["page"].Restarts != 1 {
		t.Fatal("restart reset across startup")
	}
	for i := 0; i < 3; i++ {
		now = now.Add(40 * time.Second)
		if err = restored.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	status, err := restored.Status("page")
	if err != nil || status.State != "failed" || executor.starts != 4 {
		t.Fatalf("restart budget: %#v starts=%d %v", status, executor.starts, err)
	}
}
func TestHostingFinalizerRetryAndPendingRecovery(t *testing.T) {
	s, cfg := fixture(t)
	executor := &executorFake{}
	s.executor = executor
	first, err := s.Publish(context.Background(), s.Binding(), dynamicRequest("create"))
	if err != nil {
		t.Fatal(err)
	}
	release := s.state.Sites["page"].Release
	executor.removeErr = true
	if err = s.Unpublish(context.Background(), s.Binding(), UnpublishRequest{"page", first.Revision, "remove"}); err != ErrUnavailable {
		t.Fatalf("cleanup error hidden: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("removed site remained readable")
	}
	if _, err = s.root.Stat(release); err != nil {
		t.Fatal("release removed before workload zero")
	}
	_ = s.Close()
	executor.removeErr = false
	restored, err := Open(cfg, Options{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = restored.root.Stat(release); !os.IsNotExist(err) {
		t.Fatal("finalizer did not remove retired release")
	}
}
func TestPendingWorkloadIntentSurvivesPreparationFailure(t *testing.T) {
	s, cfg := fixture(t)
	executor := &executorFake{ensureErr: true}
	s.executor = executor
	if _, err := s.Publish(context.Background(), s.Binding(), dynamicRequest("create")); err != ErrUnavailable {
		t.Fatalf("prepare failure %v", err)
	}
	if len(s.state.Pending) != 1 || len(s.List()) != 0 {
		t.Fatal("failed preparation lost durable cleanup intent")
	}
	_ = s.Close()
	executor.ensureErr = false
	restored, err := Open(cfg, Options{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if len(restored.state.Pending) != 0 || executor.removes != 1 {
		t.Fatal("orphan preparation not finalized")
	}
}
func TestDynamicPublishRequiresGoBinary(t *testing.T) {
	s, cfg := fixture(t)
	s.executor = &executorFake{}
	if err := os.MkdirAll(filepath.Join(cfg.Workspace, "static"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "static", "index.html"), []byte("page"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := dynamicRequest("no-app")
	missing.Source = "static"
	if _, err := s.Publish(context.Background(), s.Binding(), missing); err != ErrInvalid {
		t.Fatalf("missing app binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "static", "app"), []byte("#!/bin/sh\necho hi\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := dynamicRequest("script")
	script.Source = "static"
	if _, err := s.Publish(context.Background(), s.Binding(), script); err != ErrInvalid {
		t.Fatalf("non-ELF app: %v", err)
	}
	s.publishTokens = 2
	site, err := s.Publish(context.Background(), s.Binding(), dynamicRequest("valid"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.root.Stat(s.state.Sites[site.Name].Release + "/app")
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode()&0o100 == 0 {
		t.Fatalf("committed app not executable: %v", err)
	}
}
func TestPublishConcurrencyRevisionFence(t *testing.T) {
	s, _ := fixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := s.Publish(context.Background(), s.Binding(), request(id))
			results <- err
		}(id)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || len(s.List()) != 1 {
		t.Fatalf("concurrent publication: %d/%d", success, conflict)
	}
}
func TestSiteExpirySurvivesRestart(t *testing.T) {
	s, cfg := fixture(t)
	first, err := s.Publish(context.Background(), s.Binding(), request("create"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	restored, err := Open(cfg, Options{Now: func() time.Time { return first.ExpiresAt.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if len(restored.List()) != 0 {
		t.Fatal("expired site restored")
	}
	if _, err = restored.Status("page"); err != ErrNotFound {
		t.Fatalf("expired status %v", err)
	}
}
func TestHostingAuditCapacityAndRecovery(t *testing.T) {
	s, cfg := fixture(t)
	s.cfg.AuditBytes = 4096
	for i := 0; i < 20; i++ {
		if s.audit("deny", "page", 1, "denied", 0) != nil {
			break
		}
	}
	if _, err := s.Publish(context.Background(), s.Binding(), request("full")); err != ErrUnavailable || len(s.List()) != 0 {
		t.Fatalf("audit pressure %v", err)
	}
	_ = s.Close()
	// Interrupted admission is completed on restart; expired segments are removed.
	other, err := Open(cfg, Options{Now: func() time.Time { return time.Now().Add(9 * 24 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if files, _, err := other.auditFiles(); err != nil || len(files) != 0 {
		t.Fatalf("retention %v %v", files, err)
	}
	if _, err = other.beginAudit("publish", "page", 1); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	again, err := Open(cfg, Options{Now: func() time.Time { return time.Now().Add(9 * 24 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	files, _, _ := again.auditFiles()
	body, err := again.root.ReadFile(files[0])
	if err != nil || !strings.Contains(string(body), "\"outcome\":\"interrupted\"") {
		t.Fatal("missing interrupted completion")
	}
}
func TestHostingHTTPControlAndSafeStaticContract(t *testing.T) {
	s, _ := fixture(t)
	s.routes = routesFake{}
	r := request("public")
	r.Access = "public"
	site, err := s.Publish(context.Background(), s.Binding(), r)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(site.Origin, "https://")
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"HEAD", "/", 200}, {"GET", "/index.html", 200}, {"GET", "/.env", 404}, {"GET", "/../index.html", 404}, {"GET", "/missing", 404}, {"POST", "/", 404}} {
		w := serve(s, tc.method, host, tc.path, "", nil)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
		if tc.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
	handler := s.ControlHandler(strings.Repeat("t", 32))
	for _, tc := range []struct {
		path, body string
		status     int
	}{{"/v1/list", "{}", 200}, {"/v1/status", `{"name":"page"}`, 200}, {"/v1/status", `{"name":"absent"}`, 404}, {"/v1/publish", `{"owner":"bob"}`, 400}, {"/v1/list", "{} {}", 400}, {"/unknown", "{}", 404}, {"/v1/publish", `{"name":"page","source":"page","request_id":"stale"}`, 409}, {"/v1/unpublish", `{"name":"page","expected_revision":1,"request_id":"remove"}`, 200}} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.path, tc.body, w.Code)
		}
	}
}
func TestDynamicProxyOriginCookiesAndSecretStripping(t *testing.T) {
	s, _ := fixture(t)
	s.routes = routesFake{}
	executor := &executorFake{}
	s.executor = executor
	r := dynamicRequest("dynamic")
	r.Access = "public"
	site, err := s.Publish(context.Background(), s.Binding(), r)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(site.Origin, "https://")
	got := serve(s, "GET", host, "/", "", nil)
	if got.Code != 200 || got.Body.String() != "dynamic-result" {
		t.Fatalf("dynamic dispatch %d %q", got.Code, got.Body.String())
	}
	executor.response = &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": []string{"parent=bad; Domain=hub.example.test", "__Host-hub-site=spoof; Secure; Path=/", "app=ok; Path=/"}}, Body: io.NopCloser(strings.NewReader("app"))}
	got = serve(s, "GET", host, "/", "", nil)
	cookies := got.Result().Cookies()
	if len(cookies) != 1 || !strings.HasPrefix(cookies[0].Name, "__Host-site-") || !cookies[0].Secure || cookies[0].Domain != "" || cookies[0].Path != "/" {
		t.Fatalf("unsafe app cookies escaped: %#v", cookies)
	}
	req := httptest.NewRequest("POST", "https://"+host+"/", strings.NewReader("body"))
	req.Header.Set("Origin", "https://sibling.alice.hub.example.test")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal("sibling POST admitted")
	}
}
func TestCorruptStoreAndOwnerBindingFailClosed(t *testing.T) {
	s, cfg := fixture(t)
	_ = s.Close()
	for _, body := range []string{"broken", `{"schema":1,"binding":{"owner":"bob","environment":"dev"},"sites":{},"operations":{}}`} {
		if err := os.WriteFile(filepath.Join(cfg.Store, "state.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if restored, err := Open(cfg, Options{}); err == nil {
			_ = restored.Close()
			t.Fatal("corrupt/foreign state accepted")
		}
	}
}
func TestHostingConfigAndInvalidRequests(t *testing.T) {
	s, cfg := fixture(t)
	cases := []func(*Config){func(c *Config) { c.Owner = "bob" }, func(c *Config) { c.Environment = "other" }, func(c *Config) { c.MaxSites = -1 }, func(c *Config) { c.MaxBytes = -1 }, func(c *Config) { c.Domain = "127.0.0.1" }, func(c *Config) { c.Domain = "UPPER.test" }, func(c *Config) { c.Restart = "always" }}
	for _, change := range cases {
		copy := cfg
		change(&copy)
		if copy.validate() == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	for i, r := range []PublishRequest{{Name: "admin", Source: "page", RequestID: "one"}, {Name: "page", Source: "page", RequestID: "two", Mode: "shell"}} {
		s.publishTokens = 2
		if _, err := s.Publish(context.Background(), s.Binding(), r); err == nil {
			t.Fatalf("invalid request %d", i)
		}
	}
	if err := InitVolume(t.TempDir()); err != ErrDenied {
		t.Fatal("arbitrary volume initializer path")
	}
	if _, err := s.Status("missing"); err != ErrNotFound {
		t.Fatal("missing site")
	}
	if err := s.Unpublish(context.Background(), Binding{"bob", "dev"}, UnpublishRequest{}); err != ErrDenied {
		t.Fatal("foreign unpublish")
	}
	if err := s.Unpublish(context.Background(), s.Binding(), UnpublishRequest{"missing", 0, "missing"}); err != ErrNotFound {
		t.Fatal("missing unpublish")
	}
	if _, err := json.Marshal(s.List()); err != nil {
		t.Fatal(err)
	}
}
