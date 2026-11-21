package stack

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

// R9.1: extends chains resolve leaf→root with org override per level; a
// level's passports/<name>.yaml overrides the parent level per passport, and
// the embedded passport templates are the final fallback.
func TestResolveProfileExtendsChainAndOrgOverride(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "alice")
	orgDir := filepath.Join(root, "acme")

	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "default"), "profile.yaml", "description: base\nversion: 1\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "default", "passports"), "tools.yaml", "kind: tools\n# platform-default-tools\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "default", "passports"), "workspace.yaml", "kind: workspace\n# platform-default-workspace\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "analyst"), "profile.yaml", "description: analyst\nextends: default\nversion: 2\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "analyst", "passports"), "workspace.yaml", "kind: workspace\n# platform-analyst-workspace\n")
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "analyst"), "SOUL.md", "# analyst persona\n")

	// Org profile of the same name overrides the platform level; its own
	// extends still chains into the platform default for missing files.
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst"), "profile.yaml", "description: org analyst\nextends: default\nversion: 3\n")
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst", "passports"), "workspace.yaml", "kind: workspace\n# org-analyst-workspace\n")

	p, err := resolveProvisioningProfile(space, orgDir, "analyst")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "analyst" || p.Version != 3 {
		t.Fatalf("meta: %+v", p)
	}
	if string(p.Passports["workspace"]) != "kind: workspace\n# org-analyst-workspace\n" {
		t.Fatalf("workspace passport: %q", p.Passports["workspace"])
	}
	if string(p.Passports["tools"]) != "kind: tools\n# platform-default-tools\n" {
		t.Fatalf("tools passport: %q", p.Passports["tools"])
	}
	if string(p.Soul) != "# analyst persona\n" {
		t.Fatalf("soul: %q", p.Soul)
	}

	// Bare root: embedded passport templates remain the last fallback.
	p3, err := resolveProvisioningProfile(filepath.Join(t.TempDir(), "spaces", "c"), "", "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		if len(p3.Passports[name]) == 0 {
			t.Fatalf("embedded fallback missing passport %s", name)
		}
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
	orgDocs := func(user string) map[string]string {
		d := managedDocs(user)
		d["stack"] = strings.Replace(d["stack"], "identity: {user: "+user, "identity: {user: "+user+", organization: acme", 1)
		return d
	}
	publishManagedDocs(t, space, "alice", "r-0000000000000001", orgDocs("alice"))
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
	s, err := ReadEnvironment(context.Background(), testReader(space), space, "prod")
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
	publishManagedDocs(t, bobSpace, "bob", "r-0000000000000001", orgDocs("bob"))
	s2, err := ReadEnvironment(context.Background(), testReader(bobSpace), bobSpace, "prod")
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
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst", "passports"), "workspace.yaml", "kind: workspace\nwritable: [/workspace]\n# analyst-wishes\n")
	o, _ := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	o.DefaultProfile = "analyst"
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), o)

	if err := InitEnvironmentWithOrganization(space, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, space, "alice", "r-0000000000000001")
	workspace, _ := os.ReadFile(filepath.Join(space, "passports", "workspace.yaml"))
	if string(workspace) != "kind: workspace\nwritable: [/workspace]\n# analyst-wishes\n" {
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
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "analyst", "passports"), "workspace.yaml", "kind: workspace\nwritable: [/workspace]\n# analyst-wishes\n")
	writeProfileFile(t, filepath.Join(orgDir, "profiles", "lead", "passports"), "workspace.yaml", "kind: workspace\nwritable: [/workspace]\n# lead-wishes\n")
	o, _ := ReadOrganization(filepath.Join(orgDir, "settings.yaml"))
	o.DefaultProfile = "analyst"
	o.Members["bob"] = OrgMember{Role: "member", Profile: "lead"}
	saveYAML(t, filepath.Join(orgDir, "settings.yaml"), o)

	if err := InitEnvironmentWithOrganization(space, "bob", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, space, "bob", "r-0000000000000001")
	workspace, _ := os.ReadFile(filepath.Join(space, "passports", "workspace.yaml"))
	if string(workspace) != "kind: workspace\nwritable: [/workspace]\n# lead-wishes\n" {
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
	writeProfileFile(t, filepath.Join(root, "policies", "profiles", "dev", "passports"), "workspace.yaml", "kind: workspace\nwritable: [/workspace]\n# dev-wishes\n")
	if err := InitEnvironmentProvision(space, "carol", "prod", "dev"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, space, "carol", "r-0000000000000001")
	workspace, _ := os.ReadFile(filepath.Join(space, "passports", "workspace.yaml"))
	if string(workspace) != "kind: workspace\nwritable: [/workspace]\n# dev-wishes\n" {
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
	publishManagedRevision(t, space, "dave", "r-0000000000000001")
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

// R4.1: provisioning materializes the approved workspace tree — inbox split
// by source, work, projects, artifacts branches and outbox — and never
// disturbs content the user already placed there (nested Git projects live
// under workspace/projects).
func TestInitMaterializesWorkspaceTree(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "alice")
	dir := filepath.Join(root, "policies", "profiles", "dev")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := InitEnvironmentProvision(space, "alice", "prod", "dev"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, space, "alice", "r-0000000000000001")
	publishManagedRevision(t, space, "alice", "r-0123456789abcdef")
	for _, rel := range []string{
		"workspace/inbox/people", "workspace/inbox/services",
		"workspace/work", "workspace/projects",
		"workspace/artifacts/documents", "workspace/artifacts/images",
		"workspace/artifacts/videos", "workspace/artifacts/archives",
		"workspace/artifacts/services", "workspace/outbox",
	} {
		info, err := os.Stat(filepath.Join(space, rel))
		if err != nil || !info.IsDir() {
			t.Fatalf("workspace tree dir %q not created: %v", rel, err)
		}
	}

	// Re-rendering a space that already has a nested Git project and an
	// inbox original preserves both byte-for-byte.
	project := filepath.Join(space, "workspace", "projects", "app", ".git")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "HEAD"), []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	attachment := filepath.Join(space, "workspace", "inbox", "people", "att1", "files")
	if err := os.MkdirAll(attachment, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachment, "report.csv"), []byte("a,b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	// A legacy space without the new branches gets them on the next render
	// while its own content (Git project, inbox original) stays untouched.
	for _, rel := range []string{
		"workspace/inbox/services", "workspace/work", "workspace/outbox",
		"workspace/artifacts/videos", "workspace/artifacts/archives",
		"workspace/artifacts/services",
	} {
		if err := os.RemoveAll(filepath.Join(space, rel)); err != nil {
			t.Fatal(err)
		}
	}
	if err := RenderEnvironment(context.Background(), testReader(space), space, root, "prod"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"workspace/projects/app/.git/HEAD", "workspace/inbox/people/att1/files/report.csv",
		"workspace/inbox/services", "workspace/work", "workspace/outbox",
		"workspace/artifacts/videos", "workspace/artifacts/archives",
		"workspace/artifacts/services",
	} {
		if _, err := os.Stat(filepath.Join(space, rel)); err != nil {
			t.Fatalf("existing content %q lost or dir not restored: %v", rel, err)
		}
	}

	// A pre-R4 space has no inbox or projects branches at all: render
	// recreates them wholesale.
	for _, rel := range []string{"workspace/inbox", "workspace/projects"} {
		if err := os.RemoveAll(filepath.Join(space, rel)); err != nil {
			t.Fatal(err)
		}
	}
	if err := RenderEnvironment(context.Background(), testReader(space), space, root, "prod"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"workspace/inbox/people", "workspace/inbox/services", "workspace/projects"} {
		if info, err := os.Stat(filepath.Join(space, rel)); err != nil || !info.IsDir() {
			t.Fatalf("legacy branch %q not restored: %v", rel, err)
		}
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
	publishOrgRevision(t, space, "alice", "acme", "r-0000000000000001")
	if err := os.WriteFile(filepath.Join(space, "SOUL.md"), []byte("# alice notes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(context.Background(), testReader(space), space, "prod")
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
	publishManagedRevision(t, space, "alice", "r-0000000000000001")
	s, err := ReadEnvironment(context.Background(), testReader(space), space, "prod")
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
	publishManagedRevision(t, space, "alice", "r-0000000000000001")
	s, err := ReadEnvironment(context.Background(), testReader(space), space, "prod")
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

// SyncOrganizationMembers treats a space with authored passports as
// existing — the retired scope.yaml marker is gone after the cutover.
func TestSyncOrganizationMembersDetectsExistingSpace(t *testing.T) {
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	spacesRoot := filepath.Join(root, "spaces")
	if err := InitOrganization(orgDir, "acme", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := InitEnvironmentWithOrganization(filepath.Join(spacesRoot, "alice"), "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	report, err := SyncOrganizationMembers(orgDir, "acme", spacesRoot, "prod", true)
	if err != nil {
		t.Fatal(err)
	}
	existing, _ := report["existing"].([]string)
	if !slices.Contains(existing, "alice") {
		t.Fatalf("initialized space not reported as existing: %v", report)
	}
	if created, _ := report["created"].([]string); slices.Contains(created, "alice") {
		t.Fatalf("existing space re-provisioned: %v", report)
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
	stamp := time.Unix(1_700_000_000, 0)
	for _, name := range []string{"scope.yaml", "settings.yaml"} {
		if err := os.Chtimes(filepath.Join(orgDir, name), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
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
	publishOrgRevision(t, space, "alice", "acme", "r-0000000000000001")
	// alice rewrites her lock to claim the admin profile.
	if err := os.WriteFile(filepath.Join(space, "profile.lock"), []byte("profile: admin\nversion: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(context.Background(), testReader(space), space, "prod")
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
	writeProfileFile(t, filepath.Join(profileDir, "passports"), "workspace.yaml", "writable: [/workspace]\nread_only: [/config]\n# staff v1\n")
	if err := InitEnvironmentWithOrganization(space, "alice", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	publishOrgRevision(t, space, "alice", "acme", "r-0000000000000001")
	writeProfileFile(t, filepath.Join(profileDir, "passports"), "workspace.yaml", "writable: [/workspace]\nread_only: [/config]\n# staff v2\n")
	writeProfileFile(t, profileDir, "profile.yaml", "version: 2\n")
	report, err := UpdateProvisioningProfile(space, true)
	if err != nil {
		t.Fatal(err)
	}
	workspace, _ := os.ReadFile(filepath.Join(space, "passports", "workspace.yaml"))
	if string(workspace) != "writable: [/workspace]\nread_only: [/config]\n# staff v2\n" {
		t.Fatalf("org profile update not applied: %q report=%v", workspace, report)
	}
}
