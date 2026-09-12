package communication

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func streamJob(t *testing.T) (*Spool, Job) {
	t.Helper()
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "stream-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "stream-key", ChatID: 11, Text: "hello"}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	return s, job
}

func streamEvent(job Job) hubruntime.ExecuteResponse {
	return hubruntime.ExecuteResponse{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "run.admitted", EventID: "admitted"}
}

func TestCachedTerminalResponseRepairsDeliveryWithoutWorker(t *testing.T) {
	s, job := streamJob(t)
	if err := s.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	final := streamEvent(job)
	final.Status, final.Text, final.RuntimeGeneration = "completed", "cached answer", "gen-2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/resume" {
			t.Errorf("replayed original request: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(final)
	}))
	defer server.Close()
	runner := HTTPRunner{URL: server.URL, Auth: "secret", Spool: s, HTTP: server.Client()}
	if _, err := runner.RunOutcome(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil {
		t.Fatalf("no durable cached delivery: %+v %v", delivery, err)
	}
	// Crash after the receipt, before the worker can hand off to Telegram.
	if err := os.Remove(filepath.Join(s.root, "outbox", "sending", delivery.ID+".json")); err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err = s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.Text != "cached answer" {
		t.Fatalf("cached final lost: %+v %v", delivery, err)
	}
	if err := s.CompleteDelivery(delivery.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, finalWithID(final)); err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimDelivery(); err != nil || next != nil {
		t.Fatalf("duplicate cached final: %+v %v", next, err)
	}
}

func TestRestartRejectsCorruptAdmissionInsteadOfReplayingPrompt(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.mappingPath(job.ID), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSpool(s.root); err == nil {
		t.Fatal("corrupt admission metadata accepted")
	}
	if _, err := os.Stat(filepath.Join(s.root, "running", job.ID+".json")); err != nil {
		t.Fatalf("claimed job moved into replay queue: %v", err)
	}
}

func TestTerminalErrorReceiptSurvivesCrashWithoutLeakingDetails(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			s, job := streamJob(t)
			event := finalWithID(streamEvent(job))
			event.Status, event.EventID, event.LastEvent, event.Text = status, "terminal:"+status, "run."+status, "PRIVATE_ERROR_CANARY"
			if err := s.RecordStreamEvent(job, event); err != nil {
				t.Fatal(err)
			}
			delivery, err := s.ClaimDelivery()
			if err != nil || delivery == nil || strings.Contains(delivery.Text, "PRIVATE_ERROR_CANARY") || delivery.JobID != "" {
				t.Fatalf("missing/private/restart-triggering error delivery: %+v %v", delivery, err)
			}
			if err := os.Remove(filepath.Join(s.root, "outbox", "sending", delivery.ID+".json")); err != nil {
				t.Fatal(err)
			}
			s, err = NewSpool(s.root)
			if err != nil {
				t.Fatal(err)
			}
			delivery, err = s.ClaimDelivery()
			if err != nil || delivery == nil || delivery.ID != "job-"+job.ID+"-error" {
				t.Fatalf("terminal error handoff lost: %+v %v", delivery, err)
			}
			if err := s.CompleteDelivery(delivery.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordStreamEvent(job, event); err != nil {
				t.Fatal(err)
			}
			if duplicate, err := s.ClaimDelivery(); err != nil || duplicate != nil {
				t.Fatalf("duplicate terminal error: %+v %v", duplicate, err)
			}
		})
	}
}

func finalWithID(event hubruntime.ExecuteResponse) hubruntime.ExecuteResponse {
	event.EventID, event.LastEvent = "terminal:"+event.Status, "run."+event.Status
	return event
}

func TestTelegramOutputLimitsPreserveLongUnicodeResult(t *testing.T) {
	for _, size := range []int{4096, 4097} {
		text := strings.Repeat("я", size)
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if size == 4096 {
				var payload struct {
					Text string `json:"text"`
				}
				if r.URL.Path != "/bottoken/sendMessage" || json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Text != text {
					t.Error("invalid bounded message")
				}
			} else {
				if r.URL.Path != "/bottoken/sendDocument" || r.ParseMultipartForm(3<<20) != nil || r.FormValue("chat_id") != "11" {
					t.Error("invalid long-result document")
					return
				}
				file, header, err := r.FormFile("document")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				contents, err := io.ReadAll(file)
				if err != nil || string(contents) != text || header.Filename != "response.txt" {
					t.Error("long result truncated or changed")
				}
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}))
		api := newTelegramAPI(server.URL, "token", 0)
		if err := api.SendMessage(context.Background(), 11, text); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []string{"", string([]byte{255}), strings.Repeat("x", 2*1024*1024+1)} {
			if err := api.SendMessage(context.Background(), 11, invalid); err == nil {
				t.Error("invalid output dispatched")
			}
		}
		server.Close()
		if calls != 1 {
			t.Fatalf("calls=%d", calls)
		}
	}
}

