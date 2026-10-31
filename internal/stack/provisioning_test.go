package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func writeProfileFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

// R9.1: extends chains resolve leaf→root with org override per level; the
// platform default falls back to legacy policies/*-default.yaml and then to
// the embedded fleet templates.
func TestResolveProfileExtendsChainAndOrgOverride(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "alice")
	orgDir := filepath.Join(root, "acme")

	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "default"), "profile.yaml", "description: base\nversion: 1\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "default"), "agent.yaml", "user: x\n# platform-default-agent\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "default"), "workspace.yaml", "tools: {}\n# platform-default-workspace\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "analyst"), "profile.yaml", "description: analyst\nextends: default\nversion: 2\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "analyst"), "workspace.yaml", "tools: {}\n# platform-analyst-workspace\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "analyst"), "SOUL.md", "# analyst persona\n")

	// Org profile of the same name overrides the platform level; its own
	// extends still chains into the platform default for missing files.
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst"), "profile.yaml", "description: org analyst\nextends: default\nversion: 3\n")
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst"), "workspace.yaml", "tools: {}\n# org-analyst-workspace\n")

	p, err := resolveProvisioningProfile(space, orgDir, "analyst")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "analyst" || p.Version != 3 {
		t.Fatalf("meta: %+v", p)
	}
	if string(p.Workspace) != "tools: {}\n# org-analyst-workspace\n" {
		t.Fatalf("workspace: %q", p.Workspace)
	}
	if string(p.Agent) != "user: x\n# platform-default-agent\n" {
		t.Fatalf("agent: %q", p.Agent)
	}
	if string(p.Soul) != "# analyst persona\n" {
		t.Fatalf("soul: %q", p.Soul)
	}

	// Legacy fleet override still wins for the default profile's agent.yaml
	// when no profiles/default dir provides one.
	root2 := t.TempDir()
	space2 := filepath.Join(root2, "spaces", "bob")
	writeProfileFile(t, filepath.Join(root2, "policies"), "agent-default.yaml", "user: x\n# legacy-agent\n")
	writeProfileFile(t, filepath.Join(root2, "policies"), "workspace-default.yaml", "tools: {}\n# legacy-workspace\n")
	p2, err := resolveProvisioningProfile(space2, "", "default")
	if err != nil {
		t.Fatal(err)
	}
	if string(p2.Agent) != "user: x\n# legacy-agent\n" || string(p2.Workspace) != "tools: {}\n# legacy-workspace\n" {
		t.Fatalf("legacy fallback: agent=%q workspace=%q", p2.Agent, p2.Workspace)
	}

	// Bare root: embedded defaults remain the last fallback.
	p3, err := resolveProvisioningProfile(filepath.Join(t.TempDir(), "spaces", "c"), "", "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(p3.Agent) == 0 || len(p3.Workspace) == 0 {
		t.Fatal("embedded fallback missing")
	}
}

// R9.1 safety: profile names are identifiers; extends cycles fail closed.
func TestResolveProfileRejectsTraversalAndCycles(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "alice")
	if _, err := resolveProvisioningProfile(space, "", "../x"); err == nil {
		t.Fatal("traversal profile name accepted")
	}
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "aa"), "profile.yaml", "extends: bb\nversion: 1\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "bb"), "profile.yaml", "extends: aa\nversion: 1\n")
	if _, err := resolveProvisioningProfile(space, "", "aa"); err == nil {
		t.Fatal("extends cycle accepted")
	}
}

