package communication

import (
	"errors"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Pending ceilings reject honestly at admission: a scope past its queue cap
// is refused while other scopes stay claimable, and the installation-wide cap
// bounds total pending work.
func TestCeilingsPendingQueue(t *testing.T) {
	t.Setenv("HUB_SPOOL_MAX_PENDING_PER_SCOPE", "2")
	t.Setenv("HUB_SPOOL_MAX_PENDING_TOTAL", "3")
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(id, scope string) error {
		job := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: id, ScopeID: scope, Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key-" + id, CreatedAt: time.Now().UTC()}
		ok, err := spool.Enqueue(job)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("duplicate admission")
		}
		return nil
	}
	for i := 0; i < 2; i++ {
		if err := enqueue("job-a-"+string(rune('0'+i)), "user:alice"); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	if err := enqueue("job-a-2", "user:alice"); err == nil {
		t.Fatal("per-scope ceiling not enforced")
	}
	if err := enqueue("job-b-0", "user:bob"); err != nil {
		t.Fatalf("other scope refused by per-scope ceiling: %v", err)
	}
	// Total: alice 2 + bob 1 = 3 pending; the next admission exceeds the cap.
	if err := enqueue("job-b-1", "user:bob"); err == nil {
		t.Fatal("total ceiling not enforced")
	}
	// A settled job frees headroom: the next enqueue reconciles and admits.
	if err := spool.RecordOutcome("job-a-0", RunOutcome{JobID: "job-a-0", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := enqueue("job-b-1", "user:bob"); err != nil {
		t.Fatalf("settlement did not free headroom: %v", err)
	}
}

// The daily scope token ceiling counts settled spend plus everything still
// reserved: admission is the gate, never mid-run preemption.
func TestCeilingsTokenDaily(t *testing.T) {
	t.Setenv("HUB_SPOOL_MAX_PENDING_PER_SCOPE", "0")
	t.Setenv("HUB_SPOOL_MAX_PENDING_TOTAL", "0")
	t.Setenv("HUB_SCOPE_TOKEN_DAILY", "20000")
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(id string, estimate int64) error {
		job := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: id, ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key-" + id, TokenEstimate: estimate, CreatedAt: time.Now().UTC()}
		ok, err := spool.Enqueue(job)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("duplicate admission")
		}
		return nil
	}
	if err := enqueue("job-t1", 15000); err != nil {
		t.Fatal(err)
	}
	// Reserved 15000; a second estimate of 10000 would overshoot the cap.
	if err := enqueue("job-t2", 10000); err == nil {
		t.Fatal("daily token ceiling not enforced against reservations")
	}
	// Settling below estimate frees headroom for the next admission.
	report := hubruntime.SessionUsage{SessionFound: true, SessionID: "s1", InputTokens: int64ptr(4000), OutputTokens: int64ptr(1000)}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: "job-t1", ScopeID: "user:alice"}
	if err := spool.RecordUsage(job, "run-1", report); err != nil {
		t.Fatal(err)
	}
	if err := spool.RecordOutcome("job-t1", RunOutcome{JobID: "job-t1", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := enqueue("job-t2", 10000); err != nil {
		t.Fatalf("settled spend did not free headroom: %v", err)
	}
	if err := enqueue("job-t3", 6000); err == nil {
		t.Fatal("settled+reserved over cap admitted")
	}
}

// The storage ceiling stats live queue bytes under the spool mutex: once the
// queue exceeds the cap, further admission is refused until it drains.
func TestCeilingsSpoolBytes(t *testing.T) {
	t.Setenv("HUB_SPOOL_MAX_PENDING_PER_SCOPE", "0")
	t.Setenv("HUB_SPOOL_MAX_PENDING_TOTAL", "0")
	t.Setenv("HUB_SPOOL_MAX_BYTES", "1")
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(id string) error {
		job := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: id, ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key-" + id, CreatedAt: time.Now().UTC()}
		ok, err := spool.Enqueue(job)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("duplicate admission")
		}
		return nil
	}
	if err := enqueue("job-bytes-0"); err != nil {
		t.Fatalf("empty spool refused: %v", err)
	}
	if err := enqueue("job-bytes-1"); err == nil {
		t.Fatal("storage ceiling not enforced")
	}
}
