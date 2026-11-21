package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func captureOutput(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}
	return buf.String()
}

func withStdin(t *testing.T, body []byte, fn func() error) string {
	t.Helper()
	stdinFile := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	out := captureOutput(t, fn)
	os.Stdin = old
	_ = in.Close()
	return out
}

func runToolsExecOnce(t *testing.T, ctx context.Context, request api.AgentExecRequest) api.AgentExecResult {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	out := withStdin(t, body, func() error { return runToolsExec(ctx) })
	var result api.AgentExecResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("executor emitted no bounded JSON result: %q", out)
	}
	return result
}

func TestToolsExecAdmitsScopedCall(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "a.txt"), []byte("scoped"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_WORKSPACE", workspace)

	scopes := []api.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}
	result := runToolsExecOnce(t, ctx, api.AgentExecRequest{
		Tool:      "file_read",
		Arguments: map[string]any{"path": "docs/a.txt"},
		Scopes:    scopes,
	})
	if result.Error != "" {
		t.Fatal(result.Error)
	}
	if body, ok := result.Result.(map[string]any); !ok || body["text"] != "scoped" {
		t.Fatalf("unexpected result: %+v", result.Result)
	}

	// The request is the only authority input: a traversal is denied at the
	// executor's os.Root boundary even though it arrived on the private
	// channel.
	denied := runToolsExecOnce(t, ctx, api.AgentExecRequest{
		Tool:      "file_read",
		Arguments: map[string]any{"path": "../secret.txt"},
		Scopes:    scopes,
	})
	if denied.Error == "" {
		t.Fatalf("traversal reached the workspace: %+v", denied.Result)
	}
	unknown := runToolsExecOnce(t, ctx, api.AgentExecRequest{Tool: "grant_admin", Arguments: map[string]any{}})
	if unknown.Error == "" {
		t.Fatal("unknown tool executed")
	}
}

// tools-daemon serves the same admitted contract over a persistent framed
// channel: scopes still bound the call, replies demultiplex by id, EOF ends
// the process cleanly.
func daemonReplies(t *testing.T, frames []api.AgentExecFrame) map[uint64]api.AgentExecResult {
	t.Helper()
	var input bytes.Buffer
	for _, frame := range frames {
		if err := json.NewEncoder(&input).Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	out := withStdin(t, input.Bytes(), func() error { return runToolsDaemon(context.Background()) })
	replies := map[uint64]api.AgentExecResult{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var reply api.AgentExecReply
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("daemon emitted a non-frame line: %q", line)
		}
		replies[reply.ID] = reply.AgentExecResult
	}
	return replies
}

func TestToolsDaemonServesFramedCalls(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "a.txt"), []byte("scoped"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_WORKSPACE", workspace)
	scopes := []api.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}
	replies := daemonReplies(t, []api.AgentExecFrame{
		{ID: 1, AgentExecRequest: api.AgentExecRequest{Tool: "file_read", Arguments: map[string]any{"path": "docs/a.txt"}, Scopes: scopes}},
		{ID: 2, AgentExecRequest: api.AgentExecRequest{Tool: "file_read", Arguments: map[string]any{"path": "../secret.txt"}, Scopes: scopes}},
		{ID: 3, AgentExecRequest: api.AgentExecRequest{Tool: "file_list", Arguments: map[string]any{"path": "docs"}, Scopes: scopes}},
	})
	if len(replies) != 3 {
		t.Fatalf("expected 3 replies, got %+v", replies)
	}
	if body, ok := replies[1].Result.(map[string]any); !ok || body["text"] != "scoped" {
		t.Fatalf("admitted call lost its result: %+v", replies[1])
	}
	if replies[2].Error == "" {
		t.Fatal("traversal reached the workspace through the daemon")
	}
	if replies[3].Error != "" {
		t.Fatalf("scoped list failed: %+v", replies[3])
	}
}