func TestStreamReceiptsDeduplicateAndRepairOutboxOnRestart(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	event.Status, event.LastEvent, event.EventID, event.Text = "completed", "run.completed", "terminal:completed", "answer"
	for range 2 {
		if err := s.RecordStreamEvent(job, event); err != nil {
			t.Fatal(err)
		}
	}
	changed := event
	changed.EventID, changed.Text = "another-terminal", "different answer"
	if err := s.RecordStreamEvent(job, changed); err == nil {
		t.Fatal("late terminal replaced authoritative result")
	}
	if err := s.RecordOutcome(job.ID, outcomeFromEvent(changed)); err == nil {
		t.Fatal("JSON terminal replaced authoritative result")
	}
	m, _, _ := s.Mapping(job.ID)
	if m.EventPosition != 2 {
		t.Fatalf("position=%d", m.EventPosition)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.Text != "answer" {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	// Simulate a crash between the durable receipt and outbox creation.
	if err := os.Remove(filepath.Join(s.root, "outbox", "sending", delivery.ID+".json")); err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ = s.Mapping(job.ID)
	if m.Status != "completed" {
		t.Fatalf("terminal state lost: %+v", m)
	}
	delivery, err = s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.ID != "job-"+job.ID+"-response" {
		t.Fatalf("recovered delivery=%+v err=%v", delivery, err)
	}
	if err := s.CompleteDelivery(delivery.ID); err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimDelivery(); err != nil || next != nil {
		t.Fatalf("duplicate final=%+v err=%v", next, err)
	}
}

func TestStreamRejectsChangedBindingAndBoundsBeforeReceipt(t *testing.T) {
	s, job := streamJob(t)
	initial := streamEvent(job)
	if err := s.RecordStreamEvent(job, initial); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"generation", "run", "session", "event", "output", "conversation", "external", "policy", "runtime", "schema", "terminal", "actor", "user", "organization", "scope", "channel", "trigger", "idempotency"} {
		event := initial
		event.EventID = "next"
		altered := job
		switch field {
		case "generation":
			event.RuntimeGeneration = "gen-2"
		case "run":
			event.RunID = "run-2"
		case "session":
			event.SessionID = "session-2"
		case "event":
			event.EventID = ""
		case "output":
			event.Text = strings.Repeat("x", 2*1024*1024+1)
		case "conversation":
			altered.ConversationID = "another"
		case "external":
			altered.ExternalIdentityID = "another"
		case "policy":
			altered.PolicyVersion = "another"
		case "runtime":
			altered.RuntimeID = "another"
		case "schema":
			altered.Schema = 0
		case "terminal":
			event.Status, event.LastEvent, event.Text = "completed", "tool.start", "private tool arguments"
		case "actor":
			altered.ActorID = "another"
		case "user":
			altered.UserID = "another"
		case "organization":
			altered.OrganizationID = "another"
		case "scope":
			altered.ScopeID = "another"
		case "channel":
			altered.Channel = "another"
		case "trigger":
			altered.Trigger = "another"
		case "idempotency":
			altered.IdempotencyKey = "another"
		}
		if err := s.RecordStreamEvent(altered, event); err == nil {
			t.Fatalf("accepted %s", field)
		}
	}
	m, _, _ := s.Mapping(job.ID)
	if m.EventPosition != 1 {
		t.Fatalf("rejected event wrote state: %+v", m)
	}
	if err := os.WriteFile(filepath.Join(s.root, "events", "broken.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSpool(s.root); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}

func TestHTTPRunnerConsumesDurableStream(t *testing.T) {
	s, job := streamJob(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != hubruntime.RunStreamContentType {
			t.Error("stream not requested")
		}
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		event := streamEvent(job)
		_ = json.NewEncoder(w).Encode(event)
		event.Status, event.EventID, event.LastEvent, event.Text = "completed", "terminal:completed", "run.completed", "answer"
		_ = json.NewEncoder(w).Encode(event)
	}))
	defer api.Close()
	outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(context.Background(), job)
	if err != nil || outcome.Text != "answer" || outcome.RunID != "run-1" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if delivery, err := s.ClaimDelivery(); err != nil || delivery == nil || delivery.Text != "answer" {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
}

func TestAdmittedRestartResumesWithoutSecondSubmission(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ClaimJob()
	if err != nil || recovered == nil {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/resume" {
			t.Errorf("second submission: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		event.RuntimeGeneration = "gen-2"
		event.EventID = "observed:gen-2"
		_ = json.NewEncoder(w).Encode(event)
		event.Status, event.EventID, event.LastEvent = "interrupted", "terminal:interrupted", "run.interrupted"
		_ = json.NewEncoder(w).Encode(event)
	}))
	defer api.Close()
	outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(context.Background(), *recovered)
	if err == nil || outcome.Status != "interrupted" || outcome.RunID != "run-1" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	m, _, err := s.Mapping(job.ID)
	if err != nil || m.RuntimeGeneration != "gen-2" || m.Status != "interrupted" {
		t.Fatalf("mapping=%+v err=%v", m, err)
	}
}

func TestStreamDisconnectAndStaleFramesKeepOutcomeUncertain(t *testing.T) {
	for _, mode := range []string{"disconnect", "other-job", "other-generation", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			s, job := streamJob(t)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
				event := streamEvent(job)
				_ = json.NewEncoder(w).Encode(event)
				if mode == "disconnect" {
					return
				}
				if mode == "malformed" {
					_, _ = w.Write([]byte("not-json\n"))
					return
				}
				event.EventID = "terminal"
				event.Status = "completed"
				event.LastEvent = "run.completed"
				event.Text = "untrusted answer"
				if mode == "other-job" {
					event.JobID = "another"
				} else {
					event.RuntimeGeneration = "stale"
				}
				_ = json.NewEncoder(w).Encode(event)
			}))
			defer api.Close()
			outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(context.Background(), job)
			if err == nil || outcome.Status != "uncertain" {
				t.Fatalf("outcome=%+v err=%v", outcome, err)
			}
			if delivery, err := s.ClaimDelivery(); err != nil || delivery != nil {
				t.Fatalf("invalid final projected: %+v err=%v", delivery, err)
			}
		})
	}
}

