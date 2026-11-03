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

// The contract steers project creation to project_create: file_write never
// mints a new top-level project directory (SPEC-0092 R5.3).
func TestChannelContractGuidesProjectCreate(t *testing.T) {
	if !strings.Contains(ChannelContract, "project_create") {
		t.Error("channel contract must name project_create as the only way to mint a project directory")
	}
}

func TestChannelContractRequiresHumanToolHubConfirmation(t *testing.T) {
	for _, want := range []string{"await_user_confirmation", "verified channel callback", "actor", "action", "revision", "expiry", "confirmation button", "original chat", "status only", "never human consent"} {
		if !strings.Contains(ChannelContract, want) {
			t.Errorf("channel contract missing %q", want)
		}
	}
}

// The upstream api_server platform hint claims MEDIA: markers are never
// intercepted and non-image files never attach — true for a bare API caller,
// false behind the hub gateway. Every render path must carry the
// agent.platform_hints.api_server.replace override or the model trusts the
// stock hint and tells users the chat cannot receive file attachments.
func TestAPIServerPlatformHintOverrideRendered(t *testing.T) {
	for name, cfg := range map[string]M{
		"managed":   Config(Settings{CapabilityMode: "managed", Model: "m", ModelURL: "http://x", Timezone: "UTC"}),
		"unmanaged": Config(Settings{Model: "m", ModelURL: "http://x", Timezone: "UTC"}),
	} {
		agent, ok := cfg["agent"].(M)
		if !ok {
			t.Fatalf("%s: no agent section", name)
		}
		hints, ok := agent["platform_hints"].(M)
		if !ok {
			t.Fatalf("%s: agent.platform_hints missing — stock api_server hint ships", name)
		}
		server, ok := hints["api_server"].(M)
		if !ok {
			t.Fatalf("%s: api_server hint override missing", name)
		}
		repl, _ := server["replace"].(string)
		if repl != APIServerPlatformHint {
			t.Fatalf("%s: api_server replace hint is not the reviewed hub text", name)
		}
		for _, want := range []string{"MEDIA:", "attachment", "inbox_fetch", "markdown"} {
			if !strings.Contains(repl, want) {
				t.Errorf("%s: replacement hint missing %q", name, want)
			}
		}
	}
}
