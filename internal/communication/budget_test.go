package communication

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Concurrent enqueues must each hold exactly one reservation against the
// scope: the ledger, not the caller, is the atomic source of truth for
// pending demand under parallel admission.
func TestBudgetConcurrentReservation(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const jobs = 8
	var wg sync.WaitGroup
	errs := make(chan error, jobs)
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: "job-c-" + string(rune('a'+i)), ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key-c-" + string(rune('a'+i)), CreatedAt: time.Now().UTC()}
			ok, err := spool.Enqueue(job)
			if err != nil {
				errs <- err
				return
			}
			if !ok {
				errs <- errors.New("duplicate admission")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	scope := spool.budgetScope("user:alice")
	if scope.PendingJobs != jobs {
		t.Fatalf("pending jobs = %d, want %d", scope.PendingJobs, jobs)
	}
	for i := 0; i < jobs; i++ {
		id := "job-c-" + string(rune('a'+i))
		if state := spool.reservationState(id); state != "open" {
			t.Fatalf("reservation %s = %q, want open", id, state)
		}
	}
}

// A terminal outcome settles its reservation exactly once: replayed settles
// (crash recovery or a repeated terminal write) are no-ops, and the charge is
// the measured usage, not the estimate.
func TestBudgetReplaySettlement(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: "job-s1", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key-s1", CreatedAt: time.Now().UTC()}
	if ok, err := spool.Enqueue(job); err != nil || !ok {
		t.Fatalf("enqueue: ok=%v err=%v", ok, err)
	}
	report := hubruntime.SessionUsage{SessionFound: true, SessionID: "session-1", InputTokens: int64ptr(120), OutputTokens: int64ptr(30)}
	if err := spool.RecordUsage(job, "run-1", report); err != nil {
		t.Fatal(err)
	}
	if err := spool.RecordOutcome("job-s1", RunOutcome{JobID: "job-s1", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	scope := spool.budgetScope("user:alice")
	if state := spool.reservationState("job-s1"); state != "settled" || scope.PendingJobs != 0 || scope.ReservedTokens != 0 {
		t.Fatalf("after settle: state=%q scope=%+v", state, scope)
	}
	if scope.SettledIn != 120 || scope.SettledOut != 30 {
		t.Fatalf("measured charge not recorded: %+v", scope)
	}
	// Replay: a second settle must not re-charge the scope.
	spool.mu.Lock()
	err = spool.settleReservationLocked("job-s1")
	spool.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	again := spool.budgetScope("user:alice")
	if again.SettledIn != 120 || again.SettledOut != 30 || again.PendingJobs != 0 {
		t.Fatalf("replay re-charged the scope: %+v", again)
	}
}

// Fanout (retry/goal children) charges the parent's scope: every child job
// holds its own reservation under the shared scope, process demand is the
// open-reservation count, and the ledger survives a spool reopen.
func TestBudgetIncludesChildrenAndMemory(t *testing.T) {
	root := t.TempDir()
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(s *Spool, id string) {
		t.Helper()
		job := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: id, ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key-" + id, CreatedAt: time.Now().UTC()}
		if ok, err := s.Enqueue(job); err != nil || !ok {
			t.Fatalf("enqueue %s: ok=%v err=%v", id, ok, err)
		}
	}
	enqueue(spool, "job-parent")
	enqueue(spool, "job-retry-1")
	enqueue(spool, "job-goal-1")
	scope := spool.budgetScope("user:alice")
	if scope.PendingJobs != 3 || scope.ReservedTokens != 3*defaultTokenEstimate {
		t.Fatalf("children not charged to scope: %+v", scope)
	}
	// Reopen: the ledger is durable state, not in-memory accounting.
	reopened, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	scope = reopened.budgetScope("user:alice")
	if scope.PendingJobs != 3 || scope.ReservedTokens != 3*defaultTokenEstimate {
		t.Fatalf("ledger did not survive reopen: %+v", scope)
	}
	// Settling one child releases only its own reservation.
	if err := reopened.RecordOutcome("job-retry-1", RunOutcome{JobID: "job-retry-1", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	scope = reopened.budgetScope("user:alice")
	if scope.PendingJobs != 2 || spool.reservationState("job-parent") != "open" {
		t.Fatalf("child settlement leaked across scope: %+v", scope)
	}
	// A job that completed without a mapping (legacy/idempotency-less path)
	// releases its reservation when the next admission reconciles the book.
	keyless := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: "job-legacy", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", CreatedAt: time.Now().UTC()}
	if ok, err := reopened.Enqueue(keyless); err != nil || !ok {
		t.Fatalf("enqueue keyless: ok=%v err=%v", ok, err)
	}
	if scope.PendingJobs != 3 && reopened.budgetScope("user:alice").PendingJobs != 3 {
		t.Fatal("keyless job did not reserve")
	}
	if err := os.Rename(filepath.Join(root, "pending", "job-legacy.json"), filepath.Join(root, "done", "job-legacy.json")); err != nil {
		t.Fatal(err)
	}
	enqueue(reopened, "job-goal-2")
	scope = reopened.budgetScope("user:alice")
	if reopened.reservationState("job-legacy") != "released" || scope.PendingJobs != 3 {
		t.Fatalf("orphan reservation not released: %+v", scope)
	}
	// Restart orphan: a job file that vanished before any mapping leaves an
	// open reservation that startup reconciliation releases.
	if err := os.Remove(filepath.Join(root, "pending", "job-goal-2.json")); err != nil {
		t.Fatal(err)
	}
	third, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if third.reservationState("job-goal-2") != "released" || third.budgetScope("user:alice").PendingJobs != 2 {
		t.Fatalf("startup reconcile missed orphan: %+v", third.budgetScope("user:alice"))
	}
}

// Unpriceable admission fails closed: a job with no scope or a negative
// estimate never reaches the queue. A run whose provider reported no token
// counters still settles — at its estimate — and counts as unknown pricing.
func TestBudgetUnknownPricing(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id, scope string, estimate int64) Job {
		return Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: id, ScopeID: scope, Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key-" + id, TokenEstimate: estimate, CreatedAt: time.Now().UTC()}
	}
	// Scope-less admission still prices — it lands on the install bucket.
	if ok, err := spool.Enqueue(mk("job-noscope", "", 0)); err != nil || !ok {
		t.Fatalf("scopeless job rejected: ok=%v err=%v", ok, err)
	}
	if scope := spool.budgetScope(installationScope); scope.PendingJobs != 1 {
		t.Fatalf("scopeless job not charged to install bucket: %+v", scope)
	}
	if _, err := spool.Enqueue(mk("job-neg", "user:alice", -1)); err == nil {
		t.Fatal("negative estimate admitted")
	}
	if ok, err := spool.Enqueue(mk("job-u1", "user:alice", 500)); err != nil || !ok {
		t.Fatalf("enqueue: ok=%v err=%v", ok, err)
	}
	// Provider never reported token counters: settle at the estimate.
	report := hubruntime.SessionUsage{SessionFound: false}
	if err := spool.RecordUsage(mk("job-u1", "user:alice", 500), "run-1", report); err != nil {
		t.Fatal(err)
	}
	if err := spool.RecordOutcome("job-u1", RunOutcome{JobID: "job-u1", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	scope := spool.budgetScope("user:alice")
	if scope.Unknown != 1 || scope.PendingJobs != 0 || spool.reservationState("job-u1") != "settled" {
		t.Fatalf("unknown pricing not accounted: %+v", scope)
	}
}
