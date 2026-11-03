package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBoundaryFile publishes a workspace.yaml the admission reads live.
func writeBoundaryFile(t *testing.T, path, writable string) {
	t.Helper()
	body := "apiVersion: six-passport/v2\nkind: workspace\nmetadata: {space: alice, environment: prod, revision: r-1}\n" +
		"writable: [" + writable + "]\n" +
		"read_only: [/config, /skills, /workspace/inbox, /workspace/artifacts/services, /vault/memory]\n"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

// A narrowed published boundary applies to the next file_write in the same
// session: the executor reloads /config/workspace.yaml by mtime, never by
// restart (SPEC-0092 R5.3).
func TestFileWriteDeniedOutsideWritableLive(t *testing.T) {
	dir := t.TempDir()
	ws, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	boundary := filepath.Join(t.TempDir(), "config", "workspace.yaml")
	writeBoundaryFile(t, boundary, "/workspace/work, /workspace/outbox")
	tools := &Tools{Workspace: ws, BoundaryPath: boundary}

	if _, err := tools.File("write", Input{Path: "outbox/first.md", Text: "before"}); err != nil {
		t.Fatalf("write inside declared writable denied: %v", err)
	}
	// The owner applies a new revision removing /workspace/outbox; the file
	// changes on the mounted /config view without any respawn.
	writeBoundaryFile(t, boundary, "/workspace/work")
	if _, err := tools.File("write", Input{Path: "outbox/second.md", Text: "after"}); err == nil {
		t.Fatal("write under a path the new revision removed from writable still succeeded")
	}
	if _, err := tools.File("write", Input{Path: "work/still.md", Text: "ok"}); err != nil {
		t.Fatalf("write inside the surviving writable path denied: %v", err)
	}
	// A deleted boundary fails closed, never open.
	if err := os.Remove(boundary); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.File("write", Input{Path: "work/nope.md", Text: "x"}); err == nil ||
		!strings.Contains(err.Error(), "read-only") && !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("missing boundary must fail closed, got %v", err)
	}
}

// Inbound mail and per-service artifact originals stay read-only even
// though a broad /workspace writable prefix covers them textually —
// the longest matching read_only prefix wins (SPEC-0092 R5.3).
func TestInboxAndServiceArtifactsNeverWritable(t *testing.T) {
	dir := t.TempDir()
	ws, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	boundary := filepath.Join(t.TempDir(), "config", "workspace.yaml")
	writeBoundaryFile(t, boundary, "/workspace, /vault/library, /vault/attachments")
	tools := &Tools{Workspace: ws, BoundaryPath: boundary}

	for _, p := range []string{
		"inbox/people/telegram/2026-11-03/att-1/file.txt",
		"inbox/services/github/2026-11-03/att-2/file.txt",
		"artifacts/services/github/report.pdf",
	} {
		if _, err := tools.File("write", Input{Path: p, Text: "tamper"}); err == nil {
			t.Errorf("write into %s succeeded under a declared read_only prefix", p)
		}
	}
	if _, err := tools.File("write", Input{Path: "artifacts/documents/note.md", Text: "ok"}); err != nil {
		t.Fatalf("writable artifacts subtree denied: %v", err)
	}
}

// /vault/memory is the runtime's read-mostly long-term store: file tools
// never write it — the declared writable vault subtrees stay the only
// writable vault surface (SPEC-0092 R5.3).
func TestVaultMemoryNotWritableByFileTools(t *testing.T) {
	vaultDir := t.TempDir()
	vault, err := os.OpenRoot(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	ws, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	boundary := filepath.Join(t.TempDir(), "config", "workspace.yaml")
	writeBoundaryFile(t, boundary, "/workspace, /vault/library, /vault/attachments")
	tools := &Tools{Workspace: ws, Vault: vault, BoundaryPath: boundary}

	for _, p := range []string{"memory/CORE.md", "memory/daily/2026-11-03.md"} {
		if _, err := tools.File("write", Input{Root: "vault", Path: p, Text: "tamper"}); err == nil {
			t.Errorf("file_write into vault/%s succeeded", p)
		}
	}
	if _, err := tools.File("write", Input{Root: "vault", Path: "library/note.md", Text: "ok"}); err != nil {
		t.Fatalf("writable vault subtree denied: %v", err)
	}
}

// An in-root symlink inside a writable subtree cannot launder a write into
// a read_only subtree: the boundary applies to the resolved target, not
// the literal argument (SPEC-0092 R5.3).
func TestSymlinkFromWritableIntoReadOnlyDenied(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "inbox"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inbox", "original.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	ws, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	boundary := filepath.Join(t.TempDir(), "config", "workspace.yaml")
	writeBoundaryFile(t, boundary, "/workspace")
	tools := &Tools{Workspace: ws, BoundaryPath: boundary}

	if err := os.Symlink(filepath.Join("..", "inbox"), filepath.Join(dir, "work", "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := tools.File("write", Input{Path: "work/alias/original.txt", Text: "tamper"}); err == nil {
		t.Fatal("write through a symlink into a read_only subtree succeeded")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "inbox", "original.txt")); err != nil || string(got) != "original" {
		t.Fatalf("read-only file changed: %v %q", err, got)
	}
}

// A new top-level directory under /workspace/projects is project creation:
// file_write cannot mint it — only the project_create tool, which carries
// the workspace.create_project right (SPEC-0092 R5.3, catalog rights).
func TestProjectCreationRequiresCreateProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0700); err != nil {
		t.Fatal(err)
	}
	ws, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	boundary := filepath.Join(t.TempDir(), "config", "workspace.yaml")
	writeBoundaryFile(t, boundary, "/workspace")
	tools := &Tools{Workspace: ws, BoundaryPath: boundary}

	if _, err := tools.Call(context.Background(), "file_write", Input{Path: "projects/newrepo/README.md", Text: "x"}); err == nil {
		t.Fatal("file_write minted a new project directory without project_create")
	}
	if _, err := tools.Call(context.Background(), "project_create", Input{Name: "newrepo"}); err != nil {
		t.Fatalf("project_create: %v", err)
	}
	if _, err := tools.Call(context.Background(), "file_write", Input{Path: "projects/newrepo/README.md", Text: "x"}); err != nil {
		t.Fatalf("write inside the created project denied: %v", err)
	}
	// The tool must be projected: the catalog carries it with the
	// workspace.create_project right so ToolHub admits it only under grant.
	found := false
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name == "project_create" {
			found = true
			if len(spec.Requires) != 1 || spec.Requires[0] != "workspace.create_project" {
				t.Errorf("project_create requires = %v, want [workspace.create_project]", spec.Requires)
			}
		}
	}
	if !found {
		t.Error("project_create missing from the hub-agent-tools catalog")
	}
}

