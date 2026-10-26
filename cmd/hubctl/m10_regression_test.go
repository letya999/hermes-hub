package main

import (
	"github.com/letya999/hermes-hub/internal/communication"
	"os"
	"path/filepath"
	"testing"
)

func TestM10RegressionApplyKnowledgeCannotEscapeUser(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source")
	if err := os.WriteFile(src, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	_, err := applyKnowledge(state, "alice", "../../victim.txt", src)
	if err == nil {
		body, readErr := os.ReadFile(filepath.Join(state, "victim.txt"))
		t.Fatalf("PATH TRAVERSAL ACCEPTED: body=%q read=%v", body, readErr)
	}
}
func TestM10RegressionApplyDefaultKindIsIdempotent(t *testing.T) {
	s, err := communication.NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := applyRoutineSpec{ID: "default-kind", ChatID: 11, Channel: "telegram_bot", OrganizationID: "personal", RuntimeID: "alice", PolicyVersion: "policy-1", Expression: "0 * * * *", Input: "work"}
	if _, err := applyRoutine(s, "alice", spec); err != nil {
		t.Fatal(err)
	}
	if _, err := applyRoutine(s, "alice", spec); err != nil {
		t.Fatalf("UNCHANGED REAPPLY FAILS: %v", err)
	}
}
func TestM10RegressionApplyDoesNotRecoverLiveOutbox(t *testing.T) {
	root := t.TempDir()
	spoolDir := filepath.Join(root, "spool")
	stateDir := filepath.Join(root, "state")
	s, err := communication.NewSpool(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDelivery(communication.Delivery{ID: "live-delivery", Channel: "telegram_bot", ChatID: 11, Text: "in progress"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("setup claim: %v %v", d, err)
	}
	file := filepath.Join(root, "empty.yaml")
	if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyFile(spoolDir, stateDir, "alice", file, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(spoolDir, "outbox", "sending", "live-delivery.json")); err != nil {
		t.Fatalf("EMPTY APPLY RECLASSIFIED LIVE DELIVERY: %v", err)
	}
}
