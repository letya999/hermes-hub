package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// A supervisor that is down when the message arrives must not lose the job:
// dial errors are retried inside the cold-start budget and the job runs once.
func TestRunOutcomeRetriesConnectionRefusedUntilSupervisorReturns(t *testing.T) {
	old := submitRetryBase
	submitRetryBase = time.Millisecond
	defer func() { submitRetryBase = old }()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	go func() {
		time.Sleep(60 * time.Millisecond)
		reborn, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		_ = http.Serve(reborn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"job_id": "job-r", "status": "completed", "text": "ok"})
		}))
	}()
	runner := HTTPRunner{URL: "http://" + addr, Auth: "x"}
	outcome, err := runner.RunOutcome(context.Background(), Job{ID: "job-r", Text: "hi"})
	if err != nil || outcome.Text != "ok" {
		t.Fatalf("job lost during supervisor restart: %+v %v", outcome, err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("admissions=%d want 1", got)
	}
}

// Once a request may have reached the supervisor — the connection was accepted
// — retrying could double-run the job. It must fail uncertain after one try.
func TestRunOutcomeNeverRetriesAfterRequestSent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("no hijacker")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}))
	defer server.Close()
	old := submitRetryBase
	submitRetryBase = time.Millisecond
	defer func() { submitRetryBase = old }()
	runner := HTTPRunner{URL: server.URL, Auth: "x"}
	_, err := runner.RunOutcome(context.Background(), Job{ID: "job-mid", Text: "hi"})
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("post-send failure = %v, want ErrUncertain", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("submissions=%d, want 1 — a possibly-admitted job must never be retried", got)
	}
}

// HUB_RUN_BUDGET is the single run cap: a stream that stays busy is still cut
// at the budget even though the idle deadline never fires.
func TestRunOutcomeRunBudgetFromEnv(t *testing.T) {
	s, job := streamJob(t)
	t.Setenv("HUB_RUN_BUDGET", "80ms")
	t.Setenv("HUB_STREAM_IDLE", "2s")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		flusher, _ := w.(http.Flusher)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			event := streamEvent(job)
			event.LastEvent = "run.heartbeat"
			_ = json.NewEncoder(w).Encode(event)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(ctx, job)
	if err == nil || outcome.Status != "uncertain" {
		t.Fatalf("over-budget stream outcome=%+v err=%v", outcome, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("run budget not enforced: %v", elapsed)
	}
}

// Once the cold-start budget expires the job ends with a named failure, not an
// uncertain verdict and not endless retries.
func TestRunOutcomeColdStartBudgetExpires(t *testing.T) {
	old, oldBudget := submitRetryBase, coldStartBudget
	submitRetryBase = time.Millisecond
	coldStartBudget = func() time.Duration { return 50 * time.Millisecond }
	defer func() { submitRetryBase, coldStartBudget = old, oldBudget }()
	for _, kind := range []string{"dial", "503"} {
		t.Run(kind, func(t *testing.T) {
			url := ""
			if kind == "dial" {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				url = "http://" + ln.Addr().String()
				_ = ln.Close()
			} else {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				defer server.Close()
				url = server.URL
			}
			runner := HTTPRunner{URL: url, Auth: "x"}
			_, err := runner.RunOutcome(context.Background(), Job{ID: "job-dead", Text: "hi"})
			if err == nil {
				t.Fatal("unreachable supervisor produced a success")
			}
			if errors.Is(err, ErrUncertain) {
				t.Fatalf("budget expiry reported uncertain: %v", err)
			}
			if !strings.Contains(err.Error(), "runtime_unavailable") {
				t.Fatalf("failure does not name the cause: %v", err)
			}
		})
	}
}
