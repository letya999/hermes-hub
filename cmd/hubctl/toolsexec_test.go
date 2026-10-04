package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
