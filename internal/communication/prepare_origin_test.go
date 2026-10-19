package communication

import (
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
	gateway, err := New(cfg)
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
	origin, ok := spool.loadPrepareOrigin("onboard-route1")
	if !ok || origin.ConversationID != "telegram-11-t-aaaa" {
		t.Fatalf("persisted origin=%+v ok=%v", origin, ok)
	}
}

// A persisted route written by the old newest-active heuristic can point at the
// wrong session; a deterministic initiator match must override it so a retry
// of the same onboarding id stops leaking (observed: gitlab bound to the
// marketplace topic).
func TestPrepareOriginInitiatorOverridesStalePersisted(t *testing.T) {
	cfg := testConfig(t)
	gateway, err := New(cfg)
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
	if err := spool.putPrepareOrigin("onboard-route2", prepareOrigin{ConversationID: "telegram--99-t-bbbb", DeliveryTargetID: "telegram--99", ChatID: -99, TopicID: 42}); err != nil {
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
		t.Fatalf("stale origin not corrected: chat=%d topic=%d", delivery.ChatID, delivery.TopicID)
	}
	origin, _ := spool.loadPrepareOrigin("onboard-route2")
	if origin.ConversationID != "telegram-11-t-aaaa" {
		t.Fatalf("origin not rebound: %+v", origin)
	}
}
