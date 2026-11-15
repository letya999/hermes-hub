package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestManagedPromptSourcesInventory pins the M29.0 spike findings
// (CHG-0241 research.md): the exact set of sources feeding a managed turn's
// system prompt, each with its writability boundary. A render change that
// adds, drops or re-scopes a prompt source must update this inventory and
// the migration matrix deliberately — it is the baseline every later M29
// stage migrates from.
func TestManagedPromptSourcesInventory(t *testing.T) {
	managed := Settings{CapabilityMode: "managed", CapabilityProfileID: "ops", CapabilityGeneration: 3, Model: "m", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", Environment: "dev"}

	t.Run("allowed", func(t *testing.T) {
		cfg := Config(managed)
		// Pinned-Hermes evidence: memory_enabled/user_profile_enabled gate the
		// upstream MemoryStore. Managed renders keep both off, so agent-
		// writable $HERMES_HOME/memories/{MEMORY,USER}.md never reach the turn.
		mem, ok := cfg["memory"].(M)
		if !ok || mem["memory_enabled"] != false || mem["user_profile_enabled"] != false {
			t.Fatalf("managed render must keep upstream MEMORY.md/USER.md out: %#v", cfg["memory"])
		}
		// No terminal.cwd is rendered for managed spaces: upstream context
		// discovery resolves the HOME fallback — agent-writable /state/home.
		// Documented gap (ADR-0092 D5): a project AGENTS.md there still loads.
		if _, ok := cfg["terminal"]; ok {
			t.Fatalf("managed render unexpectedly pins terminal section: %#v", cfg["terminal"])
		}
		// upstream's gateway.platforms.<plat>.skip_context_files suppression
		// lever is rendered by the managed contract (R9.1): agent-writable
		// SOUL.md/AGENTS.md/MEMORY.md in the context root must not inject a
		// second authority into a managed turn.
		gw, ok := cfg["gateway"].(M)
		if !ok {
			t.Fatal("managed render must emit the gateway context-file fence")
		}
		if suppress, ok := gw["platforms"].(M)["api_server"].(M)["skip_context_files"].(bool); !ok || !suppress {
			t.Fatalf("managed render must pin api_server skip_context_files: %#v", gw)
		}
		agent, ok := cfg["agent"].(M)
		if !ok {
			t.Fatal("managed render must carry the agent section")
		}
		denied, ok := agent["disabled_toolsets"].([]string)
		if !ok {
			t.Fatal("managed render must enumerate disabled_toolsets")
		}
		for _, name := range []string{"skills", "memory"} {
			if !slices.Contains(denied, name) {
				t.Errorf("upstream %q toolset must stay disabled under managed mode", name)
			}
		}
		hint, ok := agent["platform_hints"].(M)["api_server"].(M)["replace"].(string)
		if !ok || hint != APIServerPlatformHint {
			t.Fatal("managed render must pin the hub api_server platform hint")
		}
	})

	t.Run("refusal_rollback", func(t *testing.T) {
		dir := t.TempDir()
		// Rollback case: a user-edited SOUL is never overwritten by the heal
		// path — the render preserves unknown content rather than destroying it.
		edited := []byte("# owner's own persona\n")
		if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), edited, 0600); err != nil {
			t.Fatal(err)
		}
		template := filepath.Join(dir, "config-SOUL.md")
		if err := os.WriteFile(template, []byte("# platform template\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := HealUserSoul(dir, template); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(dir, "SOUL.md"))
		if err != nil || string(body) != string(edited) {
			t.Fatalf("user-edited SOUL must survive render: %v", err)
		}
		// Refusal case: hostile managed settings never produce a render.
		hostile := managed
		hostile.Model = "m\nprompt-injection"
		if err := hostile.Validate(); err == nil {
			t.Fatal("managed settings with an injected model id must refuse validation")
		}
		// Fail-soft: a missing persona still materializes the hub-owned
		// contract layer — the managed prompt never silently empties.
		if err := os.MkdirAll(filepath.Join(dir, "generated"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := MaterializeSoulEffective(dir, managed); err != nil {
			t.Fatal(err)
		}
		effective, err := os.ReadFile(filepath.Join(dir, "generated", "soul-effective.dev.md"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(effective)
		contract := strings.Index(text, "Channel contract")
		persona := strings.Index(text, "owner's own persona")
		if contract < 0 || persona < 0 || contract > persona {
			t.Fatalf("soul-effective must layer contract ahead of persona:\n%s", text)
		}
	})
}

// A9: every managed turn's system prompt carries exactly one Hub contract —
// the read-only effective SOUL (platform contract -> org -> profile -> user)
// plus the published revision/core pointer — and no agent-writable context
// file (AGENTS.md/USER.md/MEMORY.md under /state/home or /workspace) can add
// a second authority, because the managed render pins upstream's
// skip_context_files and HERMES_IGNORE_RULES fences.
func TestManagedTurnHasSinglePinnedPromptAcrossEntrypoints(t *testing.T) {
	managed := Settings{CapabilityMode: "managed", CapabilityProfileID: "ops", CapabilityGeneration: 3, Model: "m", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", Environment: "dev"}

	cfg := Config(managed)
	gw, ok := cfg["gateway"].(M)
	if !ok {
		t.Fatal("managed config must pin the upstream context-file fence")
	}
	if v, _ := gw["platforms"].(M)["api_server"].(M)["skip_context_files"].(bool); !v {
		t.Fatalf("skip_context_files must stay on for api_server turns: %#v", gw)
	}
	env := RuntimeService(managed, t.TempDir(), t.TempDir())["environment"].(M)
	if env["HERMES_IGNORE_RULES"] != "1" {
		t.Fatalf("managed runtime must pin HERMES_IGNORE_RULES for non-gateway entrypoints: %#v", env["HERMES_IGNORE_RULES"])
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("# persona\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A planted agent-writable AGENTS.md must never reach the prompt.
	home := filepath.Join(dir, "managed", "dev", "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("PLANTED-SECOND-AUTHORITY"), 0600); err != nil {
		t.Fatal(err)
	}
	rev := filepath.Join(dir, "generated", "passports", "r-0007")
	if err := os.MkdirAll(rev, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rev, "prompt-pointer.txt"), []byte("revision=r-0007 core=terminal,files extended=+12 platform=tool_search+tool_call\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0007\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSoulEffective(dir, managed); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, filepath.Join(dir, "generated", "soul-effective.dev.md"))
	if strings.Count(text, "Channel contract") != 1 {
		t.Fatalf("managed prompt must carry exactly one Hub contract:\n%s", text[:min(len(text), 800)])
	}
	if !strings.Contains(text, "r-0007") || !strings.Contains(text, "extended=+12") {
		t.Fatalf("managed prompt must pin the current revision and bounded core inventory:\n%s", text[:min(len(text), 800)])
	}
	if strings.Contains(text, "PLANTED-SECOND-AUTHORITY") {
		t.Fatal("agent-writable AGENTS.md must never enter the managed prompt")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSoulEffectiveHonorsFamilyOff (M31.9): the rendered soul-effective
// composes the modular contract — a family switched off contributes no
// section to the materialized prompt.
func TestSoulEffectiveHonorsFamilyOff(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	s := Settings{Environment: "dev", Tools: map[string]ToolEntry{
		"watches": {Via: ToolViaOff},
	}}
	if err := MaterializeSoulEffective(dir, s); err != nil {
		t.Fatal(err)
	}
	effective, err := os.ReadFile(filepath.Join(dir, "generated", "soul-effective.dev.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(effective), "For a standalone watch") {
		t.Fatal("soul-effective renders watch guidance for a space with watches off")
	}
	if !strings.Contains(string(effective), "routine_create") {
		t.Fatal("soul-effective lost routine guidance")
	}
}

// TestSoulEffectiveGovernedDropsAbsentFamilies (M31.9 S4): once a space has a
// published passport revision, its tools projection is the admission
// authority — a family that projection does not declare contributes no
// guidance even without an explicit `via: off`.
func TestSoulEffectiveGovernedDropsAbsentFamilies(t *testing.T) {
	dir := t.TempDir()
	passports := filepath.Join(dir, "generated", "passports")
	if err := os.MkdirAll(passports, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(passports, "current"), []byte("rev-test"), 0600); err != nil {
		t.Fatal(err)
	}
	s := Settings{Environment: "dev", Tools: map[string]ToolEntry{
		"routines": {Via: ToolViaToolHub},
	}}
	if err := MaterializeSoulEffective(dir, s); err != nil {
		t.Fatal(err)
	}
	effective, err := os.ReadFile(filepath.Join(dir, "generated", "soul-effective.dev.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(effective)
	for _, gone := range []string{"watch_create", "skill_search", "file_search", "exposure_create", "await_user_confirmation"} {
		if strings.Contains(text, gone) {
			t.Fatalf("governed soul-effective leaked %q for an undeclared family", gone)
		}
	}
	if !strings.Contains(text, "routine_create") {
		t.Fatal("governed soul-effective lost the declared family's guidance")
	}
}
