package migration

import (
	"os"
	"path/filepath"
	"testing"
)

// Run validates identifiers and source safety before moving anything: bad
// inputs fail, dry-run only reports, apply stages into spaces/<id> with a
// scope.yaml and the migrated runtime/workspace state.
func TestRunValidationAndApply(t *testing.T) {
	root := t.TempDir()
	opts := func(mut func(*Options)) Options {
		o := Options{Root: root, User: "alice"}
		if mut != nil {
			mut(&o)
		}
		return o
	}
	if _, err := Run(opts(func(o *Options) { o.Environment = "staging" })); err == nil {
		t.Fatal("bad environment accepted")
	}
	if _, err := Run(opts(func(o *Options) { o.User = "" })); err == nil {
		t.Fatal("empty user accepted")
	}
	if _, err := Run(opts(func(o *Options) { o.User = "Alice!" })); err == nil {
		t.Fatal("invalid user id accepted")
	}
	if _, err := Run(opts(func(o *Options) { o.Organization = "Org" })); err == nil {
		t.Fatal("invalid org id accepted")
	}
	if _, err := Run(opts(func(o *Options) { o.Organization = "alice" })); err == nil {
		t.Fatal("org/user collision accepted")
	}
	if _, err := Run(opts(func(o *Options) { o.StateSource = filepath.Join(root, "missing") })); err == nil {
		t.Fatal("missing state source accepted")
	}
	if _, err := Run(opts(func(o *Options) { o.WorkspaceSource = filepath.Join(root, "missing") })); err == nil {
		t.Fatal("missing workspace source accepted")
	}

	// A live runtime marker refuses migration.
	state := filepath.Join(root, "legacy-state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), []byte(`{"pids":[4242]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(opts(func(o *Options) { o.StateSource = state })); err == nil {
		t.Fatal("active runtime migrated")
	}
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), []byte(`{"pids":[]}`), 0600); err != nil {
		t.Fatal(err)
	}

	// A destination with unrelated content or a foreign scope refuses.
	cluttered := filepath.Join(root, "spaces", "cluttered")
	if err := os.MkdirAll(cluttered, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cluttered, "random.db"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(opts(func(o *Options) { o.User = "cluttered" })); err == nil {
		t.Fatal("cluttered destination accepted")
	}
	foreign := filepath.Join(root, "spaces", "foreign")
	if err := os.MkdirAll(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "scope.yaml"), []byte("kind: user\nid: someone-else\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(opts(func(o *Options) { o.User = "foreign" })); err == nil {
		t.Fatal("foreign scope accepted")
	}

	// Symlinked sources refuse. Skip on platforms without symlink rights.
	link := filepath.Join(root, "linked")
	if err := os.Symlink(state, link); err == nil {
		if _, err := Run(opts(func(o *Options) { o.StateSource = link })); err == nil {
			t.Fatal("symlink source accepted")
		}
	}

	// Dry run inventories but touches nothing.
	userSrc := filepath.Join(root, "legacy-user")
	if err := os.MkdirAll(filepath.Join(userSrc, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userSrc, "docs", "a.txt"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(opts(func(o *Options) { o.UserSource = userSrc; o.StateSource = state }))
	if err != nil || report.Mode != "dry-run" {
		t.Fatalf("dry run: %+v %v", report, err)
	}
	if len(report.Objects) != 2 || report.Objects[0].SHA256 == "" {
		t.Fatalf("objects: %+v", report.Objects)
	}
	if _, err := os.Stat(filepath.Join(root, "spaces", "alice")); !os.IsNotExist(err) {
		t.Fatal("dry run created the destination")
	}

	// Apply moves user home, runtime state and workspace into the scope.
	ws := filepath.Join(root, "legacy-ws")
	if err := os.MkdirAll(filepath.Join(ws, "proj"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "proj", "main.go"), []byte("package x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "hermes", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	orgSrc := filepath.Join(root, "organizations", "acme")
	if err := os.MkdirAll(filepath.Join(orgSrc, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orgSrc, "docs", "org.md"), []byte("org"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = Run(opts(func(o *Options) {
		o.Apply = true
		o.UserSource = userSrc
		o.StateSource = state
		o.WorkspaceSource = ws
		o.Organization = "acme"
	}))
	if err != nil || report.Mode != "applied" || len(report.Destination) != 2 {
		t.Fatalf("apply: %+v %v", report, err)
	}
	dest := filepath.Join(root, "spaces", "alice")
	for _, rel := range []string{"scope.yaml", "docs/a.txt", "workspace/proj/main.go", "hermes/SOUL.md", "hermes/memories", "connections", "archive"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing migrated %s: %v", rel, err)
		}
	}
	orgDest := filepath.Join(root, "spaces", "acme")
	if _, err := os.Stat(filepath.Join(orgDest, "scope.yaml")); err != nil {
		t.Fatalf("org scope missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(orgDest, "docs", "org.md")); err != nil {
		t.Fatalf("org docs missing: %v", err)
	}
}
