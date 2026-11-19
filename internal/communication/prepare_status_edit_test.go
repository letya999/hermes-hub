package communication

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Link-free status updates still edit the existing prepare notice; action
// links have their own fresh-message regression test.
func TestPrepareNoticeEditsExistingStatusMessage(t *testing.T) {
	cfg := testConfig(t)
	gateway, err := newTestGateway(t, cfg)
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
	request := prepareOutcomeRequest{OnboardingID: "onboard-edit", Phase: "preparing"}
	if err := gateway.deliverPrepareNotice(cfg.Users[0], caller, "prepare-onboard-edit-preparing", "Подготовка идёт", request, false); err != nil {
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

// The stable wizard link is identical across phases, so each new phase notice
// must edit the one message the user already has instead of stacking a fresh
// message carrying the same URL.
func TestPrepareNoticeWizardLinkEditsExistingMessage(t *testing.T) {
	cfg := testConfig(t)
	gateway, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	sent := Delivery{
		ID:      "prepare-onboard-wiz-awaiting-credentials-aa11-notice",
		Channel: "telegram_bot", ChatID: 11,
		ConversationID: "telegram-11", DeliveryTargetID: "telegram-11",
		Text: "форма", TelegramMessageID: 4242,
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
	dir := filepath.Join(cfg.SpoolDir, "outbox", "done")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, sent.ID+".json"), sent); err != nil {
		t.Fatal(err)
	}
	wizard := "http://127.0.0.1:9999/connect/onboard-wiz?k=0123456789abcdef0123456789abcdef"
	request := prepareOutcomeRequest{OnboardingID: "onboard-wiz", Phase: "awaiting-oauth", FormURL: wizard}
	if err := gateway.deliverPrepareNotice(cfg.Users[0], caller, "prepare-onboard-wiz-awaiting-oauth-bb22", "oauth", request, false); err != nil {
		t.Fatal(err)
	}
	delivery, err := gateway.spool.ClaimDelivery()
	if err != nil || delivery == nil {
		t.Fatalf("delivery=%v err=%v", delivery, err)
	}
	if delivery.EditMessageID != 4242 {
		t.Fatalf("wizard notice stacked a new message: EditMessageID=%d want 4242", delivery.EditMessageID)
	}
}

func TestPrepareConfirmationPostsKeyboardInsteadOfEditingStatus(t *testing.T) {
	cfg := testConfig(t)
	gateway, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	sent := Delivery{
		ID: "prepare-onboard-confirm-preparing-prepare-started-notice", Channel: "telegram_bot", ChatID: 11,
		ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", TelegramMessageID: 4242,
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
	dir := filepath.Join(cfg.SpoolDir, "outbox", "done")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, sent.ID+".json"), sent); err != nil {
		t.Fatal(err)
	}
	proof := api.HumanConfirmation{OnboardingID: "onboard-confirm", Actor: caller, Nonce: "opaque", Revision: 1, ExpiresAt: time.Now().Add(time.Minute)}
	request := prepareOutcomeRequest{OnboardingID: proof.OnboardingID, Phase: "awaiting-confirm", FormURL: "http://127.0.0.1:9999/connect/onboard-confirm?k=0123456789abcdef0123456789abcdef", Confirmation: &proof}
	if err := gateway.deliverPrepareNotice(cfg.Users[0], caller, "prepare-onboard-confirm-awaiting-confirm", "confirm", request, false); err != nil {
		t.Fatal(err)
	}
	delivery, err := gateway.spool.ClaimDelivery()
	if err != nil || delivery == nil {
		t.Fatalf("delivery=%v err=%v", delivery, err)
	}
	if delivery.EditMessageID != 0 || len(delivery.Buttons) != 1 || delivery.Buttons[0].Data == "" {
		t.Fatalf("confirmation lost keyboard: edit=%d buttons=%d", delivery.EditMessageID, len(delivery.Buttons))
	}
}
