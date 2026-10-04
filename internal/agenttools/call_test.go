package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

func write(t *testing.T, v *Tools, path, text string) {
	t.Helper()
	if _, err := v.File("write", Input{Path: path, Text: text}); err != nil {
		t.Fatal(err)
	}
}

func execRead(t *testing.T, v *Tools, path string, scopes []toolhub.CapabilityScope) (any, error) {
	t.Helper()
	return v.ExecCall(context.Background(), "file_read", map[string]any{"path": path}, scopes)
}

func TestExecCallScopeBoundaries(t *testing.T) {
	v := fixture(t)
	write(t, v, "docs/a.txt", "scoped body")
	write(t, v, "secret.txt", "outside")
	scoped := []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}

	out, err := execRead(t, v, "docs/a.txt", scoped)
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["text"] != "scoped body" {
		t.Fatal(out)
	}
	// The sub-root sees only the scope: a path that would escape it is denied
	// mechanically even though admission would have denied it upstream.
	if _, err := execRead(t, v, "docs/../secret.txt", scoped); err == nil {
		t.Fatal("traversal inside a scope escaped the sub-root")
	}
	if _, err := execRead(t, v, "../secret.txt", scoped); err == nil {
		t.Fatal("path outside the admitted scope resolved")
	}

	// File-level grant: scope is the file itself; only that file resolves.
	fileScope := []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs/a.txt"}}
	if _, err := execRead(t, v, "docs/a.txt", fileScope); err != nil {
		t.Fatal(err)
	}
	if _, err := execRead(t, v, "docs/other.txt", fileScope); err == nil {
		t.Fatal("file-level grant admitted a sibling")
	}

	// Whole-domain grant (empty prefix) keeps the original roots.
	if _, err := execRead(t, v, "secret.txt", []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path"}}); err != nil {
		t.Fatal(err)
	}
	// No scopes at all: executor behavior is identical to the direct path.
	if _, err := execRead(t, v, "secret.txt", nil); err != nil {
		t.Fatal(err)
	}

	// A scope naming an argument the executor cannot map is rejected, never
	// silently ignored.
	if _, err := execRead(t, v, "docs/a.txt", []toolhub.CapabilityScope{{Resource: "files", PathArgument: "query", PathPrefix: "docs"}}); err == nil {
		t.Fatal("scope on an unmapped argument was ignored")
	}
	if _, err := execRead(t, v, "docs/a.txt", []toolhub.CapabilityScope{{Resource: "no-such-domain", PathArgument: "path", PathPrefix: "docs"}}); err == nil {
		t.Fatal("unknown scope resource was ignored")
	}
}

// T14: the sub-root is an os.Root boundary, not a string filter. A symlink
// planted inside the scope cannot smuggle an outside path into the call,
// and a scope directory swapped for a symlink fails closed on open.
func TestExecCallSymlinkBoundaries(t *testing.T) {
	v := fixture(t)
	write(t, v, "docs/a.txt", "scoped body")
	write(t, v, "secret.txt", "outside")
	ws := v.Workspace.Name()
	mklink := func(oldname, newname string) bool {
		if err := os.Symlink(oldname, newname); err != nil {
			return false
		}
		return true
	}
	scoped := []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}
	if !mklink(filepath.Join(ws, "secret.txt"), filepath.Join(ws, "docs", "link.txt")) {
		t.Skip("symlink creation requires privileges on this platform")
	}
	if _, err := execRead(t, v, "docs/link.txt", scoped); err == nil {
		t.Fatal("symlink inside the scope reached outside content")
	}
	// A whole-domain call skips the symlink too: reads refuse, listings hide
	// it rather than exposing a different-file inode under a scoped name.
	if _, err := execRead(t, v, "docs/link.txt", nil); err == nil {
		t.Fatal("unscoped read followed a symlink")
	}
	listed, err := v.ExecCall(context.Background(), "file_list", map[string]any{"path": "docs"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed.(map[string]any)["items"].([]map[string]any) {
		if item["name"] == "link.txt" {
			t.Fatal("symlinked entry listed as regular file")
		}
	}
	// Swap the scoped directory itself for a symlink: opening the scope now
	// fails closed instead of resolving the outside target.
	if err := os.RemoveAll(filepath.Join(ws, "docs")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), []byte("escaped"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !mklink(outside, filepath.Join(ws, "docs")) {
		t.Skip("symlink swap requires privileges")
	}
	if _, err := execRead(t, v, "docs/a.txt", scoped); err == nil {
		t.Fatal("swapped scope resolved a symlinked directory")
	}
	if _, err := execRead(t, v, "docs/a.txt", nil); err == nil {
		t.Fatal("unscoped read resolved a symlinked directory out of the root")
	}
}

// scopeBase resolves the resource domain an admitted boundary narrows:
// files follows the call's root argument; archive and organization scopes
// land on their own roots; a missing domain fails closed.
func TestExecCallScopeBaseResources(t *testing.T) {
	archive := t.TempDir()
	org := t.TempDir()
	if err := os.WriteFile(filepath.Join(archive, "arc.txt"), []byte("archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(org, "MEMORY.md"), []byte("org fact"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := OpenRoots(t.TempDir(), archive, org)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	read := func(args map[string]any, scopes []toolhub.CapabilityScope) (any, error) {
		return v.ExecCall(context.Background(), "file_read", args, scopes)
	}
	out, err := read(map[string]any{"root": "archive", "path": "arc.txt"}, []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "arc.txt"}})
	if err != nil || out.(map[string]any)["text"] != "archived" {
		t.Fatalf("archive-scoped read: %v %v", out, err)
	}
	if _, err := read(map[string]any{"root": "archive", "path": "other.txt"}, []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "arc.txt"}}); err == nil {
		t.Fatal("archive file-boundary admitted a sibling")
	}
	out, err = read(map[string]any{"root": "organization", "path": "MEMORY.md"}, []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "MEMORY.md"}})
	if err != nil || out.(map[string]any)["text"] != "org fact" {
		t.Fatalf("organization-scoped read: %v %v", out, err)
	}
	// Direct archive/organization resources narrow the workspace root to
	// their own boundary.
	out, err = read(map[string]any{"path": "arc.txt"}, []toolhub.CapabilityScope{{Resource: "archive", PathArgument: "path", PathPrefix: "arc.txt"}})
	if err != nil || out.(map[string]any)["text"] != "archived" {
		t.Fatalf("archive-resource scope: %v %v", out, err)
	}
	if _, err := read(map[string]any{"root": "bogus", "path": "arc.txt"}, []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "arc.txt"}}); err == nil {
		t.Fatal("unknown root ignored")
	}
	// Without an organization root the organization scope fails closed.
	narrow, err := OpenRoots(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer narrow.Close()
	if _, err := narrow.ExecCall(context.Background(), "file_read", map[string]any{"path": "x"}, []toolhub.CapabilityScope{{Resource: "organization", PathArgument: "path", PathPrefix: "x"}}); err == nil {
		t.Fatal("organization scope applied without an organization root")
	}
}

func TestExecCallDispatch(t *testing.T) {
	v := fixture(t)
	if _, err := v.ExecCall(context.Background(), "no_such_tool", map[string]any{}, nil); err == nil {
		t.Fatal("unknown tool dispatched")
	}
	// Non-filesystem capability: dispatch works with an empty scope set.
	if _, err := v.ExecCall(context.Background(), "service_catalog", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	// Malformed argument types are rejected before dispatch.
	if _, err := v.ExecCall(context.Background(), "file_read", map[string]any{"path": 42}, nil); err == nil {
		t.Fatal("non-string path argument reached dispatch")
	}
}
