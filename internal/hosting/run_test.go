package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/exposure"
)

func runDeployment(t *testing.T) Deployment {
	t.Helper()
	dir := t.TempDir()
	return Deployment{
		Config:           Config{Owner: "alice", Environment: "dev", Zone: "alice", Domain: "hub.example.test", Workspace: filepath.Join(dir, "workspace"), Store: filepath.Join(dir, "sites")},
		Slots:            []string{"page"},
		ExposureConfig:   "/run/hosting/exposure.yaml",
		ControlTokenFile: "/run/hosting/token",
		DataListen:       "127.0.0.1:8181",
		ControlListen:    "0.0.0.0:8182",
	}
}

func writeDeployment(t *testing.T, cfg Deployment) string {
	t.Helper()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "deployment.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDeploymentContract(t *testing.T) {
	cfg, err := LoadDeployment(writeDeployment(t, runDeployment(t)))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Config.Slots) != 1 || cfg.Config.Slots[0] != "page" {
		t.Fatalf("slots not projected into config: %+v", cfg.Config.Slots)
	}
	if cfg.Config.Restart != "on-failure" || cfg.Config.MaxSites != 4 {
		t.Fatalf("defaults not applied: %+v", cfg.Config)
	}
}

func TestLoadDeploymentRejections(t *testing.T) {
	if _, err := LoadDeployment(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing file must be invalid: %v", err)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeployment(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt json must be invalid: %v", err)
	}
	oversized := filepath.Join(dir, "big.json")
	if err := os.WriteFile(oversized, append([]byte(" "), make([]byte, 64<<10)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeployment(oversized); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized file must be invalid: %v", err)
	}

	reject := func(mutate func(*Deployment), why string) {
		t.Helper()
		cfg := runDeployment(t)
		mutate(&cfg)
		if _, err := LoadDeployment(writeDeployment(t, cfg)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s must be invalid: %v", why, err)
		}
	}
	reject(func(c *Deployment) { c.Slots = nil }, "empty slots")
	reject(func(c *Deployment) { c.Slots = make([]string, 17) }, "over-max slots")
	reject(func(c *Deployment) { c.Slots = []string{"page", "page"} }, "duplicate slots")
	reject(func(c *Deployment) { c.Slots = []string{"UPPER"} }, "invalid slot name")
	reject(func(c *Deployment) { c.ExposureConfig = "relative.yaml" }, "relative exposure path")
	reject(func(c *Deployment) { c.ControlTokenFile = "token" }, "relative token path")
	reject(func(c *Deployment) { c.DataListen = "0.0.0.0:8181" }, "public data listen")
	reject(func(c *Deployment) { c.ControlListen = "127.0.0.1:8182" }, "loopback control listen")
	reject(func(c *Deployment) { c.Config.Zone = "other" }, "zone-owner mismatch")
}

func TestReadTokenBoundaries(t *testing.T) {
	dir := t.TempDir()
	if _, err := readToken(filepath.Join(dir, "missing")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing token: %v", err)
	}
	if _, err := readToken(dir); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("directory token: %v", err)
	}
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := readToken(write("short", "abc")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short token: %v", err)
	}
	if _, err := readToken(write("newline", strings.Repeat("a", 20)+"\n"+strings.Repeat("b", 20))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("newline token: %v", err)
	}
	got, err := readToken(write("good", "  "+strings.Repeat("a", 40)+"  \n"))
	if err != nil || len(got) != 40 {
		t.Fatalf("valid token: %q %v", got, err)
	}
}

func TestMatchingExposureAndBroker(t *testing.T) {
	cfg := runDeployment(t)
	if matchingExposure(cfg, nil) {
		t.Fatal("nil exposure must not match")
	}
	ec := &exposure.Config{Provider: "sish", Zone: "alice", Relay: &exposure.Relay{Domain: "hub.example.test"}, Endpoints: map[string]*exposure.Endpoint{}}
	if !matchingExposure(cfg, ec) {
		t.Fatal("matching exposure rejected")
	}
	for _, mutate := range []func(*exposure.Config){
		func(e *exposure.Config) { e.Provider = "other" },
		func(e *exposure.Config) { e.Zone = "other" },
		func(e *exposure.Config) { e.Relay.Domain = "other.test" },
		func(e *exposure.Config) { e.Relay = nil },
		func(e *exposure.Config) {
			e.Endpoints = map[string]*exposure.Endpoint{"a": {}, "b": {}, "c": {}, "d": {}, "e": {}}
		},
	} {
		copy := *ec
		mutate(&copy)
		if matchingExposure(cfg, &copy) {
			t.Fatalf("mutated exposure matched: %+v", copy)
		}
	}
	if _, err := brokerForExposure(&exposure.Config{KeyRef: "file:/run/key"}); err != nil {
		t.Fatalf("file keyref must not need a broker: %v", err)
	}
	if _, err := brokerForExposure(&exposure.Config{KeyRef: "vault:key"}); err != nil {
		t.Fatalf("env broker with unset env: %v", err)
	}
}

