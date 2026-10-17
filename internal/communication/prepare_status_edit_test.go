package communication

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// Once the first prepare notice reached Telegram, every later status for the
// same onboarding must edit that message instead of stacking new ones.
func TestPrepareNoticeEditsExistingStatusMessage(t *testing.T) {
	cfg := testConfig(t)
	gateway, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	sent := Delivery{
		ID:      "prepare-onboard-edit-preparing-prepare-started-notice",
		Channel: "telegram_bot", ChatID: 11,
		ConversationID: "telegram-11", DeliveryTargetID: "telegram-11",
		Text: "Принял установку", TelegramMessageID: 4242,
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
	dir := filepath.Join(cfg.SpoolDir, "outbox", "done")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, sent.ID+".json"), sent); err != nil {
		t.Fatal(err)
	}
	request := prepareOutcomeRequest{OnboardingID: "onboard-edit", Phase: "awaiting-credentials", FormURL: "http://127.0.0.1:8090/credentials/onboard-edit?nonce=x"}
	if err := gateway.deliverPrepareNotice(cfg.Users[0], caller, "prepare-onboard-edit-awaiting-credentials", "Откройте форму", request, false); err != nil {
		t.Fatal(err)
	}
	delivery, err := gateway.spool.ClaimDelivery()
	if err != nil || delivery == nil {
		t.Fatalf("delivery=%v err=%v", delivery, err)
	}
	if delivery.EditMessageID != 4242 {
		t.Fatalf("EditMessageID=%d want 4242", delivery.EditMessageID)
	}
}
