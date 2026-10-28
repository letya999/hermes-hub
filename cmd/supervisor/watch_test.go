package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSupervisorWatchRestartsAndStops(t *testing.T) {
	if marker := os.Getenv("HUB_WATCH_TEST_MARKER"); marker != "" {
		body, _ := os.ReadFile(marker)
		if err := os.WriteFile(marker, append(body, 'x'), 0600); err != nil {
			os.Exit(2)
		}
		if len(body) == 0 {
			os.Exit(1)
		}
		time.Sleep(time.Minute)
		os.Exit(3)
	}
	marker := filepath.Join(t.TempDir(), "starts")
	t.Setenv("HUB_WATCH_TEST_MARKER", marker)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The window must cover two instrumented-binary boots plus the 1s restart
	// delay under parallel -race load; a re-exec'd test binary can take
	// seconds just to start on a contended Windows host.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	started := time.Now()
	if err := watchSupervisor(ctx, executable, []string{"-test.run=^TestSupervisorWatchRestartsAndStops$"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(body), "x") != 2 || time.Since(started) > 30*time.Second {
		t.Fatalf("watch must restart one crashed child and join it on cancellation: starts=%q error=%v elapsed=%s", body, err, time.Since(started))
	}
}

func TestSupervisorServiceFlags(t *testing.T) {
	if _, err := parseSupervisorArgs([]string{"-watch", "-env", "dev", "-image", "hermes-hub:test"}); err != nil {
		t.Fatalf("service launcher flags rejected: %v", err)
	}
}

func TestSupervisorWatchCLIStartsChild(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "supervisor.exe")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	auth := filepath.Join(root, "auth")
	if err := os.WriteFile(auth, []byte("synthetic-watch-auth"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, binary, "-watch", "-env", "dev", "-spaces", filepath.Join(root, "spaces"), "-auth-file", auth, "-listen", listener.Addr().String()).CombinedOutput()
	if !strings.Contains(string(out), "supervisor child exited; restarting in 1s") {
		t.Fatalf("CLI did not watch its failed child: %s", out)
	}
}
