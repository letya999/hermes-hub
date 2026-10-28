package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
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

// R6.1: render stamps eligibility — a skill requiring an ungranted toolset
// or connector lands eligible:false with the missing piece named; a granted
// toolset keeps it eligible.
func TestMaterializeEffectiveSkillsStampsReadiness(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	skillBody := "---\nname: needs-web\ndescription: d\nplatforms: [linux]\nrequires:\n  toolsets: [web]\n  connectors: [gitlab]\n---\nbody\n"
	if err := os.MkdirAll(filepath.Join(dir, "skills", "needs-web"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "needs-web", "SKILL.md"), []byte(skillBody), 0644); err != nil {
		t.Fatal(err)
	}
	byName := func(idx *skillstore.EffectiveIndex) skillstore.Entry {
		for _, e := range idx.Skills {
			if e.Name == "needs-web" {
				return e
			}
		}
		return skillstore.Entry{}
	}
	idx, err := MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if byName(idx).Eligible {
		t.Fatalf("unmet requires must be ineligible: %+v", byName(idx))
	}
	if len(byName(idx).Missing) != 2 {
		t.Fatalf("missing toolset+connector: %+v", byName(idx).Missing)
	}
	// index.json carries the stamped readiness too.
	body, err := os.ReadFile(filepath.Join(GeneratedEffectiveSkillsDir(dir), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk struct {
		Skills []struct {
			Name     string `json:"name"`
			Eligible bool   `json:"eligible"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(body, &disk); err != nil {
		t.Fatalf("index.json: %v", err)
	}
	for _, e := range disk.Skills {
		if e.Name == "needs-web" && e.Eligible {
			t.Fatal("stamped index.json must persist ineligible")
		}
	}
	// A granted carve-out satisfies the toolset half.
	if s.Tools == nil {
		s.Tools = map[string]ToolEntry{}
	}
	s.Tools["web"] = ToolEntry{Via: ToolViaNative}
	idx, err = MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if byName(idx).Eligible || len(byName(idx).Missing) != 1 || byName(idx).Missing[0] != "connector:gitlab" {
		t.Fatalf("granted toolset narrows missing to the connector: %+v", byName(idx).Missing)
	}
	// A configured mcp: server satisfies the connector requirement.
	s.MCP = map[string]MCPServer{"gitlab": {}}
	idx, err = MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if !byName(idx).Eligible || len(byName(idx).Missing) != 0 {
		t.Fatalf("granted toolset+connector must be eligible: %+v", byName(idx))
	}
}

// Stored .state credentials satisfy declared credential names at render:
// a missing hard credential is the eligibility veto R8.5 requires; a stored
// name satisfies without the value ever entering the index.
func TestMaterializeEffectiveSkillsStampsCreds(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	body := "---\nname: needy\ndescription: d\nrequires:\n  credentials:\n    - {name: API_KEY, hard: true}\n    - {name: FALLBACK_TOKEN}\n---\nbody\n"
	if err := os.MkdirAll(filepath.Join(dir, "skills", "needy"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "needy", "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	idx, err := MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	var e skillstore.Entry
	for _, it := range idx.Skills {
		if it.Name == "needy" {
			e = it
		}
	}
	if e.Eligible || !slices.Contains(e.Missing, "credential:API_KEY") || !slices.Contains(e.CredsMissing, "FALLBACK_TOKEN") {
		t.Fatalf("missing creds: %+v", e)
	}
	// Storing the hard name flips the entry to eligible-with-setup-needed.
	if err := os.MkdirAll(filepath.Join(dir, "skills", ".state", "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", ".state", "credentials", "needy.json"), []byte(`{"API_KEY":"v"}`), 0600); err != nil {
		t.Fatal(err)
	}
	idx, err = MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range idx.Skills {
		if it.Name == "needy" {
			e = it
		}
	}
	if !e.Eligible || !slices.Contains(e.CredsMissing, "FALLBACK_TOKEN") {
		t.Fatalf("stored hard cred must satisfy but soft stays signaled: %+v", e)
	}
}

// Declared deps are a readiness signal: unmet schemes (registry not
// operator-allowed, no toolbox for system:) report deps_missing without
// making the skill catalog-ineligible (R7.4/R7.5).
func TestMaterializeEffectiveSkillsStampsDepsMissing(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	s.CLIRegistries = []string{"pypi"}
	body := "---\nname: depful\ndescription: d\nmetadata:\n  deps: [pip:requests, npm:left-pad, system:ffmpeg]\n---\nbody\n"
	if err := os.MkdirAll(filepath.Join(dir, "skills", "depful"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "depful", "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	idx, err := MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	var e skillstore.Entry
	for _, it := range idx.Skills {
		if it.Name == "depful" {
			e = it
		}
	}
	if !e.Eligible {
		t.Fatalf("deps are a signal, not eligibility: %+v", e)
	}
	if !slices.Equal(e.DepsMissing, []string{"npm:left-pad", "system:ffmpeg"}) {
		t.Fatalf("deps_missing = %v", e.DepsMissing)
	}
	// Operator-allowed npm + a toolbox satisfy the rest.
	s.CLIRegistries = append(s.CLIRegistries, "npm")
	s.CLIToolboxes = map[string][]string{"base": {"img@sha256:abc"}}
	idx, err = MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range idx.Skills {
		if it.Name == "depful" && len(it.DepsMissing) != 0 {
			t.Fatalf("allowed registries+toolbox clear deps_missing: %v", it.DepsMissing)
		}
	}
}

// A script-bearing skill reports instructions_only unless the caller holds an
// exec grant — the terminal toolset carveout is that surface (R7.5).
func TestMaterializeEffectiveSkillsStampsInstructionsOnly(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	root := filepath.Join(dir, "skills", "scripty")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"SKILL.md":     "---\nname: scripty\ndescription: d\n---\nbody\n",
		"scripts/r.sh": "echo hi",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	byName := func(idx *skillstore.EffectiveIndex) skillstore.Entry {
		for _, e := range idx.Skills {
			if e.Name == "scripty" {
				return e
			}
		}
		return skillstore.Entry{}
	}
	if e := byName(idx); !e.HasScripts || !e.InstructionsOnly {
		t.Fatalf("no exec grant -> instructions_only: %+v", e)
	}
	s.Tools = map[string]ToolEntry{"terminal": {Via: ToolViaNative}}
	idx, err = MaterializeEffectiveSkills(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if e := byName(idx); !e.HasScripts || e.InstructionsOnly {
		t.Fatalf("terminal grant clears instructions_only: %+v", e)
	}
}

// soul-effective carries a tier-0 <available_skills> block of the eligible,
// implicit-visible catalog: hidden and ineligible skills stay out, markup in
// descriptions is escaped (R6.2/R6.6).
func TestSoulEffectiveCarriesTier0Catalog(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	writeStoreSkill(t, filepath.Join(dir, "skills"), "demo", "does <script>alert(1)</script> things")
	writeStoreSkill(t, filepath.Join(dir, "skills"), "hidden-one", "invisible")
	writeStoreSkill(t, filepath.Join(dir, "skills"), "needy", "wants gitlab")
	for name, fm := range map[string]string{
		"hidden-one": "---\nname: hidden-one\ndescription: invisible\ndisable-model-invocation: true\n---\n\nbody\n",
		"needy":      "---\nname: needy\ndescription: wants gitlab\nrequires:\n  connectors: [gitlab]\n---\n\nbody\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, "skills", name, "SKILL.md"), []byte(fm), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := MaterializeEffectiveSkills(dir, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("# persona\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSoulEffective(dir, s); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "generated", "soul-effective."+s.Environment+".md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "<available_skills") {
		t.Fatal("tier-0 catalog block missing")
	}
	for _, want := range []string{"demo", "revision="} {
		if !strings.Contains(text, want) {
			t.Fatalf("tier-0 block missing %q", want)
		}
	}
	for _, banned := range []string{"hidden-one", "needy", "<script>", "alert(1)</script>"} {
		if strings.Contains(text, banned) {
			t.Fatalf("tier-0 must exclude/escape %q", banned)
		}
	}
}

// Catalog descriptions are length-capped so a fat manifest cannot bloat or
// break the tier-0 block (R6.6).
func TestSoulEffectiveTier0CapsDescription(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	writeStoreSkill(t, filepath.Join(dir, "skills"), "fat", strings.Repeat("x", 900)+"ENDMARKER")
	if _, err := MaterializeEffectiveSkills(dir, s); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSoulEffective(dir, s); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "generated", "soul-effective."+s.Environment+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "ENDMARKER") {
		t.Fatal("over-long description must be truncated in the tier-0 block")
	}
}

// With more than ~20 skills the tier-0 block collapses to names and a
// skills_search hint instead of full descriptions (R6.2).
func TestSoulEffectiveTier0LargeCatalogDegradesToNames(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = t.TempDir()
	for i := 0; i < 22; i++ {
		writeStoreSkill(t, filepath.Join(dir, "skills"), "skill-"+string(rune('a'+i)), "unique-long-description-token-"+string(rune('a'+i)))
	}
	if _, err := MaterializeEffectiveSkills(dir, s); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSoulEffective(dir, s); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "generated", "soul-effective."+s.Environment+".md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "skills_search") {
		t.Fatal("large catalog must point at skills_search")
	}
	if strings.Contains(text, "unique-long-description-token-a") {
		t.Fatal("degraded catalog must drop descriptions")
	}
	if !strings.Contains(text, "skill-a") {
		t.Fatal("degraded catalog must still list names")
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

// R5.1: the runtime gets a writable owner store at /skills-store plus the
// env contract telling the executor which path is which — same in both modes.
func TestComposeMountsWritableSkillsStore(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	svc := RuntimeService(s, "", dir)
	var storeMount map[string]any
	for _, volume := range svc["volumes"].([]any) {
		m, ok := volume.(M)
		if !ok {
			continue
		}
		if m["target"] == "/skills-store" {
			storeMount = map[string]any{"source": m["source"], "read_only": m["read_only"]}
		}
	}
	if storeMount == nil {
		t.Fatal("owner skills store not mounted at /skills-store")
	}
	if storeMount["read_only"] == true {
		t.Fatal("owner skills store must be writable")
	}
	if storeMount["source"] != filepath.ToSlash(filepath.Join(dir, "skills")) {
		t.Fatalf("/skills-store must bind the space store, got %v", storeMount["source"])
	}
	env := svc["environment"].(M)
	if env["HUB_SKILLS_STORE"] != "/skills-store" || env["HUB_SKILLS_EFFECTIVE"] != "/skills" {
		t.Fatalf("skills env contract missing: %v", env)
	}
	// Managed mode renders the same contract.
	s.CapabilityMode = "managed"
	s.ExecutionMode = "supervisor"
	menv := RuntimeService(s, "", dir)["environment"].(M)
	if menv["HUB_SKILLS_STORE"] != "/skills-store" {
		t.Fatal("managed compose missing HUB_SKILLS_STORE")
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

// R5.2: the skills read/mutate split is a grantable section pair and maps
// `via: toolhub` entries onto the executor capability families.
func TestSkillGovernanceSections(t *testing.T) {
	inventory := map[string]api.ToolSection{}
	for _, section := range api.SeedInventory() {
		inventory[section.ID] = section
	}
	read, ok := inventory["hub:skills"]
	if !ok || read.Default != api.RuleAllow || !read.Official {
		t.Fatalf("hub:skills section missing or wrong: %+v", read)
	}
	manage, ok := inventory["hub:skills_manage"]
	if !ok || manage.Default != api.RuleAllow || !manage.Official {
		t.Fatalf("hub:skills_manage section missing or wrong: %+v", manage)
	}
	if ToolHubCapabilityID("skills") != "skills" || ToolHubCapabilityID("skills_manage") != "skills_manage" {
		t.Fatal("toolhub capability map missing skills families")
	}
}

// The toolhub container mounts the effective view read-only so the managed
// projection can inject the eligible-names enum into skill.name (R5.1).
func TestToolhubSeesEffectiveIndexForSkillEnum(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.CapabilityMode = "managed"
	s.ExecutionMode = "supervisor"
	services := Compose(s, "", dir)["services"].(M)
	toolhub, ok := services["toolhub"].(M)
	if !ok {
		t.Fatal("toolhub service missing")
	}
	if toolhub["environment"].(M)["HUB_SKILLS_EFFECTIVE"] == "" {
		t.Fatal("toolhub missing HUB_SKILLS_EFFECTIVE")
	}
	found := false
	for _, volume := range toolhub["volumes"].([]any) {
		m, ok := volume.(M)
		if ok && m["target"] == toolhub["environment"].(M)["HUB_SKILLS_EFFECTIVE"] {
			found = true
			if m["read_only"] != true {
				t.Fatal("effective view must be read-only inside toolhub")
			}
			if m["source"] != filepath.ToSlash(filepath.Join(dir, "generated", "skills-effective")) {
				t.Fatalf("effective mount must bind generated/skills-effective, got %v", m["source"])
			}
		}
	}
	if !found {
		t.Fatal("toolhub does not mount the effective skills view")
	}
}

// The toolhub container mounts the owner skill store read-only so the
// sandbox exec path can resolve (principal, skill) credential leases from
// .state/credentials for exactly one call (R8.3).
func TestToolhubMountsSkillStoreForLeaseResolution(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.CapabilityMode = "managed"
	s.ExecutionMode = "supervisor"
	services := Compose(s, "", dir)["services"].(M)
	toolhub, ok := services["toolhub"].(M)
	if !ok {
		t.Fatal("toolhub service missing")
	}
	if toolhub["environment"].(M)["HUB_SKILLS_STORE"] != "/skills-store" {
		t.Fatal("toolhub missing HUB_SKILLS_STORE")
	}
	found := false
	for _, volume := range toolhub["volumes"].([]any) {
		m, ok := volume.(M)
		if ok && m["target"] == "/skills-store" {
			found = true
			if m["read_only"] != true {
				t.Fatal("skill store must be read-only inside toolhub")
			}
			if m["source"] != filepath.ToSlash(filepath.Join(dir, "skills")) {
				t.Fatalf("skill store must bind spaces/<user>/skills, got %v", m["source"])
			}
		}
	}
	if !found {
		t.Fatal("toolhub does not mount the owner skill store")
	}
}

// The communication gateway mounts the effective view read-only so `/name`
// and `$name` harness expansion can resolve skills (R6.5).
func TestGatewaySeesEffectiveViewForSkillExpansion(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.Ingress = []string{"telegram"}
	services := Compose(s, "", dir)["services"].(M)
	gateway, ok := services["communication-hub"].(M)
	if !ok {
		t.Fatal("communication-hub service missing")
	}
	env, _ := gateway["environment"].(M)
	if env["HUB_SKILLS_EFFECTIVE"] == "" {
		t.Fatal("gateway missing HUB_SKILLS_EFFECTIVE")
	}
	found := false
	for _, volume := range gateway["volumes"].([]any) {
		m, ok := volume.(M)
		if ok && m["target"] == env["HUB_SKILLS_EFFECTIVE"] {
			found = true
			if m["read_only"] != true {
				t.Fatal("effective view must be read-only inside the gateway")
			}
			if m["source"] != filepath.ToSlash(filepath.Join(dir, "generated", "skills-effective")) {
				t.Fatalf("effective mount must bind generated/skills-effective, got %v", m["source"])
			}
		}
	}
	if !found {
		t.Fatal("gateway does not mount the effective skills view")
	}
}

// The communication gateway mounts the owner skill store writable so
// skill-scoped credential forms persist under .state/credentials (R8.2) —
// the only writable mount it gets beyond its data volume.
func TestGatewayMountsSkillStoreForCredentialForms(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.Ingress = []string{"telegram"}
	services := Compose(s, "", dir)["services"].(M)
	gateway, ok := services["communication-hub"].(M)
	if !ok {
		t.Fatal("communication-hub service missing")
	}
	env, _ := gateway["environment"].(M)
	if env["HUB_SKILLS_STORE"] != "/skills-store" {
		t.Fatal("gateway missing HUB_SKILLS_STORE")
	}
	found := false
	for _, volume := range gateway["volumes"].([]any) {
		m, ok := volume.(M)
		if ok && m["target"] == "/skills-store" {
			found = true
			if m["source"] != filepath.ToSlash(filepath.Join(dir, "skills")) {
				t.Fatalf("skill store must bind spaces/<user>/skills, got %v", m["source"])
			}
		}
	}
	if !found {
		t.Fatal("gateway does not mount the owner skill store")
	}
}

// Org-bound spaces mount the writable org store beside the user store so
// propose_to_org can land a draft there (SPEC-0083 R5.2); the mount appears
// only when the space is org-scoped.
func TestComposeMountsOrgSkillsStoreWhenOrgScoped(t *testing.T) {
	dir := t.TempDir()
	s := validSettings()
	s.Organization = "acme"
	s.OrganizationDir = t.TempDir()
	s.OrganizationSkillsDir = filepath.Join(s.OrganizationDir, "skills")
	s.OrganizationRole = "admin"
	var storeMount M
	for _, volume := range RuntimeService(s, "", dir)["volumes"].([]any) {
		if m, ok := volume.(M); ok && m["target"] == "/skills-store-org" {
			storeMount = m
		}
	}
	if storeMount == nil {
		t.Fatal("org-bound runtime must mount the org skills store")
	}
	if storeMount["read_only"] == true {
		t.Fatal("org skills store must be writable for org drafts")
	}
	if storeMount["source"] != filepath.ToSlash(s.OrganizationSkillsDir) {
		t.Fatalf("/skills-store-org must bind the org store, got %v", storeMount["source"])
	}
	if RuntimeService(s, "", dir)["environment"].(M)["HUB_SKILLS_ORG"] != "/skills-store-org" {
		t.Fatal("runtime env missing HUB_SKILLS_ORG")
	}
	// R5.4: org publish authorization needs the bound member's role inside
	// the runtime — without it skill-apply must refuse org mutations.
	if RuntimeService(s, "", dir)["environment"].(M)["HUB_ORGANIZATION_ROLE"] != "admin" {
		t.Fatal("runtime env missing HUB_ORGANIZATION_ROLE")
	}
	// A non-org space must not see the mount or the role at all.
	s2 := validSettings()
	if _, ok := RuntimeService(s2, "", dir)["environment"].(M)["HUB_ORGANIZATION_ROLE"]; ok {
		t.Fatal("non-org space must not carry an org role")
	}
	for _, volume := range RuntimeService(s2, "", dir)["volumes"].([]any) {
		if m, ok := volume.(M); ok && m["target"] == "/skills-store-org" {
			t.Fatal("non-org space must not mount an org store")
		}
	}
}