// A cancel frame for a missing call is a no-op: it must neither crash the
// daemon nor starve the framed calls around it.
func TestToolsDaemonIgnoresUnknownCancel(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "a.txt"), []byte("scoped"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_WORKSPACE", workspace)
	scopes := []api.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}
	replies := daemonReplies(t, []api.AgentExecFrame{
		{ID: 1, AgentExecRequest: api.AgentExecRequest{Tool: "file_read", Arguments: map[string]any{"path": "docs/a.txt"}, Scopes: scopes}},
		{ID: 99, Cancel: true},
		{ID: 2, Cancel: true},
		{ID: 2, AgentExecRequest: api.AgentExecRequest{Tool: "file_read", Arguments: map[string]any{"path": "docs/a.txt"}, Scopes: scopes}},
	})
	if len(replies) != 2 {
		t.Fatalf("cancel frames disturbed real calls: %+v", replies)
	}
	for _, id := range []uint64{1, 2} {
		if body, ok := replies[id].Result.(map[string]any); !ok || body["text"] != "scoped" {
			t.Fatalf("call %d lost its result: %+v", id, replies[id])
		}
	}
}

func TestToolsDaemonRejectsMalformedFrame(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HUB_WORKSPACE", workspace)
	stdinFile := filepath.Join(t.TempDir(), "frames.ndjson")
	if err := os.WriteFile(stdinFile, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	err = runToolsDaemon(context.Background())
	os.Stdin = old
	_ = in.Close()
	if err == nil {
		t.Fatal("malformed frame left the daemon alive")
	}
}

// exec-pack streams exactly the admitted scope: regular files under the
// prefix, never symlinks or outside-root content.
func TestExecPackStreamsScopedInputs(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "sub", "b.txt"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "docs", "escape.txt")
	if err := os.Symlink(filepath.Join(workspace, "secret.txt"), link); err != nil {
		t.Skip("symlinks unavailable")
	}
	t.Setenv("HUB_WORKSPACE", workspace)

	body, _ := json.Marshal(api.ScratchPackRequest{
		Path:   "docs",
		Scopes: []api.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}},
	})
	out := withStdin(t, body, func() error { return runExecPack(ctx) })

	got := map[string]string{}
	reader := tar.NewReader(strings.NewReader(out))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("pack output is not a tar: %v", err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		got[header.Name] = string(body)
	}
	if got["a.txt"] != "alpha" || got["sub/b.txt"] != "beta" || len(got) != 2 {
		t.Fatalf("pack leaked or lost scoped files: %v", got)
	}
	for name := range got {
		if strings.Contains(name, "escape") || strings.Contains(name, "secret") {
			t.Fatalf("pack exported a symlink target: %v", got)
		}
	}
}

// A file-level grant packs exactly the one file; a path outside the admitted
// scope is denied at the os.Root boundary.
func TestExecPackFileBoundaryAndScopeDenial(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "a.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "other.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_WORKSPACE", workspace)
	pack := func(t *testing.T, request api.ScratchPackRequest) (string, error) {
		t.Helper()
		body, _ := json.Marshal(request)
		stdinFile := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
			t.Fatal(err)
		}
		in, err := os.Open(stdinFile)
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdin
		os.Stdin = in
		var runErr error
		out := captureOutput(t, func() error { runErr = runExecPack(ctx); return nil })
		os.Stdin = old
		_ = in.Close()
		return out, runErr
	}
	scope := []api.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs/a.txt"}}
	out, err := pack(t, api.ScratchPackRequest{Path: "docs/a.txt", Scopes: scope})
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(strings.NewReader(out))
	header, err := reader.Next()
	if err != nil || header.Name != "a.txt" {
		t.Fatalf("file-level pack failed: %v %v", header, err)
	}
	// A path outside the scope prefix resolves inside the scope boundary and
	// fails there — never reading the workspace at large.
	if _, err = pack(t, api.ScratchPackRequest{Path: "other.txt", Scopes: scope}); err == nil {
		t.Fatal("out-of-scope path packed")
	}
	// No scopes at all still packs (whole-domain grant).
	out, err = pack(t, api.ScratchPackRequest{Path: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Fatalf("whole-domain pack lost files: %q", out)
	}
}

// Traversals and malformed requests never reach the workspace.
func TestExecPackRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HUB_WORKSPACE", t.TempDir())
	for _, request := range []api.ScratchPackRequest{
		{Path: "../escape"},
		{Path: "/abs"},
		{Path: "a/../b"},
		{Path: "does-not-exist"},
	} {
		body, _ := json.Marshal(request)
		stdinFile := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(stdinFile, body, 0o600); err != nil {
			t.Fatal(err)
		}
		in, err := os.Open(stdinFile)
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdin
		os.Stdin = in
		runErr := runExecPack(ctx)
		os.Stdin = old
		_ = in.Close()
		if runErr == nil {
			t.Fatalf("exec-pack accepted %q", request.Path)
		}
	}
}