// project_create mints exactly one clean directory name under projects/:
// traversal, nesting and absolute paths fail; an existing directory is
// idempotent (created=false), and the live boundary still decides whether
// /workspace/projects is writable at all (SPEC-0092 R5.3).
func TestProjectCreateHardening(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0700); err != nil {
		t.Fatal(err)
	}
	ws, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	boundary := filepath.Join(t.TempDir(), "config", "workspace.yaml")
	writeBoundaryFile(t, boundary, "/workspace")
	tools := &Tools{Workspace: ws, BoundaryPath: boundary}

	for _, name := range []string{"..", ".", "a/b", "/abs", `back\slash`, "", "  "} {
		if _, err := tools.Call(context.Background(), "project_create", Input{Name: name}); err == nil {
			t.Errorf("project_create accepted unsafe name %q", name)
		}
	}
	if _, err := tools.Call(context.Background(), "project_create", Input{Name: "demo"}); err != nil {
		t.Fatalf("project_create: %v", err)
	}
	// Idempotent: the second create reports the project already exists.
	res, err := tools.Call(context.Background(), "project_create", Input{Name: "demo"})
	if err != nil {
		t.Fatalf("project_create existing dir: %v", err)
	}
	if m, _ := res.(map[string]any); m["created"] != false {
		t.Errorf("re-create reported created=%v for an existing project", m["created"])
	}
	// The published boundary rules: narrowing writable so /workspace/projects
	// is uncovered denies the next create without a respawn.
	writeBoundaryFile(t, boundary, "/workspace/work")
	if _, err := tools.Call(context.Background(), "project_create", Input{Name: "blocked"}); err == nil {
		t.Error("project_create minted a project outside the narrowed writable boundary")
	}
}

// A space whose workspace predates the projects/ skeleton still admits
// project_create: the parent is minted on first use (live A5: managed
// workspace without projects/ returned "no such file or directory").
func TestProjectCreateMintsMissingParent(t *testing.T) {
	dir := t.TempDir()
	ws, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	boundary := filepath.Join(t.TempDir(), "config", "workspace.yaml")
	writeBoundaryFile(t, boundary, "/workspace")
	tools := &Tools{Workspace: ws, BoundaryPath: boundary}

	res, err := tools.Call(context.Background(), "project_create", Input{Name: "fresh"})
	if err != nil {
		t.Fatalf("project_create without a projects/ parent: %v", err)
	}
	if m, _ := res.(map[string]any); m["created"] != true {
		t.Errorf("created=%v, want true", m["created"])
	}
	if info, err := os.Stat(filepath.Join(dir, "projects", "fresh")); err != nil || !info.IsDir() {
		t.Fatalf("project dir missing: %v", err)
	}
}

// OpenRoots inside a passport-mounted runtime picks the live boundary up
// from HUB_WORKSPACE_BOUNDARY and opens the vault root from HUB_VAULT —
// file_write follows the published file, not a render-time env list
// (SPEC-0092 R5.3).
func TestOpenRootsPicksPassportBoundary(t *testing.T) {
	dir := t.TempDir()
	config := t.TempDir()
	vaultDir := t.TempDir()
	writeBoundaryFile(t, filepath.Join(config, "workspace.yaml"), "/workspace/work")
	t.Setenv("HUB_WORKSPACE_BOUNDARY", filepath.Join(config, "workspace.yaml"))
	t.Setenv("HUB_VAULT", vaultDir)
	t.Setenv("HUB_STATE", t.TempDir())

	tools, err := OpenRoots(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Workspace.Close()
	if tools.Vault == nil {
		t.Error("vault root not opened from HUB_VAULT")
	} else {
		defer tools.Vault.Close()
	}
	if _, err := tools.File("write", Input{Path: "inbox/x.md", Text: "tamper"}); err == nil {
		t.Error("write outside the published writable list succeeded")
	}
	if _, err := tools.File("write", Input{Path: "work/ok.md", Text: "ok"}); err != nil {
		t.Errorf("writable path denied: %v", err)
	}
}
