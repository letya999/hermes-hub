package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
)

func TestUsageCommandAggregates(t *testing.T) {
	spool := t.TempDir()
	now := time.Now().UTC()
	body := "{\"version\":2,\"scope_id\":\"user:me\",\"actor_id\":\"me\",\"job_id\":\"j1\",\"run_id\":\"r1\",\"input_tokens\":40,\"output_tokens\":10,\"measured_at\":\"" + now.Format(time.RFC3339Nano) + "\"}\n" +
		"{\"version\":2,\"scope_id\":\"user:me\",\"actor_id\":\"me\",\"job_id\":\"j2\",\"run_id\":\"r1\",\"input_tokens\":4,\"output_tokens\":1,\"measured_at\":\"" + now.Format(time.RFC3339Nano) + "\"}\n"
	if err := os.WriteFile(filepath.Join(spool, "usage.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runUsage(&out, spool, 24*time.Hour, "", ""); err != nil {
		t.Fatalf("usage: %v", err)
	}
	var report communication.UsageReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("report not JSON: %v", err)
	}
	if report.Totals.Jobs != 2 || report.Totals.InputTokens != 44 || report.Totals.OutputTokens != 11 {
		t.Fatalf("totals=%+v", report.Totals)
	}
	if report.Scopes["user:me"].Jobs != 2 || report.Actors["me"].Jobs != 2 {
		t.Fatalf("scopes=%v actors=%v", report.Scopes, report.Actors)
	}
}

func TestUsageDispatchRuns(t *testing.T) {
	spool := t.TempDir()
	if err := os.WriteFile(filepath.Join(spool, "usage.jsonl"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"usage", "--spool", spool, "--days", "7", "--actor", "me", "--model", "m1"}); err != nil {
		t.Fatalf("usage dispatch: %v", err)
	}
}

func TestUsageNeedsSpool(t *testing.T) {
	if err := run(t.Context(), []string{"usage"}); err == nil {
		t.Fatal("usage without --spool must fail")
	}
}

func TestUsageRejectsBadDays(t *testing.T) {
	spool := t.TempDir()
	if err := run(t.Context(), []string{"usage", "--spool", spool, "--days", "bogus"}); err == nil {
		t.Fatal("usage with bad --days must fail")
	}
}

func TestUsageRejectsPositionalArgs(t *testing.T) {
	spool := t.TempDir()
	if err := run(t.Context(), []string{"usage", "--spool", spool, "extra"}); err == nil {
		t.Fatal("usage with positional args must fail")
	}
}
