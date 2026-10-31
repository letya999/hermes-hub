package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCollect5001LinesWithoutSkip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostics.txt")
	var all []string
	for i := range 5001 {
		all = append(all, fmt.Sprintf("2026-10-28T12:00:00.%09dZ ordinal-%04d", i, i))
	}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("own\tfixture\thermes-hub-alice-dev\tnet\tUp\n"), nil
		}
		lines := all
		for i, arg := range args {
			if arg == "--since" {
				var filtered []string
				for _, line := range lines {
					stamp, _, _ := strings.Cut(line, " ")
					if stamp >= args[i+1] {
						filtered = append(filtered, line)
					}
				}
				lines = filtered
			}
			if arg == "--tail" {
				n, _ := strconv.Atoi(args[i+1])
				if len(lines) > n {
					lines = lines[len(lines)-n:]
				}
			}
		}
		return []byte(strings.Join(lines, "\n") + "\n"), nil
	}
	for range 2 {
		if err := Collect(context.Background(), path, run, testScope); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ordinal-0000", "ordinal-4999", "ordinal-5000"} {
		if strings.Count(string(body), id) != 1 {
			t.Errorf("%s count=%d, want 1", id, strings.Count(string(body), id))
		}
	}
}

func TestCollectEqualTimestampsAcrossCycles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostics.txt")
	round := 0
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("own\tfixture\thermes-hub-alice-dev\tnet\tUp\n"), nil
		}
		body := "2026-10-28T12:00:00Z first-tie\n"
		if round > 0 {
			body += "2026-10-28T12:00:00Z second-tie\n"
		}
		round++
		return []byte(body), nil
	}
	for range 3 {
		if err := Collect(context.Background(), path, run, testScope); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first-tie", "second-tie"} {
		if strings.Count(string(body), id) != 1 {
			t.Errorf("%s count=%d, want 1", id, strings.Count(string(body), id))
		}
	}
}

func TestRotateKeepsBoundedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.log")
	for i := range 5 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.WriteString(fmt.Sprint("generation-", i)); err != nil {
			t.Fatal(err)
		}
		if err = f.Truncate(10 << 20); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = (&boundedFile{path: path}).Write([]byte("next alice\n")); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(path + ".1")
	if err != nil {
		t.Fatal("previous generation lost:", err)
	}
	defer f.Close()
	prefix := make([]byte, 12)
	if _, err = f.Read(prefix); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(prefix), "generation-4") {
		t.Fatal("rotation did not preserve prior bytes")
	}
	if _, err = os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Fatal("rotated file count exceeded bound")
	}
}

func TestHostRotationResumesByGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.txt")
	host := diagHostFixture(t, dir)
	if err := os.WriteFile(host, []byte("alice before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(context.Context, ...string) ([]byte, error) { return nil, nil }
	if err := Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(host, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(10 << 20); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = (&boundedFile{path: host}).Write([]byte("alice after-rotation-with-a-longer-record\n")); err != nil {
		t.Fatal(err)
	}
	if err = Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "after-rotation-with-a-longer-record") {
		t.Fatal("new host generation skipped", err)
	}
}

func TestTraceUnknownOwnerCannotScanForeignContainers(t *testing.T) {
	opts := TraceOptions{JobID: "unknown-job", SpacesRoot: t.TempDir(), User: "alice"}
	called := false
	opts.DockerNames = func(context.Context, string, string, string) ([]string, error) {
		called = true
		return []string{"foreign"}, nil
	}
	opts.DockerLogs = func(context.Context, string) ([]byte, error) {
		called = true
		return []byte("unknown-job foreign material"), nil
	}
	if _, err := Trace(context.Background(), opts); err == nil {
		t.Error("unknown authority accepted")
	}
	if called {
		t.Fatal("unknown owner caused container discovery")
	}
}

func TestTraceExactID(t *testing.T) {
	if traceMatches(`{"job_id":"job-10"}`, map[string]bool{"job-1": true}) {
		t.Fatal("substring matched foreign job")
	}
	if !traceMatches(`{"job_id":"job-1"}`, map[string]bool{"job-1": true}) {
		t.Fatal("exact job missing")
	}
}

func TestTraceMetadataOnlyAndBoundedOutput(t *testing.T) {
	opts, spaces := traceFixture(t)
	opts.IncludeContent = false
	path := filepath.Join(spaces, "alice", "logs", "agent.log")
	var rows strings.Builder
	for range 1100 {
		rows.WriteString(`{"time":"2026-10-28T12:00:00.123456789Z","job_id":"job-1","prompt":"synthetic-private-content","synthetic-private-key":"must-stay-local"}` + "\n")
	}
	if err := os.WriteFile(path, []byte(rows.String()), 0600); err != nil {
		t.Fatal(err)
	}
	events, err := Trace(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) > 1000 {
		t.Fatalf("trace result unbounded: %d", len(events))
	}
	var out strings.Builder
	for _, event := range events {
		out.WriteString(event.Line + "\n")
	}
	for _, forbidden := range []string{"synthetic-private-content", "synthetic-private-key", "must-stay-local"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("private field leaked: %s", forbidden)
		}
	}
	if !strings.Contains(out.String(), "job-1") || !strings.Contains(out.String(), "output_limited") {
		t.Fatal("bounded result omitted metadata or explicit gap")
	}
}