func TestBrokerAuthIdentity(t *testing.T) {
	cfg := runDeployment(t)
	env := cfg.brokerAuth()
	if env.PrincipalID != "alice" || env.ContextID != "alice" || env.RuntimeID != "alice" || env.Environment != "dev" {
		t.Fatalf("broker auth identity wrong: %+v", env)
	}
	cfg.ContextID = "ctx-9"
	if env := cfg.brokerAuth(); env.ContextID != "ctx-9" {
		t.Fatalf("context override lost: %+v", env)
	}
}

type fakeRouteProvider struct {
	specs     []exposure.Spec
	closed    []string
	alive     bool
	failOpen  bool
	failClose bool
}

func (f *fakeRouteProvider) Open(_ context.Context, spec exposure.Spec) (string, error) {
	if f.failOpen {
		return "", errors.New("provider down")
	}
	f.specs = append(f.specs, spec)
	return "handle-" + spec.Endpoint, nil
}
func (f *fakeRouteProvider) Close(_ context.Context, handle string) error {
	if f.failClose {
		return errors.New("close failed")
	}
	f.closed = append(f.closed, handle)
	return nil
}
func (f *fakeRouteProvider) Alive(string) bool { return f.alive }

func TestSiteRoutesLeaseLifecycle(t *testing.T) {
	provider := &fakeRouteProvider{alive: true}
	ec := &exposure.Config{Provider: "sish", Zone: "alice", Relay: &exposure.Relay{Domain: "hub.example.test"}}
	routes := &siteRoutes{provider: provider, cfg: ec, allowed: map[string]bool{"page": true}, live: map[string]routeLease{}}
	ctx := context.Background()

	if err := routes.Ensure(ctx, "other"); !errors.Is(err, ErrDenied) {
		t.Fatalf("unallowed name: %v", err)
	}
	if err := routes.Ensure(ctx, "page"); err != nil {
		t.Fatal(err)
	}
	if len(provider.specs) != 1 || provider.specs[0].Endpoint != "page" || provider.specs[0].PublicName != "page.alice.hub.example.test" || provider.specs[0].Target != "127.0.0.1:8181" {
		t.Fatalf("route spec wrong: %+v", provider.specs)
	}
	if err := routes.Ensure(ctx, "page"); err != nil || len(provider.specs) != 1 {
		t.Fatal("fresh alive lease must not reopen")
	}
	// Stale lease closes then reopens.
	routes.live["page"] = routeLease{handle: "handle-page", expires: time.Now()}
	if err := routes.Ensure(ctx, "page"); err != nil {
		t.Fatal(err)
	}
	if len(provider.closed) != 1 || len(provider.specs) != 2 {
		t.Fatalf("stale lease must close+reopen: %+v", provider)
	}
	if err := routes.Remove(ctx, "page"); err != nil || len(routes.live) != 0 {
		t.Fatalf("remove must drop the lease: %v", err)
	}
	if err := routes.Remove(ctx, "page"); err != nil {
		t.Fatal("absent remove is a no-op")
	}
}

func TestSiteRoutesProviderFailuresFailClosed(t *testing.T) {
	provider := &fakeRouteProvider{alive: true, failOpen: true}
	routes := &siteRoutes{provider: provider, cfg: &exposure.Config{Relay: &exposure.Relay{Domain: "d.test"}}, allowed: map[string]bool{"page": true}, live: map[string]routeLease{}}
	if err := routes.Ensure(context.Background(), "page"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("provider open failure: %v", err)
	}
	if len(routes.live) != 0 {
		t.Fatal("failed open must not record a lease")
	}
	provider2 := &fakeRouteProvider{alive: true, failClose: true}
	routes2 := &siteRoutes{provider: provider2, cfg: &exposure.Config{Relay: &exposure.Relay{Domain: "d.test"}}, allowed: map[string]bool{"page": true}, live: map[string]routeLease{"page": {handle: "h", expires: time.Now()}}}
	if err := routes2.Ensure(context.Background(), "page"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("provider close failure: %v", err)
	}
	if err := routes2.Remove(context.Background(), "page"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("remove close failure: %v", err)
	}
}

func TestAcquireZoneLeaseFences(t *testing.T) {
	if _, err := acquireZoneLease("relative.lock"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative lease path: %v", err)
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireZoneLease(sub); !errors.Is(err, ErrDenied) {
		t.Fatalf("directory lease path: %v", err)
	}
	lockPath := filepath.Join(dir, "zone.lock")
	release, err := acquireZoneLease(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireZoneLease(lockPath); !errors.Is(err, ErrConflict) {
		t.Fatalf("held lease must conflict: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestInitVolumeDeniesForeignPaths(t *testing.T) {
	if err := InitVolume("/tmp/anywhere"); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign path must be denied: %v", err)
	}
	if err := InitVolume("/sites/does/not/exist/at-all-xyz"); !errors.Is(err, ErrDenied) {
		t.Fatalf("nested path must be denied: %v", err)
	}
}
