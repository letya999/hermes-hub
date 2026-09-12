package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIWorkflow(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "owner")
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"doctor", "--dir", dir}); err == nil {
		t.Fatal("doctor accepted missing model")
	}
	if err := run(ctx, []string{"render", "--dir", dir, "--root", "../.."}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.prod.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"unknown"}, {"init", "--dir", dir}, {"render", "unexpected"}, {"init", "--user", "../other"}} {
		if err := run(ctx, args); err == nil {
			t.Fatal("invalid command accepted", args)
		}
	}
}

func TestCLIOrganizationWorkflow(t *testing.T) {
	root := t.TempDir()
	org := filepath.Join(root, "organizations", "acme")
	space := filepath.Join(root, "spaces", "alice")
	if err := run(context.Background(), []string{"org-init", "--org", "acme", "--org-dir", org, "--user", "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"init", "--dir", space, "--user", "alice", "--org", "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"doctor", "--dir", space}); err == nil {
		t.Fatal("organization doctor accepted missing model")
	}
	if err := run(context.Background(), []string{"org-init", "--org-dir", org}); err == nil {
		t.Fatal("org-init without organization accepted")
	}
}

func TestCLISupervisorLifecycle(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	t.Setenv("HUB_RUNTIME_AUTH", "")
	if err := run(context.Background(), []string{"supervisor", "--spaces", t.TempDir()}); err == nil {
		t.Fatal("supervisor started without control auth")
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"supervisor", "--spaces", root, "--supervisor-auth", "secret", "--supervisor-listen", "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
}
