package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunCoverageAndValidation(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(profile, []byte("mode: set\na:1 1 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"coverage", profile, "85"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"coverage", profile, "bad"}, {"coverage", "missing", "85"}} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %#v", args)
		}
	}
	if err := run([]string{"hermes-contract", "test-image"}); err == nil {
		t.Fatal("integration-only Hermes contract accepted without build tag")
	}
	if err := run([]string{"gateway-lifecycle", "test-image"}); err == nil {
		t.Fatal("integration-only gateway lifecycle accepted without build tag")
	}
}

func TestRunProjectChecks(t *testing.T) {
	old, _ := os.Getwd()
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	if err := run([]string{"format"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"docs"}); err != nil {
		t.Fatal(err)
	}
}
