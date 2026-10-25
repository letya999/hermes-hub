package communication

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadUsageJournalAggregates(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	var body string
	for _, l := range []string{
		`{"version":2,"scope_id":"user:me","actor_id":"me","job_id":"j1","run_id":"r1","model":"m1","input_tokens":100,"output_tokens":50,"duration_ms":3000,"measured_at":"` + now.Format(time.RFC3339Nano) + `"}`,
		`{"version":2,"scope_id":"user:me","actor_id":"me","job_id":"j2","run_id":"r1","model":"m1","input_tokens":10,"output_tokens":5,"measured_at":"` + now.Format(time.RFC3339Nano) + `"}`,
		`{"version":2,"scope_id":"user:other","actor_id":"other","job_id":"j3","run_id":"r1","model":"m2","input_tokens":7,"output_tokens":3,"measured_at":"` + now.Add(-48*time.Hour).Format(time.RFC3339Nano) + `"}`,
		`{"scope_id":"user:me","actor_id":"me","job_id":"j4","run_id":"r1","measured_at":"` + now.Format(time.RFC3339Nano) + `"}`,
	} {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, "usage.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	records, err := ReadUsageJournal(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("records=%d want 4", len(records))
	}
	report := AggregateUsage(records, now.Add(-24*time.Hour), "", "")
	if report.Totals.Jobs != 3 {
		t.Fatalf("windowed jobs=%d want 3 (48h-old excluded)", report.Totals.Jobs)
	}
	if report.Totals.InputTokens != 110 || report.Totals.OutputTokens != 55 {
		t.Fatalf("totals=%+v want 110/55", report.Totals)
	}
	if report.Totals.RuntimeMS != 3000 {
		t.Fatalf("runtime_ms=%d want 3000", report.Totals.RuntimeMS)
	}
	if report.Totals.LegacyJobs != 1 {
		t.Fatalf("legacy=%d want 1", report.Totals.LegacyJobs)
	}
	if got := report.Scopes["user:me"]; got.Jobs != 3 || got.InputTokens != 110 {
		t.Fatalf("scope me=%+v", got)
	}
	if got := report.Actors["me"]; got.Jobs != 3 {
		t.Fatalf("actor me=%+v", got)
	}
	if _, ok := report.Scopes["user:other"]; ok {
		t.Fatal("out-of-window scope leaked")
	}
	if got := AggregateUsage(records, time.Time{}, "", "m2"); got.Totals.Jobs != 1 || got.Totals.InputTokens != 7 {
		t.Fatalf("model filter=%+v", got.Totals)
	}
	if got := AggregateUsage(records, time.Time{}, "other", ""); got.Totals.Jobs != 1 {
		t.Fatalf("actor filter=%+v", got.Totals)
	}
}

func TestReadUsageJournalTornTail(t *testing.T) {
	root := t.TempDir()
	body := "{\"version\":2,\"scope_id\":\"user:me\",\"actor_id\":\"me\",\"job_id\":\"j1\",\"run_id\":\"r1\",\"input_tokens\":1,\"output_tokens\":1}\n{\"version\":2,\"scope_id\":\"user"
	if err := os.WriteFile(filepath.Join(root, "usage.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	records, err := ReadUsageJournal(root)
	if err != nil {
		t.Fatalf("torn tail must be skipped: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%d want 1", len(records))
	}
}

func TestReadUsageJournalCorruptCompletedLine(t *testing.T) {
	root := t.TempDir()
	body := "{\"version\":2,\"job_id\":\"j1\",\"run_id\":\"r1\"}\nnot-json\n{\"version\":2,\"job_id\":\"j2\",\"run_id\":\"r1\"}\n"
	if err := os.WriteFile(filepath.Join(root, "usage.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUsageJournal(root); err == nil {
		t.Fatal("corrupt completed record must fail closed")
	}
}

func TestReadUsageJournalMissing(t *testing.T) {
	records, err := ReadUsageJournal(t.TempDir())
	if err != nil || len(records) != 0 {
		t.Fatalf("missing journal must be an empty report, got %v %v", records, err)
	}
}

func TestAggregateUsageUnknownAndUntimed(t *testing.T) {
	records := []UsageRecord{
		{JobID: "j1", RunID: "r1", ScopeID: "user:me", ActorID: "me", Version: 2, MeasuredAt: time.Now().UTC(), InitialSnapshot: true},
		{JobID: "j2", RunID: "r1", ScopeID: "user:me", Version: 2},
	}
	report := AggregateUsage(records, time.Now().UTC().Add(-time.Hour), "", "")
	if report.Totals.UnknownJobs != 2 || report.Totals.InitialSnapshots != 1 {
		t.Fatalf("unknown=%d initial=%d want 2/1", report.Totals.UnknownJobs, report.Totals.InitialSnapshots)
	}
	if report.Totals.Jobs != 2 {
		t.Fatalf("untimed record dropped from window: jobs=%d want 2", report.Totals.Jobs)
	}
	if _, ok := report.Actors[""]; ok {
		t.Fatal("empty actor aggregated")
	}
}
