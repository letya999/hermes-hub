package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/systemguide"
)

func TestSystemGuideRejectsCustomCatalogContentsInReservedDirectory(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	reserved := filepath.Join(s.GlobalSkillsDir, "hermes-hub-system-guide")
	if err := os.MkdirAll(reserved, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SKILL.md", "extra-instructions.md"} {
		if err := os.WriteFile(filepath.Join(reserved, name), []byte("custom replacement"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := MaterializeGlobalSkills(dir, s); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(GeneratedSkillsDir(dir), "hermes-hub-system-guide")
	entries, err := os.ReadDir(generated)
	if err != nil || len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		t.Fatalf("custom catalog injected files into mandatory guide: entries=%v err=%v", entries, err)
	}
	body, err := os.ReadFile(filepath.Join(generated, "SKILL.md"))
	if err != nil || string(body) != systemguide.Skill {
		t.Fatal("custom catalog replaced canonical system guide")
	}
}

// The materialized effective SOUL mounts read-only into a uid-10001 runtime:
// writing it through atomic() leaves 0600 and the container dies on
// "open /state/hermes/SOUL.md: permission denied". Every rewrite — render or
// spawn — must leave it world-readable like the sibling hermes-effective.
func TestMaterializeSoulEffectiveStaysContainerReadable(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("# persona\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := MaterializeSoulEffective(dir, Settings{Environment: "dev"}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(dir, "generated", "soul-effective.dev.md"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0004 == 0 {
			t.Fatalf("soul-effective not readable by the runtime uid: %v", info.Mode())
		}
	}
}

func TestSystemGuideRoutingSurvivesCustomPersona(t *testing.T) {
	for _, mode := range []string{"", "managed"} {
		for _, env := range []string{"dev", "prod"} {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "generated"), 0755); err != nil {
				t.Fatal(err)
			}
			persona := "# My custom persona\nUse short answers.\n"
			if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte(persona), 0600); err != nil {
				t.Fatal(err)
			}
			if err := MaterializeSoulEffective(dir, Settings{Environment: env, CapabilityMode: mode}); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(dir, "generated", "soul-effective."+env+".md"))
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"hermes-hub-system-guide", "system_guide", "MCP", "CLI", "connector", "provider access"} {
				if !strings.Contains(string(body), required) {
					t.Errorf("mode=%q env=%s: required guide routing %q missing", mode, env, required)
				}
			}
			original, err := os.ReadFile(filepath.Join(dir, "SOUL.md"))
			if err != nil || string(original) != persona || !strings.Contains(string(body), strings.TrimSpace(persona)) {
				t.Fatal("guide delivery overwrote custom persona")
			}
		}
	}
}

func TestSystemGuideMaterializedWithEmptyOrCustomSkillCatalog(t *testing.T) {
	for _, custom := range []bool{false, true} {
		dir := t.TempDir()
		s := validSettings()
		s.GlobalSkillsDir = ""
		if custom {
			s.GlobalSkillsDir = t.TempDir()
			userSkill := filepath.Join(s.GlobalSkillsDir, "owner-skill")
			if err := os.MkdirAll(userSkill, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(userSkill, "SKILL.md"), []byte("owner content"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if err := MaterializeGlobalSkills(dir, s); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(GeneratedSkillsDir(dir), "hermes-hub-system-guide", "SKILL.md"))
			if err != nil || string(body) != systemguide.Skill {
				t.Fatalf("custom=%v: mandatory canonical guide missing or changed: %v", custom, err)
			}
			if custom {
				body, err = os.ReadFile(filepath.Join(GeneratedSkillsDir(dir), "owner-skill", "SKILL.md"))
				if err != nil || string(body) != "owner content" {
					t.Fatal("owner skill not preserved")
				}
			}
		}
	}
}

func TestSystemGuideReachesEffectiveViewInBothModes(t *testing.T) {
	for _, mode := range []string{"", "managed"} {
		s := validSettings()
		s.CapabilityMode, s.GlobalSkillsDir = mode, ""
		dir := t.TempDir()
		if _, err := MaterializeEffectiveSkills(dir, s); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(GeneratedEffectiveSkillsDir(dir), "hermes-hub-system-guide", "SKILL.md"))
		if err != nil || string(body) != systemguide.Skill {
			t.Fatalf("mode=%q: platform guide missing from the effective view: %v", mode, err)
		}
	}
}

func TestSystemGuideLoaderDisabledWithoutCustomCatalog(t *testing.T) {
	s := validSettings()
	s.GlobalSkillsDir = ""
	// SPEC-0083 R2.6: no external_dirs in either mode — the effective view
	// mounts read-only at /skills instead of feeding the upstream loader.
	if len(Config(s)["skills"].(M)) != 0 {
		t.Fatal("unmanaged native skill loader enabled")
	}
	s.CapabilityMode = "managed"
	if len(Config(s)["skills"].(M)) != 0 {
		t.Fatal("managed native skill loader enabled")
	}
}

func TestSystemGuideContractWithoutPersona(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSoulEffective(dir, Settings{Environment: "prod"}); err != nil {
		t.Fatalf("legacy context without persona must still receive system contract: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "generated", "soul-effective.prod.md"))
	if err != nil || !strings.Contains(string(body), "hermes-hub-system-guide") {
		t.Fatal("missing mandatory contract")
	}
	if _, err := os.Stat(filepath.Join(dir, "SOUL.md")); !os.IsNotExist(err) {
		t.Fatal("generated contract must not invent an owner persona")
	}
}

func TestSystemGuideSurvivesUnavailableOrSelfReferencingCatalog(t *testing.T) {
	for _, catalog := range []string{"missing", "generated"} {
		dir := t.TempDir()
		s := validSettings()
		s.GlobalSkillsDir = filepath.Join(dir, "absent-catalog")
		if catalog == "generated" {
			s.GlobalSkillsDir = GeneratedSkillsDir(dir)
		}
		err := MaterializeGlobalSkills(dir, s)
		if catalog == "missing" && err == nil {
			t.Fatal("missing optional catalog error was suppressed")
		}
		if catalog == "generated" && err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(GeneratedSkillsDir(dir), "hermes-hub-system-guide", "SKILL.md"))
		if err != nil || string(body) != systemguide.Skill {
			t.Errorf("catalog=%s: required reference unavailable: %v", catalog, err)
		}
	}
}

func TestSystemGuideOnlyRemainsAfterOptionalCatalogIsRemoved(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	optional := filepath.Join(s.GlobalSkillsDir, "optional-skill")
	if err := os.MkdirAll(optional, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(optional, "SKILL.md"), []byte("optional content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeGlobalSkills(dir, s); err != nil {
		t.Fatal(err)
	}
	s.GlobalSkillsDir = ""
	if err := MaterializeGlobalSkills(dir, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(GeneratedSkillsDir(dir), "optional-skill")); !os.IsNotExist(err) {
		t.Fatal("removed catalog left optional skills loadable")
	}
	if _, err := os.Stat(filepath.Join(GeneratedSkillsDir(dir), "hermes-hub-system-guide", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
