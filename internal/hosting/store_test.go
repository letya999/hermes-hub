package hosting

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (*Service, Config) {
	t.Helper()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "page"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "page", "index.html"), []byte("alice-page"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "page", "app"), []byte("\x7fELF"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Owner: "alice", Environment: "dev", Zone: "alice", Domain: "hub.example.test", Workspace: workspace, Store: filepath.Join(dir, "sites")}
	s, err := Open(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, cfg
}
func request(id string) PublishRequest {
	return PublishRequest{Name: "page", Source: "page", RequestID: id, Access: "private"}
}
func TestStoreCrossOwnerDeniedBeforeIO(t *testing.T) {
	s, _ := fixture(t)
	_, err := s.Publish(context.Background(), Binding{"bob", "dev"}, request("create"))
	if !errors.Is(err, ErrDenied) || len(s.List()) != 0 {
		t.Fatalf("foreign publish: %v", err)
	}
}

func TestStoreSecondWriterAndReplay(t *testing.T) {
	s, cfg := fixture(t)
	if other, err := Open(cfg, Options{}); !errors.Is(err, ErrConflict) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("second writer: %v", err)
	}
	first, err := s.Publish(context.Background(), s.Binding(), request("create"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Publish(context.Background(), s.Binding(), request("create"))
	if err != nil || second.Revision != first.Revision || second.URL != "" {
		t.Fatalf("replay: %#v %v", second, err)
	}
	changed := request("create")
	changed.Source = "other"
	if _, err = s.Publish(context.Background(), s.Binding(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request: %v", err)
	}
	if _, err = s.Publish(context.Background(), s.Binding(), request("stale")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	_ = s.Close()
	restored, err := Open(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	replay, err := restored.Publish(context.Background(), restored.Binding(), request("create"))
	if err != nil || replay.Revision != first.Revision {
		t.Fatalf("restart replay: %#v %v", replay, err)
	}
}
func TestSnapshotRejectsUnsafeFiles(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "page/../page", "page/.env"} {
		t.Run(name, func(t *testing.T) {
			s, _ := fixture(t)
			r := request("bad")
			r.Source = name
			if _, err := s.Publish(context.Background(), s.Binding(), r); !errors.Is(err, ErrInvalid) {
				t.Fatalf("source %q: %v", name, err)
			}
		})
	}
	for _, name := range []string{".env", "credentials.json", "secrets.txt", "private.key"} {
		t.Run(name, func(t *testing.T) {
			s, cfg := fixture(t)
			if err := os.WriteFile(filepath.Join(cfg.Workspace, "page", name), []byte("forbidden"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Publish(context.Background(), s.Binding(), request("hidden")); !errors.Is(err, ErrInvalid) {
				t.Fatalf("hidden file %q: %v", name, err)
			}
		})
	}
}
func TestSnapshotSymlinkDenied(t *testing.T) {
	s, cfg := fixture(t)
	foreign := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(foreign, []byte("bob"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(cfg.Workspace, "page", "link")); err != nil {
		t.Skip("symlink privilege unavailable")
	}
	if _, err := s.Publish(context.Background(), s.Binding(), request("link")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink: %v", err)
	}
}
func TestPublishQuotaFailurePreservesRevision(t *testing.T) {
	s, cfg := fixture(t)
	s.cfg.MaxReleaseBytes = 16
	first, err := s.Publish(context.Background(), s.Binding(), request("first"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "page", "index.html"), []byte("exceeds-sixteen-byte-budget"), 0600); err != nil {
		t.Fatal(err)
	}
	r := request("next")
	r.ExpectedRevision = first.Revision
	if _, err = s.Publish(context.Background(), s.Binding(), r); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota: %v", err)
	}
	if got, _ := s.Status("page"); got.Revision != first.Revision {
		t.Fatalf("lost old revision: %#v", got)
	}
	old, err := s.root.ReadFile(s.state.Sites["page"].Release + "/index.html")
	if err != nil || string(old) != "alice-page" {
		t.Fatalf("old bytes lost: %q %v", old, err)
	}
}
func TestTombstoneSurvivesRestart(t *testing.T) {
	s, cfg := fixture(t)
	first, err := s.Publish(context.Background(), s.Binding(), request("first"))
	if err != nil {
		t.Fatal(err)
	}
	remove := UnpublishRequest{"page", first.Revision, "remove"}
	if err = s.Unpublish(context.Background(), s.Binding(), remove); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	restored, err := Open(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if len(restored.List()) != 0 {
		t.Fatal("removed site resurrected")
	}
	if err = restored.Unpublish(context.Background(), restored.Binding(), remove); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRefusesUnownedDirectoryWithoutDeletingData(t *testing.T) {
	_, cfg := fixture(t)
	foreign := t.TempDir()
	sentinel := filepath.Join(foreign, "other-project.txt")
	if err := os.WriteFile(sentinel, []byte("must remain"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Store = foreign
	if service, err := Open(cfg, Options{}); err != ErrDenied {
		if service != nil {
			_ = service.Close()
		}
		t.Fatalf("unowned store: %v", err)
	}
	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "must remain" {
		t.Fatal("unowned data changed")
	}
	cfg.Store = filepath.Join(cfg.Workspace, "sites")
	if cfg.validate() != ErrInvalid {
		t.Fatal("store may alias workspace")
	}
}
