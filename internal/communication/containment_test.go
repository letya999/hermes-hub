package communication

import (
	"context"
	"encoding/json"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMasterNoticeQueuesBeforeAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c := testConfig(t)
	var acknowledgements atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-service" {
			t.Error("notice request lost service authority")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/holds":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"principal_id":"alice","context_id":"alice","active":true,"epoch":1,"reason":"manual","notice_id":"hold-alice-alice-1","notice_pending":true},{"principal_id":"unknown","context_id":"unknown","active":true,"epoch":1,"reason":"manual","notice_id":"hold-unknown-unknown-1","notice_pending":true}]`))
		case "/v1/holds/ack":
			var body struct {
				NoticeID string `json:"notice_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(409)
				return
			}
			if body.NoticeID == "hold-unknown-unknown-1" {
				// Unroutable principal: acknowledged as undeliverable, never queued.
				w.WriteHeader(200)
				return
			}
			if body.NoticeID != "hold-alice-alice-1" {
				t.Error("acknowledged an unexpected notice")
				w.WriteHeader(409)
				return
			}
			data, err := os.ReadFile(filepath.Join(c.SpoolDir, "outbox", "pending", "hold-alice-alice-1.json"))
			var d Delivery
			if err != nil || json.Unmarshal(data, &d) != nil || d.ChatID != 11 || d.Channel != "telegram_bot" || !strings.Contains(d.Text, "manual") {
				t.Errorf("notice acknowledged before durable owner outbox: %+v %v", d, err)
			}
			if acknowledgements.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			cancel()
			w.WriteHeader(200)
		default:
			t.Errorf("notice tried to execute a runtime: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c.Supervised = true
	c.RuntimeURL = srv.URL
	c.RuntimeAuth = "synthetic-service"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.controlLoop(ctx)
	if acknowledgements.Load() != 2 {
		t.Errorf("lost-ack retry missing: %d", acknowledgements.Load())
	}
	entries, err := os.ReadDir(filepath.Join(c.SpoolDir, "outbox", "pending"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("owner notice missing, duplicated or misrouted: entries=%d %v", len(entries), err)
	}
}

func TestMasterNoticeAcksUndeliverable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c := testConfig(t)
	var acks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-service" {
			t.Error("notice request lost service authority")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/holds":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"principal_id":"nobody","context_id":"nobody","active":true,"epoch":1,"reason":"manual","notice_id":"hold-nobody-nobody-1","notice_pending":true}]`))
		case "/v1/holds/ack":
			var body struct {
				NoticeID string `json:"notice_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.NoticeID != "hold-nobody-nobody-1" {
				w.WriteHeader(409)
				return
			}
			acks.Add(1)
			cancel()
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c.Supervised = true
	c.RuntimeURL = srv.URL
	c.RuntimeAuth = "synthetic-service"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.controlLoop(ctx)
	if acks.Load() != 1 {
		t.Fatalf("undeliverable notice was not acknowledged: %d", acks.Load())
	}
	entries, err := os.ReadDir(filepath.Join(c.SpoolDir, "outbox", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("undeliverable notice reached the outbox: %d entries", len(entries))
	}
}

// The notice loop must idle when the hold set is clean: it polls fast only
// while a pending notice exists, otherwise the 1s control tick would spam
// /v1/holds forever.
func TestSyncMasterNoticesReportsPending(t *testing.T) {
	c := testConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/holds":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"principal_id":"alice","context_id":"alice","active":true,"epoch":1,"reason":"manual"},{"principal_id":"bob","context_id":"bob","active":true,"epoch":1,"reason":"manual","notice_id":"n-1","notice_pending":true}]`))
		case "/v1/holds/ack":
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c.Supervised = true
	c.RuntimeURL = srv.URL
	c.RuntimeAuth = "synthetic-service"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := g.syncMasterNotices(t.Context())
	if err != nil || !pending {
		t.Fatalf("pending notice not reported: %v %v", pending, err)
	}
}

// With a clean hold set the control loop must idle the supervisor poll: one
// sync per idle window, not one per second — the 1s tick exists for the
// delivery pipeline, not for holds.
func TestNoticePollIdlesWhenClean(t *testing.T) {
	c := testConfig(t)
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c.Supervised = true
	c.RuntimeURL = srv.URL
	c.RuntimeAuth = "synthetic-service"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	g.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go g.controlLoop(ctx)
	time.Sleep(2500 * time.Millisecond)
	if got := polls.Load(); got != 1 {
		t.Fatalf("idle loop polled holds %d times in 2.5s", got)
	}
	mu.Lock()
	now = now.Add(11 * time.Second)
	mu.Unlock()
	time.Sleep(2500 * time.Millisecond)
	if got := polls.Load(); got != 2 {
		t.Fatalf("stale window did not re-poll: %d", got)
	}
}

