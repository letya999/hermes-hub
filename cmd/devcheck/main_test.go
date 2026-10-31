package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGosecScansOnlyModulePackages(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	name, script := "go", "#!/bin/sh\nprintf '%s|%s|GOWORK=%s\\n' \"$PWD\" \"$*\" \"$GOWORK\" >> \"$GOSEC_TEST_LOG\"\n"
	if runtime.GOOS == "windows" {
		name, script = "go.cmd", "@echo off\r\n>>\"%GOSEC_TEST_LOG%\" echo %CD%^|%*^|GOWORK=%GOWORK%\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOSEC_TEST_LOG", log)
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	old, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := run([]string{"gosec"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	if strings.Count(calls, "GOWORK=off") != 2 {
		t.Error("gosec must resolve each module outside the parent workspace")
	}
	if strings.Count(calls, "gosec@v2.29.1-0.20261022115301-1cbfd7e6488c") != 2 || !strings.Contains(calls, "./cmd/... ./internal/...") || !strings.Contains(calls, filepath.Join(root, "services", "credential-broker")) {
		t.Fatalf("wrong gosec package scope: %s", calls)
	}
}

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
	lowProfile := filepath.Join(t.TempDir(), "low.out")
	if err := os.WriteFile(lowProfile, []byte("mode: atomic\na/x.go:1.1,2.1 96 1\ngithub.com/letya999/hermes-hub/internal/pa/x.go:1.1,2.1 4 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	old := coverTest
	coverTest = func(name string) *exec.Cmd {
		called = true
		if name != profile && name != lowProfile {
			t.Fatalf("coverTest(%q)", name)
		}
		return exec.Command("go", "version")
	}
	t.Cleanup(func() { coverTest = old })
	if err := run([]string{"covergo", profile, "85"}); err != nil || !called {
		t.Fatalf("covergo: err=%v called=%v", err, called)
	}
	// The package floor runs against the real gate on the merged profile now:
	// global 96% passes, but the internal package at 0% trips the 50 floor.
	if err := run([]string{"covergo", lowProfile, "85", "50"}); err == nil {
		t.Fatal("covergo accepted an internal package below the floor")
	}
	if err := run([]string{"covergo", profile, "85", "bad"}); err == nil {
		t.Fatal("covergo accepted invalid package minimum")
	}
	if err := run([]string{"covergo", profile, "bad"}); err == nil {
		t.Fatal("covergo accepted invalid minimum")
	}
	if err := run([]string{"hermes-contract", "test-image"}); err == nil {
		t.Fatal("integration-only Hermes contract accepted without build tag")
	}
	if err := run([]string{"hermes-capability-contract", "test-image"}); err == nil {
		t.Fatal("integration-only capability contract accepted without build tag")
	}
	if err := run([]string{"tts-cache-contract", "test-image"}); err == nil {
		t.Fatal("integration-only TTS contract accepted without build tag")
	}
	if err := run([]string{"gateway-lifecycle", "test-image"}); err == nil {
		t.Fatal("integration-only gateway lifecycle accepted without build tag")
	}
	// image-budget must reach the fail-closed gate (missing image is an error),
	// not fall through to the usage message.
	if err := run([]string{"image-budget", "hermes-hub:nope-missing", "core"}); err == nil || strings.Contains(err.Error(), "usage:") {
		t.Fatalf("image-budget did not reach the gate: %v", err)
	}
}

// The coverage gate must run the suite batched, like a CI shard: one go test
// invocation over ./... so package binaries parallelize across GOMAXPROCS and
// profiles merge natively. A per-package loop re-links the same
// -coverpkg=./... binary per package (49 serial instrumented runs locally).
func TestCoverTestBatchesWholeSuite(t *testing.T) {
	var cmd *exec.Cmd = coverTest("profile.out")
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"-race", "-shuffle=on", "-count=1", "-timeout=10m", "-coverpkg=./...", "-coverprofile=profile.out"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("coverTest missing %q: %v", want, cmd.Args)
		}
	}
	if cmd.Args[len(cmd.Args)-1] != "./..." {
		t.Fatalf("coverTest must run the whole suite in one invocation, got %v", cmd.Args)
	}
}

func TestRunConnectorVerbs(t *testing.T) {
	t.Setenv("HUB_REGISTRY_REPO", "o/r")
	if err := run([]string{"connector-ref", "playwright-mcp"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"connector-ref", "ghost"}); err == nil {
		t.Fatal("connector-ref accepted unknown entry")
	}
	if err := run([]string{"connector-build", "ghost", t.TempDir()}); err == nil {
		t.Fatal("connector-build accepted unknown entry")
	}
	dir := t.TempDir()
	pin := filepath.Join(dir, "pin.json")
	if err := os.WriteFile(pin, []byte(`{"id":"x","digest":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"connector-pin", "playwright-mcp", pin, "ghcr.io/o/r/x:t", filepath.Join(dir, "catalog.json")}); err == nil {
		t.Fatal("connector-pin accepted malformed pin file")
	}
	if err := run([]string{"connector-pin", "only", "four"}); err == nil {
		t.Fatal("connector-pin accepted missing args")
	}
	// Without the integration tag the verb must route to the stub's explicit
	// refusal, not fall through to the usage error.
	if err := run([]string{"connector-bench", "10"}); err == nil || strings.Contains(err.Error(), "usage:") {
		t.Fatalf("integration-only connector bench: %v", err)
	}
	if err := run([]string{"connector-bench", "bad"}); err == nil || strings.Contains(err.Error(), "usage:") {
		t.Fatal("connector-bench accepted non-numeric runs")
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
