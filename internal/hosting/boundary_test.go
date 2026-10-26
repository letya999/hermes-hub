package hosting

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFailedPreparationConsumesRevision(t *testing.T) {
	s, _ := fixture(t)
	e := &executorFake{ensureErr: true, removeErr: true}
	s.executor = e
	if _, err := s.Publish(context.Background(), s.Binding(), dynamicRequest("failed")); err == nil {
		t.Fatal("failed admission published")
	}
	var failed record
	for _, r := range s.state.Pending {
		failed = r
	}
	if failed.Revision != 1 {
		t.Fatal("missing durable failed revision")
	}
	e.ensureErr = false
	s.now = func() time.Time { return time.Now().Add(time.Minute) }
	site, err := s.Publish(context.Background(), s.Binding(), dynamicRequest("next"))
	if err != nil || site.Revision != 2 {
		t.Fatalf("revision reused: %d %v", site.Revision, err)
	}
	if s.state.Sites["page"].Release == failed.Release {
		t.Fatal("release reused")
	}
}

func TestApplicationCookiesExcludeAmbientAndOtherRevisions(t *testing.T) {
	w := Workload{Binding: Binding{"alice", "dev"}, Name: "page", Revision: 1, Release: strings.Repeat("a", 64)}
	cookie := appResponseCookie(w, &http.Cookie{Name: "session", Value: "app", Path: "/api"})
	if cookie == nil || !cookie.Secure || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatal("cookie not host-only")
	}
	req := httptest.NewRequest("GET", "https://site/", nil)
	req.AddCookie(cookie)
	req.AddCookie(&http.Cookie{Name: "hub-auth", Value: "secret"})
	if got := appRequestCookies(w, req); got != "session=app" {
		t.Fatalf("cookie boundary: %q", got)
	}
	w.Revision++
	if appRequestCookies(w, req) != "" {
		t.Fatal("old revision cookie escaped")
	}
	for _, c := range []*http.Cookie{{Name: cookieName, Value: "x"}, {Name: "session", Domain: "example.test"}, {Name: "bad name"}, {Name: "session", Value: strings.Repeat("x", 2049)}} {
		if appResponseCookie(w, c) != nil {
			t.Fatal("unsafe cookie accepted")
		}
	}
}

func TestShareBootstrapAllowsOnlyOwnExchange(t *testing.T) {
	s, _ := fixture(t)
	s.routes = routesFake{}
	r := request("share")
	r.Access = "link"
	site, err := s.Publish(context.Background(), s.Binding(), r)
	if err != nil {
		t.Fatal(err)
	}
	got := serve(s, "GET", strings.TrimPrefix(site.Origin, "https://"), "/", "", nil)
	if got.Code != 200 || !strings.Contains(got.Header().Get("Content-Security-Policy"), "connect-src 'self'") || !strings.Contains(got.Body.String(), "<body>") {
		t.Fatal("bootstrap cannot exchange token")
	}
}

type revisionExecutor struct {
	executorFake
	active                   map[string]bool
	failPrepare, failCleanup bool
}

func (e *revisionExecutor) Ensure(_ context.Context, w Workload) error {
	e.active[w.Release] = true
	if e.failPrepare {
		return ErrUnavailable
	}
	return nil
}
func (e *revisionExecutor) Remove(_ context.Context, w Workload) error {
	if e.failCleanup {
		return ErrUnavailable
	}
	delete(e.active, w.Release)
	return nil
}
func (e *revisionExecutor) Check(_ context.Context, w Workload) error {
	if !e.active[w.Release] {
		return ErrUnavailable
	}
	return nil
}

func TestStaleFinalizerCannotStopNewRelease(t *testing.T) {
	s, _ := fixture(t)
	e := &revisionExecutor{active: map[string]bool{}, failPrepare: true, failCleanup: true}
	s.executor = e
	if _, err := s.Publish(context.Background(), s.Binding(), dynamicRequest("failed")); err == nil {
		t.Fatal("failed prepare succeeded")
	}
	e.failPrepare = false
	site, err := s.Publish(context.Background(), s.Binding(), dynamicRequest("new"))
	if err != nil {
		t.Fatal(err)
	}
	current := s.state.Sites[site.Name].Release
	e.failCleanup = false
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.active[current] || len(e.active) != 1 || len(s.state.Pending) != 0 {
		t.Fatal("stale cleanup touched active release or retained failed release")
	}
}

func TestAdmissionPinChangeRevokesInsteadOfUpgrading(t *testing.T) {
	s, cfg := fixture(t)
	s.cfg.AdmissionDigest = "old"
	e := &executorFake{}
	s.executor = e
	if _, err := s.Publish(context.Background(), s.Binding(), dynamicRequest("old-pin")); err != nil {
		t.Fatal(err)
	}
	starts := e.starts
	_ = s.Close()
	cfg.AdmissionDigest = "new"
	again, err := Open(cfg, Options{Executor: e})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.Status("page"); err != ErrNotFound || e.running || e.starts != starts {
		t.Fatal("pin change silently upgraded old revision")
	}
}

func TestRemovedSlotIsRevokedOnRecovery(t *testing.T) {
	s, cfg := fixture(t)
	if _, err := s.Publish(context.Background(), s.Binding(), request("create")); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	cfg.Slots = []string{"other"}
	again, err := Open(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.Status("page"); err != ErrNotFound {
		t.Fatal("retired slot restored")
	}
}

func TestHostingBrokerIdentityMatchesExistingExposureGrant(t *testing.T) {
	cfg := Deployment{Config: Config{Owner: "alice", Environment: "prod"}, ContextID: "team", PolicyVersion: "reviewed"}
	auth := cfg.brokerAuth()
	if auth.PrincipalID != "alice" || auth.RuntimeID != "alice" || auth.ContextID != "team" || auth.PolicyVersion != "reviewed" {
		t.Fatal("hosting changed the existing exposure credential binding")
	}
	cfg.ContextID = ""
	if cfg.brokerAuth().ContextID != "alice" {
		t.Fatal("personal context fallback wrong")
	}
}