func TestTraceManagedEnvironmentLogsAndDedup(t *testing.T) {
	opts, spaces := traceFixture(t)
	opts.Environment = "dev"
	opts.Home = func(contextID, environment string) (string, error) {
		if contextID != "alice" || environment != "dev" {
			t.Fatal("wrong home scope")
		}
		return filepath.Join(spaces, contextID, "managed", environment, "hermes"), nil
	}
	home := filepath.Join(spaces, "alice", "managed", "dev", "hermes", "logs")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "agent.log"), []byte("2026-10-28T12:00:00.123456789Z run-9 selected-dev-home\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prod := filepath.Join(spaces, "alice", "managed", "prod", "hermes", "logs")
	if err := os.MkdirAll(prod, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prod, "agent.log"), []byte("2026-10-28T12:00:00Z run-9 foreign-production-canary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reads := 0
	opts.DockerNames = func(_ context.Context, principal, contextID, environment string) ([]string, error) {
		if principal != "alice" || contextID != "alice" || environment != "dev" {
			t.Fatal("wrong discovery scope")
		}
		return []string{"owned", "owned"}, nil
	}
	opts.DockerLogs = func(context.Context, string) ([]byte, error) {
		reads++
		return []byte("2026-10-28T12:00:01Z run-9 container-record\n"), nil
	}
	events, err := Trace(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, event := range events {
		out.WriteString(event.Line + "\n")
	}
	if !strings.Contains(out.String(), "selected-dev-home") || strings.Contains(out.String(), "foreign-production-canary") {
		t.Fatal("environment home isolation failed")
	}
	if reads != 1 || strings.Count(out.String(), "container-record") != 1 {
		t.Fatal("duplicate source read")
	}
}

func TestTraceRejectsForeignJobDespiteSharedSession(t *testing.T) {
	if traceMatches(`{"job_id":"foreign-job","session_id":"sess-4"}`, map[string]bool{"job-1": true, "sess-4": true}) {
		t.Fatal("foreign job matched a shared session")
	}
}

func TestDiagnosticProducerAllowlist(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)
	Logger("supervisor").Info("synthetic-private-message", "job_id", "job-1", "synthetic-private-key", "synthetic-private-value", "args", map[string]string{"dictionary-key-canary": "dictionary-value-canary"}, "level", map[string]string{"dictionary-level-canary": "level-value-canary"})
	body := buf.String()
	if !strings.Contains(body, "job-1") {
		t.Fatal("required correlation field missing")
	}
	for _, forbidden := range []string{"synthetic-private", "dictionary-key-canary", "dictionary-value-canary", "dictionary-level-canary", "level-value-canary", "args"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("producer emitted private field: %s", forbidden)
		}
	}
}

