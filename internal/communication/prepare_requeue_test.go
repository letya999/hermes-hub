package communication

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A transient send failure parks a prepare notice in outbox/failed; the
// bounded sweep must return it to pending instead of losing the onboarding
// link forever.
func TestRequeueFailedPreparesRequeuesTransientFailure(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("b", 64)
	d := Delivery{ID: "prepare-fail", ChatID: 11, Text: "credentials", PrepareKey: key, PrepareVersion: 1, CreatedAt: time.Now().UTC()}
	if ok, err := s.enqueuePrepareDelivery(d); err != nil || !ok {
		t.Fatalf("enqueue: %t %v", ok, err)
	}
	claimed, err := s.ClaimDelivery()
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if err := s.UncertainDelivery(claimed.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	n, err := s.RequeueFailedPrepares(now, PrepareRequeueMaxAttempts)
	if err != nil || n != 1 {
		t.Fatalf("requeue: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "pending", spoolFileID("prepare-fail")+".json")); err != nil {
		t.Fatalf("notice not back in pending: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "outbox", "pending", spoolFileID("prepare-fail")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var requeued Delivery
	if err := json.Unmarshal(body, &requeued); err != nil {
		t.Fatal(err)
	}
	if requeued.Attempts != claimed.Attempts+1 {
		t.Fatalf("attempts not incremented: %d -> %d", claimed.Attempts, requeued.Attempts)
	}
	if !requeued.NotBefore.After(now) {
		t.Fatalf("backoff lost: %v vs %v", requeued.NotBefore, now)
	}
	if requeued.PrepareKey != key || requeued.PrepareVersion != 1 {
		t.Fatalf("prepare identity lost: %+v", requeued)
	}
	// The backoff window is a real delay: the claim loop must not hand the
	// notice back out before NotBefore.
	if again, err := s.ClaimDelivery(); err != nil || again != nil {
		t.Fatalf("claimed inside backoff: %+v %v", again, err)
	}
	// A second sweep must not requeue the same record again — it lives in
	// pending now, waiting out its backoff.
	if n, err := s.RequeueFailedPrepares(now, PrepareRequeueMaxAttempts); err != nil || n != 0 {
		t.Fatalf("double requeue: %d %v", n, err)
	}
}

// The attempt budget is the terminal exit: once spent, the record stays
// failed for audit instead of retrying forever.
func TestRequeueFailedPreparesRespectsAttemptBound(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("c", 64)
	d := Delivery{ID: "prepare-stuck", ChatID: 11, Text: "credentials", PrepareKey: key, PrepareVersion: 1, CreatedAt: time.Now().UTC()}
	if ok, err := s.enqueuePrepareDelivery(d); err != nil || !ok {
		t.Fatalf("enqueue: %t %v", ok, err)
	}
	if _, err := s.ClaimDelivery(); err != nil {
		t.Fatal(err)
	}
	if err := s.UncertainDelivery("prepare-stuck"); err != nil {
		t.Fatal(err)
	}
	// The send loop owns attempts; simulate a notice whose budget is already
	// spent rather than waiting out real backoff windows.
	failedPath := filepath.Join(root, "outbox", "failed", spoolFileID("prepare-stuck")+".json")
	body, err := os.ReadFile(failedPath)
	if err != nil {
		t.Fatal(err)
	}
	var stuck Delivery
	if err := json.Unmarshal(body, &stuck); err != nil {
		t.Fatal(err)
	}
	stuck.Attempts = PrepareRequeueMaxAttempts
	rebody, err := json.Marshal(stuck)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failedPath, rebody, 0o600); err != nil {
		t.Fatal(err)
	}
	// Budget spent: the sweep must leave the record in failed.
	n, err := s.RequeueFailedPrepares(time.Now().UTC(), PrepareRequeueMaxAttempts)
	if err != nil || n != 0 {
		t.Fatalf("requeued past bound: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "failed", spoolFileID("prepare-stuck")+".json")); err != nil {
		t.Fatalf("exhausted notice left failed: %v", err)
	}
}

// A newer prepare version supersedes the failed notice outright — retrying
// it would resurrect a stale link.
func TestRequeueFailedPreparesSupersedesStaleVersion(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("d", 64)
	old := Delivery{ID: "prepare-v1", ChatID: 11, Text: "credentials", PrepareKey: key, PrepareVersion: 1, CreatedAt: time.Now().UTC()}
	if ok, err := s.enqueuePrepareDelivery(old); err != nil || !ok {
		t.Fatalf("enqueue v1: %t %v", ok, err)
	}
	if _, err := s.ClaimDelivery(); err != nil {
		t.Fatal(err)
	}
	if err := s.UncertainDelivery("prepare-v1"); err != nil {
		t.Fatal(err)
	}
	newer := old
	newer.ID, newer.PrepareVersion = "prepare-v2", 2
	if ok, err := s.enqueuePrepareDelivery(newer); err != nil || !ok {
		t.Fatalf("enqueue v2: %t %v", ok, err)
	}
	if _, err := s.RequeueFailedPrepares(time.Now().UTC(), PrepareRequeueMaxAttempts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "superseded", spoolFileID("prepare-v1")+".json")); err != nil {
		t.Fatalf("stale notice not superseded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "failed", spoolFileID("prepare-v1")+".json")); !os.IsNotExist(err) {
		t.Fatalf("stale notice still failed: %v", err)
	}
}

// The fence check runs before the age gate: an aged record whose version is
// stale still classifies as superseded rather than lingering as a mystery
// failure.
func TestRequeueFailedPreparesAgedStaleVersionSupersedes(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("f", 64)
	old := Delivery{ID: "prepare-aged-stale", ChatID: 11, Text: "credentials", PrepareKey: key, PrepareVersion: 1, CreatedAt: time.Now().UTC()}
	if ok, err := s.enqueuePrepareDelivery(old); err != nil || !ok {
		t.Fatalf("enqueue v1: %t %v", ok, err)
	}
	if _, err := s.ClaimDelivery(); err != nil {
		t.Fatal(err)
	}
	if err := s.UncertainDelivery("prepare-aged-stale"); err != nil {
		t.Fatal(err)
	}
	newer := old
	newer.ID, newer.PrepareVersion = "prepare-fresh", 2
	if ok, err := s.enqueuePrepareDelivery(newer); err != nil || !ok {
		t.Fatalf("enqueue v2: %t %v", ok, err)
	}
	failedPath := filepath.Join(root, "outbox", "failed", spoolFileID("prepare-aged-stale")+".json")
	aged := time.Now().Add(-2 * prepareRequeueHorizon)
	if err := os.Chtimes(failedPath, aged, aged); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequeueFailedPrepares(time.Now().UTC(), PrepareRequeueMaxAttempts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "superseded", spoolFileID("prepare-aged-stale")+".json")); err != nil {
		t.Fatalf("aged stale notice not superseded: %v", err)
	}
}

// A notice stranded longer than the retry horizon belongs to a dead or
// superseded onboarding; requeueing it would resurrect a stale link.
func TestRequeueFailedPreparesSkipsAgedRecord(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("e", 64)
	d := Delivery{ID: "prepare-aged", ChatID: 11, Text: "credentials", PrepareKey: key, PrepareVersion: 1, CreatedAt: time.Now().UTC()}
	if ok, err := s.enqueuePrepareDelivery(d); err != nil || !ok {
		t.Fatalf("enqueue: %t %v", ok, err)
	}
	if _, err := s.ClaimDelivery(); err != nil {
		t.Fatal(err)
	}
	if err := s.UncertainDelivery("prepare-aged"); err != nil {
		t.Fatal(err)
	}
	failedPath := filepath.Join(root, "outbox", "failed", spoolFileID("prepare-aged")+".json")
	aged := time.Now().Add(-2 * prepareRequeueHorizon)
	if err := os.Chtimes(failedPath, aged, aged); err != nil {
		t.Fatal(err)
	}
	n, err := s.RequeueFailedPrepares(time.Now().UTC(), PrepareRequeueMaxAttempts)
	if err != nil || n != 0 {
		t.Fatalf("aged notice requeued: %d %v", n, err)
	}
	if _, err := os.Stat(failedPath); err != nil {
		t.Fatalf("aged notice left failed: %v", err)
	}
}

// Ordinary failed deliveries keep their terminal semantics — the sweep
// exists for prepare notices, not for replies a human already saw fail.
func TestRequeueFailedPreparesSkipsOrdinaryDelivery(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDelivery(Delivery{ID: "reply-fail", ChatID: 11, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDelivery(); err != nil {
		t.Fatal(err)
	}
	if err := s.UncertainDelivery("reply-fail"); err != nil {
		t.Fatal(err)
	}
	n, err := s.RequeueFailedPrepares(time.Now().UTC(), PrepareRequeueMaxAttempts)
	if err != nil || n != 0 {
		t.Fatalf("ordinary delivery requeued: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "failed", spoolFileID("reply-fail")+".json")); err != nil {
		t.Fatalf("ordinary delivery left failed: %v", err)
	}
}
