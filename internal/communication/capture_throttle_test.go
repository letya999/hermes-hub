package communication

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// TestPumpStopsWhenToolHubSheds: a backend answering 429 stops the pass
// after the fail-stop budget — pending events survive for the next sweep
// instead of charging a saturated backend for refusals.
func TestPumpStopsWhenToolHubSheds(t *testing.T) {
	posts := &atomic.Int32{}
	shed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(shed.Close)

	f := newCaptureFixture(t)
	f.enroll("alice", true, true)
	c := baseTestConfig(t)
	c.ToolHubURL, c.Supervised, c.RuntimeAuth, c.RuntimeURL = shed.URL, true, "comm-token", f.supervisor.URL
	g := f.gateway(t, c)

	job := captureJob(g, "alice", "job-shed-1")
	f.records[job.ID] = api.RunContext{JobID: job.ID, UserID: "alice", Environment: "dev", Status: "running",
		ScopeID: job.ScopeID, Trigger: job.Trigger, Channel: job.Channel, Envelope: identity.Envelope{PrincipalID: "alice"}}
	for _, kind := range []string{"a", "b", "c", "d", "e"} {
		ev := captureEventForTurn(job)
		ev.EventID, ev.Kind = "turn-"+job.ID+"-"+kind, "turn"
		if err := g.spool.EnqueueCapture(ev); err != nil {
			t.Fatal(err)
		}
	}

	g.pumpCaptures(context.Background())
	if got := posts.Load(); got < 1 || got > int32(captureFailStop) {
		t.Fatalf("shed backend received %d posts, want between 1 and %d", got, captureFailStop)
	}
	pending, err := g.spool.pendingCaptures()
	if err != nil || len(pending) != 5 {
		t.Fatalf("shed pass lost events: pending=%d err=%v", len(pending), err)
	}
}

// TestThrottleSelfRegulates: the adaptive window opens once rejects
// outnumber accepts, and its cap keeps a probe alive — rand at 0.95 must
// always pass so recovery is observable.
func TestThrottleSelfRegulates(t *testing.T) {
	g := &Gateway{}
	now := time.Now()
	for i := 0; i < 10; i++ {
		if g.captureThrottled(now, 0.99) {
			t.Fatalf("healthy window shed at attempt %d", i)
		}
		g.captureThrottleRecord(true)
		now = now.Add(time.Millisecond)
	}
	// Backend starts refusing: requests grow past K*accepts, the throttle
	// must fire deterministically at rand just under the cap — and must
	// never fire at rand above the cap, even at worst saturation.
	seen := false
	for i := 0; i < 40 && !seen; i++ {
		if g.captureThrottled(now, 0.5) {
			seen = true
		}
		g.captureThrottleRecord(false)
		now = now.Add(time.Millisecond)
	}
	if !seen {
		t.Fatal("throttle never closed under sustained refusals")
	}
	for i := 0; i < 50; i++ {
		if g.captureThrottled(now, 0.95) {
			t.Fatal("throttle cap reached 1.0 — no recovery probe left")
		}
		g.captureThrottleRecord(false)
		now = now.Add(time.Millisecond)
	}
	// Recovery is driven by accepted probes on the still-hot window: each
	// iteration posts a probe (rand above the cap always passes) whose
	// accept rebalances the window until ordinary rand=0.5 traffic flows.
	recovered := false
	for i := 0; i < 80 && !recovered; i++ {
		if g.captureThrottled(now, 0.95) {
			t.Fatal("probe shed above the cap")
		}
		g.captureThrottleRecord(true)
		now = now.Add(time.Millisecond)
		// Sample the window without spending a request: a raw shed check
		// would itself count as an attempt.
		w := g.captureThrottle
		recovered = (w.requests-captureThrottleK*w.accepts)/(w.requests+1) <= 0.5
	}
	if !recovered {
		t.Fatal("throttle never reopened after accepts returned")
	}
}