func TestCollectCrashBeforeCursorReplaysSamePosition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostics.txt")
	attempt := 0
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("own\tfixture\thermes-hub-alice-dev\tnet\tUp\n"), nil
		}
		if attempt == 0 {
			if err := os.Mkdir(path+".cursor.json", 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path+".cursor.json", "block"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		attempt++
		return []byte("2026-10-28T12:00:00Z replay-canary\n"), nil
	}
	if err := Collect(context.Background(), path, run, testScope); err == nil {
		t.Fatal("cursor commit failure ignored")
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Count(string(data), "replay-canary") != 1 {
		t.Fatal("fault was not after data persistence")
	}
	if err = os.Remove(filepath.Join(path+".cursor.json", "block")); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path + ".cursor.json"); err != nil {
		t.Fatal(err)
	}
	if err = Collect(context.Background(), path, run, testScope); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var positions []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "replay-canary") {
			for _, field := range strings.Fields(line) {
				if strings.HasPrefix(field, "source_position=") {
					positions = append(positions, field)
				}
			}
		}
	}
	if len(positions) != 2 || positions[0] != positions[1] {
		t.Fatalf("replay lacks stable source identity: %v", positions)
	}
}

func TestStoppedUnreadableContainerReportsGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostics.txt")
	reads := 0
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("own\tfixture\thermes-hub-alice-dev\tnet\tExited (1)\n"), nil
		}
		reads++
		return nil, errors.New("synthetic unreadable source")
	}
	for range 2 {
		if err := Collect(context.Background(), path, run, testScope); err == nil {
			t.Fatal("source failure declared drained")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "diagnostic_gap=unreadable_stopped") {
		t.Fatal("source loss not visible")
	}
	body, err := os.ReadFile(path + ".cursor.json")
	if err != nil {
		t.Fatal(err)
	}
	var saved cursor
	if err = json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Drained["own"] || saved.Containers["own"] == containerDone || saved.Gaps["own"] != "unreadable_stopped" {
		t.Fatal("failed source marked fully drained")
	}
	if reads != 1 {
		t.Fatal("unchanged dead source retried without a state change")
	}
}

func TestStreamDiagnosticLinesBoundsAndControlledStop(t *testing.T) {
	called := 0
	err := streamDiagnosticLines(strings.NewReader("one\ntwo\nthree\n"), func(line string) error {
		called++
		if called == 2 {
			return errDiagnosticBatch
		}
		return nil
	})
	if !errors.Is(err, errDiagnosticBatch) || called != 2 {
		t.Fatal("controlled stop lost its boundary")
	}
	called = 0
	err = streamDiagnosticLines(strings.NewReader(strings.Repeat("x", maxDiagnosticLine+1)), func(string) error { called++; return nil })
	if !errors.Is(err, errDiagnosticLine) || called != 0 {
		t.Fatal("oversized source line accepted")
	}
}

func TestNativeCollectorUsesBoundedStreamWithFixtureExecutable(t *testing.T) {
	bin := t.TempDir()
	name := "docker"
	script := "#!/bin/sh\nif [ \"$1\" = ps ]; then printf 'own\\tfixture\\thermes-hub-alice-dev\\tnet\\tUp\\n'; else printf '2026-10-28T12:00:00Z {\"job_id\":\"job-1\",\"event\":\"fixture\",\"text\":\"private-body-canary\"}\\n'; fi\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\nif \"%1\"==\"ps\" (\r\necho own\tfixture\thermes-hub-alice-dev\tnet\tUp\r\nexit /b 0\r\n)\r\necho 2026-10-28T12:00:00Z {\"job_id\":\"job-1\",\"event\":\"fixture\",\"text\":\"private-body-canary\"}\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(t.TempDir(), "diagnostics.txt")
	scope := testScope
	scope.IncludeContent = false
	for range 2 {
		if err := Collect(context.Background(), path, nil, scope); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	streamErr := dockerLines(context.Background(), func(string) error { seen++; return errDiagnosticBatch }, "logs", "own")
	if !errors.Is(streamErr, errDiagnosticBatch) || seen != 1 {
		t.Fatal("native controlled stop failed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "job-1") != 1 || strings.Contains(string(data), "private-body-canary") {
		t.Fatal("stream wiring lost cursor or content isolation")
	}
}

func TestDiagnosticRetentionOnlyPrunesOwnedSegments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostic.log")
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, suffix := range []string{".1", ".unknown"} {
		name := path + suffix
		if err := os.WriteFile(name, []byte("old fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendDiagnostic(path, []byte("fresh"), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("expired owned segment retained")
	}
	if _, err := os.Stat(path + ".unknown"); err != nil {
		t.Fatal("unknown file removed")
	}
}

func TestOversizedHostRecordOmitsContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor.log")
	body := []byte(strings.Repeat("private-canary-", 3000))
	n, err := (&boundedFile{path: path}).Write(body)
	if err != nil || n != len(body) {
		t.Fatal("oversized log blocked producer")
	}
	saved, err := os.ReadFile(path)
	if err != nil || len(saved) > maxDiagnosticLine || strings.Contains(string(saved), "private-canary") {
		t.Fatal("oversized content persisted")
	}
}

func TestCollectSerializesCursorWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostics.txt")
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			close(entered)
			<-release
			return []byte("own\tfixture\thermes-hub-alice-dev\tnet\tUp\n"), nil
		}
		return []byte("2026-10-28T12:00:00Z owner-writer\n"), nil
	}
	go func() { result <- Collect(context.Background(), path, run, testScope) }()
	<-entered
	err := Collect(context.Background(), path, func(context.Context, ...string) ([]byte, error) {
		t.Error("contended writer queried sources")
		return nil, nil
	}, testScope)
	close(release)
	firstErr := <-result
	if err == nil || firstErr != nil {
		t.Fatalf("writer fence outcomes: second=%v first=%v", err, firstErr)
	}
}

