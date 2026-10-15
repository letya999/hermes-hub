package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// run() fails closed on flag misuse and missing required flags before any
// Docker or network mutation; read-only subcommands answer without a stack.
func TestRunDispatchGuards(t *testing.T) {
	ctx := context.Background()
	if err := run(ctx, nil); err != nil {
		t.Fatal("banner run failed")
	}
	bad := [][]string{
		{"nonsense-command"},
		{"relay"},
		{"relay", "--token-env", "X", "extra"},
		{"mcp-bridge", "--wait", "0"},
		{"mcp-bridge", "extra"},
		{"oauth-relay", "extra"},
		{"oauth-exec-relay", "extra"},
		{"apply"},
		{"apply", "-f", "x.yaml"},
		{"org-init"},
		{"supervisor"},
		{"trace"},
		{"trace", "one", "two"},
		{"doctor", "extra"},
		{"build", "-badflag"},
		{"up", "--no-build", "-bad"},
		{"build", "--no-build"},
		{"up", "--live"},
	}
	for _, args := range bad {
		if err := run(ctx, args); err == nil {
			t.Fatalf("%v: accepted", args)
		}
	}

	root := t.TempDir()
	if err := run(ctx, []string{"migrate-spaces", "--root", root, "--user", "me"}); err != nil {
		t.Fatalf("migrate-spaces dry-run: %v", err)
	}
	emptySpool := filepath.Join(root, "empty-spool")
	for _, sub := range []string{"pending", "running", "done", "failed", "outbox/pending", "outbox/sending", "outbox/failed", "mappings"} {
		if err := os.MkdirAll(filepath.Join(emptySpool, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(ctx, []string{"execution-audit", "--spool", emptySpool, "--user", "me"}); err != nil {
		t.Fatalf("execution-audit on empty spool: %v", err)
	}
	if err := run(ctx, []string{"catalog"}); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if err := run(ctx, []string{"connector"}); err == nil {
		t.Fatal("connector without subcommand accepted")
	}
	if err := run(ctx, []string{"artifact"}); err == nil {
		t.Fatal("artifact without subcommand accepted")
	}
	if err := run(ctx, []string{"secret"}); err == nil {
		t.Fatal("secret without subcommand accepted")
	}
	if err := run(ctx, []string{"context"}); err == nil {
		t.Fatal("context without subcommand accepted")
	}
}
