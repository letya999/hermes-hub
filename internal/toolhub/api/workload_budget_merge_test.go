package api

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkloadBudgetLifecycle(t *testing.T) {
	b, err := NewWorkloadBudget(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Acquire(ctx, "owner-a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire(ctx, "owner-a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate acquire: %v", err)
	}
	if err := b.Acquire(ctx, "bad id!"); err == nil {
		t.Fatal("invalid workload id accepted")
	}
	if !b.Touch("owner-a") || b.Touch("owner-b") {
		t.Fatal("touch contract broken")
	}
	active, queued := b.Snapshot()
	if len(active) != 1 || len(queued) != 0 || b.Capacity() != 1 {
		t.Fatalf("snapshot: %v %v cap=%d", active, queued, b.Capacity())
	}
	if !b.Release("owner-a") || b.Release("owner-a") {
		t.Fatal("release contract broken")
	}
	// queueing: fill slot, acquire second in goroutine, release to unblock
	if err := b.Acquire(ctx, "owner-a"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.Acquire(ctx, "owner-b") }()
	time.Sleep(50 * time.Millisecond)
	_, queued = b.Snapshot()
	if len(queued) != 1 || queued[0] != "owner-b" {
		t.Fatalf("queued: %v", queued)
	}
	b.Release("owner-a")
	if err := <-done; err != nil {
		t.Fatalf("queued acquire: %v", err)
	}
	b.Release("owner-b")
	// idle cleanup
	b.now = func() time.Time { return time.Now().Add(-2 * time.Minute) }
	if err := b.Acquire(ctx, "owner-c"); err != nil {
		t.Fatal(err)
	}
	expired := b.CleanupIdle(time.Now())
	if len(expired) != 1 || expired[0] != "owner-c" {
		t.Fatalf("expired: %v", expired)
	}
	// cancelled queued acquire
	if err := b.Acquire(ctx, "owner-d"); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	res := make(chan error, 1)
	go func() { res <- b.Acquire(cancelled, "owner-e") }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire: %v", err)
	}
	_, queued = b.Snapshot()
	if len(queued) != 0 {
		t.Fatalf("cancelled entry remained queued: %v", queued)
	}
	if _, err := NewWorkloadBudget(0, time.Minute); err == nil {
		t.Fatal("zero budget accepted")
	}
	var nilB *WorkloadBudget
	if nilB.Release("x") || nilB.Touch("x") || nilB.CleanupIdle(time.Now()) != nil || nilB.Capacity() != 0 {
		t.Fatal("nil budget misbehaved")
	}
	if err := nilB.Acquire(ctx, "x"); err == nil {
		t.Fatal("nil acquire accepted")
	}
}
