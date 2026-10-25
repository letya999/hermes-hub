package communication

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestTenantFloodDoesNotStarveOthers(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: fmt.Sprintf("a-%02d", i), IdempotencyKey: fmt.Sprintf("a-%02d", i), UserID: "alice", ScopeID: "user:alice", Text: "hello", CreatedAt: time.Unix(int64(i), 0)}
		if _, err := s.Enqueue(job); err != nil {
			t.Fatal(err)
		}
	}
	bob := Job{Envelope: identity.TelegramEnvelope("bob", 22, "bob", "policy-1"), ID: "z-bob", IdempotencyKey: "z-bob", UserID: "bob", ScopeID: "user:bob", Text: "hello", CreatedAt: time.Unix(9, 0)}
	if _, err := s.Enqueue(bob); err != nil {
		t.Fatal(err)
	}
	seenBob := false
	for i := 0; i < 2; i++ {
		job, err := s.ClaimJob()
		if err != nil || job == nil {
			t.Fatalf("claim: %v", err)
		}
		if job.PrincipalID == "bob" {
			seenBob = true
		}
		if err := s.CompleteJob(job.ID); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			s, err = NewSpool(s.root)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if !seenBob {
		t.Error("one tenant's queued burst starved unrelated ready user")
	}
}
