package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
)

func auditSpace(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tenant-a")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAuditUsageAndFlagValidation(t *testing.T) {
	ctx := context.Background()
	if err := runAudit(ctx, nil); err == nil {
		t.Fatal("bare audit must print usage")
	}
	if err := runAudit(ctx, []string{"bogus"}); err == nil {
		t.Fatal("unknown subcommand must print usage")
	}
	if err := runAudit(ctx, []string{"pending", "--operation", "x"}); err == nil {
		t.Fatal("pending must reject resolve flags")
	}
	if err := runAudit(ctx, []string{"resolve", "--operation", "op-1"}); err == nil {
		t.Fatal("resolve requires outcome and evidence")
	}
	if err := runAudit(ctx, []string{"resolve", "--operation", "op-1", "--outcome", "succeeded", "--evidence", "r", "extra"}); err == nil {
		t.Fatal("resolve must reject positional arguments")
	}
}

func TestAuditPendingAndResolveOnRealLedger(t *testing.T) {
	dir := auditSpace(t)
	ctx := context.Background()
	ledgerPath := filepath.Join(dir, "runtime", "toolhub", "audit.jsonl")

	ledger, err := audit.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	op, err := ledger.Begin(audit.Event{
		EventID: "evt-pending-1", Kind: "tool-call", PrincipalID: "me",
		Action: "write", At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = op

	if err := runAudit(ctx, []string{"pending", "--dir", dir, "--user", "me"}); err != nil {
		t.Fatalf("pending failed on a real ledger: %v", err)
	}
	if err := runAudit(ctx, []string{"resolve", "--dir", dir, "--user", "me",
		"--operation", "missing-op", "--outcome", "failed", "--evidence", "receipt-1"}); err == nil {
		t.Fatal("resolve must surface the ledger error for an unknown operation")
	}
	if err := runAudit(ctx, []string{"pending", "--dir", dir, "--user", "me"}); err != nil {
		t.Fatalf("pending must keep working after a failed resolve: %v", err)
	}
}

func TestAuditRolloverPlanAndDigestGuard(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "audit.jsonl")
	legacy, err := os.OpenFile(ledgerPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(audit.Event{
		Schema: 1, EventID: "evt-1", At: time.Now().UTC(), Kind: "tool-call",
		PrincipalID: "me", Outcome: "succeeded",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Write(append(record, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	if err := runAuditRollover("en", []string{"--ledger", ledgerPath}); err != nil {
		t.Fatalf("rollover plan failed on a real legacy ledger: %v", err)
	}
	if err := runAuditRollover("en", []string{"--ledger", ledgerPath, "--apply", "--digest", "wrong-digest"}); err == nil {
		t.Fatal("apply must refuse a mismatched digest")
	}
	if err := runAuditRollover("en", nil); err == nil {
		t.Fatal("rollover without --ledger must print usage")
	}
	if err := runAuditRollover("en", []string{"--ledger", ledgerPath, "--apply"}); err == nil {
		t.Fatal("apply without --digest must print usage")
	}
}
