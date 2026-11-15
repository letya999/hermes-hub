package main

import (
	"context"
	"fmt"
	"github.com/letya999/hermes-hub/internal/stack"
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
	// Infra owner without a channel: the gateway stays undeployed so `chat`
	// is legal, and the revision alone carries every consumer.
	writeManagedSpaceDocs(t, d, "owner", managedInfraDocs("owner", false))
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=fake-for-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "args")
	name := "docker"
	script := "#!/bin/sh\nprintf 'ENV DOCKER_BUILDKIT=%s COMPOSE_BAKE=%s COMPOSE_DOCKER_CLI_BUILD=%s\\n' \"$DOCKER_BUILDKIT\" \"$COMPOSE_BAKE\" \"$COMPOSE_DOCKER_CLI_BUILD\" >> \"$HUB_TEST_LOG\"\nprintf '%s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\nif [ -n \"$HUB_FAIL_MATCH\" ]; then for arg do if [ \"$arg\" = \"$HUB_FAIL_MATCH\" ]; then exit 7; fi; done; fi\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo ENV DOCKER_BUILDKIT=%DOCKER_BUILDKIT% COMPOSE_BAKE=%COMPOSE_BAKE% COMPOSE_DOCKER_CLI_BUILD=%COMPOSE_DOCKER_CLI_BUILD%\r\n>>\"%HUB_TEST_LOG%\" echo %*\r\nif \"%~1\"==\"build\" if \"%HUB_FAIL_MATCH%\"==\"build\" exit /b 7\r\nif not \"%~1\"==\"compose\" exit /b 0\r\nif \"%HUB_FAIL_MATCH%\"==\"config\" (echo %* | findstr /C:\" config --quiet\" >nul && exit /b 7)\r\nif \"%HUB_FAIL_MATCH%\"==\"build\" (echo %* | findstr /C:\" build\" >nul && exit /b 7)\r\nif \"%HUB_FAIL_MATCH%\"==\"up\" (echo %* | findstr /C:\" up -d\" >nul && exit /b 7)\r\n"
	}
	if runtime.GOOS == "windows" {
		script = strings.Replace(script, "if not \"%~1\"==\"compose\" exit /b 0\r\n", "if \"%~1\"==\"network\" if \"%~2\"==\"inspect\" (echo true & exit /b 0)\r\nif not \"%~1\"==\"compose\" exit /b 0\r\n", 1)
		script += "exit /b 0\r\n"
	} else {
		script += "if [ \"$1\" = network ] && [ \"$2\" = inspect ]; then printf 'true\\n'; fi\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	t.Setenv("HUB_FAIL_MATCH", "config")
	for _, op := range []string{"build", "up"} {
		if run(context.Background(), []string{op, "--dir", d, "--root", "../.."}) == nil {
			t.Fatalf("%s accepted invalid Compose configuration", op)
		}
	}
	failedConfig, _ := os.ReadFile(log)
	if strings.Contains(string(failedConfig), "image prune") || strings.Contains(string(failedConfig), " build") || strings.Contains(string(failedConfig), " up -d") || strings.Contains(string(failedConfig), "volume create") {
		t.Fatalf("invalid configuration caused Docker mutation: %s", failedConfig)
	}
	t.Setenv("HUB_FAIL_MATCH", "")
	for _, op := range []string{"doctor", "build", "up", "down", "logs", "chat", "telegram-login", "meet-auth"} {
		if err := run(context.Background(), []string{op, "--dir", d, "--root", "../.."}); err != nil {
			t.Fatal(op, err)
		}
	}
	if err := run(context.Background(), []string{"up", "--dir", d, "--root", "../..", "supervisor", "toolhub"}); err != nil {
		t.Fatal(err)
	}
	beforeNoBuild, _ := os.ReadFile(log)
	if err := run(context.Background(), []string{"up", "--dir", d, "--root", "../..", "--no-build", "supervisor", "toolhub"}); err != nil {
		t.Fatal(err)
	}
	afterNoBuild, _ := os.ReadFile(log)
	scopedUp := strings.TrimPrefix(string(afterNoBuild), string(beforeNoBuild))
	if strings.Count(string(afterNoBuild), " build supervisor toolhub") != strings.Count(string(beforeNoBuild), " build supervisor toolhub") || !strings.Contains(scopedUp, "--force-recreate --no-build --no-deps supervisor toolhub") || strings.Contains(scopedUp, "--remove-orphans") {
		t.Fatal("scoped no-build up touched unrelated services")
	}
	if err := run(context.Background(), []string{"up", "--dir", d, "--root", "../..", "--no-build"}); err != nil {
		t.Fatal(err)
	}
	afterFullNoBuild, _ := os.ReadFile(log)
	fullUp := strings.TrimPrefix(string(afterFullNoBuild), string(afterNoBuild))
	if !strings.Contains(fullUp, "--remove-orphans --no-build") || strings.Contains(fullUp, "--force-recreate") {
		t.Fatal("full no-build up forced unrelated service restarts")
	}
	if err := run(context.Background(), []string{"build", "--dir", d, "--root", "../..", "--", "--no-cache"}); err == nil {
		t.Fatal("compose option accepted as a service")
	}
	if err := run(context.Background(), []string{"exec", "--dir", d, "--", "hermes", "skills", "list"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	createRecovery := strings.Index(string(b), "volume create hermes-credential-broker-recovery-prod")
	createRecoveryKey := strings.Index(string(b), "volume create hermes-credential-broker-recovery-key-prod")
	startServices := strings.Index(string(b), " up -d")
	if createRecovery < 0 || createRecoveryKey < 0 || startServices < 0 || createRecovery > startServices || createRecoveryKey > startServices {
		t.Fatal("up did not provision the independent recovery volumes before starting services")
	}
	if !strings.Contains(string(b), "ps --filter label=com.docker.compose.service=cliproxy") {
		t.Fatal("up skipped the shared model proxy alias check")
	}
	if !strings.Contains(string(b), filepath.Join(d, "generated", "compose.prod.yaml")) || !strings.Contains(string(b), "ENV DOCKER_BUILDKIT=1 COMPOSE_BAKE=true COMPOSE_DOCKER_CLI_BUILD=1") || !strings.Contains(string(b), "image prune -f") || !strings.Contains(string(b), "--force-recreate") || !strings.Contains(string(b), "build --target prod-control") || !strings.Contains(string(b), "--no-deps supervisor toolhub") || !strings.Contains(string(b), "telegram-account.Dockerfile") || strings.Contains(string(b), "prepare") || strings.Contains(string(b), "FOWNER") || strings.Contains(string(b), "career") {
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
	// Publishing the gateway revision is the passport-path equivalent of
	// adding telegram to the old feature list.
	ownerDocs := managedInfraDocs("owner", true)
	ownerDocs["stack"] = strings.Replace(ownerDocs["stack"], "  supervisor: {mode: shared}\n", "", 1)
	writeManagedSpaceDocs(t, d, "owner", ownerDocs)
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=fake-for-test\nTELEGRAM_BOT_TOKEN=synthetic-bot\nTELEGRAM_ALLOWED_USERS=11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"up", "--dir", d, "--root", "../..", "--no-build"}); err != nil {
		t.Fatal(err)
	}
	ownershipLog, _ := os.ReadFile(log)
	if !strings.Contains(string(ownershipLog), "ps --filter label=com.docker.compose.service=communication-hub") {
		t.Fatal("up skipped the read-only Telegram ownership check")
	}
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

func TestBuildCoversRuntimeTagWhenRuntimeNotRendered(t *testing.T) {
	// SPEC-0082 R3: supervisor/managed renders drop hermes-runtime, so no
	// compose build section produces the spawned-runtime tag. hubctl must
	// build it explicitly, the same way it builds the cell images.
	d := filepath.Join(t.TempDir(), "space")
	// The managed stack declares a supervised runtime: no compose service
	// renders hermes-runtime, yet its image tag must still be built.
	ownerDocs := managedInfraDocs("owner", true)
	ownerDocs["stack"] = strings.Replace(ownerDocs["stack"], "  supervisor: {mode: shared}\n", "", 1)
	writeManagedSpaceDocs(t, d, "owner", ownerDocs)
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=fake-for-test\nTELEGRAM_BOT_TOKEN=synthetic-bot\nTELEGRAM_ALLOWED_USERS=11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, stack.ExecutionPath("prod")), []byte(`{"schema":1,"user":"owner","environment":"prod","mode":"supervisor","supervisor_url":"http://host.docker.internal:8876","native_cron":"disabled","compatibility_release":"0.5.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "args")
	name := "docker"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo %*\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := run(context.Background(), []string{"build", "--dir", d, "--root", "../.."}); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "build --target prod -t hermes-hub:0.5.0-prod") {
		t.Fatalf("supervisor render left the spawned-runtime tag unbuilt: %s", calls)
	}
	if err := os.WriteFile(log, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"build", "--dir", d, "--root", "../..", "communication-hub"}); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(log)
	if strings.Contains(string(calls), " build communication-hub") || !strings.Contains(string(calls), "build --target prod-core -t hermes-hub:0.5.0-prod-core") || strings.Contains(string(calls), "build --target prod-control") || strings.Contains(string(calls), "prune") {
		t.Fatalf("service build also built unrelated images: %s", calls)
	}
	if err := os.WriteFile(log, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"build", "--dir", d, "--root", "../..", "communication-hub", "toolhub"}); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(log)
	if strings.Count(string(calls), "build --target prod-core -t hermes-hub:0.5.0-prod-core") != 1 || strings.Count(string(calls), "build --target prod-control -t hermes-hub:0.5.0-prod-control") != 1 || strings.Contains(string(calls), "build --target prod -t hermes-hub:0.5.0-prod") || strings.Contains(string(calls), " build communication-hub") || strings.Contains(string(calls), " build toolhub") {
		t.Fatalf("multi-service build missed or duplicated a target: %s", calls)
	}
	if err := os.WriteFile(log, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"build", "--dir", d, "--root", "../..", "--image", "runtime"}); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(log)
	if !strings.Contains(string(calls), "build --target prod -t hermes-hub:0.5.0-prod") || strings.Contains(string(calls), " build communication-hub") || strings.Contains(string(calls), "build --target cli-tools") || strings.Contains(string(calls), "prune") {
		t.Fatalf("runtime-only build selected wrong images: %s", calls)
	}
	if run(context.Background(), []string{"build", "--dir", d, "--image", "runtime", "communication-hub"}) == nil {
		t.Fatal("image-only build accepted a Compose service")
	}
}
