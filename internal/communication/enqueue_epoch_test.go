package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// Supervised admission must fence every enqueue path, not only chat jobs:
// routine/cron jobs enter through Deps.EnqueueJob and previously reached the
// supervisor with operational_epoch 0, which beginJob rejects against any
// recorded hold epoch (live regression: run.failed + stale_epoch delivery).
func TestEnqueueStampsHoldEpoch(t *testing.T) {
	executed := make(chan uint64, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-service" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/holds":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"principal_id":"alice","context_id":"alice","active":false,"epoch":2,"reason":"manual","notice_id":"hold-alice-alice-2"}]`))
		case "/v1/execute":
			var request struct {
				Epoch uint64 `json:"operational_epoch"`
			}
			if json.NewDecoder(r.Body).Decode(&request) == nil {
				executed <- request.Epoch
			}
			_, _ = w.Write([]byte(`{"status":"completed","text":"ok"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := testConfig(t)
	c.Supervised = true
	c.RuntimeURL = srv.URL
	c.RuntimeAuth = "synthetic-service"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "cron-epoch", IdempotencyKey: "cron-epoch-key", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Trigger: "cron", Text: "tick"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	mapping, found, err := g.spool.Mapping("cron-epoch")
	if err != nil || !found {
		t.Fatalf("routine job mapping missing: %v", err)
	}
	if mapping.OperationalEpoch != 2 {
		t.Fatalf("mapping lost hold epoch: %d", mapping.OperationalEpoch)
	}
	claimed, err := g.spool.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatalf("stamped job must be claimable: %v", err)
	}
	if _, err := (HTTPRunner{URL: srv.URL, Auth: c.RuntimeAuth, HTTP: srv.Client()}).RunOutcome(t.Context(), *claimed); err != nil {
		t.Fatal(err)
	}
	if epoch := <-executed; epoch != 2 {
		t.Fatalf("runtime lost hold epoch: %d", epoch)
	}
}

// Durable producers bake the principal's policy attestation at creation; the
// attestation is part of the authorized intent. Admission must preserve it —
// re-stamping a stored job to the current policy would let stale schedules
// pass authorizeCurrentPolicy long after the owner re-consented.
func TestEnqueueKeepsBakedPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/holds" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed","text":"ok"}`))
	}))
	defer srv.Close()
	c := testConfig(t)
	c.Supervised = true
	c.RuntimeURL = srv.URL
	c.RuntimeAuth = "synthetic-service"
	c.Users[0].PolicyVersion = "policy-current"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	principal := c.Users[0].ID
	job := Job{Envelope: identity.TelegramEnvelope(principal, 11, principal, "policy-old"), ID: "cron-policy", IdempotencyKey: "cron-policy-key", UserID: principal, ActorID: principal, ScopeID: "user:" + principal, Channel: "telegram_bot", ChatID: 11, Trigger: "cron", Text: "tick"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	mapping, found, err := g.spool.Mapping("cron-policy")
	if err != nil || !found {
		t.Fatalf("re-stamped job mapping missing: %v", err)
	}
	if mapping.PolicyVersion != "policy-old" {
		t.Fatalf("admission must not re-stamp the baked attestation: %q", mapping.PolicyVersion)
	}
}

func TestEnqueueSuppressedByActiveHold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-service" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/v1/holds" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"principal_id":"alice","context_id":"alice","active":true,"epoch":3,"reason":"manual","notice_id":"hold-alice-alice-3"}]`))
			return
		}
		t.Errorf("held job reached the runtime: %s", r.URL.Path)
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := testConfig(t)
	c.Supervised = true
	c.RuntimeURL = srv.URL
	c.RuntimeAuth = "synthetic-service"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "held-job", IdempotencyKey: "held-key", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Trigger: "cron", Text: "tick"}
	accepted, err := g.spool.Enqueue(job)
	if err != nil || !accepted {
		t.Fatalf("held enqueue must be acknowledged, not enqueued: accepted=%v err=%v", accepted, err)
	}
	if claimed, err := g.spool.ClaimJob(); err != nil || claimed != nil {
		t.Fatalf("active hold leaked a job: %+v %v", claimed, err)
	}
}

// The hold check performs I/O (the supervisor's /v1/holds projection);
// holding the spool lock across it starves every other writer for the
// whole fetch. The enqueue boundary resolves holds before taking the lock
// and inner producers read only the last snapshot.
func TestEnqueueHoldCheckRunsOutsideSpoolLock(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.refreshHolds = func(context.Context) error {
		once.Do(func() { close(started) })
		<-release
		return nil
	}
	s.holdsSnapshot = func() []masterHold { return nil }
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "cron-lockfree", IdempotencyKey: "cron-lockfree-key", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Trigger: "cron", Text: "tick"}
	enqueued := make(chan error, 1)
	go func() {
		_, err := s.Enqueue(job)
		enqueued <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("hold resolver never ran")
	}
	read := make(chan struct{})
	go func() {
		_, _ = s.EventStateOf("unrelated")
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(2 * time.Second):
		t.Fatal("spool lock held across the hold fetch")
	}
	close(release)
	select {
	case err := <-enqueued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("enqueue never finished")
	}
	if claimed, err := s.ClaimJob(); err != nil || claimed == nil {
		t.Fatalf("resolved job must enqueue: %v", err)
	}
}
