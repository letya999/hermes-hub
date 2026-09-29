package communication

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func parallelJob(id, principal string) Job {
	return Job{
		ID: id, IdempotencyKey: "idem-" + id,
		OrganizationID: "personal", UserID: principal, ActorID: principal,
		ScopeID: "user:" + principal, Channel: "telegram_bot", Trigger: "message",
		Text:     "hi " + id,
		Envelope: identity.Envelope{Schema: identity.Schema, PrincipalID: principal, ExternalIdentityID: "ext-" + principal, ContextID: principal, RuntimeID: "rt-" + principal, ConversationID: "conv-" + id, DeliveryTargetID: "conv-" + id, PolicyVersion: "policy-1"},
	}
}

func claimID(t *testing.T, s *Spool) string {
	t.Helper()
	job, err := s.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		return ""
	}
	return job.ID
}

// Different contexts must not head-of-line block each other (ADR-0025).
func TestClaimJobSkipsBusyContext(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "b1", "a2"} {
		user := "alice"
		if id[0] == 'b' {
			user = "bob"
		}
		if _, err := s.Enqueue(parallelJob(id, user)); err != nil {
			t.Fatal(err)
		}
	}
	if got := claimID(t, s); got != "a1" {
		t.Fatalf("first claim = %q", got)
	}
	if got := claimID(t, s); got != "b1" {
		t.Fatalf("second claim must skip busy alice context, got %q", got)
	}
	if got := claimID(t, s); got != "" {
		t.Fatalf("same-context job must stay queued, got %q", got)
	}
	if err := s.CompleteJob("a1"); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "a2" {
		t.Fatalf("released context must be claimable, got %q", got)
	}
}

// An uncertain outcome keeps only its own context blocked; the same job stays
// claimable for observation (its run may still be executing).
func TestUncertainHoldsOnlyOwnContext(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "a2", "b1"} {
		user := "alice"
		if id == "b1" {
			user = "bob"
		}
		if _, err := s.Enqueue(parallelJob(id, user)); err != nil {
			t.Fatal(err)
		}
	}
	if got := claimID(t, s); got != "a1" {
		t.Fatalf("claim = %q", got)
	}
	if err := s.RecordOutcome("a1", RunOutcome{JobID: "a1", Status: "uncertain", RunID: "r1", SessionID: "s1", RuntimeGeneration: "g1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FailJob("a1"); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "b1" {
		t.Fatalf("other context must stay claimable, got %q", got)
	}
	if got := claimID(t, s); got != "" {
		t.Fatalf("uncertain context must stay blocked, got %q", got)
	}
	// RequeueObservations returns the admitted run to pending; only it may
	// re-claim the held context key.
	if err := s.RequeueObservations(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "a1" {
		t.Fatalf("requeued observation must reclaim its context, got %q", got)
	}
}

// A crash after admission rebuilds the in-flight set from durable mappings:
// the uncertain context stays blocked for other jobs after restart.
func TestRestartRebuildsInFlight(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(parallelJob("a1", "alice")); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "a1" {
		t.Fatalf("claim = %q", got)
	}
	// The run was admitted before the crash: durable run ids exist.
	if err := s.RecordOutcome("a1", RunOutcome{JobID: "a1", Status: "running", RunID: "r1", SessionID: "s1", RuntimeGeneration: "g1"}); err != nil {
		t.Fatal(err)
	}
	// Simulate restart: NewSpool re-pends the claimed job and rebuilds the
	// in-flight hold from the durable admitted mapping.
	reopened, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Enqueue(parallelJob("a2", "alice")); err != nil {
		t.Fatal(err)
	}
	// The crashed job owns the key and reclaims it itself; the queued
	// same-context job stays blocked.
	if got := claimID(t, reopened); got != "a1" {
		t.Fatalf("crashed job must reclaim its own context, got %q", got)
	}
	if got := claimID(t, reopened); got != "" {
		t.Fatalf("restarted spool must keep uncertain context blocked, got %q", got)
	}
	if _, err := reopened.Enqueue(parallelJob("b1", "bob")); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, reopened); got != "b1" {
		t.Fatalf("other context must claim after restart, got %q", got)
	}
}

// An uncertain outcome without an admitted run (e.g. supervisor rejected the
// acquire before any container started) has nothing executing: it must not
// hold the context, or the user deadlocks forever — RequeueObservations only
// re-drives admitted runs.
func TestUnadmittedUncertainReleasesContext(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "a2"} {
		if _, err := s.Enqueue(parallelJob(id, "alice")); err != nil {
			t.Fatal(err)
		}
	}
	if got := claimID(t, s); got != "a1" {
		t.Fatalf("claim = %q", got)
	}
	if err := s.RecordOutcome("a1", RunOutcome{JobID: "a1", Status: "uncertain", LastEvent: "runtime.unavailable"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FailJob("a1"); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "a2" {
		t.Fatalf("unadmitted uncertain must release the context, got %q", got)
	}
	// Same after restart: rebuild must not resurrect a hold with no run.
	if _, err := s.Enqueue(parallelJob("a3", "alice")); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, reopened); got != "a3" {
		t.Fatalf("restarted spool must not block on unadmitted uncertain, got %q", got)
	}
	// The unadmitted mapping stays uncertain (fail-closed contract) but must
	// not hold the context.
	if m, found, err := reopened.Mapping("a1"); err != nil || !found || m.Status != "uncertain" {
		t.Fatalf("unadmitted crash keeps uncertain status, got %+v found=%v err=%v", m, found, err)
	}
}

// The worker pool runs jobs of different contexts concurrently while a single
// worker would serialize them.
type blockingRunner struct {
	started chan<- string
	release <-chan struct{}
}

func (b blockingRunner) Run(_ context.Context, job Job, _ User) (string, error) {
	return "", nil
}

func (b blockingRunner) RunOutcome(ctx context.Context, job Job) (RunOutcome, error) {
	select {
	case b.started <- job.ID:
	case <-ctx.Done():
		return RunOutcome{}, ctx.Err()
	}
	select {
	case <-b.release:
		return RunOutcome{Text: "done", JobID: job.ID, Status: "completed", LastEvent: "run.completed"}, nil
	case <-ctx.Done():
		return RunOutcome{}, ctx.Err()
	}
}

func TestWorkerPoolRunsContextsInParallel(t *testing.T) {
	c := testConfig(t)
	c.Workers = 2
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan string, 4), make(chan struct{})
	g.runner = blockingRunner{started: started, release: release}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < c.Workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.worker(ctx) }()
	}
	for _, id := range []string{"pa", "pb"} {
		user := "alice"
		if id == "pb" {
			user = "bob"
		}
		if _, err := g.spool.Enqueue(parallelJob(id, user)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-deadline:
			t.Fatalf("jobs did not run in parallel: %v", seen)
		}
	}
	close(release)
	cancel()
	wg.Wait()
}