// R9.2: members accept the legacy scalar role or {role, profile}; the org
// default_profile fills members without an explicit profile, and the
// resolved assignment reaches Settings.OrganizationProfile.
func TestMembersLegacyScalarAndMapForms(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	space := filepath.Join(root, "spaces", "alice")
	if err := InitOrganization(orgDir, "acme", ""); err != nil {
		t.Fatal(err)
	}
	if err := InitEnvironmentWithOrganization(space, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	o, err := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), map[string]any{
		"schema": 1, "organization": "acme",
		"default_profile": "analyst",
		"members": map[string]any{
			"alice": "member",
			"bob":   map[string]any{"role": "admin", "profile": "lead"},
		},
		"features": o.Features,
	})
	s, err := ReadEnvironment(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if s.OrganizationProfile != "analyst" {
		t.Fatalf("default profile not applied: %q", s.OrganizationProfile)
	}
	// Member-level profile wins over the org default.
	bobSpace := filepath.Join(root, "spaces", "bob")
	if err := InitEnvironmentWithOrganization(bobSpace, "bob", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	s2, err := ReadEnvironment(bobSpace, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if s2.OrganizationProfile != "lead" {
		t.Fatalf("member profile not applied: %q", s2.OrganizationProfile)
	}
	o, _ = ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	if got := o.Members["bob"].Profile; got != "lead" {
		t.Fatalf("bob profile: %q", got)
	}
	if got := o.Members["alice"].Role; got != "member" {
		t.Fatalf("alice role: %q", got)
	}
	// Legacy scalar file round-trips scalar — no schema churn on read/write.
	raw, _ := os.ReadFile(filepath.Join(orgDir, "settings.yaml"))
	if !strings.Contains(string(raw), "alice: member") {
		t.Fatalf("scalar member form not preserved in fixture")
	}
}

// R9.3: init resolves the member's org-assigned profile, stamps its files
// and records the outcome in profile.lock.
func TestInitUsesOrgDefaultProfile(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	space := filepath.Join(root, "spaces", "alice")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst"), "profile.yaml", "description: analyst\nversion: 4\n")
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst"), "workspace.yaml", "tools: {}\n# analyst-wishes\n")
	o, _ := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	o.DefaultProfile = "analyst"
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), o)

	if err := InitEnvironmentWithOrganization(space, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	workspace, _ := os.ReadFile(filepath.Join(space, "workspace.yaml"))
	if string(workspace) != "tools: {}\n# analyst-wishes\n" {
		t.Fatalf("profile workspace not stamped: %q", workspace)
	}
	lock, err := os.ReadFile(filepath.Join(space, "profile.lock"))
	if err != nil {
		t.Fatalf("profile.lock missing: %v", err)
	}
	if !strings.Contains(string(lock), "profile: analyst") || !strings.Contains(string(lock), "version: 4") || !strings.Contains(string(lock), "workspace.yaml:") {
		t.Fatalf("lock: %q", lock)
	}
}

// R9.3: a member's assigned profile wins over the org default at init.
func TestMemberProfileOverridesDefault(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	space := filepath.Join(root, "spaces", "bob")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst"), "workspace.yaml", "tools: {}\n# analyst-wishes\n")
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "lead"), "workspace.yaml", "tools: {}\n# lead-wishes\n")
	o, _ := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	o.DefaultProfile = "analyst"
	o.Members["bob"] = OrgMember{Role: "member", Profile: "lead"}
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), o)

	if err := InitEnvironmentWithOrganization(space, "bob", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	workspace, _ := os.ReadFile(filepath.Join(space, "workspace.yaml"))
	if string(workspace) != "tools: {}\n# lead-wishes\n" {
		t.Fatalf("member profile not stamped: %q", workspace)
	}
	lock, _ := os.ReadFile(filepath.Join(space, "profile.lock"))
	if !strings.Contains(string(lock), "profile: lead") {
		t.Fatalf("lock: %q", lock)
	}
}

// R9.3: `hubctl init --profile` stamps an explicit profile on a plain space.
func TestInitExplicitProfile(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "carol")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "dev"), "workspace.yaml", "tools: {}\n# dev-wishes\n")
	if err := InitEnvironmentProvision(space, "carol", "prod", "dev"); err != nil {
		t.Fatal(err)
	}
	workspace, _ := os.ReadFile(filepath.Join(space, "workspace.yaml"))
	if string(workspace) != "tools: {}\n# dev-wishes\n" {
		t.Fatalf("explicit profile not stamped: %q", workspace)
	}
	lock, _ := os.ReadFile(filepath.Join(space, "profile.lock"))
	if !strings.Contains(string(lock), "profile: dev") {
		t.Fatalf("lock: %q", lock)
	}
}

