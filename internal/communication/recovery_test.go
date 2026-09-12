package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
			if err = s.RequeueObservations(now); err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ClaimJob()
			if err != nil || claimed == nil || claimed.Text != "" {
				t.Fatalf("recovered input=%+v %v", claimed, err)
			}
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			if err = s.RequeueObservations(now.Add(time.Hour)); err != nil {
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
			g := &Gateway{spool: s, api: transport, runner: HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}, config: Config{RuntimeURL: api.URL}, now: time.Now}
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
			if len(messages) != 1 {
				t.Fatalf("messages=%v", messages)
			}
			if completed && messages[0] != "saved answer" {
				t.Fatal(messages)
			}
			if !completed && (!strings.Contains(messages[0], "неизвестен") || strings.Contains(messages[0], "Попробуйте")) {
				t.Fatal(messages)
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
	if err := s.RequeueObservations(now); err != nil {
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
			if err := s.RequeueObservations(now.Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if claimed, err := s.ClaimJob(); err != nil || claimed != nil {
				t.Fatal("observation skipped persisted backoff")
			}
		}
		if err := s.RequeueObservations(now); err != nil {
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
