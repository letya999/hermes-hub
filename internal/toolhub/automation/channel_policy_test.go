package automation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/routines"
)

func TestLocalChannelPolicySurvivesCommunicationOutageAndRejectsRebind(t *testing.T) {
	h := newHarness(t)
	legacy := filepath.Join(h.root, "legacy")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	routing := filepath.Join(h.root, "routing.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(routing, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"organization_id":"personal","users":[{"id":"alice","enabled":true,"runtime_id":"rt-alice","policy_version":"policy-1","telegram_ids":[11]}]}`)
	policy := ChannelPolicy{RoutingFile: routing, LegacyRoot: legacy}
	if err := policy.Validate([]string{"alice"}); err != nil {
		t.Fatal(err)
	}
	h.grant(t, "alice", "write")
	host := h.host(t)
	host.cfg.AuthorizeChannel = policy.Authorize
	alice := h.envelope(t, "alice")
	draft := draft("rt-local-policy", alice)
	draft.OrganizationID = "personal"
	item, err := host.CreateSchedule(context.Background(), alice, draft, "")
	if err != nil {
		t.Fatal(err)
	}
	h.comm.down.Store(true)
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	if _, logical := h.sup.counts(); logical != 1 {
		t.Fatal("communication outage blocked due execution")
	}
	if pendingDeliveries(t, host, "alice") == 0 || h.comm.count() != 0 {
		t.Fatal("outage did not defer delivery")
	}
	h.comm.down.Store(false)
	if err := host.Recover(context.Background()); err != nil || h.comm.count() != 1 {
		t.Fatalf("delivery did not recover: %v", err)
	}
	job := commodel.Job{Envelope: alice, ID: "routine-test", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron", ChatID: 11}
	job.ConversationID = routines.RoutineConversation(item.ScheduleID)
	if err := policy.Authorize(context.Background(), item.ScheduleID, job); err != nil {
		t.Fatalf("owned route refused: %v", err)
	}
	write(`{"organization_id":"personal","users":[{"id":"bob","enabled":true,"telegram_ids":[11]}]}`)
	if err := policy.Authorize(context.Background(), item.ScheduleID, job); err == nil {
		t.Fatal("rebound chat authorized former owner")
	}
}

func TestLocalChannelPolicyRejectsSlackRebind(t *testing.T) {
	root := t.TempDir()
	routing := filepath.Join(root, "routing.json")
	write := func(owner string) {
		t.Helper()
		body := `{"organization_id":"personal","users":[{"id":"` + owner + `","enabled":true,"slack_ids":[{"team_id":"TEAM","user_id":"USER"}]}]}`
		if err := os.WriteFile(routing, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("alice")
	policy := ChannelPolicy{RoutingFile: routing, LegacyRoot: root}
	user := commodel.User{ID: "alice", Enabled: true}
	job := commodel.Job{Envelope: user.SlackEnvelope("TEAM", "USER"), ID: "routine-slack", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "slack_app", Trigger: "cron"}
	job.ConversationID = routines.RoutineConversation("rt-slack")
	if err := policy.Authorize(context.Background(), "rt-slack", job); err != nil {
		t.Fatal(err)
	}
	write("bob")
	if err := policy.Authorize(context.Background(), "rt-slack", job); err == nil {
		t.Fatal("rebound Slack sender authorized former owner")
	}
}

func TestDisabledRoutingOwnerCannotBlockEnabledOwner(t *testing.T) {
	root := t.TempDir()
	routing := filepath.Join(root, "routing.json")
	body := `{"organization_id":"personal","users":[{"id":"alice","enabled":true,"telegram_ids":[11]},{"id":"bob","enabled":false,"telegram_ids":[11]}]}`
	if err := os.WriteFile(routing, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := ChannelPolicy{RoutingFile: routing, LegacyRoot: root}
	if err := policy.Validate([]string{"alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	job := commodel.Job{Envelope: (commodel.User{ID: "alice"}).Envelope(11), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron", ChatID: 11}
	if err := policy.Authorize(context.Background(), "rt-enabled", job); err != nil {
		t.Fatal(err)
	}
	job.UserID, job.ActorID, job.PrincipalID, job.ScopeID = "bob", "bob", "bob", "user:bob"
	if err := policy.Authorize(context.Background(), "rt-enabled", job); err == nil {
		t.Fatal("disabled owner authorized")
	}
}
