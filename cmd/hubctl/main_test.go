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
