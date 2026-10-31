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

// laneJob pins a job to an explicit conversation (topic/task lane, ADR-0090).
func laneJob(id, principal, conversation string) Job {
	job := parallelJob(id, principal)
	job.Envelope.ConversationID = conversation
	job.Envelope.DeliveryTargetID = conversation
	return job
}

// Different contexts must not head-of-line block each other (ADR-0025), and
// serialization inside one context is per conversation (ADR-0090).
func TestClaimJobSkipsBusyContext(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "b1"} {
		user := "alice"
		if id[0] == 'b' {
			user = "bob"
		}
		if _, err := s.Enqueue(parallelJob(id, user)); err != nil {
			t.Fatal(err)
		}
	}
	// a2 shares a1's conversation: it queues behind the held lane.
	if _, err := s.Enqueue(laneJob("a2", "alice", "conv-a1")); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "a1" {
		t.Fatalf("first claim = %q", got)
	}
	if got := claimID(t, s); got != "b1" {
		t.Fatalf("second claim must skip busy alice context, got %q", got)
	}
	if got := claimID(t, s); got != "" {
		t.Fatalf("same-conversation job must stay queued, got %q", got)
	}
	if err := s.CompleteJob("a1"); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "a2" {
		t.Fatalf("released lane must be claimable, got %q", got)
	}
}

// Different conversations in one context are separate lanes (ADR-0090 R2):
// a user's topics run in parallel while each conversation keeps FIFO.
func TestClaimParallelConversationsSameContext(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range []Job{laneJob("a1", "alice", "topic-1"), laneJob("a2", "alice", "topic-2"), laneJob("a3", "alice", "topic-1")} {
		if _, err := s.Enqueue(job); err != nil {
			t.Fatal(err)
		}
	}
	if got := claimID(t, s); got != "a1" {
		t.Fatalf("first claim = %q", got)
	}
	if got := claimID(t, s); got != "a2" {
		t.Fatalf("second conversation must claim in parallel, got %q", got)
	}
	if got := claimID(t, s); got != "" {
		t.Fatalf("same-conversation job must stay queued, got %q", got)
	}
	if err := s.CompleteJob("a1"); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "a3" {
		t.Fatalf("freed lane must be claimable, got %q", got)
	}
}

// An uncertain outcome keeps only its own lane blocked; the same job stays
// claimable for observation (its run may still be executing).
func TestUncertainHoldsOnlyOwnContext(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(laneJob("a1", "alice", "topic-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(laneJob("a2", "alice", "topic-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(parallelJob("b1", "bob")); err != nil {
		t.Fatal(err)
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
		t.Fatalf("uncertain lane must stay blocked, got %q", got)
	}
	// RequeueObservations returns the admitted run to pending; only it may
	// re-claim the held context key.
	if err := s.RequeueObservations(time.Now().Add(time.Hour), false); err != nil {
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
	if _, err := s.Enqueue(laneJob("a1", "alice", "topic-1")); err != nil {
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
	if _, err := reopened.Enqueue(laneJob("a2", "alice", "topic-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Enqueue(laneJob("a3", "alice", "topic-2")); err != nil {
		t.Fatal(err)
	}
	// The crashed job owns the lane and reclaims it; the queued
	// same-conversation job stays blocked, another lane claims freely.
	if got := claimID(t, reopened); got != "a1" {
		t.Fatalf("crashed job must reclaim its own lane, got %q", got)
	}
	if got := claimID(t, reopened); got != "a3" {
		t.Fatalf("other lane must claim after restart, got %q", got)
	}
	if got := claimID(t, reopened); got != "" {
		t.Fatalf("restarted spool must keep the held lane blocked, got %q", got)
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
// hold the lane, or the conversation deadlocks forever — RequeueObservations
// only re-drives admitted runs.
func TestUnadmittedUncertainReleasesContext(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	// a2 shares a1's lane: only a released lane lets it claim.
	if _, err := s.Enqueue(laneJob("a1", "alice", "topic-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(laneJob("a2", "alice", "topic-1")); err != nil {
		t.Fatal(err)
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
		t.Fatalf("unadmitted uncertain must release the lane, got %q", got)
	}
	// Same after restart: rebuild must not resurrect a hold with no run.
	if _, err := s.Enqueue(laneJob("a3", "alice", "topic-1")); err != nil {
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

// Jobs without a conversation id degrade to the context key (ADR-0090 R1):
// they serialize with each other but not with named lanes.
func TestEmptyConversationFallsBackToContextKey(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bare := parallelJob("a1", "alice")
	bare.Envelope.ConversationID = ""
	for _, job := range []Job{bare, laneJob("a2", "alice", "topic-1")} {
		if _, err := s.Enqueue(job); err != nil {
			t.Fatal(err)
		}
	}
	bare2 := parallelJob("a3", "alice")
	bare2.Envelope.ConversationID = ""
	if _, err := s.Enqueue(bare2); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "a1" {
		t.Fatalf("claim = %q", got)
	}
	if got := claimID(t, s); got != "a2" {
		t.Fatalf("named lane must claim beside the context lane, got %q", got)
	}
	if got := claimID(t, s); got != "" {
		t.Fatalf("second conversationless job must stay queued, got %q", got)
	}
}

// A context at its lane cap queues further conversations like a busy lane
// (ADR-0090 R3); other contexts stay claimable.
func TestContextParallelCapSkips(t *testing.T) {
	t.Setenv("HUB_CONTEXT_MAX_PARALLEL", "2")
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range []Job{laneJob("a1", "alice", "topic-1"), laneJob("a2", "alice", "topic-2"), laneJob("a3", "alice", "topic-3"), parallelJob("b1", "bob")} {
		if _, err := s.Enqueue(job); err != nil {
			t.Fatal(err)
		}
	}
	if got := claimID(t, s); got != "a1" {
		t.Fatalf("claim = %q", got)
	}
	// Scope rotation gives bob the next claim before alice's second lane.
	if got := claimID(t, s); got != "b1" {
		t.Fatalf("rotation must serve the other scope, got %q", got)
	}
	if got := claimID(t, s); got != "a2" {
		t.Fatalf("second lane must claim under the cap, got %q", got)
	}
	if got := claimID(t, s); got != "" {
		t.Fatalf("third lane over the cap must wait, got %q", got)
	}
	if err := s.CompleteJob("a1"); err != nil {
		t.Fatal(err)
	}
	if got := claimID(t, s); got != "a3" {
		t.Fatalf("freed lane slot must be claimable, got %q", got)
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
