package communication

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// Two concurrent sessions of one principal: the onboarding was initiated by the
// older running job; a newer active job in another topic must not steal the
// outcome route (observed: gitlab form landed in the marketplace topic).
func TestPrepareOriginFollowsInitiatingJobNotNewest(t *testing.T) {
	cfg := testConfig(t)
	gateway, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	spool := gateway.spool
	writeMapping := func(id, conversation string, chat, topic int64, created time.Time, status, result string) {
		mapping := JobMapping{
			JobID: id, PrincipalID: "alice", Trigger: "message", Status: status,
			ConversationID: conversation, DeliveryTargetID: "telegram-" + id,
			ChatID: chat, TopicID: topic, CreatedAt: created, Result: result,
		}
		if err := atomicJSON(filepath.Join(spool.root, "mappings", spoolFileID(id)+".json"), mapping); err != nil {
			t.Fatal(err)
		}
	}
	onboardingCreated := time.Now().UTC().Add(-2 * time.Minute)
	// Initiating job in the DM session — finished by outcome time and its
	// report named the installed connector.
	writeMapping("job-init", "telegram-11-t-aaaa", 11, 0, onboardingCreated.Add(-30*time.Second), "completed", "Повторил установку GitLab MCP через ToolHub")
	// Newer job in a group topic that started inside the window and is still
	// running — every timing signal points at it, but its work is unrelated.
	writeMapping("job-other", "telegram--99-t-bbbb", -99, 42, onboardingCreated.Add(-10*time.Second), "running", "")
	// An even older completed job — must not win the window match.
	writeMapping("job-old", "telegram-11-t-cccc", 11, 0, onboardingCreated.Add(-time.Hour), "completed", "done")

	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	request := prepareOutcomeRequest{
		OnboardingID: "onboard-route1", Phase: "awaiting-credentials",
		FormURL:    "http://127.0.0.1:8090/credentials/onboard-route1?nonce=x",
		Repository: "https://github.com/zereight/gitlab-mcp",
		CreatedAt:  onboardingCreated.Format(time.RFC3339Nano),
	}
	if err := gateway.deliverPrepareNotice(cfg.Users[0], caller, "prepare-onboard-route1-awaiting-credentials", "Откройте форму", request, false); err != nil {
		t.Fatal(err)
	}
	delivery, err := spool.ClaimDelivery()
	if err != nil || delivery == nil {
		t.Fatalf("delivery=%v err=%v", delivery, err)
	}
	if delivery.ChatID != 11 || delivery.TopicID != 0 {
		t.Fatalf("notice leaked to another session: chat=%d topic=%d", delivery.ChatID, delivery.TopicID)
	}
}

// Leftover route records from builds that persisted origins are dead data:
// nothing reads them, so they cannot steer a delivery even when a live
// initiator exists.
func TestPrepareOriginIgnoresLegacyRouteRecords(t *testing.T) {
	cfg := testConfig(t)
	gateway, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	spool := gateway.spool
	onboardingCreated := time.Now().UTC().Add(-10 * time.Minute)
	mapping := JobMapping{
		JobID: "job-real", PrincipalID: "alice", Trigger: "message", Status: "completed",
		ConversationID: "telegram-11-t-aaaa", DeliveryTargetID: "telegram-11",
		ChatID: 11, CreatedAt: onboardingCreated.Add(-time.Minute),
		Result: "Запустил установку GitLab MCP",
	}
	if err := atomicJSON(filepath.Join(spool.root, "mappings", spoolFileID("job-real")+".json"), mapping); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{"conversation_id": "telegram--99-t-bbbb", "delivery_target_id": "telegram--99", "chat_id": -99, "topic_id": 42}
	if err := os.MkdirAll(filepath.Join(spool.root, "prepares"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(spool.root, "prepares", "dead-route.json"), legacy); err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	request := prepareOutcomeRequest{
		OnboardingID: "onboard-route2", Phase: "failed",
		Repository: "https://github.com/zereight/gitlab-mcp",
		CreatedAt:  onboardingCreated.Format(time.RFC3339Nano),
	}
	if err := gateway.deliverPrepareNotice(cfg.Users[0], caller, "prepare-onboard-route2-failed", "Не удалось", request, false); err != nil {
		t.Fatal(err)
	}
	delivery, err := spool.ClaimDelivery()
	if err != nil || delivery == nil {
		t.Fatalf("delivery=%v err=%v", delivery, err)
	}
	if delivery.ChatID != 11 {
		t.Fatalf("legacy route steered the notice: chat=%d topic=%d", delivery.ChatID, delivery.TopicID)
	}
}

func TestPrepareOriginIgnoresOldUncertainMockJob(t *testing.T) {
	cfg := testConfig(t)
	gateway, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := JobMapping{JobID: "mock-old", PrincipalID: "alice", Trigger: "message", Status: "uncertain",
		ConversationID: "telegram-800000903209265300", DeliveryTargetID: "telegram-800000903209265300",
		Channel: "mock", ChatID: 800000903209265300, CreatedAt: time.Now().Add(-20 * time.Minute)}
	if err := atomicJSON(filepath.Join(gateway.spool.root, "mappings", "mock-old.json"), old); err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	request := prepareOutcomeRequest{OnboardingID: "onboard-direct", Phase: "awaiting-credentials",
		Repository: "https://github.com/github/github-mcp-server", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := gateway.deliverPrepareNotice(cfg.Users[0], caller, "prepare-onboard-direct-awaiting-credentials", "Synthetic notice", request, false); err != nil {
		t.Fatal(err)
	}
	delivery, err := gateway.spool.ClaimDelivery()
	if err != nil || delivery == nil || delivery.ChatID != 11 || delivery.Channel != "telegram_bot" {
		t.Fatalf("old mock job captured direct notice: delivery=%v err=%v", delivery != nil, err)
	}
	old.CreatedAt = time.Now().Add(-time.Minute)
	if err := atomicJSON(filepath.Join(gateway.spool.root, "mappings", "mock-old.json"), old); err != nil {
		t.Fatal(err)
	}
	request.OnboardingID, request.CreatedAt = "onboard-direct-2", ""
	if err := gateway.deliverPrepareNotice(cfg.Users[0], caller, "prepare-onboard-direct-2-awaiting-credentials", "Synthetic notice", request, false); err != nil {
		t.Fatal(err)
	}
	delivery, err = gateway.spool.ClaimDelivery()
	if err != nil || delivery == nil || delivery.ChatID != 11 || delivery.Channel != "telegram_bot" {
		t.Fatalf("uncertain mock job captured direct notice: delivery=%v err=%v", delivery != nil, err)
	}
}
