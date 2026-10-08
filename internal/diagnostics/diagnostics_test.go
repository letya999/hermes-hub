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
	"slices"
	"strings"
	"testing"
	"time"
)

// testScope owns Alice's dev stack: project-labeled services plus runtimes
// attached to her agent network.
var testScope = Scope{Project: "hermes-hub-alice-dev", AgentNet: "hermes-hub-agent-alice-dev", Owner: "alice"}

func TestFollowAutomaticallyRefreshesOneFileAndIncludesHostSupervisor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "supervisor.log"), []byte("supervisor acquired alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("abc123\thermes-hub-alice-dev-toolhub-1\thermes-hub-alice-dev\thermes-hub-alice-dev_default\tUp 2 hours\n" +
				"foreign\thermes-hub-bob-dev-toolhub-1\thermes-hub-bob-dev\thermes-hub-bob-dev_default\tUp 2 hours\n"), nil
		}
		cancel()
		return []byte("2026-09-26T12:00:00.000000000Z toolhub admitted alice\n"), nil
	}
	path := filepath.Join(dir, "hermes-diagnostics.txt")
	done := make(chan struct{})
	go func() { Follow(ctx, path, run, time.Second, testScope); close(done) }()
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
			return []byte("id1\thermes-context-one\t\thermes-hub-agent-alice-dev\tUp 1 hour\n"), nil
		}
		logCalls = append(logCalls, append([]string{}, args...))
		return []byte("2026-09-26T12:00:00.000000000Z first\n2026-09-26T12:00:01.000000000Z second\n"), nil
	}
	for range 2 {
		if err := Collect(context.Background(), path, run, testScope); err != nil {
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
			return []byte("broken\n\thermes-empty-id\nid1\thermes-context-one\t\thermes-hub-agent-alice-dev\tUp 1 hour\n"), nil
		}
		return []byte("garbage\n2026-09-26T12:00:00Z accepted\nnot-a-date rejected\n"), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
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
			return []byte("id1\thermes-hub-alice-dev-runtime-1\thermes-hub-alice-dev\thermes-hub-alice-dev_default\tUp 1 hour\n" +
				"foreign\thermes-hub-bob-dev-runtime-1\thermes-hub-bob-dev\thermes-hub-bob-dev_default\tUp 1 hour\n"), nil
		}
		return []byte("2026-09-26T12:00:00.000000000Z recovered\n"), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	broken := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-hub-alice-dev-runtime-1\thermes-hub-alice-dev\thermes-hub-alice-dev_default\tUp 1 hour\n"), nil
		}
		return nil, errors.New("daemon lost")
	}
	if err := Collect(context.Background(), path, broken, testScope); err == nil {
		t.Fatal("ignored Docker log failure")
	}
	body, err := os.ReadFile(path)
	if err != nil || strings.Count(string(body), "recovered") != 1 {
		t.Fatal(string(body), err)
	}
	if err := Collect(context.Background(), path, func(context.Context, ...string) ([]byte, error) { return nil, errors.New("daemon down") }, testScope); err == nil {
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
			return []byte("id1\thermes-context-one\t\thermes-hub-agent-alice-dev\tUp 1 hour\n"), nil
		}
		return []byte("2026-09-26T12:00:00Z new log\n"), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
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
	if err := os.WriteFile(host, []byte("long supervisor first line for alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(host, []byte("new alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "long supervisor first line for alice") || !strings.Contains(string(body), "[host-supervisor] new alice") {
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
			return []byte("id1\thermes-context-one\t\thermes-hub-agent-alice-dev\tUp 1 hour\n"), nil
		}
		return []byte("2026-09-26T12:00:00Z retained after rollover\n"), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
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
	if err := Collect(context.Background(), path, func(context.Context, ...string) ([]byte, error) { return nil, nil }, testScope); err == nil {
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
			return []byte("id1\thermes-context-one\t\thermes-hub-agent-alice-dev\tUp 1 hour\n"), nil
		}
		return []byte("2026-09-26T12:00:00.000000000Z line\n"), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err == nil {
		t.Fatal("accepted cursor directory")
	}
	if err := os.Remove(path + ".cursor.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Collect(context.Background(), path, run, testScope); err == nil {
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
			return []byte("id1\thermes-context-one\t\thermes-hub-agent-alice-dev\tUp 1 hour\n"), nil
		}
		cancel()
		return []byte("2026-09-26T12:00:00.000000000Z recovered\n"), nil
	}
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	Follow(ctx, path, run, time.Millisecond, testScope)
	if body, err := os.ReadFile(path); err != nil || !strings.Contains(string(body), "recovered") {
		t.Fatal(string(body), err)
	}
}

