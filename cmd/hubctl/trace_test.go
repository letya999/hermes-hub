package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hubctl trace prints the merged, chronological, redacted feed for a job.
func TestRunTracePrintsMergedFeed(t *testing.T) {
	previous := traceDockerNames
	traceDockerNames = func(context.Context) ([]string, error) { return nil, nil }
	t.Cleanup(func() { traceDockerNames = previous })
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	if err := os.MkdirAll(filepath.Join(spaces, ".control"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(spaces, "alice", "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".local"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"jobs":{"job-1":{"request":{"principal_id":"alice"},"response":{"run_id":"run-9","session_id":"sess-4"},"status":"completed"}}}`
	if err := os.WriteFile(filepath.Join(spaces, ".control", "supervisor.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisorLog := filepath.Join(root, ".local", "supervisor.log")
	if err := os.WriteFile(supervisorLog, []byte("{\"time\":\"2026-01-02T03:04:01Z\",\"component\":\"supervisor\",\"msg\":\"admitted\",\"job_id\":\"job-1\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spaces, "alice", "logs", "agent.log"), []byte("2026-01-02T03:04:03Z run-9 reply sess-4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runTrace(context.Background(), &out, "job-1", "", spaces); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "admitted") || !strings.Contains(got, "reply") {
		t.Fatalf("merged feed missing sources: %q", got)
	}
	if strings.Index(got, "admitted") > strings.Index(got, "reply") {
		t.Fatalf("events out of order: %q", got)
	}
	if !strings.Contains(got, "supervisor") || !strings.Contains(got, "space/agent.log") {
		t.Fatalf("sources not labeled: %q", got)
	}
}

// `hubctl trace job-1 --spaces <dir>` reaches runTrace — the op accepts a
// positional job ID and an explicit spaces root.
func TestTraceCommandDispatches(t *testing.T) {
	previous := traceDockerNames
	traceDockerNames = func(context.Context) ([]string, error) { return nil, nil }
	t.Cleanup(func() { traceDockerNames = previous })
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	if err := os.MkdirAll(filepath.Join(spaces, ".control"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".local"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"jobs":{"job-1":{"request":{"principal_id":"alice"},"status":"completed"}}}`
	if err := os.WriteFile(filepath.Join(spaces, ".control", "supervisor.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisorLog := filepath.Join(root, ".local", "supervisor.log")
	if err := os.WriteFile(supervisorLog, []byte("{\"time\":\"2026-01-02T03:04:01Z\",\"component\":\"supervisor\",\"msg\":\"admitted\",\"job_id\":\"job-1\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = write
	runErr := run(context.Background(), []string{"trace", "--spaces", spaces, "job-1"})
	_ = write.Close()
	os.Stdout = old
	out, _ := io.ReadAll(read)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(string(out), "admitted") {
		t.Fatalf("trace output missing: %q", out)
	}
}

// Managed-container logs join the feed — a tool call audit line from the
// runtime container appears in the trace output.
func TestRunTraceIncludesContainerLogs(t *testing.T) {
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	if err := os.MkdirAll(filepath.Join(spaces, ".control"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".local"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"jobs":{"job-1":{"request":{"principal_id":"alice"},"response":{"run_id":"run-9"},"status":"completed"}}}`
	if err := os.WriteFile(filepath.Join(spaces, ".control", "supervisor.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	prevNames, prevLogs := traceDockerNames, traceDockerLogs
	traceDockerNames = func(ctx context.Context) ([]string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("Docker trace has no bounded deadline")
		}
		return []string{"hermes-context-aabb"}, nil
	}
	traceDockerLogs = func(_ context.Context, name string) ([]byte, error) {
		return []byte("2026-01-02T03:04:06Z run-9 tool call audited\n"), nil
	}
	defer func() { traceDockerNames, traceDockerLogs = prevNames, prevLogs }()
	var out bytes.Buffer
	if err := runTrace(context.Background(), &out, "job-1", "", spaces); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "audited") || !strings.Contains(out.String(), "docker/hermes-context-aabb") {
		t.Fatalf("container line missing: %q", out.String())
	}
}
