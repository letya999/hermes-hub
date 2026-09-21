package toolhub

import (
	"context"
	"testing"
	"time"
)

func TestWorkloadBudgetQueuesAndReclaimsIdle(t *testing.T) {
	b, err := NewWorkloadBudget(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire(context.Background(), "work-a"); err != nil {
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	go func() { ready <- b.Acquire(context.Background(), "work-b") }()
	select {
	case err := <-ready:
		t.Fatalf("queued workload acquired too early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	active, queued := b.Snapshot()
	if len(active) != 1 || len(queued) != 1 || queued[0] != "work-b" {
		t.Fatalf("unexpected budget snapshot active=%v queued=%v", active, queued)
	}
	if !b.Release("work-a") {
		t.Fatal("release failed")
	}
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	if !b.Touch("work-b") {
		t.Fatal("touch failed")
	}
	if got := b.Capacity(); got != 1 {
		t.Fatalf("capacity=%d", got)
	}
	if expired := b.CleanupIdle(time.Now().Add(2 * time.Minute)); len(expired) != 1 || expired[0] != "work-b" {
		t.Fatalf("expired=%v", expired)
	}
}

func TestWorkloadBudgetRejectsDuplicateAndCancelledQueue(t *testing.T) {
	if _, err := NewWorkloadBudget(0, time.Minute); err == nil {
		t.Fatal("zero budget accepted")
	}
	if _, err := NewWorkloadBudget(1001, time.Minute); err == nil {
		t.Fatal("oversized budget accepted")
	}
	if _, err := NewWorkloadBudget(1, 0); err == nil {
		t.Fatal("zero idle TTL accepted")
	}
	b, err := NewWorkloadBudget(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire(context.Background(), "work-a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire(context.Background(), "work-a"); err == nil {
		t.Fatal("duplicate workload accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Acquire(ctx, "work-b"); err == nil {
		t.Fatal("cancelled workload acquired")
	}
	if b.Release("work-missing") || b.Touch("work-missing") {
		t.Fatal("unknown workload mutated budget")
	}
	var nilBudget *WorkloadBudget
	if nilBudget.Capacity() != 0 {
		t.Fatal("nil budget capacity")
	}
	if active, queued := nilBudget.Snapshot(); active != nil || queued != nil || nilBudget.CleanupIdle(time.Now()) != nil {
		t.Fatal("nil budget snapshot")
	}
}
