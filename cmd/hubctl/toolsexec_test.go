package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

func runToolsExecOnce(t *testing.T, ctx context.Context, request toolhub.AgentExecRequest) toolhub.AgentExecResult {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
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
	out := captureOutput(t, func() error { return run(ctx, []string{"tools-exec"}) })
	os.Stdin = old
	_ = in.Close()
	var result toolhub.AgentExecResult
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

	scopes := []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}
	result := runToolsExecOnce(t, ctx, toolhub.AgentExecRequest{
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
	denied := runToolsExecOnce(t, ctx, toolhub.AgentExecRequest{
		Tool:      "file_read",
		Arguments: map[string]any{"path": "../secret.txt"},
		Scopes:    scopes,
	})
	if denied.Error == "" {
		t.Fatalf("traversal reached the workspace: %+v", denied.Result)
	}
	unknown := runToolsExecOnce(t, ctx, toolhub.AgentExecRequest{Tool: "grant_admin", Arguments: map[string]any{}})
	if unknown.Error == "" {
		t.Fatal("unknown tool executed")
	}
}

// tools-daemon serves the same admitted contract over a persistent framed
// channel: scopes still bound the call, replies demultiplex by id, EOF ends
// the process cleanly.
func TestToolsDaemonServesFramedCalls(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "a.txt"), []byte("scoped"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_WORKSPACE", workspace)
	scopes := []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}
	frames := []toolhub.AgentExecFrame{
		{ID: 1, AgentExecRequest: toolhub.AgentExecRequest{Tool: "file_read", Arguments: map[string]any{"path": "docs/a.txt"}, Scopes: scopes}},
		{ID: 2, AgentExecRequest: toolhub.AgentExecRequest{Tool: "file_read", Arguments: map[string]any{"path": "../secret.txt"}, Scopes: scopes}},
		{ID: 3, AgentExecRequest: toolhub.AgentExecRequest{Tool: "file_list", Arguments: map[string]any{"path": "docs"}, Scopes: scopes}},
	}
	var input bytes.Buffer
	for _, frame := range frames {
		if err := json.NewEncoder(&input).Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	stdinFile := filepath.Join(t.TempDir(), "frames.ndjson")
	if err := os.WriteFile(stdinFile, input.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	out := captureOutput(t, func() error { return run(ctx, []string{"tools-daemon"}) })
	os.Stdin = old
	_ = in.Close()
	replies := map[uint64]toolhub.AgentExecResult{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var reply toolhub.AgentExecReply
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("daemon emitted a non-frame line: %q", line)
		}
		replies[reply.ID] = reply.AgentExecResult
	}
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
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "a.txt"), []byte("scoped"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_WORKSPACE", workspace)
	scopes := []toolhub.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}
	frames := []toolhub.AgentExecFrame{
		{ID: 1, AgentExecRequest: toolhub.AgentExecRequest{Tool: "file_read", Arguments: map[string]any{"path": "docs/a.txt"}, Scopes: scopes}},
		{ID: 99, Cancel: true},
		{ID: 2, Cancel: true},
		{ID: 2, AgentExecRequest: toolhub.AgentExecRequest{Tool: "file_read", Arguments: map[string]any{"path": "docs/a.txt"}, Scopes: scopes}},
	}
	var input bytes.Buffer
	for _, frame := range frames {
		if err := json.NewEncoder(&input).Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	stdinFile := filepath.Join(t.TempDir(), "frames.ndjson")
	if err := os.WriteFile(stdinFile, input.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	out := captureOutput(t, func() error { return run(ctx, []string{"tools-daemon"}) })
	os.Stdin = old
	_ = in.Close()
	replies := map[uint64]toolhub.AgentExecResult{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var reply toolhub.AgentExecReply
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("daemon emitted a non-frame line: %q", line)
		}
		replies[reply.ID] = reply.AgentExecResult
	}
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
	err = run(context.Background(), []string{"tools-daemon"})
	os.Stdin = old
	_ = in.Close()
	if err == nil {
		t.Fatal("malformed frame left the daemon alive")
	}
}
