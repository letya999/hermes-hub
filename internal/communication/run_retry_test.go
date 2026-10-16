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

// A user message whose run fails gets exactly one automatic retry as a fresh
// run; the owner hears the error only after that retry also fails. Without
// the retry the job dies on the first transient provider blip.
func TestWorkerRetriesFailedMessageJobOnce(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
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
	job := Job{Envelope: user.envelope(11), ID: "telegram-x", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "telegram:x", ChatID: 11, Text: "hi", CreatedAt: time.Now().UTC()}
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
	if calls != 2 {
		t.Fatalf("runner calls=%d, want 2 (one automatic retry)", calls)
	}
	for _, dir := range []string{"pending", "running", "failed", "done"} {
		if _, err := os.Stat(filepath.Join(g.spool.root, dir, "telegram-x-r2.json")); err == nil {
			t.Fatalf("unbounded retry: telegram-x-r2.json in %s", dir)
		}
	}
	fake.mu.Lock()
	sent := len(fake.sent)
	fake.mu.Unlock()
	if sent != 1 {
		t.Fatalf("error deliveries=%d, want exactly 1 after the retry failed", sent)
	}
}
