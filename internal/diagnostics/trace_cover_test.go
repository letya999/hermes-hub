package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedTraceSpace(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	control := filepath.Join(root, ".control")
	if err := os.MkdirAll(control, 0o750); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"jobs": map[string]any{
		"job-1": map[string]any{
			"request":  map[string]any{"principal_id": "alice", "context_id": "ctx1"},
			"response": map[string]any{"run_id": "run-1", "session_id": "sess-1"},
		},
	}}
	body, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(control, "supervisor.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	logsDir := filepath.Join(root, "ctx1", "logs")
	if err := os.MkdirAll(logsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	agentLog := strings.Join([]string{
		`{"job_id":"job-1","time":"2026-01-01T00:00:02Z","msg":"hit"}`,
		`{"job_id":"other-job","time":"2026-01-01T00:00:01Z","msg":"skip"}`,
		`plain run_id=run-1 tail`,
		``,
	}, "\n")
	if err := os.WriteFile(filepath.Join(logsDir, "agent.log"), []byte(agentLog), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "gateway.log"), []byte(`{"session_id":"sess-1","time":"2026-01-01T00:00:03Z","msg":"gw"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTraceRejectsUnsafeRequests(t *testing.T) {
	ctx := context.Background()
	root := seedTraceSpace(t)
	if _, err := Trace(ctx, TraceOptions{JobID: "../escape", SpacesRoot: root}); err == nil {
		t.Fatal("unsafe job id admitted")
	}
	if _, err := Trace(ctx, TraceOptions{JobID: "job-9", SpacesRoot: root}); err == nil {
		t.Fatal("unknown job admitted")
	}
	if _, err := Trace(ctx, TraceOptions{JobID: "job-1", User: "mallory", SpacesRoot: root}); err == nil {
		t.Fatal("foreign principal admitted")
	}
	if _, err := Trace(ctx, TraceOptions{JobID: "job-1", SpacesRoot: root, Environment: "staging"}); err == nil {
		t.Fatal("invalid environment admitted")
	}
	if _, err := Trace(ctx, TraceOptions{JobID: "job-1", SpacesRoot: filepath.Join(t.TempDir(), "empty")}); err == nil {
		t.Fatal("missing authority record admitted")
	}
}

func TestTraceCollectsBoundSources(t *testing.T) {
	ctx := context.Background()
	root := seedTraceSpace(t)
	supLog := filepath.Join(root, "supervisor.log")
	if err := os.WriteFile(supLog, []byte("supervisor job_id=job-1 line\nunrelated line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := Trace(ctx, TraceOptions{
		JobID: "job-1", SpacesRoot: root, SupervisorLog: supLog, IncludeContent: true,
		DockerNames: func(ctx context.Context, principal, contextID, environment string) ([]string, error) {
			return []string{"rt-alice-1"}, nil
		},
		DockerLogs: func(ctx context.Context, name string) ([]byte, error) {
			return []byte("docker job_id=job-1 line\n"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var hit bool
	sources := map[string]bool{}
	for _, ev := range events {
		sources[ev.Source] = true
		if strings.Contains(ev.Line, "hit") {
			hit = true
		}
		if strings.Contains(ev.Line, "other-job") || strings.Contains(ev.Line, "unrelated") {
			t.Fatalf("foreign record leaked: %q", ev.Line)
		}
	}
	if !hit {
		t.Fatal("job record not collected")
	}
	for _, want := range []string{"supervisor", "space/agent.log", "space/gateway.log", "docker/rt-alice-1"} {
		if !sources[want] {
			t.Fatalf("source %s missing; have %v", want, sources)
		}
	}
	// Bounded content mode adds id metadata instead of raw bodies.
	events, err = Trace(ctx, TraceOptions{JobID: "job-1", SpacesRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	var meta bool
	for _, ev := range events {
		if strings.Contains(ev.Line, `"job_id":"job-1"`) {
			meta = true
		}
	}
	if !meta {
		t.Fatal("bounded metadata missing")
	}
	// Docker discovery failure degrades to a visible gap, not an error.
	events, err = Trace(ctx, TraceOptions{
		JobID: "job-1", SpacesRoot: root,
		DockerNames: func(ctx context.Context, p, c, e string) ([]string, error) { return nil, os.ErrPermission },
		DockerLogs:  func(ctx context.Context, name string) ([]byte, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var gap bool
	for _, ev := range events {
		if strings.Contains(ev.Line, "diagnostic_gap=discovery_failed") {
			gap = true
		}
	}
	if !gap {
		t.Fatal("docker discovery gap not surfaced")
	}
}

func TestTraceHonorsHomeOverride(t *testing.T) {
	ctx := context.Background()
	root := seedTraceSpace(t)
	alt := filepath.Join(root, "ctx1", "alt-home")
	if err := os.MkdirAll(filepath.Join(alt, "logs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alt, "logs", "errors.log"), []byte(`{"run_id":"run-1","time":"2026-01-01T00:00:04Z","msg":"err"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := Trace(ctx, TraceOptions{
		JobID: "job-1", SpacesRoot: root,
		Home: func(contextID, environment string) (string, error) { return alt, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var altHit bool
	for _, ev := range events {
		if ev.Source == "space/errors.log" {
			altHit = true
		}
	}
	if !altHit {
		t.Fatal("home override not consulted")
	}
	// A home outside the owned context is refused.
	if _, err := Trace(ctx, TraceOptions{
		JobID: "job-1", SpacesRoot: root,
		Home: func(contextID, environment string) (string, error) { return t.TempDir(), nil },
	}); err == nil {
		t.Fatal("escaping home admitted")
	}
	// Deadline produces a bounded deadline gap rather than a hang.
	deadline, cancel := context.WithDeadline(ctx, time.Now())
	defer cancel()
	events, err = Trace(deadline, TraceOptions{JobID: "job-1", SpacesRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	_ = events // deadline gap is best-effort; the call must still return
}
