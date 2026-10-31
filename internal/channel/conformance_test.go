package channel

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// Conformance: every registered adapter runs the same contract checks, so a
// new channel proves the seam without touching gateway tests. Deps are
// stubbed — the checks assert contract shape, not provider I/O.

func adapterFixtures(t *testing.T) map[string]Channel {
	t.Helper()
	return map[string]Channel{
		"telegram_bot": NewTelegram(TelegramDeps{API: func() TelegramSender { return nil }}),
		"slack_app":    NewSlack(SlackDeps{Slack: func() SlackPoster { return nil }}),
		"mock": NewMock(MockDeps{
			LinkOwner: func(chatID int64) (string, error) { return "alice", nil },
			UserByPrincipal: func(id string) commodel.User {
				return commodel.User{ID: id, TelegramIDs: []int64{11}}
			},
			OwnsRoutineEnvelope: func(job commodel.Job, canonical identity.Envelope) bool { return job.Envelope.Equal(canonical) },
			Capture:             func(d *commodel.Delivery) error { return nil },
		}),
	}
}

// CHG-0129 S2.7: Caps().Actions declares that a channel carries actionable
// affordances (link surfaces the user can act on). Adapters without it keep
// receiving the same notice with the plain link in the message text — the
// fallback is the contract, not a degraded mode.
func TestConformanceActionsCapability(t *testing.T) {
	fixtures := adapterFixtures(t)
	if !fixtures["telegram_bot"].Caps().Actions {
		t.Fatal("telegram must declare action-link delivery")
	}
	for _, id := range []string{"slack_app", "mock"} {
		if fixtures[id].Caps().Actions {
			t.Fatalf("%s must not declare action-link delivery", id)
		}
	}
}

// Every adapter names a stable channel id and answers Caps without a gateway.
func TestConformanceIDAndCaps(t *testing.T) {
	for id, ch := range adapterFixtures(t) {
		if ch.ID() != id {
			t.Fatalf("adapter reports %q, registered as %q", ch.ID(), id)
		}
		_ = ch.Caps()
	}
}

// Deliveries without a valid chat identity are rejected at normalize —
// channel-specific identity rules never fall back to a default transport.
func TestConformanceNormalizeRejectsBrokenIdentity(t *testing.T) {
	for id, ch := range adapterFixtures(t) {
		d := commodel.Delivery{ChatID: 0}
		if err := ch.NormalizeDelivery(&d); err == nil {
			t.Fatalf("%s accepted a delivery with no chat identity", id)
		}
	}
}

// Occurrence validation rejects a caller envelope that is not the channel's
// own — this is the durable-ownership gate a replayed record must pass.
func TestConformanceValidateOccurrenceRejectsForeignCaller(t *testing.T) {
	foreign := identity.Envelope{Schema: 99, PrincipalID: "mallory"}
	for id, ch := range adapterFixtures(t) {
		o := commodel.RoutineOccurrence{ScheduleID: "r-1", Revision: 1, Job: commodel.Job{ChatID: 11}}
		if err := ch.ValidateOccurrence(o, foreign); err == nil {
			t.Fatalf("%s accepted a foreign caller envelope", id)
		}
	}
}

// Mock-channel admission is ownership-bound: a chat claimed by another user
// refuses with the contract error the HTTP surface maps to 403.
func TestConformanceMockPrepareRoutineDenied(t *testing.T) {
	ch := NewMock(MockDeps{
		LinkOwner: func(chatID int64) (string, error) { return "bob", nil },
	})
	caller := identity.MockEnvelope("alice", 42, "alice", "policy-1")
	in := commodel.Schedule{ScheduleID: "r-1", Channel: "mock", ChatID: 42}
	if _, err := ch.PrepareRoutine(&in, caller); err == nil {
		t.Fatal("mock admitted a routine on someone else's chat")
	}
}

// Telegram normalize derives the stable transport ids from the chat — the
// durable record a gateway writes carries channel-owned routing untouched.
func TestConformanceTelegramNormalizeKeepsAddress(t *testing.T) {
	ch := adapterFixtures(t)["telegram_bot"]
	d := commodel.Delivery{ChatID: -100777, TopicID: 5}
	if err := ch.NormalizeDelivery(&d); err != nil {
		t.Fatalf("telegram rejected a valid delivery: %v", err)
	}
	if d.ChatID != -100777 || d.TopicID != 5 {
		t.Fatalf("address mangled: chat %d topic %d", d.ChatID, d.TopicID)
	}
	if d.ConversationID != "telegram--100777" || d.DeliveryTargetID != "telegram--100777" {
		t.Fatalf("transport ids not channel-derived: %q / %q", d.ConversationID, d.DeliveryTargetID)
	}
}
