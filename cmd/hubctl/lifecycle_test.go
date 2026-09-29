package main

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPruneDanglingImagesKeepsForeignContainersAndBoundsCache(t *testing.T) {
	var calls []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "image":
			if args[1] == "ls" {
				return []byte("old-image\n"), nil
			}
		case "ps":
			return []byte("hermes-hub-local-dev-old\tExited (0)\nhermes-hub-local-dev-live\tUp 1 minute\nai-stp-api-1\tExited (0)\n"), nil
		}
		return nil, nil
	}
	if err := pruneDanglingImages(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"rm hermes-hub-local-dev-old", "image prune -f", "buildx prune --max-used-space 8gb -f"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s: %s", want, joined)
		}
	}
	if strings.Contains(joined, "rm ai-stp") || strings.Contains(joined, "rm hermes-hub-local-dev-live") {
		t.Fatal(joined)
	}
	if err := pruneDanglingImages(context.Background(), func(context.Context, ...string) ([]byte, error) { return nil, fmt.Errorf("daemon down") }); err == nil {
		t.Fatal("accepted Docker failure")
	}
}

func TestLifecycleAndFailurePropagation(t *testing.T) {
	d := filepath.Join(t.TempDir(), "space with spaces")
	if err := stack.InitEnvironment(d, "owner", "dev"); err != nil {
		t.Fatal(err)
	}
	s, _ := stack.Read(filepath.Join(d, "settings.yaml"))
	s.Model = "test"
	s.ModelURL = "http://model.invalid/v1"
	save := func() {
		b, _ := yaml.Marshal(s)
		if err := os.WriteFile(filepath.Join(d, "settings.yaml"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=fake-for-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "args")
	name := "docker"
	script := "#!/bin/sh\nprintf 'ENV DOCKER_BUILDKIT=%s COMPOSE_BAKE=%s COMPOSE_DOCKER_CLI_BUILD=%s\\n' \"$DOCKER_BUILDKIT\" \"$COMPOSE_BAKE\" \"$COMPOSE_DOCKER_CLI_BUILD\" >> \"$HUB_TEST_LOG\"\nprintf '%s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\nif [ -n \"$HUB_FAIL_MATCH\" ]; then for arg do if [ \"$arg\" = \"$HUB_FAIL_MATCH\" ]; then exit 7; fi; done; fi\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo ENV DOCKER_BUILDKIT=%DOCKER_BUILDKIT% COMPOSE_BAKE=%COMPOSE_BAKE% COMPOSE_DOCKER_CLI_BUILD=%COMPOSE_DOCKER_CLI_BUILD%\r\n>>\"%HUB_TEST_LOG%\" echo %*\r\nif not \"%~1\"==\"compose\" exit /b 0\r\nif \"%HUB_FAIL_MATCH%\"==\"build\" (echo %* | findstr /C:\" build\" >nul && exit /b 7)\r\nif \"%HUB_FAIL_MATCH%\"==\"up\" (echo %* | findstr /C:\" up -d\" >nul && exit /b 7)\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	for _, op := range []string{"doctor", "build", "up", "down", "logs", "chat", "telegram-login", "meet-auth"} {
		if err := run(context.Background(), []string{op, "--dir", d, "--root", "../.."}); err != nil {
			t.Fatal(op, err)
		}
	}
	if err := run(context.Background(), []string{"exec", "--dir", d, "--", "hermes", "skills", "list"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), filepath.Join(d, "generated", "compose.prod.yaml")) || !strings.Contains(string(b), "ENV DOCKER_BUILDKIT=1 COMPOSE_BAKE=true COMPOSE_DOCKER_CLI_BUILD=1") || !strings.Contains(string(b), "image prune -f") || !strings.Contains(string(b), "--force-recreate") || !strings.Contains(string(b), "telegram-account.Dockerfile") || strings.Contains(string(b), "prepare") || strings.Contains(string(b), "FOWNER") || strings.Contains(string(b), "career") {
		t.Fatal(string(b))
	}
	for _, fail := range []string{"build", "up"} {
		t.Setenv("HUB_FAIL_MATCH", fail)
		before, _ := os.ReadFile(log)
		if run(context.Background(), []string{"up", "--dir", d, "--root", "../.."}) == nil {
			t.Fatal("ignored docker error", fail)
		}
		after, _ := os.ReadFile(log)
		if strings.Count(string(after), "image prune -f")-strings.Count(string(before), "image prune -f") != 2 {
			t.Fatal("expected cleanup before and after failed", fail, "build")
		}
	}
	t.Setenv("HUB_FAIL_MATCH", "")
	s.Features = append(s.Features, "telegram")
	save()
	if run(context.Background(), []string{"chat", "--dir", d}) == nil {
		t.Fatal("two agents sharing one home")
	}
	for _, args := range [][]string{{"exec", "--dir", d}, {"doctor", "--dir", "missing"}, {"build", "--dir", "missing"}, {"chat", "--dir", "missing"}, {"companion", "--config", "missing"}, {"tools", "--workspace", "missing"}, {"init", "--env", "staging"}, {"init", "--bad-flag"}, {"doctor build"}} {
		if run(context.Background(), args) == nil {
			t.Fatal("invalid command accepted", args)
		}
	}
	if err := run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"catalog"}); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(d, "secrets.prod.env"))
	if run(context.Background(), []string{"doctor", "--dir", d}) == nil {
		t.Fatal("missing env accepted")
	}
}
