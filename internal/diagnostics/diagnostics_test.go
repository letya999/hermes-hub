package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFollowAutomaticallyRefreshesOneFileAndIncludesHostSupervisor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "supervisor.log"), []byte("supervisor acquired alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("abc123\thermes-hub-alice-prod-toolhub-1\nforeign\tother-project\n"), nil
		}
		cancel()
		return []byte("2026-09-26T12:00:00.000000000Z toolhub admitted alice\n"), nil
	}
	path := filepath.Join(dir, "hermes-diagnostics.txt")
	done := make(chan struct{})
	go func() { Follow(ctx, path, run, time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic collector did not stop")
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "toolhub admitted alice") || !strings.Contains(string(body), "supervisor acquired alice") || strings.Contains(string(body), "other-project") {
		t.Fatal(string(body), err)
	}
}

func TestCollectUsesContainerCursorWithoutDuplicatingLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	var logCalls [][]string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-context-one\n"), nil
		}
		logCalls = append(logCalls, append([]string{}, args...))
		return []byte("2026-09-26T12:00:00.000000000Z first\n2026-09-26T12:00:01.000000000Z second\n"), nil
	}
	for range 2 {
		if err := Collect(context.Background(), path, run); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil || strings.Count(string(body), "first") != 1 || strings.Count(string(body), "second") != 1 {
		t.Fatal(string(body), err)
	}
	if len(logCalls) != 2 || !strings.Contains(strings.Join(logCalls[1], " "), "--since 2026-09-26T12:00:01.000000000Z") {
		t.Fatal(logCalls)
	}
}

func TestCollectSkipsMalformedDockerRowsAndTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("broken\n\thermes-empty-id\nid1\thermes-context-one\n"), nil
		}
		return []byte("garbage\n2026-09-26T12:00:00Z accepted\nnot-a-date rejected\n"), nil
	}
	if err := Collect(context.Background(), path, run); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "accepted") || strings.Contains(string(body), "rejected") {
		t.Fatal(string(body), err)
	}
}

func TestCollectRecoversBadCursorAndPreservesFileOnDockerError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hermes-diagnostics.txt")
	if err := os.WriteFile(path+".cursor.json", []byte("bad cursor"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-hub-bob-prod-runtime-1\nforeign\tother-project\n"), nil
		}
		return []byte("2026-09-26T12:00:00.000000000Z recovered\n"), nil
	}
	if err := Collect(context.Background(), path, run); err != nil {
		t.Fatal(err)
	}
	broken := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-hub-bob-prod-runtime-1\n"), nil
		}
		return nil, errors.New("daemon lost")
	}
	if err := Collect(context.Background(), path, broken); err == nil {
		t.Fatal("ignored Docker log failure")
	}
	body, err := os.ReadFile(path)
	if err != nil || strings.Count(string(body), "recovered") != 1 {
		t.Fatal(string(body), err)
	}
	if err := Collect(context.Background(), path, func(context.Context, ...string) ([]byte, error) { return nil, errors.New("daemon down") }); err == nil {
		t.Fatal("ignored Docker listing failure")
	}
}