func TestStreamReceiptPersistenceFailureAndRecoveryAuthority(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	if err := os.Remove(filepath.Join(s.root, "events")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, event); err == nil {
		t.Fatal("missing journal ignored")
	}
	m, _, _ := s.Mapping(job.ID)
	if m.EventPosition != 0 || m.RunID != "" {
		t.Fatalf("failed persistence advanced state: %+v", m)
	}
	if err := os.Mkdir(filepath.Join(s.root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	bad := event
	bad.RunID = "another"
	if err := s.RebindObservation(job, bad); err == nil {
		t.Fatal("recovery changed run")
	}
	event.RuntimeGeneration = "gen-2"
	if err := s.RebindObservation(job, event); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "events", entries[0].Name()), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, event); err == nil {
		t.Fatal("corrupt receipt ignored")
	}
}

func TestRecoveryRejectsCrossAudienceReceipt(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	event.Status, event.LastEvent, event.EventID, event.Text = "completed", "run.completed", "terminal", "answer"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "events"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(s.root, "events", entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var receipt streamReceipt
		if err := json.Unmarshal(b, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Delivery == nil {
			continue
		}
		receipt.Delivery.ChatID = 22
		receipt.Delivery.ConversationID = "telegram-22"
		receipt.Delivery.DeliveryTargetID = "telegram-22"
		if err := atomicJSON(path, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewSpool(s.root); err == nil {
		t.Fatal("cross-audience receipt recovered")
	}
}

func TestRecoveryRepairsReceiptBeforeTerminalMappingAndClaim(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	event.Status, event.LastEvent, event.EventID, event.Text = "completed", "run.completed", "terminal", "answer"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	// Restore the mapping to its pre-terminal state: the terminal receipt is the
	// only surviving evidence of a crash before mapping/outbox handoff.
	m, _, err := s.Mapping(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.Status, m.EventPosition, m.Result = "running", 1, ""
	if err := atomicJSON(s.mappingPath(job.ID), m); err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimJob(); err != nil || claimed != nil {
		t.Fatalf("terminal job rerun: %+v err=%v", claimed, err)
	}
	m, _, err = s.Mapping(job.ID)
	if err != nil || m.Status != "completed" || m.Result != "answer" {
		t.Fatalf("mapping=%+v err=%v", m, err)
	}
}

func TestApprovalIdentityAndDeadlineSurviveRestart(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:a", "a"
	event.ApprovalChoices = []string{"once", "session", "always", "deny", "injected-choice"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	before, _, err := s.Mapping(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := s.Mapping(job.ID)
	if err != nil || after.ApprovalState != "pending" || after.ApprovalID != "a" || !after.ApprovalDeadline.Equal(before.ApprovalDeadline) || len(after.ApprovalChoices) != 4 {
		t.Fatalf("approval lost or expanded: %+v err=%v", after, err)
	}
	event.Status, event.LastEvent, event.EventID = "cancelled", "run.cancelled", "terminal:cancelled"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	after, _, err = s.Mapping(job.ID)
	if err != nil || after.ApprovalState != "closed" {
		t.Fatalf("terminal run left actionable approval: %+v err=%v", after, err)
	}
}
