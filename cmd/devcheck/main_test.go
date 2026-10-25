package main

import (
	"os"
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
	called := false
	var gotPkgMin float64
	old := coverAll
	coverAll = func(name string, minimum, packageMinimum float64, listPackages func() ([]string, error), testPackage func(pkg, out string) error) (float64, error) {
		called = true
		gotPkgMin = packageMinimum
		if name != profile || minimum != 85 {
			t.Fatalf("coverAll(%q, %v)", name, minimum)
		}
		return 99, nil
	}
	t.Cleanup(func() { coverAll = old })
	if err := run([]string{"covergo", profile, "85"}); err != nil || !called || gotPkgMin != 0 {
		t.Fatalf("covergo: err=%v called=%v pkgMin=%v", err, called, gotPkgMin)
	}
	if err := run([]string{"covergo", profile, "85", "50"}); err != nil || gotPkgMin != 50 {
		t.Fatalf("covergo pkgmin: err=%v pkgMin=%v", err, gotPkgMin)
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
