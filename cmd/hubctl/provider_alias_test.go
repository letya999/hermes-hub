package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The infra owner's own cliproxy lives in the standalone
// hermes-hub-shared-<env> project after the plan split: `up` must skip it
// like any same-project proxy instead of refusing it as foreign.
func TestUpInfraSkipsOwnSharedProxy(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "owner")
	writeManagedSpaceDocs(t, dir, "owner", managedInfraDocs("owner", false))
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "docker"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\ncase \"$*\" in *service=cliproxy*) echo c1;; inspect?c1) echo '[{\"Config\":{\"Labels\":{\"com.docker.compose.project\":\"hermes-hub-shared-dev\"}},\"NetworkSettings\":{\"Networks\":{\"hermes-hub-runtime\":{}}}}]';; esac\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo %*\r\necho %* | findstr /C:\"service=cliproxy\" >nul && echo c1\r\necho %* | findstr /C:\"inspect c1\" >nul && echo [{\"Config\":{\"Labels\":{\"com.docker.compose.project\":\"hermes-hub-shared-dev\"}},\"NetworkSettings\":{\"Networks\":{\"hermes-hub-runtime\":{}}}}]\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(bin, "args")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := run(context.Background(), []string{"up", "--dir", dir, "--root", "../..", "--env", "dev", "--no-build"}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "up -d") {
		t.Fatalf("infra up did not reach compose up: %s", out)
	}
}

// The infra owner's communication-hub lives in the shared project too:
// `up` must not refuse the owner's own gateway as a competing bot.
func TestUpInfraSkipsOwnSharedGateway(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "owner")
	writeManagedSpaceDocs(t, dir, "owner", managedInfraDocs("owner", true))
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=test-key\nTELEGRAM_BOT_TOKEN=synthetic-bot\nTELEGRAM_ALLOWED_USERS=11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "docker"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\ncase \"$*\" in *service=communication-hub*) echo g1;; inspect?g1) echo '[{\"Config\":{\"Labels\":{\"com.docker.compose.project\":\"hermes-hub-shared-dev\"},\"Env\":[\"TELEGRAM_BOT_TOKEN=synthetic-bot\"]}}]';; esac\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo %*\r\necho %* | findstr /C:\"service=communication-hub\" >nul && echo g1\r\necho %* | findstr /C:\"inspect g1\" >nul && echo [{\"Config\":{\"Labels\":{\"com.docker.compose.project\":\"hermes-hub-shared-dev\"},\"Env\":[\"TELEGRAM_BOT_TOKEN=synthetic-bot\"]}}]\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(bin, "args")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := run(context.Background(), []string{"up", "--dir", dir, "--root", "../..", "--env", "dev", "--no-build"}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "up -d") {
		t.Fatalf("infra up did not reach compose up: %s", out)
	}
}

func TestSharedCLIProxyAliasRejectsAnotherInfraOwner(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("foreign\n"), nil
		}
		return []byte(`[{"Config":{"Labels":{"com.docker.compose.project":"hermes-hub-other-dev"}},"NetworkSettings":{"Networks":{"hermes-hub-runtime":{}}}}]`), nil
	}
	err := checkProviderAliasOwnership(context.Background(), "hermes-hub-local-dev", run)
	if err == nil || !strings.Contains(err.Error(), "hermes-hub-other-dev") {
		t.Fatalf("shared provider alias conflict accepted: %v", err)
	}
}
