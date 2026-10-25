package communication

import (
	"context"
	"encoding/json"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGroupRevocationCancelsQueuedApproval(t *testing.T) {
	g, err := New(groupConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "@helper act"}}); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatal(err)
	}
	event := streamEvent(*job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:one", "one"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := g.spool.RecordStreamEvent(*job, event); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.RequestApproval(job.ID, job.Envelope, "one", "once"); err != nil {
		t.Fatal(err)
	}
	approvals := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c hubruntime.RunControl
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			t.Error(err)
		}
		if c.Action == "approve" {
			approvals++
		}
		json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: job.ID, RunID: event.RunID, SessionID: event.SessionID, RuntimeGeneration: event.RuntimeGeneration, Status: "cancelled"})
	}))
	defer api.Close()
	g.config.RuntimeURL = api.URL
	g.config.Groups[0].Members = []int64{22}
	g.controlOne(context.Background())
	mapping, _, err := g.spool.Mapping(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approvals != 0 || !mapping.CancelRequested || mapping.Control == nil || mapping.Control.Action != "cancel" {
		t.Errorf("revoked member's approval remained executable: approvals=%d cancel=%v control=%+v", approvals, mapping.CancelRequested, mapping.Control)
	}
}
