package communication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func queuedJob(id, principal, contextID, conv string) Job {
	j := Job{ID: id, ScopeID: "user:" + principal, Trigger: "message"}
	j.PrincipalID = principal
	j.ContextID = contextID
	j.ConversationID = conv
	j.CreatedAt = time.Now()
	return j
}

func pendingNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Conversation lanes serialize: a second job in the same lane waits until the
// claimed one finishes, and completion reopens the lane.
func TestClaimSerializesConversationLanes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"job-1", "job-2"} {
		if ok, err := s.Enqueue(queuedJob(id, "alice", "private", "conv-1")); err != nil || !ok {
			t.Fatalf("enqueue %s: ok=%v err=%v", id, ok, err)
		}
	}
	first, err := s.ClaimJob()
	if err != nil || first == nil {
		t.Fatalf("first claim: %v %v", first, err)
	}
	if second, err := s.ClaimJob(); err != nil || second != nil {
		t.Fatalf("busy lane claimed: %v %v", second, err)
	}
	// A job in another lane of the same context is not blocked.
	if ok, err := s.Enqueue(queuedJob("job-3", "alice", "private", "conv-2")); err != nil || !ok {
		t.Fatalf("enqueue job-3: ok=%v err=%v", ok, err)
	}
	other, err := s.ClaimJob()
	if err != nil || other == nil || other.ID != "job-3" {
		t.Fatalf("sibling lane starved: %v %v", other, err)
	}
	if err := s.CompleteJob(first.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := s.CompleteJob(other.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
	next, err := s.ClaimJob()
	if err != nil || next == nil || next.ID != "job-2" {
		t.Fatalf("released lane not claimed: %v %v", next, err)
	}
	if err := s.FailJob(next.ID); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "failed", "job-2.json")); err != nil {
		t.Fatal("failed job not parked")
	}
	if s.ClaimPolls() < 3 {
		t.Fatal("claim polls not counted")
	}
}

// The durable cursor rotates claim order across (principal, context) scopes:
// after scope A is served, scope B is next even though A parked earlier.
func TestClaimRotatesFairScopes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range []Job{
		queuedJob("j-a", "alice", "ctx-a", "c1"),
		queuedJob("j-b", "alice", "ctx-b", "c1"),
	} {
		if ok, err := s.Enqueue(j); err != nil || !ok {
			t.Fatalf("enqueue %s: %v", j.ID, err)
		}
	}
	first, err := s.ClaimJob()
	if err != nil || first == nil || first.ID != "j-a" {
		t.Fatalf("first claim must take the lowest scope: %v %v", first, err)
	}
	second, err := s.ClaimJob()
	if err != nil || second == nil || second.ID != "j-b" {
		t.Fatalf("cursor must rotate to the next scope: %v %v", second, err)
	}
	// The cursor position itself is durable.
	if _, err := os.Stat(filepath.Join(root, "admission-cursor.json")); err != nil {
		t.Fatal("fairness cursor not persisted")
	}
}

