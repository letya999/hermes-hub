package main

import (
	"context"
	"github.com/letya999/hermes-hub/internal/stack"
	"os"
	"path/filepath"
	"testing"
)

func TestGroupInitCreatesFreshIsolatedScope(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "team")
	args := []string{"group-init", "--dir", dir, "--user", "team", "--env", "dev"}
	if err := run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	scope, err := stack.ReadScope(dir)
	if err != nil || scope.Kind != stack.GroupScope || scope.ID != "team" {
		t.Fatalf("scope: %v %v", scope, err)
	}
	marker := filepath.Join(dir, "hermes", "memories", "private.md")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), args); err == nil {
		t.Fatal("group init overwrote an existing home")
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "keep" {
		t.Fatal("existing memory changed")
	}
	personal := filepath.Join(t.TempDir(), "alice")
	if err := stack.InitEnvironment(personal, "alice", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"group-init", "--dir", personal, "--user", "alice", "--env", "dev"}); err == nil {
		t.Fatal("personal home silently converted to group")
	}
}
