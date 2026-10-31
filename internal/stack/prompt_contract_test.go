package stack

import (
	"strings"
	"testing"
)

// The stable prompt contract is the agent's only routing map for monitoring
// asks: it must steer "follow/monitor a source" requests to watch_subscribe
// and away from the cron-shaped kind=agent misuse that re-wakes the model
// on a timer to re-check a source that produced nothing.
func TestChannelContractGuidesWatchSubscription(t *testing.T) {
	for _, want := range []string{
		"watch_subscribe",
		"sink=notify",
		"sink=agent",
		"delivery=stream",
		"watch_resume",
		"filter",
		"assignment_plan",
	} {
		if !strings.Contains(ChannelContract, want) {
			t.Errorf("channel contract missing %q", want)
		}
	}
}

func TestChannelContractRequiresHumanToolHubConfirmation(t *testing.T) {
	for _, want := range []string{"await_user_confirmation", "verified channel callback", "actor", "action", "revision", "expiry", "confirmation button", "original chat", "status only", "never human consent"} {
		if !strings.Contains(ChannelContract, want) {
			t.Errorf("channel contract missing %q", want)
		}
	}
}
