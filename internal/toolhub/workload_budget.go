package toolhub

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// WorkloadBudget bounds live MCP processes independently from image reuse.
// Queue entries contain workload IDs only; credentials and user state never
// enter this control structure.
type WorkloadBudget struct {
	mu      sync.Mutex
	max     int
	idleTTL time.Duration
	active  map[string]time.Time
	queued  []string
	changed chan struct{}
	now     func() time.Time
}

func NewWorkloadBudget(maxActive int, idleTTL time.Duration) (*WorkloadBudget, error) {
	if maxActive < 1 || maxActive > 1000 || idleTTL <= 0 {
		return nil, fmt.Errorf("%w: invalid workload budget", ErrInvalid)
	}
	return &WorkloadBudget{max: maxActive, idleTTL: idleTTL, active: map[string]time.Time{}, changed: make(chan struct{}), now: time.Now}, nil
}

// Acquire waits in FIFO order until a slot is available. A caller must release
// the returned workload ID, or CleanupIdle will reclaim it after idleTTL.
func (b *WorkloadBudget) Acquire(ctx context.Context, workloadID string) error {
	if b == nil || workloadID == "" || !identity.ValidID(workloadID) {
		return fmt.Errorf("%w: workload ID", ErrInvalid)
	}
	enqueued := false
	for {
		b.mu.Lock()
		if _, ok := b.active[workloadID]; ok || (!enqueued && slices.Contains(b.queued, workloadID)) {
			b.mu.Unlock()
			return fmt.Errorf("%w: workload already scheduled", ErrConflict)
		}
		if len(b.active) < b.max && len(b.queued) == 0 {
			b.active[workloadID] = b.now()
			b.mu.Unlock()
			return nil
		}
		if !enqueued {
			b.queued = append(b.queued, workloadID)
			enqueued = true
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			b.mu.Lock()
			b.removeQueuedLocked(workloadID)
			b.signalLocked()
			b.mu.Unlock()
			return ctx.Err()
		case <-changed:
			b.mu.Lock()
			if len(b.queued) > 0 && b.queued[0] == workloadID && len(b.active) < b.max {
				b.queued = b.queued[1:]
				b.active[workloadID] = b.now()
				b.mu.Unlock()
				return nil
			}
			b.mu.Unlock()
		}
	}
}

func (b *WorkloadBudget) Release(workloadID string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.active[workloadID]; !ok {
		return false
	}
	delete(b.active, workloadID)
	b.signalLocked()
	return true
}

func (b *WorkloadBudget) Touch(workloadID string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.active[workloadID]; !ok {
		return false
	}
	b.active[workloadID] = b.now()
	return true
}

// CleanupIdle returns and removes expired active workloads. The caller owns
// stopping those processes and may persist their stopped state afterwards.
func (b *WorkloadBudget) CleanupIdle(now time.Time) []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var expired []string
	for id, touched := range b.active {
		if now.Sub(touched) >= b.idleTTL {
			expired = append(expired, id)
			delete(b.active, id)
		}
	}
	if len(expired) > 0 {
		slices.Sort(expired)
		b.signalLocked()
	}
	return expired
}

func (b *WorkloadBudget) Snapshot() (active, queued []string) {
	if b == nil {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.active {
		active = append(active, id)
	}
	queued = append(queued, b.queued...)
	slices.Sort(active)
	return active, queued
}

func (b *WorkloadBudget) Capacity() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.max
}

func (b *WorkloadBudget) removeQueuedLocked(id string) {
	for i, candidate := range b.queued {
		if candidate == id {
			b.queued = append(b.queued[:i], b.queued[i+1:]...)
			return
		}
	}
}

func (b *WorkloadBudget) signalLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}
