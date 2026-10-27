package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// writeStoreSkill drops a minimal valid skill into a store root.
func writeStoreSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializeEffectiveSkillsBuildsPrecedenceView(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	writeStoreSkill(t, s.GlobalSkillsDir, "platform-skill", "from platform")
	writeStoreSkill(t, filepath.Join(dir, "skills"), "user-skill", "from user")
	writeStoreSkill(t, filepath.Join(dir, "skills"), "platform-skill", "shadowed copy")
	if _, err := MaterializeEffectiveSkills(dir, s); err != nil {
		t.Fatal(err)
	}
	view := GeneratedEffectiveSkillsDir(dir)
	for _, name := range []string{"platform-skill", "user-skill"} {
		if _, err := os.Stat(filepath.Join(view, name, "SKILL.md")); err != nil {
			t.Fatalf("view missing %s: %v", name, err)
		}
	}
	var idx struct {
		Skills []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"skills"`
	}
	body, err := os.ReadFile(filepath.Join(view, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, e := range idx.Skills {
		sources[e.Name] = e.Source
	}
	if sources["platform-skill"] != "platform" {
		t.Fatalf("platform skill lost precedence to user copy: %#v", idx.Skills)
	}
	if sources["user-skill"] != "user" {
		t.Fatalf("user skill missing from index: %#v", idx.Skills)
	}
}

func TestMaterializeEffectiveSkillsOrgOverridesUser(t *testing.T) {
	dir := t.TempDir()
	orgDir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	s.Organization = "acme"
	s.OrganizationDir = orgDir
	writeStoreSkill(t, filepath.Join(orgDir, "skills"), "shared-skill", "from org")
	writeStoreSkill(t, filepath.Join(dir, "skills"), "shared-skill", "from user")
	idx, err := MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	winner := ""
	for _, e := range idx.Skills {
		if e.Name == "shared-skill" {
			winner = e.Source
		}
	}
	if winner != "org" {
		t.Fatalf("org skill must hide the user copy: %#v", idx.Skills)
	}
	body, err := os.ReadFile(filepath.Join(GeneratedEffectiveSkillsDir(dir), "shared-skill", "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "from org") {
		t.Fatalf("view must carry the org tree: %q %v", body, err)
	}
	if len(idx.Notices) == 0 || idx.Notices[0].Kind != "collision" {
		t.Fatalf("hidden user skill must record a collision notice: %#v", idx.Notices)
	}
}

func TestComposeMountsEffectiveViewReadOnly(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	var skillsMount map[string]any
	for _, volume := range RuntimeService(s, "", dir)["volumes"].([]any) {
		m, ok := volume.(M)
		if !ok {
			continue
		}
		if m["target"] == "/opt/hub/skills" {
			t.Fatal("legacy /opt/hub/skills mount must be gone")
		}
		if m["target"] == "/skills" {
			skillsMount = map[string]any{"source": m["source"], "read_only": m["read_only"]}
		}
	}
	if skillsMount == nil {
		t.Fatal("effective view not mounted at /skills")
	}
	if skillsMount["read_only"] != true {
		t.Fatal("effective view must mount read-only")
	}
	if skillsMount["source"] != filepath.ToSlash(GeneratedEffectiveSkillsDir(dir)) {
		t.Fatalf("/skills must bind generated/skills-effective, got %v", skillsMount["source"])
	}
}

func TestManagedComposeMountsEffectiveViewReadOnly(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.CapabilityMode = "managed"
	s.ExecutionMode = "supervisor"
	found := false
	for _, volume := range RuntimeService(s, "", dir)["volumes"].([]any) {
		m, ok := volume.(M)
		if !ok {
			continue
		}
		if m["target"] == "/opt/hub/skills" {
			t.Fatal("legacy /opt/hub/skills mount must be gone")
		}
		if m["target"] == "/skills" {
			found = true
			if m["read_only"] != true {
				t.Fatal("managed /skills must mount read-only")
			}
		}
	}
	if !found {
		t.Fatal("managed runtime missing the effective view mount")
	}
}

func TestComposeSealsHermesSkillsDirInBothModes(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{"", "managed"} {
		s := validSettings()
		s.CapabilityMode = mode
		if mode == "managed" {
			s.ExecutionMode = "supervisor"
		}
		sealed := false
		for _, tmpfs := range RuntimeService(s, "", dir)["tmpfs"].([]string) {
			if strings.HasPrefix(tmpfs, "/state/hermes/skills:") {
				sealed = true
				if !strings.Contains(tmpfs, "ro") {
					t.Fatalf("mode=%q: hermes skills overlay must be read-only: %s", mode, tmpfs)
				}
			}
		}
		if !sealed {
			t.Fatalf("mode=%q: /state/hermes/skills is not an empty read-only mount", mode)
		}
	}
}

func TestManagedRenderIncludesPlatformCatalogInView(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "config"), 0700)
	_ = os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("original"), 0600)
	writeStoreSkill(t, filepath.Join(root, "config", "skills"), "platform-skill", "from platform")
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	s, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	s.Model, s.ModelURL = "test", "http://model-relay:8318/v1"
	s.CapabilityMode, s.CapabilityProfileID, s.CapabilityGeneration = "managed", "p", 1
	s.ExecutionMode = "supervisor"
	s.SupervisorURL = "http://localhost:8876"
	s.Tools, s.Ingress = testTools("workspace"), testIngress("workspace")
	if err := saveSpace(d, s); err != nil {
		t.Fatal(err)
	}
	selection := `{"schema":1,"user":"me","environment":"prod","mode":"supervisor","supervisor_url":"http://localhost:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`
	if err := os.WriteFile(filepath.Join(d, ExecutionPath("prod")), []byte(selection), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := "OPENAI_API_KEY=model\n"
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte(secrets), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "generated", "skills-effective", "platform-skill", "SKILL.md")); err != nil {
		t.Fatalf("managed view missing platform catalog skill: %v", err)
	}
}

func TestOrganizationInitCreatesUnifiedSkillsStore(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(orgDir, "skills")); err != nil || !info.IsDir() {
		t.Fatalf("org init did not create the unified skills store: %v", err)
	}
	space := filepath.Join(root, "spaces", "alice")
	if err := InitEnvironmentWithOrganization(space, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if s.OrganizationSkillsDir != filepath.Join(orgDir, "skills") {
		t.Fatalf("org skills dir must point at the unified store, got %q", s.OrganizationSkillsDir)
	}
}

func TestRenderMigratesLegacyHermesSkillsIntoStore(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "config"), 0700)
	_ = os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("original"), 0600)
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	s, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	s.Model, s.ModelURL = "test", "http://model-relay:8318/v1"
	s.Tools, s.Ingress = testTools("workspace"), testIngress("workspace")
	if err := saveSpace(d, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Legacy layout: a consented skill, an unrecorded one, and a byte-identical
	// copy of a shipped platform skill that must be dropped, not migrated.
	writeStoreSkill(t, filepath.Join(d, "hermes", "skills"), "consented-skill", "legacy consented")
	writeStoreSkill(t, filepath.Join(d, "hermes", "skills"), "agent-skill", "unrecorded")
	writeStoreSkill(t, filepath.Join(root, "config", "skills"), "shipped", "platform original")
	writeStoreSkill(t, filepath.Join(d, "hermes", "skills"), "shipped", "platform original")
	registry := `{"schema":1,"user":"me","skills":[{"name":"consented-skill","consent":true}]}`
	if err := os.MkdirAll(filepath.Join(d, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "skills", "registry.json"), []byte(registry), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "skills", "consented-skill", "SKILL.md")); err != nil {
		t.Fatalf("consented legacy skill not published into the store: %v", err)
	}
	st, err := skillstore.Open(filepath.Join(d, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	drafts, err := st.Drafts()
	if err != nil || len(drafts) == 0 {
		t.Fatalf("unrecorded legacy skill did not land as a draft: %v", err)
	}
	for _, dr := range drafts {
		if dr.Name == "shipped" {
			t.Fatal("bundled copy entered the store as a draft")
		}
	}
	if _, err := os.Stat(filepath.Join(d, "skills", "shipped")); !os.IsNotExist(err) {
		t.Fatal("bundled copy entered the user store")
	}
}

func TestRenderMigratesLegacyOrgSkillsIntoOrgStore(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	writeStoreSkill(t, filepath.Join(orgDir, "hermes", "skills"), "org-legacy", "legacy org skill")
	d := filepath.Join(root, "spaces", "alice")
	if err := InitEnvironmentWithOrganization(d, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	s, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	s.Model, s.ModelURL = "test", "http://model-relay:8318/v1"
	s.Tools, s.Ingress = testTools("workspace"), testIngress("workspace")
	if err := saveSpace(d, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	_ = os.Mkdir(filepath.Join(repo, "config"), 0700)
	_ = os.WriteFile(filepath.Join(repo, "config", "SOUL.md"), []byte("original"), 0600)
	if err := RenderEnvironment(d, repo, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(orgDir, "skills", "org-legacy", "SKILL.md")); err != nil {
		t.Fatalf("legacy org skill not migrated into the org store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d, "generated", "skills-effective", "org-legacy", "SKILL.md")); err != nil {
		t.Fatalf("migrated org skill missing from the effective view: %v", err)
	}
}

func TestInitCreatesUnifiedSkillsStore(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(d, "skills")); err != nil || !info.IsDir() {
		t.Fatalf("init did not create the unified skills store: %v", err)
	}
}

func TestConfigRendersEmptySkillsSectionInBothModes(t *testing.T) {
	for _, mode := range []string{"", "managed"} {
		s := validSettings()
		s.CapabilityMode = mode
		s.OrganizationSkillsDir = t.TempDir() // legacy org source must not leak
		skills, ok := Config(s)["skills"].(M)
		if !ok {
			t.Fatalf("mode=%q: skills section missing", mode)
		}
		if len(skills) != 0 {
			t.Fatalf("mode=%q: skills must render empty (no external_dirs/disabled): %#v", mode, skills)
		}
	}
}
