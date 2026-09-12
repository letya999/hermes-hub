package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestQueuedCancellationSurvivesRestartAndRejectsOtherConversation(t *testing.T) {
	s, job := streamJob(t)
	other := job.Envelope
	other.ConversationID = "other"
	if _, err := s.RequestCancel(job.ID, other); err == nil {
		t.Fatal("other conversation cancelled work")
	}
	mapping, err := s.RequestCancel(job.ID, job.Envelope)
	if err != nil || mapping.Status != "cancelled" {
		t.Fatalf("mapping=%+v err=%v", mapping, err)
	}
	if _, err = s.RequestCancel(job.ID, job.Envelope); err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimJob(); err != nil || claimed != nil {
		t.Fatalf("cancelled job claimed: %+v %v", claimed, err)
	}
}

func TestClaimedCancellationRequiresRuntimeConfirmation(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	mapping, err := s.RequestCancel(job.ID, job.Envelope)
	if err != nil || !mapping.CancelRequested || mapping.Status == "cancelled" {
		t.Fatalf("mapping=%+v err=%v", mapping, err)
	}
}

func TestExpiredPendingApprovalDuplicateFailsClosed(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:one", "one"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestApproval(job.ID, job.Envelope, "one", "once"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	mapping, err := s.loadMappingLocked(job.ID)
	if err == nil {
		mapping.ApprovalDeadline = time.Now().Add(-time.Second)
		err = s.writeMappingLocked(mapping)
	}
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestApproval(job.ID, job.Envelope, "one", "once"); err == nil {
		t.Fatal("expired pending decision was acknowledged as accepted")
	}
}

func TestControlWorkerDurableApprovalAndNoBlindRetry(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	event.Status = "waiting_for_approval"
	event.LastEvent = "approval.request"
	event.EventID = "approval:one"
	event.ApprovalID = "one"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestApproval(job.ID, job.Envelope, "one", "once"); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestApproval(job.ID, job.Envelope, "one", "deny"); err == nil {
		t.Fatal("conflicting approval accepted")
	}
	var err error
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		mapping, _, err := s.Mapping(job.ID)
		if err != nil || mapping.Control.State != "uncertain" {
			t.Errorf("intent not persisted before dispatch: %+v %v", mapping.Control, err)
		}
		var request hubruntime.RunControl
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Envelope != job.Envelope || request.RequestID != "one" || request.Choice != "once" {
			t.Errorf("wrong control: %+v", request)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer api.Close()
	g := &Gateway{spool: s, config: Config{RuntimeURL: api.URL, RuntimeAuth: "secret"}}
	g.controlOne(context.Background())
	g.controlOne(context.Background())
	if calls != 1 {
		t.Fatalf("blind retry: %d", calls)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	g.spool = s
	g.controlOne(context.Background())
	if calls != 1 {
		t.Fatalf("restart repeated decision: %d", calls)
	}
	event.Status, event.LastEvent, event.EventID = "interrupted", "run.interrupted", "terminal:interrupted"
	if err = s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	mapping, _, err := s.Mapping(job.ID)
	if err != nil || mapping.Control.State != "closed" {
		t.Fatalf("terminal left actionable uncertainty: %+v %v", mapping.Control, err)
	}
}

func TestCancelCompletionUsesDurableFinalHandoff(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestCancel(job.ID, job.Envelope); err != nil {
		t.Fatal(err)
	}
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		event.Status, event.Text = "completed", "saved answer"
		_ = json.NewEncoder(w).Encode(event)
	}))
	defer api.Close()
	g := &Gateway{spool: s, config: Config{RuntimeURL: api.URL, RuntimeAuth: "secret"}}
	g.controlOne(context.Background())
	g.controlOne(context.Background())
	mapping, _, err := s.Mapping(job.ID)
	if err != nil || mapping.Status != "completed" || mapping.Control.State != "closed" || calls != 1 {
		t.Fatalf("mapping=%+v err=%v calls=%d", mapping, err, calls)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.Text != "saved answer" || delivery.ChatID != job.ChatID || delivery.ConversationID != job.ConversationID {
		t.Fatalf("handoff=%+v %v", delivery, err)
	}
	if err = s.RecordOutcome(job.ID, RunOutcome{Status: "uncertain"}); err == nil {
		t.Fatal("late disconnect downgraded terminal result")
	}
}

func TestControlsRejectDifferentExternalIdentityAndSchema(t *testing.T) {
	s, job := streamJob(t)
	callers := []struct {
		name   string
		schema bool
	}{{"external identity", false}, {"schema", true}}
	for _, tc := range callers {
		t.Run(tc.name, func(t *testing.T) {
			caller := job.Envelope
			if tc.schema {
				caller.Schema++
			} else {
				caller.ExternalIdentityID = "different-account"
			}
			if _, err := s.RequestCancel(job.ID, caller); err == nil {
				t.Fatal("unauthorized cancellation accepted")
			}
			if err := s.RequestApproval(job.ID, caller, "one", "once"); err == nil {
				t.Fatal("unauthorized approval accepted")
			}
			mapping, ok, err := s.Mapping(job.ID)
			if err != nil || !ok || mapping.CancelRequested || mapping.Control != nil {
				t.Fatalf("rejected caller changed mapping: %+v %v", mapping, err)
			}
		})
	}
	mapping, _, err := s.Mapping(job.ID)
	if err != nil || mapping.IdentitySchema != job.Envelope.Schema || mapping.ExternalIdentityID != job.Envelope.ExternalIdentityID {
		t.Fatalf("identity not persisted: %+v %v", mapping, err)
	}
}

func TestControlWorkerRecoversLostApprovalByReadOnlyQuery(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	event.Status = "waiting_for_approval"
	event.LastEvent = "approval.request"
	event.EventID = "approval:one"
	event.ApprovalID = "one"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestApproval(job.ID, job.Envelope, "one", "once"); err != nil {
		t.Fatal(err)
	}
	decisions, queries := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c hubruntime.RunControl
		if json.NewDecoder(r.Body).Decode(&c) != nil || c.Envelope != job.Envelope || c.RunID != event.RunID || c.RuntimeGeneration != event.RuntimeGeneration {
			t.Error("lost original binding")
		}
		if !c.Reconcile {
			decisions++
			w.WriteHeader(502)
			return
		}
		queries++
		event.Status = "running"
		event.ApprovalID = ""
		event.ApprovalChoices = nil
		_ = json.NewEncoder(w).Encode(event)
	}))
	defer api.Close()
	g := &Gateway{spool: s, config: Config{RuntimeURL: api.URL, RuntimeAuth: "secret"}}
	g.controlOne(context.Background())
	g.controlOne(context.Background())
	if decisions != 1 || queries != 0 {
		t.Fatal("backoff ignored")
	}
	restarted, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	g.spool = restarted
	g.controlOne(context.Background())
	if queries != 0 {
		t.Fatal("restart reset durable backoff")
	}
	g.spool.mu.Lock()
	mapping, err := g.spool.loadMappingLocked(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	copy := *mapping.Control
	copy.NextAttemptAt = time.Time{}
	mapping.Control = &copy
	err = g.spool.writeMappingLocked(mapping)
	g.spool.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	g.controlOne(context.Background())
	g.controlOne(context.Background())
	mapping, _, err = g.spool.Mapping(job.ID)
	if err != nil || decisions != 1 || queries != 1 || mapping.Control.State != "closed" {
		t.Fatalf("recovery: %+v decisions=%d queries=%d err=%v", mapping.Control, decisions, queries, err)
	}
}
