package communication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// alwaysFailRunner reproduces a terminal run.failed — what a transient model
// provider outage (upstream 503 after the runtime's own retries) looks like
// to the gateway.
type alwaysFailRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *alwaysFailRunner) Run(context.Context, Job, User) (string, error) {
	return "", errors.New("hermes run ended without completion")
}

func (r *alwaysFailRunner) RunOutcome(_ context.Context, job Job) (RunOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return RunOutcome{JobID: job.ID, Status: "failed", LastEvent: "run.failed"}, errors.New("hermes run ended without completion")
}

// A terminal failure does not prove the run had no effects. Keep its original
// logical operation and notify once instead of creating fresh authorization.
func TestFailedRunAfterToolIsNotResubmitted(t *testing.T) {
	c := testConfig(t)
	g, err := newTestGateway(t, c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &alwaysFailRunner{}
	g.api, g.runner = fake, runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	user := c.Users[0]
	job := Job{Envelope: user.Envelope(11), ID: "telegram-x", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "telegram:x", ChatID: 11, Text: "hi", CreatedAt: time.Now().UTC()}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		sent := len(fake.sent)
		fake.mu.Unlock()
		if sent > 0 {
			break
		}
		if _, err := os.Stat(filepath.Join(g.spool.root, "failed", "telegram-x-r1.json")); err == nil {
			// The retry is spent; the single error delivery may still be
			// in the outbox — keep waiting for it briefly.
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()
	if calls != 1 {
		t.Fatalf("runner calls=%d, want 1 without verified pre-effect proof", calls)
	}
	for _, dir := range []string{"pending", "running", "failed", "done"} {
		if _, err := os.Stat(filepath.Join(g.spool.root, dir, "telegram-x-r1.json")); err == nil {
			t.Fatalf("unverified fresh retry: telegram-x-r1.json in %s", dir)
		}
	}
	fake.mu.Lock()
	sent := len(fake.sent)
	fake.mu.Unlock()
	if sent != 1 {
		t.Fatalf("error deliveries=%d, want exactly 1 for the original failed operation", sent)
	}
}
