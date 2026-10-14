package communication

import (
	"context"
	"encoding/json"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClosedApprovalNeverDelivered(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	s, job := streamJob(t)
	g.spool = s
	fake := &fakeAPI{}
	g.api = fake
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:read", "read"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	event.Status, event.LastEvent, event.EventID = "cancelled", "run.cancelled", "terminal:cancelled"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		g.deliverOne(context.Background())
	}
	if len(fake.keyboards) != 0 {
		t.Fatalf("closed approval was sent: %v", fake.keyboards)
	}
}

func TestApprovalMessageRemovedAfterDecisionAndRestart(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	s, job := streamJob(t)
	g.spool = s
	deleted := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			var payload struct {
				ChatID    int64 `json:"chat_id"`
				MessageID int   `json:"message_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.ChatID != job.ChatID || payload.MessageID != 731 {
				t.Errorf("wrong delete target: %+v %v", payload, err)
			}
			deleted++
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":731}}`))
	}))
	defer server.Close()
	g.api = newTelegramAPI(server.URL, "fixture", time.Second)
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:read", "read"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if deleted != 0 {
		t.Fatal("pending approval removed")
	}
	if err := s.RequestApproval(job.ID, job.Envelope, "read", "once"); err != nil {
		t.Fatal(err)
	}
	g.spool, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	g.closeApprovalMessages(context.Background())
	if deleted != 0 {
		t.Fatalf("saved but unapplied decision removed keyboard: deletes=%d", deleted)
	}
	s.mu.Lock()
	m, err := s.loadMappingLocked(job.ID)
	if err == nil {
		m.Control.State = "applied"
		err = s.writeMappingLocked(m)
	}
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	g.closeApprovalMessages(context.Background())
	if deleted != 1 {
		t.Fatalf("applied decision left keyboard: deletes=%d", deleted)
	}
	g.controlOne(context.Background())
	if deleted != 1 {
		t.Fatalf("cleanup repeated: deletes=%d", deleted)
	}
}

func TestQueuedApprovalIsCurrentAndActionable(t *testing.T) {
	for _, scenario := range []string{"superseded", "decision-saved", "expired", "cancel-requested", "corrupt", "run-changed", "generation-changed", "audience-changed", "policy-changed"} {
		t.Run(scenario, func(t *testing.T) {
			g, err := New(testConfig(t))
			if err != nil {
				t.Fatal(err)
			}
			s, job := streamJob(t)
			g.spool = s
			fake := &fakeAPI{}
			g.api = fake
			event := streamEvent(job)
			event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:old", "old"
			event.ApprovalChoices = []string{"once", "deny"}
			if err := s.RecordStreamEvent(job, event); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			m, err := s.loadMappingLocked(job.ID)
			if err != nil {
				s.mu.Unlock()
				t.Fatal(err)
			}
			switch scenario {
			case "run-changed":
				m.RunID = "new-run"
			case "generation-changed":
				m.RuntimeGeneration = "new-generation"
			case "audience-changed":
				m.DeliveryTargetID = "foreign"
			case "policy-changed":
				m.PolicyVersion = "new-policy"
			case "superseded":
				m.ApprovalID = "new"
			case "decision-saved":
				m.Control = &PendingControl{Action: "approve", RequestID: "old", State: "pending"}
			case "expired":
				m.ApprovalDeadline = time.Now().Add(-time.Second)
			case "cancel-requested":
				m.CancelRequested = true
			}
			if scenario == "corrupt" {
				err = os.WriteFile(s.mappingPath(job.ID), []byte("{"), 0600)
			} else {
				err = s.writeMappingLocked(m)
			}
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			g.deliverOne(context.Background())
			if len(fake.keyboards) != 0 {
				t.Fatalf("%s keyboard sent", scenario)
			}
		})
	}
}