func TestCollectAcceptsLegacyCursorWithoutContainers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	if err := os.WriteFile(path+".cursor.json", []byte(`{"supervisor":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-context-one\n"), nil
		}
		return []byte("2026-09-26T12:00:00Z new log\n"), nil
	}
	if err := Collect(context.Background(), path, run); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(path); err != nil || !strings.Contains(string(body), "new log") {
		t.Fatal(string(body), err)
	}
}

func TestCollectHandlesHostLogRollover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hermes-diagnostics.txt")
	host := filepath.Join(dir, "supervisor.log")
	run := func(context.Context, ...string) ([]byte, error) { return nil, nil }
	if err := os.WriteFile(host, []byte("long supervisor first line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Collect(context.Background(), path, run); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(host, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Collect(context.Background(), path, run); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "long supervisor first line") || !strings.Contains(string(body), "[host-supervisor] new") {
		t.Fatal(string(body), err)
	}
}

func TestCollectBoundsCombinedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxLogBytes); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-context-one\n"), nil
		}
		return []byte("2026-09-26T12:00:00Z retained after rollover\n"), nil
	}
	if err := Collect(context.Background(), path, run); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(path); err != nil || !strings.Contains(string(body), "retained after rollover") || len(body) > 200 {
		t.Fatal(len(body), err)
	}
}

func TestCollectRejectsUnexpectedOutputPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".local"), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".local", "hermes-diagnostics.txt")
	if err := Collect(context.Background(), path, func(context.Context, ...string) ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("accepted file instead of diagnostics directory")
	}
}

func TestCollectRejectsUnreadableCursorAndLogTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hermes-diagnostics.txt")
	if err := os.Mkdir(path+".cursor.json", 0700); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-context-one\n"), nil
		}
		return []byte("2026-09-26T12:00:00.000000000Z line\n"), nil
	}
	if err := Collect(context.Background(), path, run); err == nil {
		t.Fatal("accepted cursor directory")
	}
	if err := os.Remove(path + ".cursor.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Collect(context.Background(), path, run); err == nil {
		t.Fatal("accepted directory as diagnostic log")
	}
}

func TestFollowRecoversAfterDockerListingFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			count++
			if count == 1 {
				return nil, errors.New("daemon starting")
			}
			return []byte("id1\thermes-context-one\n"), nil
		}
		cancel()
		return []byte("2026-09-26T12:00:00.000000000Z recovered\n"), nil
	}
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	Follow(ctx, path, run, time.Millisecond)
	if body, err := os.ReadFile(path); err != nil || !strings.Contains(string(body), "recovered") {
		t.Fatal(string(body), err)
	}
}

func TestDockerRunnerUsesDockerCLI(t *testing.T) {
	bin := t.TempDir()
	name, script := "docker", "#!/bin/sh\necho id1\n"
	if runtime.GOOS == "windows" {
		name, script = "docker.cmd", "@echo off\r\necho id1\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if body, err := Docker(context.Background(), "ps"); err != nil || !strings.Contains(string(body), "id1") {
		t.Fatal(string(body), err)
	}
}

func TestHTTPLogsRouteWithoutSecretPathOrQuery(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	handler := HTTP("broker", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/connect/private-token?code=secret", nil))
	if got := output.String(); !strings.Contains(got, "broker http method=POST route=/connect") || strings.Contains(got, "private-token") || strings.Contains(got, "secret") {
		t.Fatal(got)
	}
}

func TestSafeRouteBoundsIdentifiers(t *testing.T) {
	for input, want := range map[string]string{"/": "/", "/v1/jobs/private-id": "/v1/jobs", "/oauth/callback?code=secret": "/oauth"} {
		if got := safeRoute(input); got != want {
			t.Fatalf("%q: got %q, want %q", input, got, want)
		}
	}
}

func TestHostLogMirrorsAndBoundsSupervisorFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.log")
	stop, err := HostLog(path)
	if err != nil {
		t.Fatal(err)
	}
	log.Print("supervisor started")
	stop()
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "supervisor started") {
		t.Fatal(string(body), err)
	}
	w := &boundedFile{path: path}
	if _, err := w.Write(bytes.Repeat([]byte("x"), 10<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new log line\n")); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(path)
	if err != nil || string(body) != "new log line\n" {
		t.Fatal(len(body), err)
	}
}

func TestHostLogRejectsFileAsDirectory(t *testing.T) {
	dir := t.TempDir()
	occupied := filepath.Join(dir, "occupied")
	if err := os.WriteFile(occupied, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := HostLog(filepath.Join(occupied, "supervisor.log")); err == nil {
		t.Fatal("accepted file as diagnostics directory")
	}
	if _, err := (&boundedFile{path: dir}).Write([]byte("line")); err == nil {
		t.Fatal("accepted directory as host log")
	}
}
