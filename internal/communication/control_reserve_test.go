package communication

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// Ordinary reply deliveries are capped so a flood cannot fill the outbox and
// starve control-plane sends (approvals, cancel-acks, hold notices). Control
// deliveries carry a reserved lane: they still enqueue past the ordinary cap
// and claim ahead of ordinary traffic.
func TestControlReserveUnderLoad(t *testing.T) {
	t.Setenv("HUB_SPOOL_MAX_DELIVERIES", "3")
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(id string, control bool) error {
		return spool.EnqueueDelivery(Delivery{ID: id, Channel: "telegram_bot", ChatID: 11, Text: "x", Control: control, CreatedAt: time.Now().UTC()})
	}
	for _, id := range []string{"d-01", "d-02", "d-03"} {
		if err := enqueue(id, false); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	if err := enqueue("d-04", false); err == nil {
		t.Fatal("ordinary delivery cap not enforced")
	}
	if err := enqueue("z-ctl", true); err != nil {
		t.Fatalf("control delivery refused under ordinary load: %v", err)
	}
	// Approval prompts are control plane by contract even when the caller
	// never sets the flag.
	if err := spool.EnqueueDelivery(Delivery{ID: "y-appr", ApprovalID: "appr-1", Channel: "telegram_bot", ChatID: 11, Text: "approve?", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("approval refused under ordinary load: %v", err)
	}
	d, err := spool.ClaimDelivery()
	if err != nil {
		t.Fatal(err)
	}
	if d == nil || d.ID != "y-appr" {
		t.Fatalf("control delivery did not claim first: %+v", d)
	}
	if err := spool.CompleteDelivery("y-appr"); err != nil {
		t.Fatal(err)
	}
	next, err := spool.ClaimDelivery()
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.ID != "z-ctl" {
		t.Fatalf("control delivery not claimed before ordinary: %+v", next)
	}
	if err := spool.CompleteDelivery("z-ctl"); err != nil {
		t.Fatal(err)
	}
	last, err := spool.ClaimDelivery()
	if err != nil {
		t.Fatal(err)
	}
	if last == nil || last.ID != "d-01" {
		t.Fatalf("ordinary delivery order broken: %+v", last)
	}
}

// Background work (cron) always ranks behind interactive jobs inside one
// scope; a cron job parked longer than the aging bound counts as foreground
// so a busy scope can never starve scheduled work forever.
func TestBackgroundAging(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(id, trigger string) {
		job := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: id, ScopeID: "user:alice", Channel: "telegram_bot", Trigger: trigger, IdempotencyKey: "key-" + id, CreatedAt: time.Now().UTC()}
		if ok, err := spool.Enqueue(job); err != nil || !ok {
			t.Fatalf("enqueue %s: ok=%v err=%v", id, ok, err)
		}
	}
	// "a-fresh" sorts first by name and stays background; "b-old" has been
	// parked in pending past the aging bound and is promoted to foreground.
	enqueue("a-fresh", "cron")
	enqueue("b-old", "cron")
	parked := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(filepath.Join(spool.root, "pending", "b-old.json"), parked, parked); err != nil {
		t.Fatal(err)
	}
	job, err := spool.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.ID != "b-old" {
		t.Fatalf("aged background job not promoted: %+v", job)
	}
}
