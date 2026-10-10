package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func testJob(id, scope string) Job {
	return Job{ID: id, ScopeID: scope}
}

func TestUsageJournalDedupesAndTotals(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := hubruntime.SessionUsage{
		SessionFound:    true,
		SessionID:       "sess-1",
		InputTokens:     int64ptr(10),
		OutputTokens:    int64ptr(5),
		CacheReadTokens: int64ptr(2),
	}
	if err := s.RecordUsage(testJob("j-1", "user:alice"), "run-1", report); err != nil {
		t.Fatal(err)
	}
	// Same job and run recorded again (replay, restart): still one entry.
	if err := s.RecordUsage(testJob("j-1", "user:alice"), "run-1", report); err != nil {
		t.Fatal(err)
	}
	// A new attempt under the same job is a separate entry.
	report.InputTokens = int64ptr(4)
	if err := s.RecordUsage(testJob("j-1", "user:alice"), "run-2", report); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(testJob("j-2", "user:bob"), "run-3", report); err != nil {
		t.Fatal(err)
	}
	totals, err := s.UsageTotals("user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if totals.Jobs != 2 || totals.InputTokens != 14 || totals.OutputTokens != 10 {
		t.Fatalf("alice totals=%+v", totals)
	}
	totals, err = s.UsageTotals("user:bob")
	if err != nil {
		t.Fatal(err)
	}
	if totals.Jobs != 1 || totals.InputTokens != 4 {
		t.Fatalf("bob totals=%+v", totals)
	}
	// A fresh spool over the same directory keeps the journal (append-only durable).
	reopened, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.RecordUsage(testJob("j-1", "user:alice"), "run-1", report); err != nil {
		t.Fatal(err)
	}
	totals, err = reopened.UsageTotals("user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if totals.Jobs != 2 {
		t.Fatalf("journal lost across reopen: %+v", totals)
	}
}

// A completed run is metered once: the runner asks the measured usage and the
// journal records it against the job's scope. Metering never fails the job.
func TestRunOutcomeMetersTerminalJob(t *testing.T) {
	s, job := streamJob(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/execute":
			_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{
				Text: "reply", JobID: job.ID, SessionID: "session-1", RunID: "run-1",
				RuntimeGeneration: "gen-1", Status: "completed"})
		case "/v1/usage":
			_ = json.NewEncoder(w).Encode(hubruntime.SessionUsage{
				SessionFound: true, SessionID: "session-1",
				InputTokens: int64ptr(10), OutputTokens: int64ptr(5)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := HTTPRunner{URL: server.URL, Auth: "secret", HTTP: server.Client(), Limit: time.Minute, Spool: s}
	outcome, err := runner.RunOutcome(context.Background(), job)
	if err != nil || outcome.Text != "reply" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var totals UsageTotals
	for time.Now().Before(deadline) {
		if totals, err = s.UsageTotals("user:alice"); err == nil && totals.Jobs == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if totals.Jobs != 1 || totals.InputTokens != 10 || totals.OutputTokens != 5 {
		t.Fatalf("usage not journaled: %+v", totals)
	}

	// A usage endpoint that is down must not fail the run.
	job2 := job
	job2.ID, job2.IdempotencyKey = "unmetered-job", "unmetered-key"
	if _, err := s.Enqueue(job2); err != nil {
		t.Fatal(err)
	}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/execute" {
			_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{
				Text: "ok", JobID: job2.ID, SessionID: "session-1", RunID: "run-9",
				RuntimeGeneration: "gen-1", Status: "completed"})
			return
		}
		http.Error(w, "no meter", http.StatusBadGateway)
	}))
	defer broken.Close()
	outcome, err = (HTTPRunner{URL: broken.URL, Auth: "secret", HTTP: broken.Client(), Limit: time.Minute, Spool: s}).RunOutcome(context.Background(), job2)
	if err != nil || outcome.Text != "ok" {
		t.Fatalf("metering failure broke the job: %+v %v", outcome, err)
	}
}

func TestUsageCommandShowsJournalTotals(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	runner := &usageStubRunner{report: hubruntime.SessionUsage{SessionFound: true, InputTokens: int64ptr(3)}}
	g.runner = runner
	user := g.user("alice")
	if err := g.spool.RecordUsage(testJob("j-1", "user:alice"), "run-1", hubruntime.SessionUsage{SessionFound: true, InputTokens: int64ptr(10), OutputTokens: int64ptr(4)}); err != nil {
		t.Fatal(err)
	}
	task, err := g.spool.ResolveTask(user, 11, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.usageCommand(context.Background(), user, 11, task); !strings.Contains(got, "Всего по журналу: 1 запуск") || !strings.Contains(got, "вход 10") {
		t.Fatalf("journal totals missing: %s", got)
	}
}

func TestRunOutcomeMetersStreamedJob(t *testing.T) {
	s, job := streamJob(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/execute":
			w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
			event := streamEvent(job)
			_ = json.NewEncoder(w).Encode(event)
			event.Status, event.EventID, event.LastEvent, event.Text = "completed", "terminal:completed", "run.completed", "answer"
			_ = json.NewEncoder(w).Encode(event)
		case "/v1/usage":
			_ = json.NewEncoder(w).Encode(hubruntime.SessionUsage{
				SessionFound: true, SessionID: "session-1",
				InputTokens: int64ptr(7), OutputTokens: int64ptr(3)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	outcome, err := (HTTPRunner{URL: server.URL, Auth: "secret", HTTP: server.Client(), Limit: time.Minute, Spool: s}).RunOutcome(context.Background(), job)
	if err != nil || outcome.Text != "answer" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	totals, err := s.UsageTotals("user:alice")
	if err != nil || totals.Jobs != 1 || totals.InputTokens != 7 || totals.OutputTokens != 3 {
		t.Fatalf("streamed job not metered: %+v %v", totals, err)
	}
}
