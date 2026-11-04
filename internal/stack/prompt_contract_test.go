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

// TestChannelContractIsModular (M31.9): the contract composes per-family
// sections — a family the operator switched off contributes no guidance,
// while families without an explicit off entry keep theirs.
func TestChannelContractIsModular(t *testing.T) {
	s := Settings{Tools: map[string]ToolEntry{
		"watches": {Via: ToolViaOff},
		"skills":  {Via: ToolViaOff},
	}}
	body := s.channelContract(false)
	for _, gone := range []string{"watch_create", "watch_sample", "skill_search", "skill_edit"} {
		if strings.Contains(body, gone) {
			t.Fatalf("off family leaked guidance %q into contract", gone)
		}
	}
	for _, kept := range []string{"routine_create", "tool_search", "MEDIA:", "inbox_fetch"} {
		if !strings.Contains(body, kept) {
			t.Fatalf("enabled/always family lost guidance %q", kept)
		}
	}
	// No tools config at all → full contract, identical to the const.
	full := Settings{}.channelContract(false)
	if full != ChannelContract {
		t.Fatal("ungoverned space must render the complete channel contract")
	}
}

// TestChannelContractDropsAbsentFamilies (M31.9 S4): under a published
// passport the tools projection is the authority — a toolset not declared
// there is not admitted, so its section must produce zero prompt text and
// zero hallucination bait. Cross-family clauses (skills pointing at
// file/document/media/terminal tools, watches pointing at routine/assignment
// management) drop with the family they describe.
func TestChannelContractDropsAbsentFamilies(t *testing.T) {
	g := Settings{Tools: map[string]ToolEntry{
		"routines": {Via: ToolViaToolHub},
		"files":    {Via: ToolViaToolHub},
	}}
	body := g.channelContract(true)
	for _, gone := range []string{
		"watch_create", "watch_edit", "watch_list", "watch_remove", "watch_sample",
		"skill_search", "skill_edit",
		"assignment_create", "assignment_edit", "assignment_plan",
		"exposure_create", "exposure_remove", "exposure_list",
		"document_extract", "media_inspect", "terminal_exec", "file_write",
		"await_user_confirmation",
		"For SSH operations",
	} {
		if strings.Contains(body, gone) {
			t.Errorf("absent family leaked guidance %q into governed contract", gone)
		}
	}
	for _, kept := range []string{"routine_create", "routine_edit", "file_search", "tool_search", "tool_call", "platform_guide", "MEDIA:", "inbox_fetch"} {
		if !strings.Contains(body, kept) {
			t.Errorf("enabled/always family lost guidance %q", kept)
		}
	}
}

// TestChannelContractHonorsAliases (M31.9 S4): the operator-facing aliases
// fold onto their capability family — switching `file` or `services` off
// drops the `files`/`connectors` guidance exactly like the canonical name.
func TestChannelContractHonorsAliases(t *testing.T) {
	s := Settings{Tools: map[string]ToolEntry{
		"file":      {Via: ToolViaOff},
		"artifacts": {Via: ToolViaOff},
		"services":  {Via: ToolViaOff},
	}}
	body := s.channelContract(false)
	for _, gone := range []string{"file_search", "file_list", "await_user_confirmation"} {
		if strings.Contains(body, gone) {
			t.Fatalf("off alias leaked family guidance %q", gone)
		}
	}
	if !strings.Contains(body, "watch_create") {
		t.Fatal("enabled family lost guidance under alias off")
	}
	// The artifacts alias alone — with no canonical `files` entry — still
	// switches the files family off.
	alone := Settings{Tools: map[string]ToolEntry{"artifacts": {Via: ToolViaOff}}}
	if body := alone.channelContract(false); strings.Contains(body, "file_search") {
		t.Fatal("artifacts alias alone failed to drop the files section")
	}
}

// TestChannelContractDropsCrossFamilyClauses (M31.9 S4): guidance inside an
// enabled family's section that names another family's tools is still
// hallucination bait when that other family is off — the clause drops with
// the family it describes, not with the section that hosts it.
func TestChannelContractDropsCrossFamilyClauses(t *testing.T) {
	s := Settings{Tools: map[string]ToolEntry{
		// The governed projection emits the canonical `skills` toolset only —
		// `skills_manage` is a legacy key that never survives it.
		"skills":  {Via: ToolViaToolHub},
		"watches": {Via: ToolViaToolHub},
	}}
	body := s.channelContract(true)
	for _, gone := range []string{
		"file_write", "document_extract", "media_inspect", "terminal_exec",
		"routine_edit", "routine_create", "assignment",
	} {
		if strings.Contains(body, gone) {
			t.Errorf("cross-family clause leaked %q into governed contract", gone)
		}
	}
	for _, kept := range []string{"skill_search", "skill_edit", "watch_create", "watch_sample", "confirm_url", "base_revision", "input()"} {
		if !strings.Contains(body, kept) {
			t.Errorf("enabled family's own guidance lost %q", kept)
		}
	}
}
