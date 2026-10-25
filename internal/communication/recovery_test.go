package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestLiveRecoveryObservesOriginalRunWithoutInput(t *testing.T) {
	for _, sensitive := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "sensitive"}[sensitive], func(t *testing.T) {
			s, err := NewSpool(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "recover", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "recover", ChatID: 11, Text: "private input", Sensitive: sensitive}
			if _, err = s.Enqueue(job); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ClaimJob(); err != nil {
				t.Fatal(err)
			}
			event := streamEvent(job)
			if err = s.RecordStreamEvent(job, event); err != nil {
				t.Fatal(err)
			}
			if err = s.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "uncertain"}); err != nil {
				t.Fatal(err)
			}
			if err = s.FailJob(job.ID); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err = s.RequeueObservations(now, false); err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ClaimJob()
			if err != nil || claimed == nil || claimed.Text != "" {
				t.Fatalf("recovered input=%+v %v", claimed, err)
			}
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/usage" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				calls++
				var request hubruntime.ExecuteRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Text != "" || r.URL.Path != "/v1/resume" || request.Envelope != job.Envelope {
					t.Errorf("resubmitted input or changed owner: %+v %s", request, r.URL.Path)
				}
				w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
				event.Status, event.LastEvent, event.EventID = "interrupted", "run.interrupted", "terminal:interrupted"
				_ = json.NewEncoder(w).Encode(event)
			}))
			defer api.Close()
			outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(context.Background(), *claimed)
			if err == nil || outcome.Status != "interrupted" || calls != 1 {
				t.Fatalf("outcome=%+v %v calls=%d", outcome, err, calls)
			}
			if err = s.FailJob(job.ID); err != nil {
				t.Fatal(err)
			}
			if err = s.RequeueObservations(now.Add(time.Hour), false); err != nil {
				t.Fatal(err)
			}
			if claimed, err = s.ClaimJob(); err != nil || claimed != nil {
				t.Fatalf("terminal replay=%+v %v", claimed, err)
			}
		})
	}
}

func TestWorkerUsesDurableCompletionAndDoesNotSuggestUncertainReplay(t *testing.T) {
	for _, completed := range []bool{true, false} {
		t.Run(map[bool]string{true: "durable-completion", false: "unknown-admission"}[completed], func(t *testing.T) {
			s, job := streamJob(t)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if completed {
					event := streamEvent(job)
					event.Status, event.LastEvent, event.EventID, event.Text = "completed", "run.completed", "terminal:completed", "saved answer"
					if err := s.RecordStreamEvent(job, event); err != nil {
						t.Error(err)
					}
				}
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "uncertain"})
			}))
			defer api.Close()
			transport := &fakeAPI{}
			g := &Gateway{spool: s, api: transport, runner: HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}, config: Config{RuntimeURL: api.URL, Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); g.worker(ctx) }()
			dir := "failed"
			if completed {
				dir = "done"
			}
			deadline := time.Now().Add(30 * time.Second)
			settled := false
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(s.root, dir, spoolFileID(job.ID)+".json")); err == nil {
					settled = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			<-done
			if !settled {
				t.Fatal("worker did not settle durable outcome")
			}
			mapping, _, err := s.Mapping(job.ID)
			if err != nil || (completed && mapping.Status != "completed") || (!completed && mapping.Status != "uncertain") {
				t.Fatalf("mapping=%+v %v", mapping, err)
			}
			transport.mu.Lock()
			messages := append([]string(nil), transport.sent...)
			transport.mu.Unlock()
			if delivery, err := s.ClaimDelivery(); err != nil {
				t.Fatal(err)
			} else if delivery != nil {
				messages = append(messages, delivery.Text)
			}
			if completed && len(messages) != 1 {
				t.Fatalf("messages=%v", messages)
			}
			if completed && messages[0] != "saved answer" {
				t.Fatal(messages)
			}
			// SPEC-0053 R3.2: an uncertain outcome sends no owner notice while
			// the reconciliation budget is open — no premature "unknown", no
			// replay suggestion.
			if !completed && len(messages) != 0 {
				t.Fatalf("premature uncertain notice: %v", messages)
			}
		})
	}
}