// R9.3: layout.yaml declares extra workspace dirs init creates; traversal
// and denied segments are rejected.
func TestInitProfileLayoutDirs(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "dave")
	dir := filepath.Join(root, "policies", "profiles", "dev")
	writeProfileFile(t, dir, "layout.yaml", "dirs: [workspace/notes, workspace/incoming]\n")
	writeProfileFile(t, dir, "workspace.yaml", "tools: {}\n# dev\n")
	if err := InitEnvironmentProvision(space, "dave", "prod", "dev"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"workspace/notes", "workspace/incoming"} {
		info, err := os.Stat(filepath.Join(space, rel))
		if err != nil || !info.IsDir() {
			t.Fatalf("layout dir %q not created: %v", rel, err)
		}
	}

	bad := filepath.Join(root, "spaces", "eve")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "evil"), "layout.yaml", "dirs: [../../escape]\n")
	if err := InitEnvironmentProvision(bad, "eve", "prod", "evil"); err == nil {
		t.Fatal("traversal layout dir accepted")
	}
}

// R9.4: the effective SOUL layers platform contract → org → profile → user.
func TestEffectiveSoulLayerOrder(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	space := filepath.Join(root, "spaces", "alice")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orgDir, "SOUL.md"), []byte("# acme org rules\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst"), "SOUL.md", "# analyst persona\n")
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst"), "profile.yaml", "version: 1\n")
	o, _ := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	o.DefaultProfile = "analyst"
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), o)
	if err := InitEnvironmentWithOrganization(space, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(space, "SOUL.md"), []byte("# alice notes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSoulEffective(space, s); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(space, "generated", "soul-effective.prod.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	contract := strings.Index(text, "Channel contract")
	org := strings.Index(text, "acme org rules")
	profile := strings.Index(text, "analyst persona")
	user := strings.Index(text, "alice notes")
	if contract < 0 || org < 0 || profile < 0 || user < 0 {
		t.Fatalf("missing layer(s): contract=%d org=%d profile=%d user=%d\n%s", contract, org, profile, user, text)
	}
	if !(contract < org && org < profile && profile < user) {
		t.Fatalf("layer order wrong: contract=%d org=%d profile=%d user=%d", contract, org, profile, user)
	}
}

// A plain (non-org) space layers its init-stamped profile SOUL the same way
// an org space layers its assignment.
func TestProfileSoulLayersForPlainSpace(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "alice")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "ops"), "SOUL.md", "# ops voice\n")
	if err := InitEnvironmentProvision(space, "alice", "prod", "ops"); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSoulEffective(space, s); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(space, "generated", "soul-effective.prod.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "ops voice") {
		t.Fatalf("stamped profile SOUL did not layer:\n%s", body)
	}
}