// The hourly compaction must apply the configured journal cap, not the
// built-in default — a small deployment limit has to reach the spool.
func TestControlLoopCompactsWithConfiguredLimits(t *testing.T) {
	c := testConfig(t)
	c.SpoolMaxUsageEntries = 3
	c.SpoolMaxFiles = 2048
	c.SpoolMaxArchiveBytes = 64 << 20
	now := time.Now()
	if err := os.MkdirAll(c.SpoolDir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(c.SpoolDir, "usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		e := usageEntry{Version: 2, ScopeID: "user:alice", ContextID: "c", SessionID: "s", JobID: "j" + string(rune('0'+i)), RunID: "r" + string(rune('0'+i)), MeasuredAt: now.Add(-time.Duration(i) * time.Hour)}
		body, _ := json.Marshal(e)
		if _, err := f.Write(append(body, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go g.controlLoop(ctx)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := g.spool.usageEntriesLocked()
		if err == nil && len(entries) <= 3 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	entries, _ := g.spool.usageEntriesLocked()
	t.Fatalf("journal not capped at configured limit: %d entries", len(entries))
}

func TestMasterHoldFencesOutbox(t *testing.T) {
	for _, tc := range []struct {
		name, holds       string
		status, wantSends int
		directory         string
		epoch             uint64
	}{
		{"held", `[{"principal_id":"alice","context_id":"alice","active":true,"epoch":1}]`, 200, 0, "failed", 0},
		{"old-after-resume", `[{"principal_id":"alice","context_id":"alice","active":false,"epoch":2}]`, 200, 0, "failed", 0},
		{"fresh-after-resume", `[{"principal_id":"alice","context_id":"alice","active":false,"epoch":2}]`, 200, 1, "done", 2},
		{"other-context", `[{"principal_id":"bob","context_id":"bob","active":true,"epoch":1}]`, 200, 1, "done", 0},
		{"unavailable", "", 503, 0, "pending", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/holds" {
					t.Errorf("unexpected operational request: %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.holds))
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
			g.restart = func(context.Context, hubruntime.ExecuteRequest) error { return nil }
			api := &fakeAPI{}
			g.api = api
			s, job := streamJob(t)
			g.spool = s
			job.ID = "epoch-job"
			job.IdempotencyKey = "epoch-key"
			encoded, _ := json.Marshal(struct {
				Job
				Epoch uint64 `json:"operational_epoch"`
			}{job, tc.epoch})
			if err := json.Unmarshal(encoded, &job); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Enqueue(job); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "completed", Text: "private-result"}); err != nil {
				t.Fatal(err)
			}
			d := Delivery{ID: "fenced-response", JobID: job.ID, Channel: "telegram_bot", ChatID: 11, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Text: "private-result", CreatedAt: g.now()}
			if err := s.EnqueueDelivery(d); err != nil {
				t.Fatal(err)
			}
			g.deliverOne(t.Context())
			if len(api.sent) != tc.wantSends {
				t.Errorf("hold boundary sent %d messages, want %d", len(api.sent), tc.wantSends)
			}
			path := filepath.Join(s.root, "outbox", tc.directory, "fenced-response.json")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("delivery not retained as %s: %v", tc.directory, err)
			}
			if tc.wantSends == 0 {
				var saved struct {
					Attempts  int    `json:"attempts"`
					BlockedBy string `json:"blocked_by"`
				}
				if json.Unmarshal(body, &saved) != nil || saved.Attempts != 0 || saved.BlockedBy == "" {
					t.Errorf("blocked outcome not explicit: %+v", saved)
				}
			}
		})
	}
}

func TestEnqueueUsesFreshAdmissionEpoch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		holds   string
		allowed bool
	}{
		{"held", 200, `[{"principal_id":"alice","context_id":"alice","active":true,"epoch":1}]`, false},
		{"unavailable", 503, ``, false},
		{"resumed", 200, `[{"principal_id":"alice","context_id":"alice","active":false,"epoch":2}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wakes := make(chan uint64, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-service" {
					t.Error("missing service authority")
					w.WriteHeader(401)
					return
				}
				if r.URL.Path == "/v1/holds" {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.holds))
					return
				}
				var request struct {
					Epoch uint64 `json:"operational_epoch"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid runtime request")
				}
				switch r.URL.Path {
				case "/v1/wake":
					wakes <- request.Epoch
					w.WriteHeader(200)
				case "/v1/execute":
					if request.Epoch != 2 {
						t.Errorf("runtime lost epoch: %d", request.Epoch)
					}
					_, _ = w.Write([]byte(`{"status":"completed","text":"ok"}`))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
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
			g.api = &fakeAPI{}
			user := c.Users[0]
			task, err := g.spool.ResolveTask(user, 11, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			err = g.enqueueChannelJob(t.Context(), user, task, "telegram_bot", "new-job", "new-key", 11, 0, "hello", 11, "")
			job, claimErr := g.spool.ClaimJob()
			if claimErr != nil {
				t.Fatal(claimErr)
			}
			if !tc.allowed {
				if job != nil {
					t.Error("denied admission enqueued work")
				}
				if (tc.name == "held") != (err == nil) {
					t.Errorf("held update must be acknowledged; unavailable control must be retried: %v", err)
				}
				return
			}
			if err != nil || job == nil {
				t.Fatalf("fresh admission failed: %v", err)
			}
			if job.OperationalEpoch != 2 {
				t.Errorf("durable job lost epoch: %d", job.OperationalEpoch)
			}
			select {
			case epoch := <-wakes:
				if epoch != 2 {
					t.Errorf("wake lost epoch: %d", epoch)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("wake did not finish")
			}
			if _, err := (HTTPRunner{URL: srv.URL, Auth: c.RuntimeAuth, HTTP: srv.Client()}).RunOutcome(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
		})
	}
}