func TestCollectRedactsSecretsAtIngest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	dir := filepath.Dir(path)
	if err := os.WriteFile(filepath.Join(dir, "supervisor.log"), []byte("leaked token=ghp_1234567890abcdef for alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-context-one\t\thermes-hub-agent-alice-dev\tUp 1 hour\n"), nil
		}
		return []byte("2026-09-26T12:00:00.000000000Z auth failed ATATT3xFfGF0mNYB2nJ0srzVG0kKmVmqIlzWI\n" +
			"2026-09-26T12:00:01.000000000Z dial postgres://alice:hunter2@db.internal:5432/app\n" +
			"2026-09-26T12:00:02.000000000Z kept sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08\n"), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ghp_1234567890abcdef", "ATATT3xFfGF0", "hunter2"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("secret %s stored unredacted:\n%s", secret, body)
		}
	}
	if !strings.Contains(string(body), "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08") {
		t.Fatalf("hex digest wrongly redacted:\n%s", body)
	}
}

func TestCollectContinuesAfterContainerLogFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-broken\thermes-hub-alice-dev\tnet\tUp 1 hour\n" +
				"id2\thermes-working\thermes-hub-alice-dev\tnet\tUp 1 hour\n"), nil
		}
		if args[len(args)-1] == "id1" {
			return nil, errors.New("daemon lost")
		}
		return []byte("2026-09-26T12:00:00.000000000Z healthy line\n"), nil
	}
	err := Collect(context.Background(), path, run, testScope)
	if err == nil || !strings.Contains(err.Error(), "hermes-broken") {
		t.Fatalf("expected named failure, got %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "healthy line") {
		t.Fatalf("working container lost collection: %s %v", body, err)
	}
	// The failed container keeps its previous position: a recovering next
	// cycle does not re-ingest or skip its backlog.
	run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id1\thermes-broken\thermes-hub-alice-dev\tnet\tUp 1 hour\n" +
				"id2\thermes-working\thermes-hub-alice-dev\tnet\tUp 1 hour\n"), nil
		}
		if args[len(args)-1] == "id1" {
			return []byte("2026-09-26T12:00:03.000000000Z recovered broken\n"), nil
		}
		return []byte(""), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	if !strings.Contains(string(body), "recovered broken") || strings.Count(string(body), "healthy line") != 1 {
		t.Fatalf("cursor drift after failure:\n%s", body)
	}
}

func TestCollectDrainsStoppedContainerOnceAndRearmsOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	row := "id1\thermes-context-one\thermes-hub-alice-dev\tnet\t"
	logCalls := 0
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte(row + "Exited (0) 2 hours ago\n"), nil
		}
		logCalls++
		return []byte("2026-09-26T12:00:00.000000000Z final crash line\n"), nil
	}
	for range 3 {
		if err := Collect(context.Background(), path, run, testScope); err != nil {
			t.Fatal(err)
		}
	}
	if logCalls != 1 {
		t.Fatalf("stopped container was drained %d times", logCalls)
	}
	run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte(row + "Up 5 seconds\n"), nil
		}
		logCalls++
		return []byte("2026-09-26T12:00:01.000000000Z new life\n"), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "new life") || logCalls != 2 {
		t.Fatalf("restarted container was not re-drained: %s (calls=%d)", body, logCalls)
	}
}