// R9.5: grants.yaml is a template of capability rules; it only becomes
// authority through the confirmed operator policy path and never past the
// organization ceiling.
func TestProfileGrantsMaterialize(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "alice")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "ops"), "grants.yaml", `rules:
  - capability_id: files
    implementation_digest: sha256:`+strings.Repeat("a", 64)+`
    action: read
    resource: workspace
    path_prefix: docs
    limits: {output_bytes: 1024, timeout_seconds: 5}
`)
	p, err := resolveProvisioningProfile(space, "", "ops")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := ProfileGrantRules(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].CapabilityID != "files" || rules[0].Action != "read" ||
		rules[0].PathPrefix != "docs" || rules[0].Limits.OutputBytes != 1024 {
		t.Fatalf("grants.yaml did not materialize: %+v", rules)
	}

	// Inside the ceiling and confirmed: valid. Past the ceiling or
	// unconfirmed: rejected — a profile grants nothing by itself.
	base := api.CapabilityPolicy{
		Schema: api.SchemaVersion, PolicyID: "acme-ops", Organization: "acme",
		Members: []string{"alice"}, Revision: 1, IssuedBy: "operator",
		IssuedAt: time.Now(), Reason: "profile ops defaults", Status: api.ActiveStatus,
		Defaults: rules,
	}
	wide := base
	wide.Ceiling = rules
	if err := api.Confirm(&wide, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := wide.Validate(); err != nil {
		t.Fatalf("ceiling-contained confirmed grants rejected: %v", err)
	}
	narrow := base
	narrow.Ceiling = []api.CapabilityRule{{CapabilityID: "files", ImplementationDigest: rules[0].ImplementationDigest,
		Action: "read", Resource: "workspace", PathPrefix: "docs/sub", Limits: api.CapabilityLimits{OutputBytes: 2048, TimeoutSeconds: 10}}}
	if err := api.Confirm(&narrow, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := narrow.Validate(); err == nil {
		t.Fatal("profile grants escaped the organization ceiling")
	}
	unconfirmed := wide
	unconfirmed.Confirmation = nil
	if err := unconfirmed.Validate(); err == nil {
		t.Fatal("grants became policy without operator confirmation")
	}
}

// R9.6: skills.yaml pins standard skills by reference — they order first in
// the effective catalog, and a reference to an ineligible skill fails the
// render instead of silently dropping.
func TestProfileSkillsPinAndEligibility(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "alice")
	profileDir := filepath.Join(root, "policies", "profiles", "ops")
	writeProfileFile(t, profileDir, "skills.yaml", "skills: [zeta, alpha]\n")

	catalog := t.TempDir()
	for name, body := range map[string]string{
		"alpha":  "---\nname: alpha\ndescription: a\n---\nalpha body\n",
		"zeta":   "---\nname: zeta\ndescription: z\n---\nzeta body\n",
		"locked": "---\nname: locked\ndescription: gated\nrequires:\n  credentials:\n    - name: LOCKED_TOKEN\n      hard: true\n---\nlocked body\n",
	} {
		dir := filepath.Join(catalog, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := InitEnvironmentProvision(space, "alice", "prod", "ops"); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.GlobalSkillsDir = catalog
	idx, err := MaterializeEffectiveSkills(space, s)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, e := range idx.Skills {
		if !e.Hidden {
			order = append(order, e.Name)
		}
	}
	if len(order) < 2 || order[0] != "zeta" || order[1] != "alpha" {
		t.Fatalf("pinned skills did not order first: %v", order)
	}

	// Pinning a skill the space cannot run is a render-time error.
	writeProfileFile(t, profileDir, "skills.yaml", "skills: [locked]\n")
	if _, err := MaterializeEffectiveSkills(space, s); err == nil {
		t.Fatal("ineligible pinned skill rendered silently")
	}
}

// Red-team: an org member cannot pull in a profile they were never assigned
// by editing their own profile.lock — the org assignment wins at render.
func TestOrgMemberCannotPullUnassignedProfile(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	space := filepath.Join(root, "spaces", "alice")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	o, err := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o.DefaultProfile = "staff"
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), o)
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "staff"), "skills.yaml", "skills: [public-doc]\n")
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "admin"), "skills.yaml", "skills: [admin-console]\n")

	catalog := t.TempDir()
	for name, body := range map[string]string{
		"public-doc":    "---\nname: public-doc\ndescription: p\n---\np\n",
		"admin-console": "---\nname: admin-console\ndescription: a\n---\na\n",
	} {
		dir := filepath.Join(catalog, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := InitEnvironmentWithOrganization(space, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	// alice rewrites her lock to claim the admin profile.
	if err := os.WriteFile(filepath.Join(space, "profile.lock"), []byte("profile: admin\nversion: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.GlobalSkillsDir = catalog
	idx, err := MaterializeEffectiveSkills(space, s)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Skills[0].Name != "public-doc" {
		t.Fatalf("org assignment lost to a forged lock: %v", idx.Skills[0].Name)
	}
}

// Org members apply profile updates from their organization overlay — the
// org copy shadows any platform profile of the same name.
func TestProfileApplyUsesOrgOverlay(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	space := filepath.Join(root, "spaces", "alice")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	o, err := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	o.DefaultProfile = "staff"
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), o)
	profileDir := filepath.Join(orgDir, "profiles", "staff")
	writeProfileFile(t, profileDir, "profile.yaml", "version: 1\n")
	writeProfileFile(t, profileDir, "workspace.yaml", "tools: {}\n# staff v1\n")
	if err := InitEnvironmentWithOrganization(space, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	writeProfileFile(t, profileDir, "workspace.yaml", "tools: {}\n# staff v2\n")
	writeProfileFile(t, profileDir, "profile.yaml", "version: 2\n")
	report, err := UpdateProvisioningProfile(space, true)
	if err != nil {
		t.Fatal(err)
	}
	workspace, _ := os.ReadFile(filepath.Join(space, "workspace.yaml"))
	if string(workspace) != "tools: {}\n# staff v2\n" {
		t.Fatalf("org profile update not applied: %q report=%v", workspace, report)
	}
}
