package stack

import (
	"strings"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

func TestChannelContractExplainsBrowserWait(t *testing.T) {
	for _, phrase := range []string{"each open creates a new tab", "numeric duration in seconds", "use \"2\" for two seconds"} {
		if !strings.Contains(ChannelContract, phrase) {
			t.Errorf("browser contract missing %q", phrase)
		}
	}
}

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

func TestChannelContractWarnsSettingsCanInterruptTurn(t *testing.T) {
	if !strings.Contains(ChannelContract, "settings edit may respawn") || !strings.Contains(ChannelContract, "same-turn restore") {
		t.Fatal("settings guidance must protect same-turn restoration")
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
	for _, want := range []string{"you_must=click_link", "verified channel callback", "actor", "action", "revision", "expiry", "confirmation button", "original chat", "status only", "never human consent"} {
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
		"managed":   Config(Settings{Model: "m", ModelURL: "http://x", Timezone: "UTC"}),
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
		for _, want := range []string{"MEDIA:", "attachment", "inbox/people/", "markdown"} {
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
	for _, kept := range []string{"routine_create", "tool_search", "MEDIA:", "inbox/people/"} {
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
	for _, kept := range []string{"routine_create", "routine_edit", "file_search", "tool_search", "tool_call", "platform_guide", "MEDIA:", "inbox/people/"} {
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

// R6 contract guidance: the stable prompt names the memory write path and
// its denial semantics — remember/fact lanes, the propose/owner-confirmation
// gate for existing-memory edits and the invalid-shape retry discipline.
func TestChannelContractGuidesMemoryCard(t *testing.T) {
	for _, want := range []string{"memory_card", "remember", "status=denied", "status=invalid", "propose", "owner", "send an existing memory entry", "do not create a new file"} {
		if !strings.Contains(contractMemory, want) {
			t.Fatalf("contract missing %q", want)
		}
	}
	// The selected tool and the independent config switch must both admit
	// the section. A disabled switch leaves the tool selected but inert.
	enabled, disabled := true, false
	governed := Settings{Tools: map[string]ToolEntry{"memory": {Via: ToolViaToolHub}}, PassportConfig: &spawnv1.Config{Memory: spawnv1.Memory{Enabled: &enabled}}}
	if body := governed.channelContract(true); !strings.Contains(body, "memory_card") {
		t.Fatal("enabled memory space lost the memory guidance")
	}
	governed.PassportConfig.Memory.Enabled = &disabled
	if body := governed.channelContract(true); strings.Contains(body, "memory_card") {
		t.Fatal("disabled memory kept the memory guidance")
	}
	if body := (Settings{Tools: map[string]ToolEntry{"memory": {Via: ToolViaOff}}, PassportConfig: &spawnv1.Config{Memory: spawnv1.Memory{Enabled: &enabled}}}).channelContract(true); strings.Contains(body, "memory_card") {
		t.Fatal("disabled memory toolset kept the memory guidance")
	}
}

func TestChannelContractDoesNotInventFormattingHistory(t *testing.T) {
	if !strings.Contains(contractMid, "do not invent an edit history") {
		t.Fatal("channel contract lost formatting evidence rule")
	}
	if !strings.Contains(contractHead, "instead of listing speculative causes") {
		t.Fatal("channel contract lost failure diagnostic rule")
	}
}

func TestChannelContractExplainsFiveSessionLimit(t *testing.T) {
	if !strings.Contains(contractMid, "Up to five of this owner's sessions") || !strings.Contains(contractMid, "additional runs wait") {
		t.Fatal("always-on contract must explain the five-session limit and queue")
	}
}

// Incident 2026-11-12: settings_read returned the complete reviewed key set,
// yet the model swept the workspace for a language knob for three minutes —
// argument denials surfaced as "not an admitted tool name" and sent it back
// into tool_search. The contract must make authoritative inventory answers
// terminal, point language asks at the channel command and read denials as
// denials.
func TestChannelContractTerminatesAuthoritativeAnswers(t *testing.T) {
	for _, want := range []string{"manageable_keys", "/language", "denial", "ends the search"} {
		if !strings.Contains(contractMid, want) {
			t.Errorf("channel contract missing %q", want)
		}
	}
}

func TestChannelContractBansContextEcho(t *testing.T) {
	// Incident 2026-11-10: the model reproduced injected context blocks and the
	// retained step log verbatim — 220K chars fanned out as 75 chat messages.
	if !strings.Contains(contractMid, "never reply content") {
		t.Fatal("contract lost the context-echo ban")
	}
	if !strings.Contains(contractMid, "list only this run's actual calls") {
		t.Fatal("contract lost the this-run call-report rule")
	}
	if !strings.Contains(contractMid, "tool_call rejects them") {
		t.Fatal("contract lost the visible-tools-are-direct rule")
	}
}
