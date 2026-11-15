package passportview

import (
	"os"
	"path/filepath"
	"testing"
)

// LoadEffectiveGenerated roots the same strict six-doc read at the
// generated/ dir itself — mount views (the cliproxy init companion's
// /run/model-gen) carry no enclosing space dir.
func TestLoadEffectiveGeneratedReadsMountedView(t *testing.T) {
	gen := filepath.Join(t.TempDir(), "generated")
	revDir := filepath.Join(gen, "passports", "r-x")
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: alice, environment: dev, revision: r-x}\n"
	docs := map[string]string{
		"stack.yaml":     head + "kind: stack\nidentity: {user: alice}\n",
		"config.yaml":    head + "kind: config\n",
		"models.yaml":    head + "kind: models\nservices:\n  google-pro-1:\n    via: cliproxy\n    endpoint: http://cliproxy-google-1:8317/v1\n",
		"workspace.yaml": head + "kind: workspace\n",
		"tools.yaml":     head + "kind: tools\n",
		"grants.yaml":    head + "kind: grants\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(revDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gen, "passports", "current"), []byte("r-x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	eff, err := LoadEffectiveGenerated(gen)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Stack.Identity.User != "alice" || eff.Models.Services["google-pro-1"].Via != "cliproxy" {
		t.Fatalf("generated-rooted load lost content: %+v", eff.Models)
	}
	for _, mixed := range []string{
		"apiVersion: six-passport/v2\nmetadata: {space: bob, environment: dev, revision: r-x}\nkind: config\n",
		"apiVersion: six-passport/v2\nmetadata: {space: alice, environment: prod, revision: r-x}\nkind: config\n",
	} {
		if err := os.WriteFile(filepath.Join(revDir, "config.yaml"), []byte(mixed), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadEffectiveGenerated(gen); err == nil {
			t.Fatal("mixed space or environment loaded as one revision")
		}
	}
}

// The memoized slot follows the current pointer: republishing a new
// revision, or mutating a published document, busts the cache so the
// next call re-reads and re-validates instead of serving stale state.
func TestLoadEffectiveGeneratedFollowsRepublishAndMutation(t *testing.T) {
	gen := filepath.Join(t.TempDir(), "generated")
	writeRev := func(rev, user string) {
		revDir := filepath.Join(gen, "passports", rev)
		if err := os.MkdirAll(revDir, 0700); err != nil {
			t.Fatal(err)
		}
		head := "apiVersion: six-passport/v2\nmetadata: {space: alice, environment: dev, revision: " + rev + "}\n"
		docs := map[string]string{
			"stack.yaml":     head + "kind: stack\nidentity: {user: " + user + "}\n",
			"config.yaml":    head + "kind: config\n",
			"models.yaml":    head + "kind: models\n",
			"workspace.yaml": head + "kind: workspace\n",
			"tools.yaml":     head + "kind: tools\n",
			"grants.yaml":    head + "kind: grants\n",
		}
		for name, body := range docs {
			if err := os.WriteFile(filepath.Join(revDir, name), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeRev("r-a", "alice")
	writeRev("r-b", "carol")
	if err := os.WriteFile(filepath.Join(gen, "passports", "current"), []byte("r-a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	eff, err := LoadEffectiveGenerated(gen)
	if err != nil || eff.Stack.Identity.User != "alice" {
		t.Fatalf("first load: %v %+v", err, eff.Stack.Identity)
	}
	if err := os.WriteFile(filepath.Join(gen, "passports", "current"), []byte("r-b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	eff, err = LoadEffectiveGenerated(gen)
	if err != nil || eff.Stack.Identity.User != "carol" {
		t.Fatalf("republish not picked up: %v %+v", err, eff.Stack.Identity)
	}
	if err := os.WriteFile(filepath.Join(gen, "passports", "r-b", "grants.yaml"), []byte("apiVersion: six-passport/v2\nmetadata: {space: alice, environment: dev, revision: r-OTHER}\nkind: grants\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEffectiveGenerated(gen); err == nil {
		t.Fatal("in-place mutation served from cache without re-validation")
	}
}

// A current pointer whose id escapes the passports tree is a poisoned
// publish: both the pointer read and the full load refuse to join it.
func TestCurrentRejectsTraversalRevision(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generated")
	if err := os.MkdirAll(filepath.Join(gen, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gen, "passports", "current"), []byte("../../etc\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Current(dir); err == nil {
		t.Fatal("traversal revision accepted by Current")
	}
	if _, err := LoadEffective(dir); err == nil {
		t.Fatal("traversal revision accepted by LoadEffective")
	}
}
