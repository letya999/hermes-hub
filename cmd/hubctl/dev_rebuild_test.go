package main

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/stack"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDevRebuildServiceBinaryMap(t *testing.T) {
	for service, binary := range map[string]string{
		"communication-hub":   "communication-hub",
		"credential-broker":   "credential-broker",
		"hub-media":           "hub-media",
		"toolhub":             "toolhub",
		"workload-controller": "hubctl",
		"supervisor":          "hubctl",
		"hermes-runtime":      "hub-runtime",
	} {
		spec, ok := devService(service)
		if !ok || spec.binary != binary {
			t.Fatalf("%s: %+v, %v", service, spec, ok)
		}
	}
}

func TestDevRebuildRejectsProdAndUnsupportedBeforeDocker(t *testing.T) {
	for _, args := range [][]string{
		{"--env", "prod", "communication-hub"},
		{"--env", "dev", "cliproxy"},
		{"--env", "dev", "../communication-hub"},
	} {
		if err := runDevRebuild(context.Background(), args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestDevRebuildCompilesAndStartsOnlySelectedService(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "space")
	if err := stack.InitEnvironment(dir, "owner", "dev"); err != nil {
		t.Fatal(err)
	}
	s, err := stack.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Model, s.ModelURL = "test", "http://model.invalid/v1"
	s.Ingress = append(s.Ingress, "telegram")
	if err := stack.WriteSpace(dir, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=fake-for-test\nTELEGRAM_BOT_TOKEN=synthetic-bot\nTELEGRAM_ALLOWED_USERS=11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	dockerName, goName := "docker", "go"
	dockerScript := "#!/bin/sh\nprintf 'docker %s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\n[ \"$HUB_FAIL_DOCKER\" = 1 ] && [ \"$1\" = build ] && exit 9\n[ \"$HUB_FAIL_UP\" = 1 ] && [ \"$1\" = compose ] && echo \"$*\" | grep -q ' up ' && exit 9\ncase \"$*\" in *Architecture*) echo amd64;; *.Id*) echo sha256:old;; esac\n"
	goScript := "#!/bin/sh\nprintf 'go %s GOOS=%s GOARCH=%s CGO_ENABLED=%s\\n' \"$*\" \"$GOOS\" \"$GOARCH\" \"$CGO_ENABLED\" >> \"$HUB_TEST_LOG\"\n[ \"$HUB_FAIL_GO\" = 1 ] && exit 9\nexit 0\n"
	if runtime.GOOS == "windows" {
		dockerName, goName = "docker.cmd", "go.cmd"
		dockerScript = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo docker %*\r\nif \"%HUB_FAIL_DOCKER%\"==\"1\" if \"%~1\"==\"build\" exit /b 9\r\nif \"%HUB_FAIL_UP%\"==\"1\" echo %* | findstr /C:\" up \" >nul && exit /b 9\r\necho %* | findstr /C:\"Architecture\" >nul && echo amd64\r\necho %* | findstr /C:\".Id\" >nul && echo sha256:old\r\nexit /b 0\r\n"
		goScript = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo go %* GOOS=%GOOS% GOARCH=%GOARCH% CGO_ENABLED=%CGO_ENABLED%\r\nif \"%HUB_FAIL_GO%\"==\"1\" exit /b 9\r\nexit /b 0\r\n"
	}
	for name, script := range map[string]string{dockerName: dockerScript, goName: goScript} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := run(context.Background(), []string{"dev-rebuild", "--dir", dir, "--root", "../..", "--env", "dev", "communication-hub"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	for _, want := range []string{"config --quiet", "go build", "GOOS=linux GOARCH=amd64 CGO_ENABLED=0", "Dockerfile.dev-overlay", "hermes-hub-dev-owner-communication-hub:local", "--no-build --no-deps communication-hub", "buildx prune --max-used-space 8gb -f"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %s in %s", want, calls)
		}
	}
	if strings.Contains(calls, "cli-tools") || strings.Contains(calls, "cell-proxy") {
		t.Fatalf("unrelated images rebuilt: %s", calls)
	}
	for _, tc := range []struct{ service, source, base string }{
		{"toolhub", "./cmd/toolhub", "hermes-hub:0.3.0-dev-control"},
		{"credential-broker", "./cmd/credential-broker", "hermes-hub:0.3.0-dev"},
		{"workload-controller", "./cmd/hubctl", "hermes-hub:0.3.0-dev-control"},
		{"hermes-runtime", "./cmd/runtime", "hermes-hub:0.3.0-dev"},
	} {
		before, _ := os.ReadFile(log)
		if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", tc.service}); err != nil {
			t.Fatal(tc.service, err)
		}
		all, _ := os.ReadFile(log)
		if tc.service == "hermes-runtime" && !strings.Contains(string(all[len(before):]), "./cmd/hubctl") {
			t.Error("runtime overlay must rebuild its tools executor hubctl alongside hub-runtime")
		}
		for _, want := range []string{tc.source, "BASE=" + tc.base, "--no-build --no-deps " + tc.service} {
			if !strings.Contains(string(all), want) {
				t.Fatalf("%s missing %s in %s", tc.service, want, all)
			}
		}
	}
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "hub-media"}); err == nil {
		t.Fatal("built disabled hub-media service")
	}
	selection, _ := json.Marshal(stack.ExecutionSelection{Schema: 1, User: "owner", Environment: "dev", Mode: "supervisor", SupervisorURL: "http://host.docker.internal:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"})
	if err := os.WriteFile(filepath.Join(dir, stack.ExecutionPath("dev")), selection, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "hermes-runtime"}); err != nil {
		t.Fatal(err)
	}
	selected, err := os.ReadFile(filepath.Join(dir, "generated", "dev-runtime-image"))
	if err != nil || strings.TrimSpace(string(selected)) != "hermes-hub-dev-owner-hermes-runtime:local" {
		t.Fatalf("supervisor runtime image selection: %q %v", selected, err)
	}
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "communication-hub"}); err != nil {
		t.Fatal(err)
	}
	scriptSelected, err := os.ReadFile(filepath.Join(dir, "generated", "dev-script-image"))
	if err != nil || strings.TrimSpace(string(scriptSelected)) != "hermes-hub-dev-owner-communication-hub:local" {
		t.Fatalf("script image selection: %q %v", scriptSelected, err)
	}
	if err := run(context.Background(), []string{"build", "--dir", dir, "--root", "../..", "--env", "dev", "cliproxy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated", "dev-runtime-image")); !os.IsNotExist(err) {
		t.Fatal("full core build retained stale supervised dev image selection")
	}
	if _, err := os.Stat(filepath.Join(dir, "generated", "dev-script-image")); !os.IsNotExist(err) {
		t.Fatal("full core build retained stale script image selection")
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "dev-runtime-image"), selected, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"build", "--dir", dir, "--root", "../..", "--env", "dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated", "dev-runtime-image")); !os.IsNotExist(err) {
		t.Fatal("unscoped full build retained stale supervised dev image selection")
	}
	before, _ := os.ReadFile(log)
	t.Setenv("HUB_FAIL_GO", "1")
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "communication-hub"}); err == nil {
		t.Fatal("accepted failed Go compile")
	}
	after, _ := os.ReadFile(log)
	if strings.Count(string(after), "docker build ") != strings.Count(string(before), "docker build ") {
		t.Fatal("Docker built after failed compilation")
	}
	t.Setenv("HUB_FAIL_GO", "")
	t.Setenv("HUB_FAIL_DOCKER", "1")
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "communication-hub"}); err == nil {
		t.Fatal("accepted failed Docker build")
	}
	afterDocker, _ := os.ReadFile(log)
	if strings.Count(string(afterDocker), "buildx prune --max-used-space 8gb -f") <= strings.Count(string(after), "buildx prune --max-used-space 8gb -f") {
		t.Fatal("failed Docker build skipped cleanup")
	}
	t.Setenv("HUB_FAIL_DOCKER", "")
	s.TelegramAuth = true
	if err := stack.WriteSpace(dir, s); err != nil {
		t.Fatal(err)
	}
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "communication-hub"}); err != nil {
		t.Fatal(err)
	}
	withAuth, _ := os.ReadFile(log)
	if !strings.Contains(string(withAuth), "-tags=telegramauth") || !strings.Contains(string(withAuth), "BASE=hermes-hub-telegram-auth:0.3.0-dev") {
		t.Fatal("telegram auth dev build lost tagged gateway binary or base image")
	}
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "credential-broker"}); err != nil {
		t.Fatal(err)
	}
	brokerAuth, _ := os.ReadFile(log)
	if strings.Count(string(brokerAuth), "BASE=hermes-hub-telegram-auth:0.3.0-dev") < 2 {
		t.Fatal("broker did not retain the selected telegram auth base")
	}
	t.Setenv("HUB_FAIL_UP", "1")
	beforeFailedUp, _ := os.ReadFile(log)
	if err := runDevRebuild(context.Background(), []string{"--dir", dir, "--root", "../..", "communication-hub"}); err == nil {
		t.Fatal("accepted failed container start")
	}
	afterFailedUp, _ := os.ReadFile(log)
	newCalls := string(afterFailedUp[len(beforeFailedUp):])
	if !strings.Contains(newCalls, "docker tag sha256:old hermes-hub-dev-owner-communication-hub:local") || strings.Count(newCalls, "--no-build --no-deps communication-hub") != 2 {
		t.Fatalf("failed start did not restore previous image and retry service: %s", newCalls)
	}
}