func TestCollectScopesContainersAndSupervisorLinesToOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	dir := filepath.Dir(path)
	host := "reaped runtime for alice\n" +
		"acquire failed for bob/ctx: timeout\n" +
		"spawned hermes-context-deadbeef\n" // unattributed line about a foreign runtime
	if err := os.WriteFile(filepath.Join(dir, "supervisor.log"), []byte(host), 0600); err != nil {
		t.Fatal(err)
	}
	var logsIDs []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("own\thermes-hub-alice-dev-toolhub-1\thermes-hub-alice-dev\thermes-hub-alice-dev_default\tUp 1 hour\n" +
				"spawned\thermes-context-a1b2c3d4\t\thermes-hub-agent-alice-dev,hermes-hub-runtime\tUp 1 hour\n" +
				"foreign\thermes-hub-bob-dev-toolhub-1\thermes-hub-bob-dev\thermes-hub-bob-dev_default\tUp 1 hour\n" +
				"othernet\thermes-context-feedface\t\thermes-hub-agent-bob-dev\tUp 1 hour\n" +
				"partial\thermes-hub-alice2-dev-x\thermes-hub-alice2-dev\tnet\tUp 1 hour\n"), nil
		}
		logsIDs = append(logsIDs, args[len(args)-1])
		return []byte("2026-09-26T12:00:00.000000000Z line\n"), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"foreign", "othernet", "partial"} {
		if slices.Contains(logsIDs, id) {
			t.Fatalf("foreign container %s was collected: %v", id, logsIDs)
		}
	}
	for _, id := range []string{"own", "spawned"} {
		if !slices.Contains(logsIDs, id) {
			t.Fatalf("own container %s was skipped: %v", id, logsIDs)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "reaped runtime for alice") {
		t.Fatalf("owner supervisor line dropped:\n%s", body)
	}
	for _, foreign := range []string{"bob/ctx", "deadbeef"} {
		if strings.Contains(string(body), foreign) {
			t.Fatalf("foreign supervisor line leaked (%s):\n%s", foreign, body)
		}
	}
}

func TestCollectStalledDockerLogsDoesNotStarveCycle(t *testing.T) {
	previous := dockerCallTimeout
	dockerCallTimeout = 50 * time.Millisecond
	defer func() { dockerCallTimeout = previous }()
	path := filepath.Join(t.TempDir(), "hermes-diagnostics.txt")
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("stalled\thermes-stalled\thermes-hub-alice-dev\tnet\tUp 1 hour\n" +
				"healthy\thermes-healthy\thermes-hub-alice-dev\tnet\tUp 1 hour\n"), nil
		}
		if args[len(args)-1] == "stalled" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte("2026-09-26T12:00:00.000000000Z healthy\n"), nil
	}
	start := time.Now()
	err := Collect(context.Background(), path, run, testScope)
	if err == nil || !strings.Contains(err.Error(), "hermes-stalled") {
		t.Fatalf("stalled container was not reported: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("stalled docker logs starved the cycle")
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "healthy") {
		t.Fatalf("stalled container starved a healthy one: %s %v", body, err)
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"connect ATATT3xFfGF0mNYB2nJ0srzVG0kKmVmqIlzWI failed":                           "ATATT",
		"key AIzaSyD4iE2xVSpkLLOXoyq2uexnEPLwDcxBNz open":                                "AIza",
		"slack xoxe.xoxp-1-MiNTyT0kEn":                                                   "xoxe",
		"linear lin_api_AbCdEf123456":                                                    "lin_api",
		"stripe sk_live_51HxK2eLk":                                                       "sk_live",
		"sendgrid SG.AbCdEfGh12345.RaNdOm123456789":                                      "SG.",
		"bare wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY":                                    "wJalrXUtn",
		"commit 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 pushed": "",
		"container hermes-context-ecbc0f0382251589 ready":                                "",
		"line without secrets at all":                                                    "",
	}
	for in, wantGone := range cases {
		out := Redact(in)
		if wantGone == "" {
			if out != in {
				t.Fatalf("over-redacted %q -> %q", in, out)
			}
			continue
		}
		if strings.Contains(out, wantGone) || !strings.Contains(out, "<redacted>") {
			t.Fatalf("not redacted %q -> %q", in, out)
		}
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