func TestObservationBackoffAndUnknownAdmission(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{Status: "uncertain"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FailJob(job.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.RequeueObservations(now, false); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimJob(); err != nil || claimed != nil {
		t.Fatal("unknown admission repeated")
	}
	if err := s.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 8; attempt++ {
		if err := s.RecordOutcome(job.ID, RunOutcome{Status: "uncertain"}); err != nil {
			t.Fatal(err)
		}
		if attempt > 0 {
			if err := s.FailJob(job.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.RequeueObservations(now.Add(-time.Second), false); err != nil {
				t.Fatal(err)
			}
			if claimed, err := s.ClaimJob(); err != nil || claimed != nil {
				t.Fatal("observation skipped persisted backoff")
			}
		}
		if err := s.RequeueObservations(now, false); err != nil {
			t.Fatal(err)
		}
		mapping, _, err := s.Mapping(job.ID)
		if err != nil || mapping.NextObservationAt.Sub(now) > 30*time.Second {
			t.Fatalf("unbounded delay: %+v %v", mapping, err)
		}
		if claimed, err := s.ClaimJob(); err != nil || claimed == nil {
			t.Fatalf("missing observation: %+v %v", claimed, err)
		}
		now = mapping.NextObservationAt
	}
}

func TestObservationStopsAfterBudgetNotice(t *testing.T) {
	for _, state := range []string{"pending", "sending", "done", "failed", "absent", "before-budget"} {
		t.Run(state, func(t *testing.T) {
			s, job := streamJob(t)
			if _, err := s.ClaimJob(); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordOutcome(job.ID, RunOutcome{Status: "uncertain"}); err != nil {
				t.Fatal(err)
			}
			if err := s.FailJob(job.ID); err != nil {
				t.Fatal(err)
			}
			mapping, _, err := s.Mapping(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			now := mapping.UncertainSince.Add(hubruntime.RunBudget())
			if state == "before-budget" {
				now = now.Add(-time.Second)
			}
			if state != "absent" {
				dir := state
				if state == "before-budget" {
					dir = "done"
				}
				d := Delivery{ID: "job-" + job.ID + "-uncertain", ChatID: job.ChatID, Text: "unknown outcome"}
				if err := atomicJSON(filepath.Join(s.root, "outbox", dir, spoolFileID(d.ID)+".json"), d); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := s.RequeueObservations(now, true); err != nil {
					t.Fatal(err)
				}
			}
			claimed, err := s.ClaimJob()
			wantObservation := state == "absent" || state == "before-budget"
			if err != nil || (claimed != nil) != wantObservation {
				t.Fatalf("observation=%v want=%v err=%v", claimed != nil, wantObservation, err)
			}
			if !wantObservation {
				after, _, err := s.Mapping(job.ID)
				if err != nil || after.Status != "uncertain" || !after.UncertainSince.Equal(mapping.UncertainSince) || after.ObservationAttempts != mapping.ObservationAttempts {
					t.Fatalf("changed unknown outcome: %+v err=%v", after, err)
				}
			}
		})
	}
}

type uncertainRunner struct{}

func (uncertainRunner) RunOutcome(context.Context, Job) (RunOutcome, error) {
	return RunOutcome{Status: "uncertain", LastEvent: "run.unknown"}, ErrUncertain
}
func (uncertainRunner) Run(context.Context, Job, User) (string, error) {
	return "", ErrUncertain
}

// SPEC-0053 R3.2: a freshly-uncertain job must not tell the user its outcome is
// unknown while the reconciliation budget is still open — the reconciler or a
// named cause will settle it first.
func TestGatewayNoUncertainNoticeBeforeBudget(t *testing.T) {
	s, job := streamJob(t)
	transport := &fakeAPI{}
	g := &Gateway{spool: s, api: transport, runner: uncertainRunner{}, config: Config{RuntimeURL: "http://runtime", Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	settled := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(s.root, "failed", spoolFileID(job.ID)+".json")); err == nil {
			settled = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if !settled {
		t.Fatal("worker did not settle uncertain job")
	}
	mapping, found, err := s.Mapping(job.ID)
	if err != nil || !found || mapping.Status != "uncertain" {
		t.Fatalf("mapping=%+v found=%v err=%v", mapping, found, err)
	}
	transport.mu.Lock()
	sent := append([]string(nil), transport.sent...)
	transport.mu.Unlock()
	if delivery, err := s.ClaimDelivery(); err != nil {
		t.Fatal(err)
	} else if delivery != nil {
		sent = append(sent, delivery.Text)
	}
	for _, text := range sent {
		if strings.Contains(text, "неизвестен") || strings.Contains(text, job.ID) {
			t.Fatalf("uncertain notice sent before reconciliation budget: %v", sent)
		}
	}
}

// SPEC-0053 R3.2: once the reconciliation window has lapsed, a still-unknown
// outcome is no longer held silent — the owner gets the named notice.
func TestUncertainNoticeAfterBudget(t *testing.T) {
	s, job := streamJob(t)
	if err := s.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "uncertain", LastEvent: "run.unknown"}); err != nil {
		t.Fatal(err)
	}
	mapping, found, err := s.Mapping(job.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	// Backdate the reconciliation window so it has already lapsed.
	mapping.UncertainSince = time.Now().Add(-hubruntime.RunBudget() - time.Minute)
	if err := atomicJSON(s.mappingPath(job.ID), &mapping); err != nil {
		t.Fatal(err)
	}
	transport := &fakeAPI{}
	g := &Gateway{spool: s, api: transport, runner: uncertainRunner{}, config: Config{RuntimeURL: "http://runtime", Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	noticed := false
	for time.Now().Before(deadline) {
		transport.mu.Lock()
		for _, text := range transport.sent {
			if strings.Contains(text, job.ID) {
				noticed = true
			}
		}
		transport.mu.Unlock()
		if noticed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if !noticed {
		t.Fatal("no uncertain notice after reconciliation budget lapsed")
	}
}

// SPEC-0053 R3.2: an uncertain job whose admission was never confirmed is
// re-observed through the supervisor's resume path — the reconciler's settled
// outcome is what finally reaches the user, exactly once.
func TestWorkerReObservesUnknownAdmission(t *testing.T) {
	s, job := streamJob(t)
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "uncertain", LastEvent: "run.unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FailJob(job.ID); err != nil {
		t.Fatal(err)
	}
	var resumed atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/resume" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		resumed.Add(1)
		var request hubruntime.ExecuteRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "run-7", SessionID: "session-7", RuntimeGeneration: "gen-7", Status: "completed", LastEvent: "run.completed", EventID: "terminal:completed", Text: "late answer"})
	}))
	defer api.Close()
	transport := &fakeAPI{}
	g := &Gateway{spool: s, api: transport, runner: HTTPRunner{URL: api.URL, Auth: "secret", Spool: s, JobsAPI: true}, config: Config{RuntimeURL: api.URL, Supervised: true, Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	answered := false
	for time.Now().Before(deadline) {
		transport.mu.Lock()
		for _, text := range transport.sent {
			if text == "late answer" {
				answered = true
			}
		}
		transport.mu.Unlock()
		if answered {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if !answered {
		t.Fatal("settled outcome was not delivered after resume")
	}
	mapping, _, err := s.Mapping(job.ID)
	if err != nil || mapping.Status != "completed" {
		t.Fatalf("mapping=%+v err=%v", mapping, err)
	}
}

// A resume conflict means the supervisor has not settled the job yet (or is
// still reconciling): the mapping stays uncertain and keeps cycling rather
// than failing the user with a transport error.
func TestResumeConflictStaysUncertain(t *testing.T) {
	s, job := streamJob(t)
	if err := s.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "uncertain", LastEvent: "run.unknown"}); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "run admission unknown"})
	}))
	defer api.Close()
	outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s, JobsAPI: true}).RunOutcome(context.Background(), job)
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("resume conflict = outcome %+v err %v, want uncertain", outcome, err)
	}
}