func TestReadDiagnosticTailAcrossRotationsWithinBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostic.log")
	for suffix, data := range map[string]string{"": "newest-line\n", ".1": "older-line\n", ".2": "oldest-line\n"} {
		if err := os.WriteFile(path+suffix, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, truncated, err := ReadDiagnosticTail(path, 1024)
	if err != nil || truncated {
		t.Fatal("complete tail unexpectedly partial", err)
	}
	if strings.Index(string(data), "oldest-line") > strings.Index(string(data), "newest-line") {
		t.Fatal("generations merged out of order")
	}
	data, truncated, err = ReadDiagnosticTail(path, 24)
	if err != nil || !truncated || len(data) > 24 || !strings.Contains(string(data), "newest-line") {
		t.Fatalf("tail bound/gap wrong: %q truncated=%v err=%v", data, truncated, err)
	}
}

func TestDiagnosticSourcesRejectSymlinkAliases(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "synthetic-sensitive-file")
	if err := os.WriteFile(secret, []byte("0123456789abcdef0123456789abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "diagnostic.cursor.json")
	if err := os.Symlink(secret, alias); err != nil {
		t.Skip("symlink fixture unavailable on this host")
	}
	if _, err := readDiagnosticFile(alias, 128); err == nil {
		t.Fatal("diagnostic source followed an alias")
	}
}

func TestHostLogRejectsDirectoryAliasWithoutWideningPermissions(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "logs-alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Skip("symlink fixture unavailable")
	}
	stop, err := HostLog(filepath.Join(alias, "supervisor.log"))
	if err == nil {
		stop()
		t.Error("host logger accepted directory alias")
	}
	info, statErr := os.Stat(private)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Fatal("logger widened another directory's permissions")
	}
}

func TestDiagnosticMetadataRejectsProseInCodeFields(t *testing.T) {
	line := `{"job_id":"owned-job","event":"private prose canary","reason_code":"private prose canary","method":"PRINT private-prose-canary","route":"/v1/jobs/private-id-canary?token=private-query-canary"}`
	projected := ProjectDiagnosticLine(line, false)
	for _, value := range []string{"private prose canary", "private-prose-canary", "private-id-canary", "private-query-canary"} {
		if strings.Contains(projected, value) {
			t.Errorf("free content survived a metadata field: %s", value)
		}
	}
	if !strings.Contains(projected, "owned-job") || !strings.Contains(projected, "/v1/jobs") {
		t.Fatal("safe correlation/route missing")
	}
}

func TestDiagnosticReaderRejectsDirectoryAlias(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "supervisor.log"), []byte("private-file-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "host-alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Skip("symlink fixture unavailable")
	}
	if _, err := readDiagnosticFile(filepath.Join(alias, "supervisor.log"), 128); err == nil {
		t.Fatal("diagnostic reader followed a directory alias")
	}
}
