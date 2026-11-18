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
)

func storeFixture(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	return Config{
		Owner: "alice", Environment: "dev", Zone: ZoneForOwner("alice"),
		Domain: "sites.example.com", Workspace: ws, Store: filepath.Join(root, "store"),
	}
}

func stateFixture(cfg Config) diskState {
	return diskState{
		Schema: 1, Binding: Binding{Owner: cfg.Owner, Environment: cfg.Environment},
		Sites: map[string]record{}, Operations: map[string]operation{},
		Pending: map[string]record{}, Retired: map[string]record{}, Sequences: map[string]uint64{},
	}
}

func siteRecord(cfg Config, name string) record {
	return record{
		Release: strings.Repeat("ab", 32),
		Site: Site{
			Name: name, Revision: 1, State: "ready", Mode: "static", Access: "public",
			Origin: cfg.origin(name), ExpiresAt: time.Now().Add(time.Hour),
		},
	}
}

func writeState(t *testing.T, cfg Config, st diskState) {
	t.Helper()
	body, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Store, "state.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func openOnce(t *testing.T, cfg Config, opts Options) *Service {
	t.Helper()
	s, err := Open(cfg, opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenRejectsFileStoreAndLockConflict(t *testing.T) {
	cfg := storeFixture(t)
	// A regular file at the store path is not a usable store.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	bad := cfg
	bad.Store = file
	if _, err := Open(bad, Options{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("file store: %v", err)
	}
	// A symlinked store must not be followed into another directory.
	link := filepath.Join(t.TempDir(), "store-link")
	if err := os.Symlink(t.TempDir(), link); err == nil {
		bad.Store = link
		if _, err := Open(bad, Options{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("symlink store: %v", err)
		}
	}
	first := openOnce(t, cfg, Options{})
	// A second writer on the same store must conflict, not share.
	if _, err := Open(cfg, Options{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second writer: %v", err)
	}
	_ = first
}

// A corrupted state file must never silently reset the store: every malformed
// record variant fails closed at Open instead of being trusted.
func TestLoadRejectsCorruptState(t *testing.T) {
	cfg := storeFixture(t)
	seed := openOnce(t, cfg, Options{})
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	valid := siteRecord(cfg, "blog")
	cases := map[string]func(st *diskState){
		"wrong schema":   func(st *diskState) { st.Schema = 2 },
		"wrong binding":  func(st *diskState) { st.Binding.Owner = "mallory" },
		"nil sites":      func(st *diskState) { st.Sites = nil },
		"nil operations": func(st *diskState) { st.Operations = nil },
		"site name mismatch": func(st *diskState) {
			r := valid
			r.Name = "other"
			st.Sites["blog"] = r
		},
		"site zero revision": func(st *diskState) {
			r := valid
			r.Revision = 0
			st.Sites["blog"] = r
		},
		"site bad release": func(st *diskState) {
			r := valid
			r.Release = "nothex"
			st.Sites["blog"] = r
		},
		"site negative bytes": func(st *diskState) {
			r := valid
			r.Bytes = -1
			st.Sites["blog"] = r
		},
		"site negative files": func(st *diskState) {
			r := valid
			r.Files = -1
			st.Sites["blog"] = r
		},
		"site unknown state": func(st *diskState) {
			r := valid
			r.State = "limbo"
			st.Sites["blog"] = r
		},
		"site foreign origin": func(st *diskState) {
			r := valid
			r.Origin = "https://blog.evil.example.com"
			st.Sites["blog"] = r
		},
		"site unknown access": func(st *diskState) {
			r := valid
			r.Access = "weird"
			st.Sites["blog"] = r
		},
		"site unknown mode": func(st *diskState) {
			r := valid
			r.Mode = "weird"
			st.Sites["blog"] = r
		},
		"pending key mismatch": func(st *diskState) {
			r := valid
			r.Release = strings.Repeat("cd", 32)
			st.Pending[strings.Repeat("ef", 32)] = r
		},
		"pending bad release": func(st *diskState) {
			r := valid
			st.Pending["short"] = r
		},
		"pending bad name": func(st *diskState) {
			r := valid
			r.Name = "BAD!"
			r.Release = strings.Repeat("cd", 32)
			st.Pending[strings.Repeat("cd", 32)] = r
		},
		"pending zero revision": func(st *diskState) {
			r := valid
			r.Revision = 0
			r.Release = strings.Repeat("cd", 32)
			st.Pending[strings.Repeat("cd", 32)] = r
		},
		"retired key mismatch": func(st *diskState) {
			r := valid
			r.Release = strings.Repeat("cd", 32)
			st.Retired[strings.Repeat("ef", 32)] = r
		},
		"sequence bad name": func(st *diskState) {
			st.Sequences["BAD!"] = 1
		},
		"sequence zero": func(st *diskState) {
			st.Sequences["blog"] = 0
		},
	}
	for name, mutate := range cases {
		st := stateFixture(cfg)
		mutate(&st)
		writeState(t, cfg, st)
		if _, err := Open(cfg, Options{}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: corrupt state must fail closed, got %v", name, err)
		}
	}
	// Raw non-JSON content fails the same way.
	if err := os.WriteFile(filepath.Join(cfg.Store, "state.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(cfg, Options{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("truncated state must fail closed")
	}
	// Control: a valid state reopens cleanly.
	writeState(t, cfg, stateFixture(cfg))
	openOnce(t, cfg, Options{})
}

func TestRecoverMarksStaleOperationsAndExpiresSites(t *testing.T) {
	cfg := storeFixture(t)
	seed := openOnce(t, cfg, Options{})
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	st := stateFixture(cfg)
	st.Operations["req-1"] = operation{Digest: "x", Result: Site{Name: "blog", State: "pending"}}
	expired := siteRecord(cfg, "old-site")
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	expired.TokenHash = "tok"
	st.Sites["old-site"] = expired
	st.Sites["blog"] = siteRecord(cfg, "blog")
	writeState(t, cfg, st)

	s := openOnce(t, cfg, Options{})
	if got := s.state.Operations["req-1"].Result.State; got != "failed" {
		t.Fatalf("interrupted operation must be marked failed, got %q", got)
	}
	if got := s.state.Sites["old-site"].State; got != "removed" {
		t.Fatalf("expired site must be retired, got %q", got)
	}
	if s.state.Sites["old-site"].TokenHash != "" {
		t.Fatal("expired site must drop its token")
	}
	if got := s.List(); len(got) != 1 || got[0].Name != "blog" {
		t.Fatalf("list must show only the live site: %v", got)
	}
	if _, err := s.Status("old-site"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired site status: %v", err)
	}
	site, err := s.Status("blog")
	if err != nil || site.Name != "blog" {
		t.Fatalf("live site status: %v %v", site, err)
	}
}

// Orphan release directories are collected on recovery; a directory that does
// not even look like a release fails closed rather than being deleted blindly.
func TestRecoverRemovesOrphanReleases(t *testing.T) {
	cfg := storeFixture(t)
	seed := openOnce(t, cfg, Options{})
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(cfg.Store, strings.Repeat("de", 32))
	if err := os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	s := openOnce(t, cfg, Options{})
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan release dir must be removed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(cfg.Store, "foreign-junk")
	if err := os.MkdirAll(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(cfg, Options{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("foreign directory must fail closed, got %v", err)
	}
}

func TestRecoverDynamicSiteRestartAndRetire(t *testing.T) {
	cfg := storeFixture(t)
	seed := openOnce(t, cfg, Options{})
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	// A ready dynamic site with a dead workload is restarted (bounded by a
	// restart counter), while a pending static release is retired.
	dynamic := siteRecord(cfg, "app")
	dynamic.Mode = "dynamic"
	dynamic.AdmissionDigest = cfg.AdmissionDigest
	dynamic.Release = strings.Repeat("ab", 32)
	pending := siteRecord(cfg, "pending-site")
	pending.Release = strings.Repeat("cd", 32)
	st := stateFixture(cfg)
	st.Sites["app"] = dynamic
	st.Pending[pending.Release] = pending
	writeState(t, cfg, st)

	executor := &executorFake{} // running=false → Check fails → restart path
	s := openOnce(t, cfg, Options{Executor: executor})
	if len(s.state.Pending) != 0 {
		t.Fatalf("static pending release must be retired: %v", s.state.Pending)
	}
	got := s.state.Sites["app"]
	if got.Restarts != 1 || got.State != "ready" {
		t.Fatalf("dead workload must restart once, got %+v", got.Site)
	}
	if executor.starts != 1 {
		t.Fatalf("recovery must re-ensure the workload: %d", executor.starts)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Without an executor a ready dynamic site cannot be recovered at all.
	writeState(t, cfg, st)
	if _, err := Open(cfg, Options{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dynamic site without executor must fail closed, got %v", err)
	}
}

func TestReconcileAndReplayBoundaries(t *testing.T) {
	cfg := storeFixture(t)
	s := openOnce(t, cfg, Options{})
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile on a healthy store: %v", err)
	}
	// replay: unknown key is a miss; known key with a different digest is a
	// conflict; matching digest returns the stored result.
	if _, hit, err := s.replay("!!bad-key!!", "in"); !errors.Is(err, ErrInvalid) || hit {
		t.Fatalf("invalid key: hit=%v err=%v", hit, err)
	}
	if _, hit, err := s.replay("req-new", "in"); err != nil || hit {
		t.Fatalf("unknown key: hit=%v err=%v", hit, err)
	}
	s.state.Operations["req-x"] = operation{Digest: digest("input"), Result: Site{Name: "blog", State: "ready"}}
	if _, hit, err := s.replay("req-x", "different"); !errors.Is(err, ErrConflict) || !hit {
		t.Fatalf("digest mismatch: hit=%v err=%v", hit, err)
	}
	site, hit, err := s.replay("req-x", "input")
	if err != nil || !hit || site.Name != "blog" {
		t.Fatalf("idempotent replay: site=%+v hit=%v err=%v", site, hit, err)
	}
	// A faulted or closed service denies everything.
	s.storageFault = true
	if err := s.Reconcile(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("faulted service reconcile: %v", err)
	}
	s.storageFault = false
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed service reconcile: %v", err)
	}
}