// SPEC-0053 R3.3: a poison update must not kill the polling loop — it is
// quarantined with metadata only and the offset advances past it.
func TestHandleUpdateErrorQuarantinesAndContinues(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{err: errors.New("answer callback failed")}
	g := &Gateway{spool: s, api: api, config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	updates := []Update{
		{UpdateID: 100, CallbackQuery: &CallbackQuery{ID: "cb-poison", From: &TGUser{ID: 11}, Data: "bogus"}},
		{UpdateID: 101, Message: &Message{MessageID: 5, From: &TGUser{ID: 99}, Chat: TGChat{ID: 55, Type: "private"}, Text: "hello"}},
	}
	offset, err := g.processBatch(context.Background(), updates, 0)
	if err != nil {
		t.Fatalf("poison update terminated the batch: %v", err)
	}
	if offset != 102 {
		t.Fatalf("offset = %d, want 102 (past both updates)", offset)
	}
	if _, err := os.Stat(filepath.Join(s.root, "quarantine", "100.json")); err != nil {
		t.Fatalf("poison update not quarantined: %v", err)
	}
	// The second update still processed: unknown-user reply queued.
	entries, _ := os.ReadDir(filepath.Join(s.root, "outbox", "pending"))
	if len(entries) == 0 {
		t.Fatal("update after poison was never processed")
	}
}

// SPEC-0053 R3.3: the quarantine record holds metadata only — update content
// can contain secrets and is never persisted there.
func TestQuarantineHasNoContentByDefault(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{err: errors.New("answer failed")}
	g := &Gateway{spool: s, api: api, config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	poison := Update{UpdateID: 200, CallbackQuery: &CallbackQuery{ID: "cb-x", From: &TGUser{ID: 11}, Data: "opaque-secret-payload", Message: &Message{Chat: TGChat{ID: 55}, Text: "do not store me"}}}
	if _, err := g.processBatch(context.Background(), []Update{poison}, 0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.root, "quarantine", "200.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"do not store me", "opaque-secret-payload", "answer failed", "text", "data"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("quarantine record leaked content: %s", string(b))
		}
	}
}

// SPEC-0053 R3.3: the quarantine directory is capped — a poison storm evicts
// the oldest records instead of filling the disk.
func TestQuarantineEvictsOldestBeyondCap(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.root, "quarantine")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < quarantineCap; i++ {
		name := filepath.Join(dir, strconv.Itoa(1000+i)+".json")
		if err := os.WriteFile(name, []byte(`{"update_id":1}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Oldest record: give it a clearly-old mtime.
	old := filepath.Join(dir, "1000.json")
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(old, past, past)
	if err := s.Quarantine(Update{UpdateID: 9999}, errors.New("boom"), time.Now()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != quarantineCap {
		t.Fatalf("quarantine size=%d err=%v, want %d", len(entries), err, quarantineCap)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("oldest quarantine record was not evicted")
	}
	if _, err := os.Stat(filepath.Join(dir, "9999.json")); err != nil {
		t.Fatal("new quarantine record missing")
	}
}

// SPEC-0053 R3.4: the synthetic canary runs through the real chain but in its
// own conversation — never inside a user chat.
func TestCanaryUsesOwnConversation(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		var request hubruntime.ExecuteRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		for _, event := range []hubruntime.ExecuteResponse{
			{JobID: request.JobID, SessionID: "canary-sess", RunID: "canary-run", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "run.admitted", EventID: "admitted"},
			{JobID: request.JobID, SessionID: "canary-sess", RunID: "canary-run", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "tool.start", EventID: "tool-1", ToolName: "file_list"},
			{JobID: request.JobID, SessionID: "canary-sess", RunID: "canary-run", RuntimeGeneration: "gen-1", Status: "completed", LastEvent: "run.completed", EventID: "terminal:completed", Text: "0 files"},
		} {
			_ = json.NewEncoder(w).Encode(event)
		}
	}))
	defer api.Close()
	transport := &fakeAPI{}
	g := &Gateway{spool: s, api: transport, runner: HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}, config: Config{RuntimeURL: api.URL, Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	defer func() { cancel(); <-done }()
	class := g.runCanary(ctx, time.Now())
	if class != "" {
		t.Fatalf("canary class=%q, want ok", class)
	}
	entries, _ := os.ReadDir(filepath.Join(s.root, "mappings"))
	own := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(s.root, "mappings", entry.Name()))
		var mapping JobMapping
		if json.Unmarshal(b, &mapping) != nil || !strings.HasPrefix(mapping.JobID, "canary-") {
			continue
		}
		if mapping.Channel != "mock" {
			t.Fatalf("canary ran on channel %q", mapping.Channel)
		}
		if mapping.ConversationID == "telegram-11" || mapping.DeliveryTargetID == "telegram-11" {
			t.Fatal("canary ran inside the user's own conversation")
		}
		own = true
	}
	if !own {
		t.Fatal("no canary job mapping found")
	}
}

// SPEC-0053 R3.4: the canary is opt-in — without HUB_CANARY_INTERVAL it never
// ticks, so a default deployment injects nothing.
func TestCanaryDisabledByDefault(t *testing.T) {
	t.Setenv("HUB_CANARY_INTERVAL", "")
	if got := canaryInterval(); got != 0 {
		t.Fatalf("canary interval=%v, want disabled", got)
	}
}

func TestCanaryIntervalParses(t *testing.T) {
	t.Setenv("HUB_CANARY_INTERVAL", "5m")
	if got := canaryInterval(); got != 5*time.Minute {
		t.Fatalf("interval=%v, want 5m", got)
	}
	t.Setenv("HUB_CANARY_INTERVAL", "300")
	if got := canaryInterval(); got != 300*time.Second {
		t.Fatalf("interval=%v, want 300s", got)
	}
}

// SPEC-0053 R3.4: one owner notice per failure class — a flapping canary
// doesn't spam, and a new class notifies once more.
func TestCanaryNoticeOncePerFailureClass(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s, api: &fakeAPI{}, config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	for i := 0; i < 3; i++ {
		g.recordCanary(context.Background(), time.Now(), "timeout")
	}
	g.recordCanary(context.Background(), time.Now(), "delivery")
	var notices []Delivery
	for {
		d, err := s.ClaimDelivery()
		if err != nil || d == nil {
			break
		}
		notices = append(notices, *d)
	}
	if len(notices) != 2 {
		t.Fatalf("notices=%d, want 2 (one per class)", len(notices))
	}
}

// SPEC-0053 R3.4: recovery sends exactly one deterministic notice.
func TestCanaryRecoveryNotice(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s, api: &fakeAPI{}, config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	g.recordCanary(context.Background(), time.Now(), "timeout")
	g.recordCanary(context.Background(), time.Now(), "")
	g.recordCanary(context.Background(), time.Now(), "")
	var texts []string
	for {
		d, err := s.ClaimDelivery()
		if err != nil || d == nil {
			break
		}
		texts = append(texts, d.Text)
	}
	joined := strings.Join(texts, " | ")
	if len(texts) != 2 || !strings.Contains(joined, "не проходит") || !strings.Contains(joined, "восстановилась") {
		t.Fatalf("notices=%v", texts)
	}
}

// SPEC-0053 R3.4: a canary run that completes without the required tool call
// is a failure — the chain ran but the tool surface is broken.
func TestCanaryToolCallVerified(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		var request hubruntime.ExecuteRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		// Completed run but NO tool.start event — the tool never fired.
		for _, event := range []hubruntime.ExecuteResponse{
			{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "run.admitted", EventID: "admitted"},
			{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "completed", LastEvent: "run.completed", EventID: "terminal:completed", Text: "0 files"},
		} {
			_ = json.NewEncoder(w).Encode(event)
		}
	}))
	defer api.Close()
	g := &Gateway{spool: s, api: &fakeAPI{}, runner: HTTPRunner{URL: api.URL, Auth: "x", Spool: s}, config: Config{RuntimeURL: api.URL, Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	defer func() { cancel(); <-done }()
	class := g.runCanary(ctx, time.Now())
	if class == "" || class == "timeout" {
		t.Fatalf("canary passed a tool-less run: class=%q", class)
	}
}

// SPEC-0053 R3.4 / red-team: the canary must only ever fire read tools — a
// write or unexpected tool call is its own failure class.
func TestCanaryRejectsWriteTool(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		var request hubruntime.ExecuteRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		for _, event := range []hubruntime.ExecuteResponse{
			{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "run.admitted", EventID: "admitted"},
			{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "tool.start", EventID: "tool-1", ToolName: "file_write"},
			{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "completed", LastEvent: "run.completed", EventID: "terminal:completed", Text: "done"},
		} {
			_ = json.NewEncoder(w).Encode(event)
		}
	}))
	defer api.Close()
	g := &Gateway{spool: s, api: &fakeAPI{}, runner: HTTPRunner{URL: api.URL, Auth: "x", Spool: s}, config: Config{RuntimeURL: api.URL, Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	defer func() { cancel(); <-done }()
	if class := g.runCanary(ctx, time.Now()); class == "" {
		t.Fatal("canary accepted a write-tool run")
	}
}

// A second tool call after the required read is still a violation — the
// canary's run must be exactly one read call, not "contains one".
func TestCanaryRejectsExtraToolCalls(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		var request hubruntime.ExecuteRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		for _, event := range []hubruntime.ExecuteResponse{
			{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "run.admitted", EventID: "admitted"},
			{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "tool.start", EventID: "tool-1", ToolName: "file_list"},
			{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "tool.start", EventID: "tool-2", ToolName: "file_write"},
			{JobID: request.JobID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "completed", LastEvent: "run.completed", EventID: "terminal:completed", Text: "done"},
		} {
			_ = json.NewEncoder(w).Encode(event)
		}
	}))
	defer api.Close()
	g := &Gateway{spool: s, api: &fakeAPI{}, runner: HTTPRunner{URL: api.URL, Auth: "x", Spool: s}, config: Config{RuntimeURL: api.URL, Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	defer func() { cancel(); <-done }()
	if class := g.runCanary(ctx, time.Now()); class == "" {
		t.Fatal("canary accepted a second tool call")
	}
}

func TestCanaryToolIdentityIgnoresDisplayTextAndAllowsHermesWrapper(t *testing.T) {
	s, job := streamJob(t)
	g := &Gateway{spool: s}
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	for _, event := range []hubruntime.ExecuteResponse{
		{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "tool.start", EventID: "describe", ToolName: "tool_describe"},
		{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "tool.start", EventID: "wrapper", ToolName: "tool_call"},
		{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "tool.start", EventID: "read", ToolName: "mcp__hub__file_list"},
	} {
		if err := s.RecordStreamEvent(job, event); err != nil {
			t.Fatal(err)
		}
	}
	if class := g.canaryToolCheck(job.ID); class != "" {
		t.Fatalf("wrapped read rejected: %q", class)
	}
	if err := s.RecordStreamEvent(job, hubruntime.ExecuteResponse{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "tool.start", EventID: "spoof", ToolName: "file_write", Text: "file_list"}); err != nil {
		t.Fatal(err)
	}
	if class := g.canaryToolCheck(job.ID); class != "unexpected_tool" {
		t.Fatalf("display text hid write tool: %q", class)
	}
}

// The run completed, the tool fired — but the response never reached the
// mock outbox: that's its own failure class.
func TestCanaryDeliveryMissing(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s, api: &fakeAPI{}, config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	prev := canaryDeliveryGrace
	canaryDeliveryGrace = 300 * time.Millisecond
	defer func() { canaryDeliveryGrace = prev }()
	result := make(chan string, 1)
	go func() { result <- g.runCanary(context.Background(), time.Now()) }()
	// Settle the canary job manually once it appears — terminal mapping with a
	// tool.start receipt, but no delivery worker ever runs.
	deadline := time.Now().Add(10 * time.Second)
	var job *Job
	for time.Now().Before(deadline) && job == nil {
		job, _ = s.ClaimJob()
		time.Sleep(20 * time.Millisecond)
	}
	if job == nil {
		t.Fatal("canary job never enqueued")
	}
	for _, event := range []hubruntime.ExecuteResponse{
		{JobID: job.ID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "run.admitted", EventID: "admitted"},
		{JobID: job.ID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "tool.start", EventID: "tool-1", ToolName: "file_list"},
		{JobID: job.ID, SessionID: "s", RunID: "r", RuntimeGeneration: "gen-1", Status: "completed", LastEvent: "run.completed", EventID: "terminal:completed", Text: "0 files"},
	} {
		if err := s.RecordStreamEvent(*job, event); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case class := <-result:
		if class != "delivery" {
			t.Fatalf("class=%q, want delivery", class)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("canary hung waiting for delivery")
	}
}

// The readiness surface already says file_list is gone — the canary names the
// surface class without burning a doomed run.
func TestCanarySurfaceFailure(t *testing.T) {
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runtimes" || r.Header.Get("Authorization") != "Bearer supervisor-auth" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"principal_id": "alice", "context_id": "alice", "missing_tools": []string{"file_list"},
				"tool_reasons": map[string]string{"hub": "toolhub_unreachable"}},
		})
	}))
	defer supervisor.Close()
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s, api: &fakeAPI{}, config: Config{
		Supervised:  true,
		RuntimeURL:  supervisor.URL,
		RuntimeAuth: "supervisor-auth",
		Users:       []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}},
	}, now: time.Now}
	prev := canaryJobTimeout
	canaryJobTimeout = 2 * time.Second
	defer func() { canaryJobTimeout = prev }()
	if class := g.runCanary(context.Background(), time.Now()); class != "surface" {
		t.Fatalf("class=%q, want surface", class)
	}
}

// Every tick lands in canary.jsonl — durable history, bounded size.
func TestCanaryJournalBounded(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s, api: &fakeAPI{}, config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	for i := 0; i < 5; i++ {
		g.recordCanary(context.Background(), time.Now(), "")
	}
	g.recordCanary(context.Background(), time.Now(), "timeout")
	journal := filepath.Join(s.root, "canary.jsonl")
	b, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 6 {
		t.Fatalf("journal lines=%d, want 6", len(lines))
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("journal line not JSON: %v", err)
	}
	if last["class"] != "timeout" {
		t.Fatalf("last class=%v, want timeout", last["class"])
	}
}

// The journal is bounded: once it passes the size cap it keeps only the most
// recent entries rather than growing forever.
func TestCanaryJournalTruncates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(s.root, "canary.jsonl")
	var big strings.Builder
	for i := 0; i < 30000; i++ {
		big.WriteString(`{"ts":"2026-01-01T00:00:00Z","class":"old"}` + "\n")
	}
	if err := os.WriteFile(journal, []byte(big.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s, api: &fakeAPI{}, config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	g.recordCanary(context.Background(), time.Now(), "timeout")
	b, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 2000 {
		t.Fatalf("journal not bounded: %d lines", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "timeout") {
		t.Fatalf("newest entry lost in truncation")
	}
}

// The canary loop ticks on its interval, records every outcome, and stops with
// the gateway context.
func TestCanaryLoopTicksAndStops(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s, api: &fakeAPI{}, config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, now: time.Now}
	prev := canaryJobTimeout
	canaryJobTimeout = 150 * time.Millisecond
	defer func() { canaryJobTimeout = prev }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.canaryLoop(ctx, 30*time.Millisecond) }()
	// Two ticks land timeout results (nothing settles the jobs), then stop.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(s.root, "canary.jsonl")); err == nil && strings.Count(string(b), "\n") >= 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canary loop did not stop on cancel")
	}
	b, err := os.ReadFile(filepath.Join(s.root, "canary.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "timeout") < 1 {
		t.Fatalf("no tick outcomes recorded: %q", b)
	}
}

// Run() starts the canary loop only when HUB_CANARY_INTERVAL is set — the
// probe stays opt-in.
func TestRunStartsCanaryWhenConfigured(t *testing.T) {
	t.Setenv("HUB_CANARY_INTERVAL", "40ms")
	prev := canaryJobTimeout
	canaryJobTimeout = 100 * time.Millisecond
	defer func() { canaryJobTimeout = prev }()
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &conflictPoller{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	journal := filepath.Join(g.spool.root, "canary.jsonl")
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(journal); err == nil && len(b) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	<-done
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("Run() never started the configured canary: %v", err)
	}
}

func TestCanaryTimeoutStartsWhenRoundActuallyRuns(t *testing.T) {
	c := testConfig(t)
	c.RuntimeURL, c.RuntimeAuth = "", ""
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	previous := canaryJobTimeout
	canaryJobTimeout = 1100 * time.Millisecond
	defer func() { canaryJobTimeout = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.canaryLoop(ctx, 20*time.Millisecond) }()
	defer func() { cancel(); <-done }()
	journal := filepath.Join(g.spool.root, "canary.jsonl")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(journal)
		if err == nil {
			lines := bytes.Split(bytes.TrimSpace(body), []byte{'\n'})
			if len(lines) >= 2 {
				var first, second struct {
					Timestamp time.Time `json:"ts"`
				}
				if json.Unmarshal(lines[0], &first) != nil || json.Unmarshal(lines[1], &second) != nil {
					t.Fatal("invalid canary journal")
				}
				if second.Timestamp.Sub(first.Timestamp) < time.Second {
					t.Fatalf("second round reused a stale ticker time: %v", second.Timestamp.Sub(first.Timestamp))
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("two canary rounds did not finish")
}
