package stack

import (
	"strings"
	"testing"
)

// The stable prompt contract is the agent's only routing map for monitoring
// asks: it must steer "follow/monitor a source" requests to watch_create
// and away from the cron-shaped kind=agent misuse that re-wakes the model
// on a timer to re-check a source that produced nothing.
func TestChannelContractGuidesWatchSubscription(t *testing.T) {
	for _, want := range []string{
		"watch_create",
		"sink=notify",
		"sink=agent",
		"delivery=stream",
		"watch_edit",
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
// know that a tool enabled mid-session is already visible through tool_search
// and dispatchable through tool_call (ADR-0089 D1/D3, ADR-0094 D6).
func TestChannelContractNamesLiveToolInventory(t *testing.T) {
	if !strings.Contains(ChannelContract, "tool_search") {
		t.Error("channel contract must name tool_search as the live caller-scoped inventory")
	}
	if !strings.Contains(ChannelContract, "tool_call") {
		t.Error("channel contract must explain that a listed name absent from the visible tool list is callable through tool_call")
	}
}

// M31.9: the contract teaches the model the frozen catalog r5 wire names.
// A legacy name in the guidance steers calls at tools the surface no longer
// projects, so every retired name is forbidden and the r5 name that replaced
// it is required.
func TestChannelContractUsesR5ToolNames(t *testing.T) {
	for _, stale := range []string{
		"system_guide", "tools_list", " invoke ", "watch_subscribe", "watch_pause",
		"watch_resume", "watch_delete", "assignment_update", "assignment_apply",
		"assignment_progress", "routine_get", "routine_delete", "skills_search",
		"skills_list", "skill_view", "skill_manage", "image_inspect", "code_exec",
		"exposure_open", "exposure_close", "search_files",
	} {
		if strings.Contains(ChannelContract, stale) {
			t.Errorf("channel contract still names retired tool %q", stale)
		}
	}
	for _, name := range []string{
		"platform_guide", "tool_search", "tool_call", "watch_create", "watch_edit",
		"watch_remove", "watch_list", "watch_sample", "assignment_edit",
		"routine_read", "skill_search", "skill_edit", "media_inspect",
		"terminal_exec", "exposure_create", "exposure_remove", "file_search",
	} {
		if !strings.Contains(ChannelContract, name) {
			t.Errorf("channel contract missing r5 tool %q", name)
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
