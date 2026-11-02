package passport_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
)

// publishNormative stages the six normative fixtures as one revision.
func publishNormative(t *testing.T, dir, rev string) string {
	t.Helper()
	revDir := filepath.Join(dir, "generated", "passports", rev)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range passport.Names {
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), normative(t, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return revDir
}

func TestLoadEffectiveRejectsMixedRevision(t *testing.T) {
	dir := t.TempDir()
	revDir := publishNormative(t, dir, "r-0123456789abcdef")
	// One passport carries a different revision id — refuse the whole set.
	mixed := []byte("apiVersion: six-passport/v2\nkind: grants\nmetadata: {space: alice, environment: dev, revision: r-deadbeef, generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\nallow: {}\n")
	if err := os.WriteFile(filepath.Join(revDir, "grants.yaml"), mixed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.LoadEffective(dir); err == nil {
		t.Fatal("mixed-revision set loaded")
	}
}

func TestLoadEffectiveRejectsMissingPassport(t *testing.T) {
	dir := t.TempDir()
	revDir := publishNormative(t, dir, "r-0123456789abcdef")
	if err := os.Remove(filepath.Join(revDir, "models.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.LoadEffective(dir); err == nil {
		t.Fatal("incomplete revision loaded")
	}

	// A space with no published revision at all fails too — no legacy fallback.
	empty := t.TempDir()
	if _, err := passport.LoadEffective(empty); err == nil {
		t.Fatal("space without a revision loaded")
	}
}

func TestLoadEffectiveReturnsTypedBodies(t *testing.T) {
	dir := t.TempDir()
	publishNormative(t, dir, "r-0123456789abcdef")
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	eff, err := passport.LoadEffective(dir)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Header.Metadata.Revision != "r-0123456789abcdef" || eff.Stack.Identity.User != "alice" {
		t.Fatalf("effective header/stack: %+v", eff.Header)
	}
	if eff.Models.Default != "everyday" || eff.Tools.Core["files"].Via != "toolhub" || eff.Grants.Allow["models"][2] != "switch" {
		t.Fatalf("effective bodies lost content")
	}
	if len(eff.Workspace.Writable) != 9 || eff.Config.Ports["oauth-callback"] != 8008 {
		t.Fatalf("effective workspace/config lost content")
	}
}
