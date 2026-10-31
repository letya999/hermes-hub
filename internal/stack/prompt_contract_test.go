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

// The skills contract guidance must reflect the honest-inventory semantics:
// skills_list flags ineligible entries and names their gaps so the agent can
// route a user ask to the missing setup instead of treating the catalog as
// silently complete.
func TestChannelContractDescribesSkillInventory(t *testing.T) {
	if !strings.Contains(ChannelContract, "eligible") {
		t.Error("channel contract must describe the eligible flag on skills_list entries")
	}
	if !strings.Contains(ChannelContract, "missing") {
		t.Error("channel contract must describe missing requirements on ineligible entries")
	}
}

// The contract names the live inventory path: without it the model cannot
// know that a tool enabled mid-session is already visible through tools_list
// and dispatchable through invoke (ADR-0089 D1/D3).
func TestChannelContractNamesLiveToolInventory(t *testing.T) {
	if !strings.Contains(ChannelContract, "tools_list") {
		t.Error("channel contract must name tools_list as the live caller-scoped inventory")
	}
	if !strings.Contains(ChannelContract, "invoke") {
		t.Error("channel contract must explain that a listed name absent from the visible tool list is callable through invoke")
	}
}
