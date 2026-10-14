package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/supervisor"
)

func TestSupervisorCLIAuthAndDefaults(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_AUTH", "supervisor-secret")
	if supervisorAuthFromEnv() != "supervisor-secret" || envOr("MISSING_SUPERVISOR_ENV", "fallback") != "fallback" {
		t.Fatal("supervisor environment defaults are wrong")
	}
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	t.Setenv("HUB_RUNTIME_AUTH", "")
	oldArgs := os.Args
	os.Args = []string{"hub-supervisor"}
	t.Cleanup(func() { os.Args = oldArgs })
	if err := run(); err == nil {
		t.Fatal("supervisor started without auth")
	}
}

// A control plane binding every interface is a deliberate opt-in, never a
// default or a typo: wildcard listen requires -allow-wildcard-listen.
func TestWildcardListenRequiresFlag(t *testing.T) {
	for _, args := range [][]string{
		{"-listen", "0.0.0.0:8876"},
		{"-listen", "[::]:8876"},
		{"-listen", ":8876"},
	} {
		if _, err := parseSupervisorArgs(args); err == nil {
			t.Fatalf("wildcard listen accepted without opt-in: %v", args)
		}
	}
	for _, args := range [][]string{
		{"-listen", "127.0.0.1:8876"},
		{"-listen", "0.0.0.0:8876", "-allow-wildcard-listen"},
	} {
		if _, err := parseSupervisorArgs(args); err != nil {
			t.Fatalf("explicit listen rejected: %v %v", args, err)
		}
	}
}

// The token file is a secret: reject symlinks, empty files and unsafe
// permissions/locations, accept a regular owner-only file.
func TestAuthFileRejectsUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "supervisor.token")
	if err := os.WriteFile(good, []byte("  secret-token  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.token")
	if err := os.WriteFile(empty, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.token")
	if err := os.Symlink(good, link); err != nil {
		t.Skip("no symlink privilege")
	}
	if token, err := readSupervisorAuthFile(good, dir); err != nil || token != "secret-token" {
		t.Fatalf("valid auth file rejected: %q %v", token, err)
	}
	for _, path := range []string{empty, link, filepath.Join(dir, "missing.token")} {
		if _, err := readSupervisorAuthFile(path, dir); err == nil {
			t.Fatalf("unsafe auth file accepted: %s", path)
		}
	}
	if runtime.GOOS == "windows" {
		outside := filepath.Join("C:\\", "auth.token")
		if _, err := readSupervisorAuthFile(outside, dir); err == nil {
			t.Fatal("auth file outside profile/control accepted on Windows")
		}
	} else {
		world := filepath.Join(dir, "world.token")
		if err := os.WriteFile(world, []byte("secret"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := readSupervisorAuthFile(world, dir); err == nil {
			t.Fatal("group/world-readable auth file accepted")
		}
	}
}

// -auth on argv leaks the token into process listings; it still works but
// must warn. -auth-file takes precedence silently.
func TestAuthFlagWarnsAndAuthFileWins(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "supervisor.token")
	if err := os.WriteFile(tokenFile, []byte("file-token"), 0600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	o, err := parseSupervisorArgs([]string{"-auth", "argv-token"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := o.resolveAuth()
	if err != nil || token != "argv-token" || !strings.Contains(buf.String(), "deprecated") {
		t.Fatalf("argv auth: token=%q warn=%q err=%v", token, buf.String(), err)
	}
	buf.Reset()
	o, err = parseSupervisorArgs([]string{"-auth", "argv-token", "-auth-file", tokenFile, "-spaces", dir})
	if err != nil {
		t.Fatal(err)
	}
	token, err = o.resolveAuth()
	if err != nil || token != "file-token" || strings.Contains(buf.String(), "deprecated") {
		t.Fatalf("auth file: token=%q warn=%q err=%v", token, buf.String(), err)
	}
}

// A bare supervisor launch must keep runtimes warm for the shared 30 min
// default, not the old 5-minute window.
func TestSupervisorFlagDefaults(t *testing.T) {
	parsed, err := parseSupervisorArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.warmTTL != supervisor.DefaultWarmTTL {
		t.Fatalf("warm-ttl default=%s, want %s", parsed.warmTTL, supervisor.DefaultWarmTTL)
	}
}