// SPEC-0053 R1.8/D7: the listing/read-only guidance is part of the rendered
// channel contract in the effective SOUL; per-run requests keep carrying the
// user's text unchanged.
func TestListingRequestCarriesCurrentRequestAndReadOnlyGuidance(t *testing.T) {
	for _, guidance := range []string{"current user request", "read-only file_list or search_files", "fenced code block"} {
		if !strings.Contains(stack.ChannelContract, guidance) {
			t.Errorf("channel contract missing guidance %q", guidance)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request hubruntime.ExecuteRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Text != "Какие файлы в workspace? Покажи дерево." {
			t.Errorf("current request changed: %q", request.Text)
		}
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{Status: "completed", Text: "tree"})
	}))
	defer server.Close()
	_, err := (HTTPRunner{URL: server.URL, HTTP: server.Client()}).RunOutcome(context.Background(), Job{Text: "Какие файлы в workspace? Покажи дерево."})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTerminalApprovalMessageRemoved(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	s, job := streamJob(t)
	g.spool = s
	deleted := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deleted++
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":731}}`))
	}))
	defer server.Close()
	g.api = newTelegramAPI(server.URL, "fixture", time.Second)
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:read", "read"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	event.Status, event.LastEvent, event.EventID = "cancelled", "run.cancelled", "terminal:cancelled"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	g.controlOne(context.Background())
	if deleted != 1 {
		t.Fatalf("terminal approval left in chat: deletes=%d", deleted)
	}
}

func TestFailJobPreservesCompletedResult(t *testing.T) {
	s, job := streamJob(t)
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{Status: "completed", Text: "workspace tree", LastEvent: "run.completed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FailJob(job.ID); err != nil {
		t.Fatal(err)
	}
	m, _, err := s.Mapping(job.ID)
	if err != nil || m.Status != "completed" || m.Result != "workspace tree" {
		t.Fatalf("completed result lost: %+v %v", m, err)
	}
}

func TestOldClosedApprovalTapRemovesOnlyOwnersMessage(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	s, job := streamJob(t)
	g.spool = s
	fake := &fakeAPI{}
	g.api = fake
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:read", "read"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("delivery: %+v %v", d, err)
	}
	event.Status, event.LastEvent, event.EventID = "cancelled", "run.cancelled", "terminal:cancelled"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	query := &CallbackQuery{ID: "tap", From: &TGUser{ID: 99}, Message: &Message{MessageID: 731, Chat: TGChat{ID: 11, Type: "private"}}, Data: d.Buttons[0].Data}
	if err := g.handleCallback(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 0 {
		t.Fatal("stranger removed owner's approval")
	}
	query.From.ID = 11
	if err := g.handleCallback(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != 731 {
		t.Fatalf("old closed approval still visible: %v", fake.deleted)
	}
}

func TestLegacyKeyboardWithoutProvenanceNotActionable(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	// A keyboard delivery queued before send-provenance existed cannot be
	// reconciled to its native request, so it must not render actionable.
	if err := g.spool.EnqueueDelivery(Delivery{ID: "legacy-keys", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "Подтвердите.", Buttons: []DeliveryButton{{Text: "once", Data: "a:1"}, {Text: "deny", Data: "a:2"}}}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.keyboards) != 0 {
		t.Fatalf("provenance-less keyboard sent: %v", fake.keyboards)
	}
}

func TestApprovalKeyboardSentWithoutReceiptStaysUncertain(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	s, job := streamJob(t)
	g.spool = s
	fake := &fakeAPI{}
	g.api = fake
	// A known-good keyboard send whose durable receipt cannot persist must not
	// re-enter the resend path; the delivery stays explicitly uncertain.
	if err := os.WriteFile(filepath.Join(s.root, "approval-messages"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:read", "read"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.keyboards) != 1 {
		t.Fatalf("keyboard not sent: %v", fake.keyboards)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "outbox", "failed"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("uncertain delivery lost: %v %v", entries, err)
	}
	g.deliverOne(context.Background())
	if len(fake.keyboards) != 1 {
		t.Fatal("known-sent keyboard resent")
	}
}

func TestApprovalCleanupRejectsReceiptOutsideSpool(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	s, job := streamJob(t)
	g.spool = s
	fake := &fakeAPI{}
	g.api = fake
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID = "cancelled", "run.cancelled", "terminal:cancelled"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "foreign.json")
	d := Delivery{ID: "foreign", ApprovalJobID: job.ID, ApprovalID: "read", TelegramMessageID: 731, ChatID: job.ChatID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, b, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.root, "approval-messages")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "foreign.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	g.controlOne(context.Background())
	if len(fake.deleted) != 0 {
		t.Fatalf("foreign receipt caused deletion: %v", fake.deleted)
	}
}