// Idempotency replays and holds keep their contracts: same key + same payload
// dedupes silently, same key + different payload is rejected, an active hold
// accepts but suppresses, and a lifted hold stamps the operational epoch.
func TestEnqueueIdempotencyAndHolds(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := queuedJob("job-k", "alice", "private", "conv-1")
	job.IdempotencyKey = "key-1"
	job.Text = "payload-a"
	if ok, err := s.Enqueue(job); err != nil || !ok {
		t.Fatalf("first enqueue: ok=%v err=%v", ok, err)
	}
	dup := queuedJob("job-k2", "alice", "private", "conv-1")
	dup.IdempotencyKey = "key-1"
	dup.Text = "payload-b"
	if ok, err := s.Enqueue(dup); err == nil || ok {
		t.Fatalf("same key different payload admitted: ok=%v err=%v", ok, err)
	}
	dup.Text = "payload-a"
	if ok, err := s.Enqueue(dup); err != nil || ok {
		t.Fatalf("idempotent replay must dedupe: ok=%v err=%v", ok, err)
	}

	// holdsSnapshot: active hold suppresses; lifted hold stamps the epoch.
	s.holdsSnapshot = func() []masterHold {
		return []masterHold{{PrincipalID: "alice", ContextID: "private", Active: true}}
	}
	held := queuedJob("job-held", "alice", "private", "conv-9")
	if ok, err := s.Enqueue(held); err != nil || !ok {
		t.Fatalf("held enqueue: ok=%v err=%v", ok, err)
	}
	for _, n := range pendingNames(t, root) {
		if strings.HasPrefix(n, "job-held") {
			t.Fatal("held job must not reach the pending dir")
		}
	}
	s.holdsSnapshot = func() []masterHold {
		return []masterHold{{PrincipalID: "alice", ContextID: "private", Active: false, Epoch: 7}}
	}
	stamped := queuedJob("job-epoch", "alice", "private", "conv-9")
	if ok, err := s.Enqueue(stamped); err != nil || !ok {
		t.Fatalf("epoch enqueue: ok=%v err=%v", ok, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "pending", "job-epoch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"operational_epoch":7`) {
		t.Fatal("lifted hold epoch not stamped on the job")
	}
	// refreshHolds errors surface before admission.
	s.refreshHolds = func(ctx context.Context) error { return errors.New("holds down") }
	if _, err := s.Enqueue(queuedJob("job-x", "alice", "private", "c")); err == nil {
		t.Fatal("refresh failure must reject enqueue")
	}
}

// A claimed job whose mapping already settled is redirected to its terminal
// dir instead of being handed to a worker — never re-executed.
func TestClaimSkipsTerminalMapping(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := queuedJob("job-done", "alice", "private", "conv-1")
	job.IdempotencyKey = "k-done"
	if ok, err := s.Enqueue(job); err != nil || !ok {
		t.Fatalf("enqueue: ok=%v err=%v", ok, err)
	}
	s.mu.Lock()
	if err := s.updateMappingLocked("job-done", RunOutcome{Status: "completed", LastEvent: "job.completed"}, "completed", true); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	if claimed, err := s.ClaimJob(); err != nil || claimed != nil {
		t.Fatalf("terminal-mapped job claimed: %v %v", claimed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "done", "job-done.json")); err != nil {
		t.Fatal("terminal job not moved to done")
	}
}

// A sensitive job's text lives only in memory: after a restart the payload is
// gone, so the job must fail explicitly rather than dispatch empty input.
func TestClaimDropsSensitivePayloadAfterRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := queuedJob("job-secret", "alice", "private", "conv-1")
	job.Sensitive = true
	job.Text = "spoken-secret"
	if ok, err := s.Enqueue(job); err != nil || !ok {
		t.Fatalf("enqueue: ok=%v err=%v", ok, err)
	}
	// The on-disk job carries the hash, never the payload.
	body, err := os.ReadFile(filepath.Join(root, "pending", "job-secret.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "spoken-secret") {
		t.Fatal("sensitive payload persisted to disk")
	}
	reopened, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ClaimJob(); err == nil {
		t.Fatal("restart must not dispatch a payload-less sensitive job")
	}
	if _, err := os.Stat(filepath.Join(root, "failed", "job-secret.json")); err != nil {
		t.Fatal("payload-less job not parked in failed")
	}
}

// Restart recovery reconciles running jobs by their mapping: terminal work
// moves to its final dir, admitted runs become uncertain-but-observed, and
// legacy unmapped jobs return to pending — none are silently replayed.
func TestRecoverReconcilesRunningJobs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	write := func(dir, id string) {
		t.Helper()
		if err := atomicJSON(filepath.Join(root, dir, id+".json"), queuedJob(id, "alice", "private", "c")); err != nil {
			t.Fatal(err)
		}
	}
	write("running", "j-completed")
	write("running", "j-uncertain")
	write("running", "j-legacy")
	s.mu.Lock()
	for _, m := range []JobMapping{
		{JobID: "j-completed", IdempotencyKey: "k1", Status: "completed"},
		{JobID: "j-uncertain", IdempotencyKey: "k2", Status: "running", RunID: "r1", SessionID: "s1"},
	} {
		if err := s.writeMappingLocked(m); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
	}
	s.mu.Unlock()
	if err := s.recoverJobs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "done", "j-completed.json")); err != nil {
		t.Fatal("terminal running job not moved to done")
	}
	if _, err := os.Stat(filepath.Join(root, "pending", "j-uncertain.json")); err != nil {
		t.Fatal("admitted run not requeued for observation")
	}
	s.mu.Lock()
	mapping, mapErr := s.loadMappingLocked("j-uncertain")
	s.mu.Unlock()
	if mapErr != nil || mapping.Status != "uncertain" || mapping.LastKnownEvent != "run.unknown" {
		t.Fatalf("interrupted run must become uncertain: %+v %v", mapping, mapErr)
	}
	if _, err := os.Stat(filepath.Join(root, "pending", "j-legacy.json")); err != nil {
		t.Fatal("legacy job not returned to pending")
	}
}

// Jobs written before mappings existed get their routing record backfilled
// from the durable file; an in-flight context hold is rebuilt too.
func TestRecoverRebuildsMappingsAndLanes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := queuedJob("j-backfill", "alice", "private", "conv-1")
	job.IdempotencyKey = "k-backfill"
	if err := atomicJSON(filepath.Join(root, "running", job.ID+".json"), job); err != nil {
		t.Fatal(err)
	}
	if err := s.rebuildMappings(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	mapping, mapErr := s.loadMappingLocked("j-backfill")
	s.mu.Unlock()
	if mapErr != nil {
		t.Fatal(mapErr)
	}
	if mapping.Status != "running" || mapping.IdempotencyKey != "k-backfill" {
		t.Fatalf("backfilled mapping: %+v", mapping)
	}
	if err := s.rebuildIdempotencyIndex(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.idempotencyIndexPath("k-backfill")); err != nil {
		t.Fatal("idempotency index not backfilled")
	}
}
