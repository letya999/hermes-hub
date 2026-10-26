package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/exposure"
)

func TestControllerClientContractAndNoAmbientCredentials(t *testing.T) {
	seen := []string{}
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-controller-token" {
			t.Error("wrong controller auth")
		}
		var in ExecutorRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Error("invalid wire request")
		}
		seen = append(seen, in.Action)
		if fail {
			http.Error(w, "do not forward this private error", http.StatusServiceUnavailable)
			return
		}
		if in.Request != nil {
			if in.Request.Header.Get("Authorization") != "" || strings.Contains(in.Request.Header.Get("Cookie"), cookieName) || in.Request.Header.Get("X-Forwarded-Host") != "" || in.Request.Header.Get("Cookie") != "app=value" {
				t.Errorf("unsafe relay headers")
			}
		}
		_ = json.NewEncoder(w).Encode(RelayResponse{Status: 201, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: []byte("application")})
	}))
	defer server.Close()
	c := &ControllerClient{URL: server.URL, Token: "scoped-controller-token", HTTP: server.Client()}
	workload := Workload{Binding: Binding{"alice", "dev"}, Name: "page", Revision: 1, Release: strings.Repeat("b", 64), ExpiresAt: time.Now().Add(time.Hour)}
	if err := c.Ensure(context.Background(), workload); err != nil {
		t.Fatal(err)
	}
	if err := c.Check(context.Background(), workload); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "https://page.alice.hub.example.test/api", strings.NewReader("request"))
	req.Header.Set("Authorization", "Bearer viewer-secret")
	req.Header.Set("X-Forwarded-Host", "foreign")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "hosting-session"})
	req.AddCookie(&http.Cookie{Name: "parent_auth", Value: "must-not-reach-app"})
	req.AddCookie(appResponseCookie(workload, &http.Cookie{Name: "app", Value: "value"}))
	response, err := c.RoundTrip(context.Background(), workload, req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 201 || string(body) != "application" {
		t.Fatal("relay response changed")
	}
	if err = c.Remove(context.Background(), workload); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "ensure,check,request,remove" {
		t.Fatalf("wrong operations %v", seen)
	}
	fail = true
	if err = c.Ensure(context.Background(), workload); err != ErrUnavailable {
		t.Fatal("raw provider error exposed")
	}
	req = httptest.NewRequest("POST", "https://site/api", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	if _, err = c.RoundTrip(context.Background(), workload, req); err != ErrQuota {
		t.Fatal("unbounded request body")
	}
	invalid := &ControllerClient{}
	if err = invalid.Check(context.Background(), workload); err != ErrUnavailable {
		t.Fatal("unconfigured controller")
	}
}

type providerFake struct {
	opens, closes       int
	alive               bool
	failOpen, failClose bool
}

func (p *providerFake) Open(_ context.Context, spec exposure.Spec) (string, error) {
	if spec.Target != "127.0.0.1:8181" || spec.Bind != "page.alice" || spec.PublicName != "page.alice.hub.example.test" {
		return "", ErrDenied
	}
	if p.failOpen {
		return "", ErrUnavailable
	}
	p.opens++
	p.alive = true
	return "handle", nil
}
func (p *providerFake) Close(context.Context, string) error {
	if p.failClose {
		return ErrUnavailable
	}
	p.closes++
	p.alive = false
	return nil
}
func (p *providerFake) Alive(string) bool { return p.alive }
func TestHostingRouteReconciliationAndReservedNames(t *testing.T) {
	p := &providerFake{}
	routes := &siteRoutes{provider: p, cfg: &exposure.Config{Zone: "alice", Relay: &exposure.Relay{Domain: "hub.example.test"}}, allowed: map[string]bool{"page": true}, live: map[string]routeLease{}}
	ctx := context.Background()
	if err := routes.Ensure(ctx, "bob"); err != ErrDenied {
		t.Fatal("undelegated name accepted")
	}
	if err := routes.Ensure(ctx, "page"); err != nil {
		t.Fatal(err)
	}
	if err := routes.Ensure(ctx, "page"); err != nil || p.opens != 1 {
		t.Fatal("duplicate forward")
	}
	p.alive = false
	if err := routes.Ensure(ctx, "page"); err != nil || p.opens != 2 || p.closes != 1 {
		t.Fatal("lost SSH forward not reconciled")
	}
	p.failClose = true
	if err := routes.Remove(ctx, "page"); err != ErrUnavailable || len(routes.live) != 1 {
		t.Fatal("lost cleanup intent")
	}
	p.failClose = false
	routes.close()
	if len(routes.live) != 0 {
		t.Fatal("route not closed")
	}
	if err := routes.Remove(ctx, "page"); err != nil {
		t.Fatal("non-idempotent removal")
	}
	p.failOpen = true
	if err := routes.Ensure(ctx, "page"); err != ErrUnavailable {
		t.Fatal("provider failure hidden")
	}
}
func TestDeploymentStrictInputAndTokenFiles(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte(strings.Repeat("a", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	token, err := ReadControlToken(tokenPath)
	if err != nil || len(token) != 32 {
		t.Fatal("valid token not read")
	}
	if err = os.WriteFile(tokenPath, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadControlToken(tokenPath); err != ErrInvalid {
		t.Fatal("weak token accepted")
	}
	if _, err = ReadControlToken(filepath.Join(dir, "missing")); err != ErrUnavailable {
		t.Fatal("missing token")
	}
	if _, err = LoadDeployment(filepath.Join(dir, "absent")); err != ErrInvalid {
		t.Fatal("missing config")
	}
	cfg := Deployment{Config: Config{Owner: "alice", Environment: "dev", Zone: "alice", Domain: "hub.example.test", Workspace: "/workspace", Store: "/sites"}, Slots: []string{"page"}, ExposureConfig: "/exposure/config.yaml", ControlTokenFile: "/run/token", ControlListen: "0.0.0.0:8182", DataListen: "127.0.0.1:8181"}
	p := filepath.Join(dir, "hosting.json")
	data, _ := json.Marshal(cfg)
	if err = os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDeployment(p)
	if err != nil || len(loaded.Config.Slots) != 1 {
		t.Fatalf("valid deployment: %v", err)
	}
	for _, body := range []string{"{}", string(data) + " {}", strings.TrimSuffix(string(data), "}") + ",\"unknown\":true}"} {
		if err = os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = LoadDeployment(p); err == nil {
			t.Fatal("invalid deployment accepted")
		}
	}
}
func TestSessionRotationExpiryAndAuditPrivacy(t *testing.T) {
	s, _ := fixture(t)
	now := s.now()
	s.now = func() time.Time { return now }
	site := publishLink(t, s)
	u, _ := url.Parse(site.URL)
	exchange := serve(s, "POST", u.Host, "/_hosting/exchange", u.Fragment, nil)
	cookie := exchange.Result().Cookies()[0]
	// Unsafe sibling exchange and malformed cookies never expose application bytes.
	req := httptest.NewRequest("POST", "https://"+u.Host+"/_hosting/exchange", strings.NewReader(u.Fragment))
	req.Header.Set("Origin", "https://sibling.alice.hub.example.test")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal("sibling exchange accepted")
	}
	for _, value := range []string{"bad", "bad.bad", "a.b.c"} {
		got := serve(s, "GET", u.Host, "/index.html", "", &http.Cookie{Name: cookieName, Value: value})
		if got.Code != 404 {
			t.Fatal("invalid session accepted")
		}
	}
	now = now.Add(2 * time.Hour)
	if got := serve(s, "GET", u.Host, "/index.html", "", cookie); got.Code != 404 {
		t.Fatal("session outlived one hour")
	}
	r := request("replace")
	r.Access = "link"
	r.ExpectedRevision = site.Revision
	second, err := s.Publish(context.Background(), s.Binding(), r)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || second.URL == site.URL {
		t.Fatal("republish did not rotate")
	}
	if got := serve(s, "POST", u.Host, "/_hosting/exchange", u.Fragment, nil); got.Code != 404 {
		t.Fatal("old share token still valid")
	}
	files, _, err := s.auditFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append(files, "state.json") {
		body, err := s.root.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), u.Fragment) || strings.Contains(string(body), "sibling.alice") {
			t.Fatal("token or request header persisted")
		}
	}
}
func TestHostingLimitsBeforeDispatch(t *testing.T) {
	s, _ := fixture(t)
	fixed := time.Now()
	s.now = func() time.Time { return fixed }
	s.routes = routesFake{}
	r := request("public")
	r.Access = "public"
	site, err := s.Publish(context.Background(), s.Binding(), r)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(site.Origin, "https://")
	for i := 0; i < 20; i++ {
		if got := serve(s, "HEAD", host, "/", "", nil); got.Code != 200 {
			t.Fatal("within rate denied")
		}
	}
	got := serve(s, "HEAD", host, "/", "", nil)
	if got.Code != 429 || got.Header().Get("Retry-After") == "" {
		t.Fatal("site rate not enforced")
	}
	for i := 0; i < 8; i++ {
		s.inflight[uint64(i)] = activeRequest{"page", func() {}}
	}
	s.userRate = rateBudget{}
	s.siteRates["page"] = &rateBudget{}
	got = serve(s, "GET", host, "/", "", nil)
	if got.Code != 429 {
		t.Fatal("inflight limit")
	}
}
func TestSnapshotHardlinkDenied(t *testing.T) {
	s, cfg := fixture(t)
	original := filepath.Join(cfg.Workspace, "page", "index.html")
	if err := os.Link(original, filepath.Join(cfg.Workspace, "page", "linked")); err != nil {
		t.Skip("hardlinks unavailable")
	}
	if _, err := s.Publish(context.Background(), s.Binding(), request("hardlink")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("hardlink accepted: %v", err)
	}
}

func TestHostingServerLifecycleAndListenerSeparation(t *testing.T) {
	s, _ := fixture(t)
	s.routes = routesFake{}
	r := request("public")
	r.Access = "public"
	site, err := s.Publish(context.Background(), s.Binding(), r)
	if err != nil {
		t.Fatal(err)
	}
	data, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	control, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	token := strings.Repeat("t", 32)
	go func() { done <- serveListeners(ctx, s, token, data, control) }()
	client := &http.Client{Timeout: 2 * time.Second}
	req := httptest.NewRequest("GET", "http://"+data.Addr().String()+"/", nil)
	req.RequestURI = ""
	req.Host = strings.TrimPrefix(site.Origin, "https://")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || string(body) != "alice-page" {
		t.Fatal("data listener did not serve committed bytes")
	}
	req = httptest.NewRequest("POST", "http://"+control.Addr().String()+"/v1/list", strings.NewReader("{}"))
	req.RequestURI = ""
	req.Header.Set("Authorization", "Bearer "+token)
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("private listener unavailable")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hosting shutdown leaked servers")
	}
}

func TestHostingZoneLeaseFencesOtherProcesses(t *testing.T) {
	if len(os.Args) > 2 && os.Args[len(os.Args)-2] == "hosting-zone-child" {
		release, err := acquireZoneLease(os.Args[len(os.Args)-1])
		if err == ErrConflict {
			_, _ = io.WriteString(os.Stdout, "denied")
			os.Exit(0)
		}
		if err != nil {
			os.Exit(2)
		}
		_ = release()
		_, _ = io.WriteString(os.Stdout, "acquired")
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "address.lock")
	release, err := acquireZoneLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := func() string {
		t.Helper()
		out, err := exec.Command(binary, "-test.run=^TestHostingZoneLeaseFencesOtherProcesses$", "--", "hosting-zone-child", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if child() != "denied" {
		t.Fatal("second process entered the active zone")
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	if child() != "acquired" {
		t.Fatal("released lease did not recover")
	}
}
