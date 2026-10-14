package diagnostics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixture: a supervisor state file, a supervisor host log, a user space with
// Hermes logs, and two fake containers — all mentioning job-1's lineage.
func traceFixture(t *testing.T) (opts TraceOptions, spaces string) {
	t.Helper()
	root := t.TempDir()
	spaces = filepath.Join(root, "spaces")
	state := filepath.Join(spaces, ".control")
	if err := os.MkdirAll(filepath.Join(spaces, "alice", "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".local"), 0o700); err != nil {
		t.Fatal(err)
	}
	supervisorJSON := `{"jobs":{"job-1":{"request":{"principal_id":"alice","context_id":"alice"},"response":{"run_id":"run-9","session_id":"sess-4"},"status":"completed","updated_at":"2026-01-02T03:04:05Z"}}}`
	if err := os.WriteFile(filepath.Join(state, "supervisor.json"), []byte(supervisorJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisorLog := filepath.Join(root, ".local", "supervisor.log")
	lines := []string{
		`{"time":"2026-01-02T03:04:07Z","component":"supervisor","msg":"job finished","job_id":"job-1","run_id":"run-9","principal":"alice"}`,
		`{"time":"2026-01-02T03:04:01Z","component":"supervisor","msg":"job admitted","job_id":"job-1","principal":"alice"}`,
		`{"time":"2026-01-02T03:04:02Z","component":"supervisor","msg":"unrelated","job_id":"job-2"}`,
	}
	if err := os.WriteFile(supervisorLog, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	agentLog := strings.Join([]string{
		"2026-01-02T03:04:05Z run-9 model reply sess-4",
		"2026-01-02T03:04:03Z run-9 admitted sess-4",
		"2026-01-02T03:04:04Z other-run noise",
	}, "\n")
	if err := os.WriteFile(filepath.Join(spaces, "alice", "logs", "agent.log"), []byte(agentLog), 0o600); err != nil {
		t.Fatal(err)
	}
	return TraceOptions{
		JobID:         "job-1",
		User:          "alice",
		SpacesRoot:    spaces,
		SupervisorLog: supervisorLog,
		DockerNames: func(context.Context) ([]string, error) {
			return []string{"hermes-context-aabb", "toolhub"}, nil
		},
		DockerLogs: func(_ context.Context, name string) ([]byte, error) {
			switch name {
			case "hermes-context-aabb":
				return []byte("2026-01-02T03:04:06Z job-1 delivered run-9"), nil
			default:
				return []byte("2026-01-02T03:04:06Z unrelated container line"), nil
			}
		},
	}, spaces
}

// Lines from every source merge into one chronological feed (SPEC-0053 R3.5).
func TestTraceMergesSourcesInOrder(t *testing.T) {
	opts, _ := traceFixture(t)
	events, err := Trace(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		got = append(got, e.Line)
	}
	want := []string{
		"job admitted",
		"admitted sess-4",
		"model reply sess-4",
		"delivered run-9",
		"job finished",
	}
	if len(got) != len(want) {
		t.Fatalf("events=%d want=%d: %v", len(got), len(want), got)
	}
	for i, fragment := range want {
		if !strings.Contains(got[i], fragment) {
			t.Fatalf("event %d = %q, want fragment %q (all: %v)", i, got[i], fragment, got)
		}
	}
}

// Secrets and credential-shaped material never leave the trace.
func TestTraceRedactsSecretsAndContent(t *testing.T) {
	opts, _ := traceFixture(t)
	logPath := filepath.Join(opts.SpacesRoot, "alice", "logs", "agent.log")
	b, _ := os.ReadFile(logPath)
	b = append(b, []byte("\n2026-01-02T03:04:08Z run-9 token=sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789 leak")...)
	if err := os.WriteFile(logPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := Trace(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, e := range events {
		joined.WriteString(e.Line + "\n")
	}
	if strings.Contains(joined.String(), "sk-proj") {
		t.Fatalf("secret leaked in trace: %s", joined.String())
	}
	if !strings.Contains(joined.String(), "<redacted>") {
		t.Fatalf("redaction marker missing: %s", joined.String())
	}
}

// A trace for another principal's job is refused — space logs of someone else
// never enter the output, even partially.
func TestTraceRefusesOtherPrincipal(t *testing.T) {
	opts, _ := traceFixture(t)
	opts.User = "bob"
	events, err := Trace(context.Background(), opts)
	if err == nil {
		t.Fatalf("trace for another principal was allowed: %v", events)
	}
}
