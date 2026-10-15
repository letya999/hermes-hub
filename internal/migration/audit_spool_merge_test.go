package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
)

// AuditExecutionSpool counts only the owner's settled work: claimed jobs
// block executor transfer, foreign-user entries are skipped, terminal jobs
// need their mapping, and in-flight outbox entries are uncertain.
func TestAuditExecutionSpoolOwnershipAndSettlement(t *testing.T) {
	root := t.TempDir()
	spoolDir := filepath.Join(root, "spool")
	spool, err := communication.NewSpool(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	jobFor := func(user, id string) communication.Job {
		return communication.Job{Envelope: identity.TelegramEnvelope(user, 11, user, "policy-1"), ID: id, IdempotencyKey: id, OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", Trigger: "message", ChatID: 11, Text: "input", CreatedAt: time.Now()}
	}
	if _, err := spool.Enqueue(jobFor("alice", "claimed")); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Enqueue(jobFor("bob", "foreign")); err != nil {
		t.Fatal(err)
	}
	if err := spool.EnqueueDelivery(communication.Delivery{ID: "out1", ChatID: 11, Text: "delivery"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := spool.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v", err)
	}
	for claimed.UserID != "alice" {
		if err := spool.CompleteJob(claimed.ID); err != nil {
			t.Fatal(err)
		}
		claimed, err = spool.ClaimJob()
		if err != nil || claimed == nil {
			t.Fatalf("claim: %v", err)
		}
	}
	if d, err := spool.ClaimDelivery(); err != nil || d == nil {
		t.Fatalf("claim delivery: %v", err)
	}

	// A claimed (running) job must settle before the executor can change.
	if _, err := AuditExecutionSpool(spoolDir, "alice"); err == nil {
		t.Fatal("running job allowed executor change")
	}
	if err := spool.CompleteJob(claimed.ID); err != nil {
		t.Fatal(err)
	}
	audit, err := AuditExecutionSpool(spoolDir, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if audit.Terminal != 1 || audit.Pending != 0 || audit.UncertainDeliveries != 1 {
		t.Fatalf("audit: %+v", audit)
	}

	// A pending job without a mapping is ambiguous, never silently countable.
	spool2Dir := filepath.Join(root, "spool2")
	if _, err := communication.NewSpool(spool2Dir); err != nil {
		t.Fatal(err)
	}
	orphan := jobFor("alice", "orphan")
	body, _ := json.Marshal(orphan)
	if err := os.WriteFile(filepath.Join(spool2Dir, "pending", "orphan.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := AuditExecutionSpool(spool2Dir, "alice"); err == nil {
		t.Fatal("mapping-less pending job accepted")
	}
	if _, err := AuditExecutionSpool(spool2Dir, "Bad User"); err == nil {
		t.Fatal("invalid user accepted")
	}
	if _, err := AuditExecutionSpool(filepath.Join(root, "missing"), "alice"); err == nil {
		t.Fatal("missing spool accepted")
	}
}
