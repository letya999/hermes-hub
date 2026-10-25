package communication

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// #327: the admission reservation adds one ledger write to enqueue; the whole
// reserve->settle cycle must stay cheap enough that 20 concurrent users feel
// no added wait.
func BenchmarkReserveSettle(b *testing.B) {
	s, err := NewSpool(filepath.Join(b.TempDir(), "spool"))
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := fmt.Sprintf("job-%d", i)
		job := Job{Envelope: identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), ID: id, ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key-" + id, CreatedAt: time.Now().UTC()}
		if _, err := s.Enqueue(job); err != nil {
			b.Fatal(err)
		}
		if err := s.RecordOutcome(id, RunOutcome{JobID: id, Status: "failed"}); err != nil {
			b.Fatal(err)
		}
	}
}

// #327: a fair claim over the production ceiling — 20 scopes each holding a
// pending job — is one bounded scan; admission order rotates by scope.
func BenchmarkClaimFair20Scopes(b *testing.B) {
	s, err := NewSpool(filepath.Join(b.TempDir(), "spool"))
	if err != nil {
		b.Fatal(err)
	}
	seed := func() {
		for i := 0; i < 20; i++ {
			id := fmt.Sprintf("job-%02d", i)
			job := Job{Envelope: identity.Envelope{Schema: identity.Schema, PrincipalID: fmt.Sprintf("user%02d", i), ContextID: fmt.Sprintf("ctx%02d", i), PolicyVersion: "policy-1"}, ID: id, ScopeID: fmt.Sprintf("user:user%02d", i), Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key-" + id, CreatedAt: time.Now().UTC()}
			if _, err := s.Enqueue(job); err != nil {
				b.Fatal(err)
			}
		}
	}
	seed()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		claimed, err := s.ClaimJob()
		if err != nil {
			b.Fatal(err)
		}
		if claimed == nil {
			b.StopTimer()
			seed()
			b.StartTimer()
			continue
		}
		if err := s.FailJob(claimed.ID); err != nil {
			b.Fatal(err)
		}
	}
}
