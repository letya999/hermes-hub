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

func validDeployment(t *testing.T) Deployment {
	t.Helper()
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	store := filepath.Join(root, "store")
	for _, d := range []string{ws, store} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return Deployment{
		Config: Config{
			Owner: "alice", Environment: "prod", Zone: "alice", Domain: "sites.example.com",
			Workspace: ws, Store: store,
		},
		Slots:            []string{"blog"},
		ExposureConfig:   filepath.Join(root, "exposure.yaml"),
		ControlTokenFile: filepath.Join(root, "control.token"),
		DataListen:       "127.0.0.1:8181",
		ControlListen:    "0.0.0.0:8182",
	}
}

func TestLoadDeploymentValidation(t *testing.T) {
	if _, err := LoadDeployment(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing: %v", err)
	}
	dir := t.TempDir()
	if _, err := LoadDeployment(dir); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dir: %v", err)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, 65<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeployment(big); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize: %v", err)
	}
	seq := 0
	write := func(body string) string {
		t.Helper()
		seq++
		p := filepath.Join(dir, "d"+strings.Repeat("x", seq)+".json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadDeployment(write("not json")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad json: %v", err)
	}
	if _, err := LoadDeployment(write(`{"slots":["blog"],"unknown":1}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown field: %v", err)
	}
	cfg := validDeployment(t)
	body, _ := json.Marshal(cfg)
	if _, err := LoadDeployment(write(string(body))); err != nil {
		t.Fatalf("valid deployment: %v", err)
	}
	mutate := func(f func(*Deployment)) string {
		t.Helper()
		bad := validDeployment(t)
		f(&bad)
		raw, _ := json.Marshal(bad)
		return write(string(raw))
	}
	for _, f := range []func(*Deployment){
		func(d *Deployment) { d.Slots = nil },
		func(d *Deployment) { d.Slots = []string{"blog", "blog"} },
		func(d *Deployment) { d.Slots = []string{"Bad Name"} },
		func(d *Deployment) { d.Slots = []string{"api"} },
		func(d *Deployment) { d.ExposureConfig = "rel.yaml" },
		func(d *Deployment) { d.ControlTokenFile = "rel.token" },
		func(d *Deployment) { d.DataListen = "0.0.0.0:8181" },
		func(d *Deployment) { d.ControlListen = "127.0.0.1:8182" },
		func(d *Deployment) { d.Owner = "bad owner!" },
		func(d *Deployment) { d.Environment = "staging" },
		func(d *Deployment) { d.Zone = "other-zone" },
		func(d *Deployment) { d.Domain = "no-dot" },
		func(d *Deployment) { d.Domain = "10.0.0.1" },
		func(d *Deployment) { d.Domain = "Upper.example.com" },
		func(d *Deployment) { d.Restart = "always" },
		func(d *Deployment) { d.Workspace = d.Store },
		func(d *Deployment) { d.Workspace = filepath.Join(d.Store, "inside") },
		func(d *Deployment) { d.MaxSites = 100 },
		func(d *Deployment) { d.MaxBytes = 1 },
		func(d *Deployment) { d.MaxFiles = 100001 },
	} {
		if _, err := LoadDeployment(mutate(f)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("mutation admitted: %v", err)
		}
	}
	// Slot ceiling: MaxSites bounds the deployment slot list.
	over := validDeployment(t)
	over.MaxSites = 1
	over.Slots = []string{"a", "b"}
	raw, _ := json.Marshal(over)
	if _, err := LoadDeployment(write(string(raw))); !errors.Is(err, ErrInvalid) {
		t.Fatal("over-slots admitted")
	}
	over.Slots = []string{"a", "b"}
	over.MaxSites = 4
	raw, _ = json.Marshal(over)
	if _, err := LoadDeployment(write(string(raw))); err != nil {
		t.Fatalf("two slots under max: %v", err)
	}
}

func TestReadTokenGuards(t *testing.T) {
	if _, err := ReadControlToken(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing: %v", err)
	}
	dir := t.TempDir()
	if _, err := ReadControlToken(dir); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dir: %v", err)
	}
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadControlToken(short); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short: %v", err)
	}
	multi := filepath.Join(dir, "multi")
	if err := os.WriteFile(multi, []byte(strings.Repeat("a", 40)+"\n"+strings.Repeat("b", 4)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadControlToken(multi); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multiline: %v", err)
	}
	good := filepath.Join(dir, "good")
	token := strings.Repeat("k", 48)
	if err := os.WriteFile(good, []byte("  "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadControlToken(good); err != nil || got != token {
		t.Fatalf("good: %q %v", got, err)
	}
}

func TestMatchingExposure(t *testing.T) {
	cfg := validDeployment(t)
	if matchingExposure(cfg, nil) {
		t.Fatal("nil exposure matched")
	}
	ec := &exposure.Config{Provider: "sish", Zone: "alice", Relay: &exposure.Relay{Domain: "sites.example.com"}, Endpoints: map[string]*exposure.Endpoint{}}
	if !matchingExposure(cfg, ec) {
		t.Fatal("valid exposure rejected")
	}
	for _, f := range []func(*exposure.Config){
		func(e *exposure.Config) { e.Provider = "frp" },
		func(e *exposure.Config) { e.Zone = "other" },
		func(e *exposure.Config) { e.Relay = nil },
		func(e *exposure.Config) { e.Relay.Domain = "other.example.com" },
		func(e *exposure.Config) {
			e.MaxExposures = 1
			e.Endpoints = map[string]*exposure.Endpoint{"x": {Target: "t"}}
		},
	} {
		bad := *ec
		f(&bad)
		if matchingExposure(cfg, &bad) {
			t.Fatal("mismatched exposure admitted")
		}
	}
	if c, err := brokerForExposure(&exposure.Config{KeyRef: "file:/tmp/k"}); err != nil || c.URL != "" {
		t.Fatalf("file keyref: %v %v", c, err)
	}
}

type stubProvider struct {
	opens, closes int
	openErr       error
	closeErr      error
	alive         bool
}

func (p *stubProvider) Open(ctx context.Context, spec exposure.Spec) (string, error) {
	p.opens++
	if p.openErr != nil {
		return "", p.openErr
	}
	return "h-" + spec.Endpoint, nil
}

func (p *stubProvider) Close(ctx context.Context, handle string) error {
	p.closes++
	return p.closeErr
}

func (p *stubProvider) Alive(handle string) bool { return p.alive }

func TestSiteRoutesLeases(t *testing.T) {
	provider := &stubProvider{alive: true}
	routes := &siteRoutes{
		provider: provider,
		cfg:      &exposure.Config{Zone: "z", Relay: &exposure.Relay{Domain: "d.example.com"}},
		allowed:  map[string]bool{"blog": true},
		live:     map[string]routeLease{},
	}
	ctx := context.Background()
	if err := routes.Ensure(ctx, "foreign"); !errors.Is(err, ErrDenied) {
		t.Fatalf("unlisted slot: %v", err)
	}
	if err := routes.Ensure(ctx, "blog"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if provider.opens != 1 {
		t.Fatalf("opens: %d", provider.opens)
	}
	if err := routes.Ensure(ctx, "blog"); err != nil || provider.opens != 1 {
		t.Fatalf("live lease reopened: %d", provider.opens)
	}
	// A dead lease is closed and reopened.
	provider.alive = false
	if err := routes.Ensure(ctx, "blog"); err != nil || provider.opens != 2 || provider.closes != 1 {
		t.Fatalf("dead lease: opens=%d closes=%d err=%v", provider.opens, provider.closes, err)
	}
	// A not-found close is tolerated on the way to a fresh lease.
	provider.alive = true
	routes.live["blog"] = routeLease{handle: "h-gone", expires: time.Now().Add(-time.Hour)}
	provider.closeErr = exposure.ErrNotFound
	if err := routes.Ensure(ctx, "blog"); err != nil || provider.opens != 3 {
		t.Fatalf("not-found close: %v", err)
	}
	// A real close failure refuses; so does an open failure.
	routes.live["blog"] = routeLease{handle: "h-x", expires: time.Now().Add(-time.Hour)}
	provider.alive = false
	provider.closeErr = errors.New("daemon")
	if err := routes.Ensure(ctx, "blog"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("close failure: %v", err)
	}
	provider.closeErr = nil
	provider.openErr = errors.New("daemon")
	if err := routes.Ensure(ctx, "blog"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("open failure: %v", err)
	}
	provider.openErr = nil
	if err := routes.Remove(ctx, "ghost"); err != nil {
		t.Fatalf("remove unknown: %v", err)
	}
	if err := routes.Remove(ctx, "blog"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	provider.closeErr = errors.New("daemon")
	routes.live["blog"] = routeLease{handle: "h-blog", expires: time.Now().Add(time.Hour)}
	if err := routes.Remove(ctx, "blog"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("remove failure: %v", err)
	}
	provider.closeErr = nil
	routes.close()
	if len(routes.live) != 0 {
		t.Fatal("close left leases")
	}
}

func TestZoneLease(t *testing.T) {
	if _, err := acquireZoneLease("relative.lock"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative: %v", err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link.lock")
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err == nil {
		if _, err := acquireZoneLease(link); !errors.Is(err, ErrDenied) {
			t.Fatalf("symlink: %v", err)
		}
	}
	lockPath := filepath.Join(dir, "zone.lock")
	release, err := acquireZoneLease(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireZoneLease(lockPath); !errors.Is(err, ErrConflict) {
		t.Fatalf("second lease: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestInitVolumeGuards(t *testing.T) {
	if err := InitVolume("/etc"); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign path: %v", err)
	}
	if err := InitVolume("/sites"); err == nil {
		t.Fatal("missing /sites admitted")
	}
}

func TestDeploymentBrokerAuth(t *testing.T) {
	cfg := validDeployment(t)
	env := cfg.brokerAuth()
	if env.PrincipalID != "alice" || env.ContextID != "alice" || env.Environment != "prod" {
		t.Fatalf("broker auth: %+v", env)
	}
	cfg.ContextID = "ctx-9"
	if env = cfg.brokerAuth(); env.ContextID != "ctx-9" {
		t.Fatalf("context override: %+v", env)
	}
}

func testSnapshotService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	store := filepath.Join(root, "store")
	src := filepath.Join(ws, "site", "nested")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "site", "index.html"), []byte("<h1>x</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	wsRoot, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot, err := os.OpenRoot(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wsRoot.Close(); storeRoot.Close() })
	s := &Service{
		cfg:       Config{Store: store, MaxFiles: 100, MaxBytes: metadataReserve + (16 << 20), MaxReleaseBytes: 8 << 20},
		root:      storeRoot,
		workspace: wsRoot,
	}
	return s, store
}

func TestSnapshotCopiesTree(t *testing.T) {
	s, store := testSnapshotService(t)
	size, files, err := s.snapshot(context.Background(), "site", "rel-1")
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || size != int64(len("<h1>x</h1>")+len("data")) {
		t.Fatalf("copied %d files %d bytes", files, size)
	}
	for _, p := range []string{"index.html", filepath.Join("nested", "a.txt")} {
		if _, err := os.Lstat(filepath.Join(store, "rel-1", p)); err != nil {
			t.Fatalf("release missing %s: %v", p, err)
		}
	}
	if _, _, err := s.snapshot(context.Background(), "site", "rel-2"); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRefusals(t *testing.T) {
	s, _ := testSnapshotService(t)
	ctx := context.Background()
	if _, _, err := s.snapshot(ctx, "missing", "rel-x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing source: %v", err)
	}
	if _, _, err := s.snapshot(ctx, "site/index.html", "rel-x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("file source: %v", err)
	}
	// An empty source dir yields ErrInvalid (files == 0 at the end).
	emptyBase := t.TempDir()
	if err := os.MkdirAll(filepath.Join(emptyBase, "emptydir"), 0o750); err != nil {
		t.Fatal(err)
	}
	ws2, err := os.OpenRoot(emptyBase)
	if err != nil {
		t.Fatal(err)
	}
	defer ws2.Close()
	s2 := &Service{cfg: s.cfg, root: s.root, workspace: ws2}
	if _, _, err := s2.snapshot(ctx, "emptydir", "rel-e"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty dir: %v", err)
	}
	// A config-named file is refused mid-walk.
	badBase := t.TempDir()
	if err := os.MkdirAll(filepath.Join(badBase, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badBase, "src", "config.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws3, err := os.OpenRoot(badBase)
	if err != nil {
		t.Fatal(err)
	}
	defer ws3.Close()
	s3 := &Service{cfg: s.cfg, root: s.root, workspace: ws3}
	if _, _, err := s3.snapshot(ctx, "src", "rel-b"); !errors.Is(err, ErrInvalid) {
		t.Fatal("config file admitted")
	}
	// A symlink entry is refused.
	if err := os.Remove(filepath.Join(badBase, "src", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badBase, "src", "real.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(badBase, "src", "link.txt")); err == nil {
		if _, _, err := s3.snapshot(ctx, "src", "rel-l"); !errors.Is(err, ErrInvalid) {
			t.Fatal("symlink admitted")
		}
	}
	// Quota bound: the file ceiling trips mid-walk.
	s4 := &Service{cfg: Config{Store: s.cfg.Store, MaxFiles: 6, MaxBytes: s.cfg.MaxBytes, MaxReleaseBytes: s.cfg.MaxReleaseBytes}, root: s.root, workspace: s.workspace}
	if _, _, err := s4.snapshot(ctx, "site", "rel-q"); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota: %v", err)
	}
}

func TestServiceOpenAndHelpers(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Owner: "alice", Environment: "dev", Zone: ZoneForOwner("alice"),
		Domain:    "sites.example.com",
		Workspace: ws, Store: filepath.Join(root, "store"),
	}
	s, err := Open(cfg, Options{Now: func() time.Time { return time.Unix(1, 0) }})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := s.Binding(); got != (Binding{Owner: "alice", Environment: "dev"}) {
		t.Fatalf("binding: %+v", got)
	}
	if len(s.List()) != 0 {
		t.Fatal("empty store listed sites")
	}
	if _, err := s.Status("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("status: %v", err)
	}
	if err := s.authorized(Binding{Owner: "mallory", Environment: "dev"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign binding: %v", err)
	}
	if digest("a") == digest("b") || len(digest("a")) != 64 {
		t.Fatal("digest unstable")
	}
	if id, err := randomID(); err != nil || len(id) != 64 {
		t.Fatalf("randomID: %v %q", err, id)
	}
	if !minTime(time.Unix(1, 0), time.Unix(2, 0)).Equal(time.Unix(1, 0)) || !minTime(time.Unix(3, 0), time.Unix(2, 0)).Equal(time.Unix(2, 0)) {
		t.Fatal("minTime")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("close not idempotent")
	}
	if err := s.authorized(Binding{Owner: "alice", Environment: "dev"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed service: %v", err)
	}
}
