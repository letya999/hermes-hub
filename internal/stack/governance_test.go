package stack

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// seedGovernance writes a valid governance document into the space's
// runtime/toolhub dir before the first Read, so no migration allow is
// recorded — the doc the test authors is the whole policy.
func seedGovernance(t *testing.T, dir string, mutate func(*api.Governance)) *api.Governance {
	t.Helper()
	doc := api.NewGovernance()
	if mutate != nil {
		mutate(doc)
	}
	path := filepath.Join(dir, "runtime", "toolhub", "governance.json")
	if err := api.SaveGovernance(path, doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func activeToolGrant(t *testing.T, doc *api.Governance, principal, section string, ttl time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	grant := api.NewToolGrant("grant-"+principal+"-"+strings.ReplaceAll(section, ":", "-"), principal, section, "test grant", "operator", now.Add(ttl), nil)
	if err := api.Confirm(&grant, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := doc.PutToolGrant(grant, now); err != nil {
		t.Fatal(err)
	}
	stamped, err := doc.RequestToolGrant(principal, grant.GrantID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.AcknowledgeToolGrant(principal, grant.GrantID, stamped.RequestNonce, now); err != nil {
		t.Fatal(err)
	}
}

// Before the first apply, the authored stack passport is the only infra
// claim a fresh space has — the peek must consult it so the sibling that
// renders the control plane is found before any publication exists.
func TestSpaceRendersInfraFromAuthoredPassport(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	for _, name := range []string{"infra", "member"} {
		if err := os.MkdirAll(filepath.Join(spaces, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := InitEnvironment(filepath.Join(spaces, "infra"), "infra", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := InitEnvironment(filepath.Join(spaces, "member"), "member", "prod"); err != nil {
		t.Fatal(err)
	}
	// The member's operator narrows the default infra:true stamp before the
	// first apply — a peer-space home shares the control plane.
	stackPath := filepath.Join(spaces, "member", "passports", "stack.yaml")
	body, err := os.ReadFile(stackPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stackPath, []byte(strings.Replace(string(body), "{user: member, infra: true}", "{user: member, infra: false}", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if !SpaceRendersInfra(filepath.Join(spaces, "infra")) {
		t.Fatal("authored infra:true passport not consulted")
	}
	if SpaceRendersInfra(filepath.Join(spaces, "member")) {
		t.Fatal("non-infra authored passport claimed the control plane")
	}
	if got := InfraSpaceDir(filepath.Join(spaces, "member")); got != filepath.Join(spaces, "infra") {
		t.Fatalf("member did not resolve the infra sibling through authored passports: %q", got)
	}
}

func TestGovernanceUserMCPDeniedByDefault(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("me")
	docs["tools"] += "extended:\n  user_mcp: {via: mcp-raw}\nconnectors:\n  gitea: {url: \"https://mcp.example.com/gitea\"}\n"
	publishManagedDocs(t, d, "me", "r-0000000000000001", docs)
	_, err := ReadEffective(d, "prod")
	if err == nil || !strings.Contains(err.Error(), "user_mcp") {
		t.Fatalf("user-declared MCP admitted without a grant: %v", err)
	}
}

func TestGovernanceUserMCPGrantUnlocks(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("me")
	docs["tools"] += "extended:\n  user_mcp: {via: mcp-raw}\nconnectors:\n  gitea: {url: \"https://mcp.example.com/gitea\"}\n"
	publishManagedDocs(t, d, "me", "r-0000000000000001", docs)
	seedGovernance(t, d, func(doc *api.Governance) {
		activeToolGrant(t, doc, "me", api.SectionUserMCP, time.Hour)
	})
	s, err := ReadEffective(d, "prod")
	if err != nil {
		t.Fatalf("granted user_mcp still denied: %v", err)
	}
	if _, ok := s.MCP["gitea"]; !ok {
		t.Fatal("granted MCP dropped")
	}
}

func TestGovernanceExplicitWishDenied(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "me", "r-0000000000000001")
	// Default workspace selects `file: toolhub`; a global deny on that section
	// must fail the whole read, not silently filter. `web: native` is an
	// explicit wish now (R3.1) — grant nothing and a deny must fail the read.
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-files", Scope: api.ScopeGlobal, Section: "hub:files", Effect: api.RuleDeny, Reason: "test deny", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	_, err := ReadEffective(d, "prod")
	if err == nil || !strings.Contains(err.Error(), "hub:files") {
		t.Fatalf("denied explicit tools entry admitted: %v", err)
	}
	docs := managedDocs("me")
	docs["tools"] += "extended:\n  web: {via: native}\n"
	publishManagedDocs(t, d, "me", "r-0000000000000002", docs)
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-web", Scope: api.ScopeGlobal, Section: "native:web", Effect: api.RuleDeny, Reason: "test deny", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	if _, err := ReadEffective(d, "prod"); err == nil || !strings.Contains(err.Error(), "native:web") {
		t.Fatalf("denied native toolset admitted: %v", err)
	}
}

// TestGovernanceAssignmentsSection: the assignments wish is a governed hub
// section — `assignments: toolhub` must resolve to a known inventory entry
// (SPEC-0080), not fail as an unknown section.
func TestGovernanceAssignmentsSection(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "me", "r-0000000000000001")
	docs := managedDocs("me")
	docs["tools"] += "extended:\n  assignments: {via: toolhub}\n"
	publishManagedDocs(t, d, "me", "r-0000000000000001", docs)
	if _, err := ReadEffective(d, "prod"); err != nil {
		t.Fatalf("assignments: toolhub must be a known section: %v", err)
	}
}

// TestGovernanceSkillsSections: the skills family is a governed hub section —
// the default workspace wish `skills` must resolve to the hub:skills
// inventory entry (SPEC-0084), not fail as an unknown capability family.
func TestGovernanceSkillsSections(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("me")
	docs["tools"] += "extended:\n  skills: {via: toolhub}\n"
	publishManagedDocs(t, d, "me", "r-0000000000000001", docs)
	s, err := ReadEffective(d, "prod")
	if err != nil {
		t.Fatalf("skills wishes must resolve to known sections: %v", err)
	}
	if entry, ok := s.Tools["skills"]; !ok || entry.Via != ToolViaToolHub {
		t.Fatalf("default workspace must wish skills: toolhub, got %+v", s.Tools["skills"])
	}
}

func TestGovernanceMigrationPreservesSelection(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	// terminal is deny-by-default; a space that selected it before governance
	// existed keeps it as an explicit recorded migration allow.
	docs := managedDocs("me")
	docs["tools"] += "extended:\n  terminal: {via: native}\n"
	publishManagedDocs(t, d, "me", "r-0000000000000001", docs)
	if _, err := ReadEffective(d, "prod"); err != nil {
		t.Fatalf("migration seed read: %v", err)
	}
	doc, err := api.LoadGovernance(filepath.Join(d, "runtime", "toolhub", "governance.json"))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range doc.Rules {
		if r.Scope == api.ScopeUser && r.Subject == "me" && r.Section == "native:terminal" && r.Effect == api.RuleAllow {
			found = true
		}
	}
	if !found {
		t.Fatal("pre-governance terminal selection not preserved as a migration rule")
	}
	// The preserved selection still evaluates allow for this principal.
	if d := doc.Evaluate("me", "", "native:terminal", nil, time.Now()); !d.Allowed() {
		t.Fatalf("migration allow not effective: %+v", d)
	}
	// ...but not for a different principal.
	if d := doc.Evaluate("other", "", "native:terminal", nil, time.Now()); d.Allowed() {
		t.Fatalf("migration allow leaked to another principal: %+v", d)
	}
}

func TestGovernanceImplicitSectionsFilteredAtRender(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "me", "r-0000000000000001")
	// A native the space never wished for stays off even when a policy rule
	// names it — and evaluating it must never fail the read (R3.1/R3.2).
	// Unemitted sections are simply not on the render; govAllows reports the
	// recorded decision only for evaluated sections.
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-cronjob", Scope: api.ScopeGlobal, Section: "native:cronjob", Effect: api.RuleDeny, Reason: "test", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	s, err := ReadEffective(d, "prod")
	if err != nil {
		t.Fatalf("unwished native section deny must not fail the read: %v", err)
	}
	cfg := Config(s)
	for _, channel := range []string{"cli", "local", "telegram", "api_server", "cron"} {
		if sets := cfg["platform_toolsets"].(M)[channel].([]string); slices.Contains(sets, "cronjob") {
			t.Fatalf("%s emitted denied cronjob: %v", channel, sets)
		}
	}
}

func TestGovernancePolicySnapshotRendered(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "config"), 0700)
	_ = os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("stub"), 0600)
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("me")
	docs["tools"] += "extended:\n  media: {via: toolhub}\n"
	publishManagedDocs(t, d, "me", "r-0000000000000001", docs)
	// cap:read on a selected family narrows it to read_only in the snapshot;
	// an explicit-entry deny would fail the read instead (proven above), so
	// the deny list is exercised through a default-scope attribute deny on a
	// family the space does not select.
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules,
			api.PolicyRule{RuleID: "cap-media", Scope: api.ScopeGlobal, Section: "hub:media", Effect: api.RuleCapRead, Reason: "test", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1},
			api.PolicyRule{RuleID: "deny-terminal-family", Scope: api.ScopeGlobal, Section: "hub:terminal", Effect: api.RuleDeny, Reason: "test", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	if _, err := ReadEffective(d, "prod"); err != nil {
		t.Fatalf("media cap: %v", err)
	}
	if err := Render(d, root); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(d, "generated", "tool-policy.prod.json"))
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	var snapshot api.ToolPolicySnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.CappedReadOnly("media") || snapshot.Denied("media") || snapshot.Denied("files") {
		t.Fatalf("snapshot sets wrong: %+v", snapshot)
	}
	if snapshot.Principal != "me" {
		t.Fatalf("snapshot principal: %+v", snapshot)
	}
	// A published workspace passport binds the whole /config view read-only
	// (SPEC-0092 R5.2) — the snapshot rides inside it rather than as its own
	// per-file mount. The supervised runtime is spawned by the supervisor,
	// so compose.prod.yaml carries no runtime volumes; the view file is the
	// render artifact the spawn spec binds.
	view, err := os.ReadFile(filepath.Join(d, "generated", "config-view", "prod", "tool-policy.json"))
	if err != nil {
		t.Fatalf("policy snapshot missing from the config view: %v", err)
	}
	if !strings.Contains(string(view), `"principal": "me"`) {
		t.Fatalf("config view snapshot is a different document: %s", view)
	}
}

func TestGovernanceOrgScopedCatalog(t *testing.T) {
	d := t.TempDir()
	orgDir := filepath.Join(filepath.Dir(d), "organizations", "acme")
	if err := InitOrganization(orgDir, "acme", "me"); err != nil {
		t.Fatal(err)
	}
	// The org's raw catalog lives in its own authority file; the user space
	// merges it as org_mcp instead of declaring connectors itself.
	orgSettings := filepath.Join(orgDir, "settings.yaml")
	body, err := os.ReadFile(orgSettings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orgSettings, append(body, []byte("mcp_servers:\n  orgwiki: {url: \"https://wiki.acme.example/mcp\", tools: {include: [search]}}\nread_only_mcp: [orgwiki]\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initEnvironment(d, "me", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	// An org-scoped space treats its merged catalog as org_mcp; deny that
	// section and the read must fail.
	docs := managedDocs("me")
	docs["stack"] = strings.Replace(docs["stack"], "identity: {user: me", "identity: {user: me, organization: acme", 1)
	docs["tools"] += "extended:\n  org_mcp: {via: mcp-raw}\n"
	publishManagedDocs(t, d, "me", "r-0000000000000001", docs)
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-orgmcp", Scope: api.ScopeOrg, Subject: "acme", Section: api.SectionOrgMCP, Effect: api.RuleDeny, Reason: "org freeze", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	if _, err := ReadEffective(d, "prod"); err == nil || !strings.Contains(err.Error(), "org_mcp") {
		t.Fatalf("org catalog deny ignored: %v", err)
	}
	// The same deny for a different organization does not touch this space.
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-orgmcp-other", Scope: api.ScopeOrg, Subject: "otherorg", Section: api.SectionOrgMCP, Effect: api.RuleDeny, Reason: "org freeze", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	if _, err := ReadEffective(d, "prod"); err != nil {
		t.Fatalf("foreign-org deny applied: %v", err)
	}
}

func TestGovernanceCorruptDocFailsClosed(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "me", "r-0000000000000001")
	path := filepath.Join(d, "runtime", "toolhub", "governance.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEffective(d, "prod"); err == nil {
		t.Fatal("corrupt governance document read permissively")
	}
}

func TestGovernanceSnapshotUnit(t *testing.T) {
	// toolPolicySnapshot on a Settings with no governance view stays empty —
	// unconfigured render paths emit a no-deny document, not a guess.
	s := Settings{User: "me"}
	snap := s.toolPolicySnapshot()
	if snap.DenyAll || len(snap.Deny) != 0 || len(snap.ReadOnly) != 0 {
		t.Fatalf("empty view produced denies: %+v", snap)
	}
}

// sectionForEntry maps every tools entry shape to its governance section:
// native toolsets by name, toolhub families by capability alias, managed MCP
// servers into their hub section, mcp-raw into the mutable user/org catalog.
func TestSectionForEntryMapping(t *testing.T) {
	s := Settings{User: "alice"}
	for _, tc := range []struct {
		name  string
		entry ToolEntry
		want  string
	}{
		{"terminal", ToolEntry{Via: ToolViaNative}, "native:terminal"},
		{"file", ToolEntry{Via: ToolViaToolHub}, "hub:files"},
		{"code_exec", ToolEntry{Via: ToolViaToolHub}, "hub:terminal"},
		{"websearch", ToolEntry{Via: ToolViaToolHub}, "hub:websearch"},
		{"gitea", ToolEntry{Via: ToolViaMCP}, "hub:gitea"},
		{"gitea", ToolEntry{Via: ToolViaMCPRaw}, api.SectionUserMCP},
		{"x", ToolEntry{Via: ToolViaOff}, ""},
		{"x", ToolEntry{}, ""},
	} {
		if got := s.sectionForEntry(tc.name, tc.entry); got != tc.want {
			t.Fatalf("%s via %q: got %q want %q", tc.name, tc.entry.Via, got, tc.want)
		}
	}
	// Org-scoped spaces route mcp-raw into the organization catalog section.
	org := Settings{User: "alice", Organization: "acme"}
	if got := org.sectionForEntry("gitea", ToolEntry{Via: ToolViaMCPRaw}); got != api.SectionOrgMCP {
		t.Fatalf("org mcp-raw: %s", got)
	}
}

// TestGovernanceDocPathInfraSibling: the shared document lives in the infra
// space's runtime dir — a secondary space (infra: false) must reach it, not
// the alphabetically-first sibling's leftover doc. The infra space itself
// always resolves its own dir (seed target), never a sibling's.
func TestGovernanceDocPathInfraSibling(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	write := func(space, rel, body string) {
		path := filepath.Join(spaces, space, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	doc := filepath.Join("runtime", "toolhub", "governance.json")
	write("artem_main", filepath.Join("passports", "stack.yaml"), "kind: stack\nidentity: {user: artem_main, infra: false}\n")
	write("artem_main", doc, "{}")
	write("local", filepath.Join("passports", "stack.yaml"), "kind: stack\nidentity: {user: local, infra: true}\n")
	write("local", doc, "{}")
	write("artem_two", filepath.Join("passports", "stack.yaml"), "kind: stack\nidentity: {user: artem_two, infra: false}\n")

	want := filepath.Join(spaces, "local", doc)
	if got := governanceDocPath(filepath.Join(spaces, "artem_two")); got != want {
		t.Fatalf("secondary picked %q, want infra doc %q", got, want)
	}
	// A secondary's leftover own doc is last resort, never authority over the
	// infra document.
	write("artem_two", doc, "{}")
	if got := governanceDocPath(filepath.Join(spaces, "artem_two")); got != want {
		t.Fatalf("secondary own doc shadowed the infra doc: %q", got)
	}
	// The infra space resolves its own dir whether the file exists or not.
	if got := governanceDocPath(filepath.Join(spaces, "local")); got != want {
		t.Fatalf("infra space doc path: %q", got)
	}
	os.Remove(filepath.Join(spaces, "local", doc))
	if got := governanceDocPath(filepath.Join(spaces, "local")); got != want {
		t.Fatalf("infra space seed target must be its own dir: %q", got)
	}
	// A missing infra document stays missing; a stale sibling document is
	// never promoted into shared authority.
	other := filepath.Join(spaces, "artem_two")
	os.Remove(filepath.Join(spaces, "artem_two", doc))
	if got := governanceDocPath(other); got != want {
		t.Fatalf("missing infra document selected a sibling: %q", got)
	}
	write("remote", filepath.Join("passports", "stack.yaml"), "kind: stack\nidentity: {user: remote, infra: true}\n")
	write("remote", doc, "{}")
	if got := governanceDocPath(other); got != filepath.Join(other, doc) {
		t.Fatalf("ambiguous infra owners selected a foreign document: %q", got)
	}
}

// denyDefaultSelections preserves only deny-default sections the space
// explicitly picked — never user_mcp, never implicit toolsets.
func TestDenyDefaultSelections(t *testing.T) {
	s := Settings{User: "alice", Tools: map[string]ToolEntry{
		"terminal": {Via: ToolViaNative},
		"file":     {Via: ToolViaNative},  // deny-default since R3.1: preserved
		"gitea":    {Via: ToolViaMCPRaw},  // user_mcp: never preserved
		"off":      {Via: ToolViaOff},     // disabled: skipped
		"ssh":      {Via: ToolViaToolHub}, // hub:ssh is deny-default exec
	}}
	got := s.denyDefaultSelections(api.NewGovernance())
	want := []string{"native:file", "native:terminal"}
	if !slices.Equal(got, want) {
		t.Fatalf("preserved %v want %v", got, want)
	}
}

// Implicit sections omit the whole toolset surface in managed mode and
// include the guest browser surface only when the browser entry is on.
func TestImplicitToolsetSections(t *testing.T) {
	managed := Settings{User: "alice", CapabilityMode: "managed"}
	if got := implicitToolsetSections(managed); got != nil {
		t.Fatalf("managed implicit: %v", got)
	}
	plain := implicitToolsetSections(Settings{User: "alice"})
	if !slices.Equal(plain, []string{api.SectionHubTools}) {
		t.Fatalf("unmanaged implicit must be hub tools only: %v", plain)
	}
	if slices.Contains(plain, "hub:browser_guest") {
		t.Fatal("guest surface listed without browser")
	}
	withBrowser := implicitToolsetSections(Settings{User: "alice", Tools: map[string]ToolEntry{"browser": {Via: ToolViaNative}}})
	if !slices.Contains(withBrowser, "hub:browser_guest") {
		t.Fatalf("guest surface missing: %v", withBrowser)
	}
}

// govDecision/govForcedRO on a hand-built Settings (no Read) report nothing —
// the governance boundary is the load path.
func TestGovViewAbsent(t *testing.T) {
	s := Settings{User: "alice"}
	if s.govAllows("native:terminal") != true {
		t.Fatal("no view must be permissive")
	}
	if _, ok := s.govDecision("native:terminal"); ok {
		t.Fatal("decision on unviewed settings")
	}
	if s.govForcedRO("file") {
		t.Fatal("forced ro on unviewed settings")
	}
}

// Before the first apply, a sibling's authored config passport is the only
// port claim — collision detection must honor it exactly like the published
// revision.
func TestReservedSiblingPortsFromAuthoredPassport(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	member := filepath.Join(spaces, "member")
	if err := os.MkdirAll(member, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(spaces, "peer", "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	authored := "kind: config\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"
	if err := os.WriteFile(filepath.Join(spaces, "peer", "passports", "config.yaml"), []byte(authored), 0600); err != nil {
		t.Fatal(err)
	}
	got := ReservedSiblingPorts(member)
	if got[8008] != "peer" || got[6081] != "peer" {
		t.Fatalf("authored sibling ports not reserved: %v", got)
	}
}

// The shared workload-controller lives on the infra-owner's compose project;
// a secondary space's operator calls must present the infra sibling's
// credentials. InfraSpaceDir mirrors governanceDocPath's precedence: own
// space when it renders the control plane, else the infra sibling, else own.
func TestInfraSpaceDir(t *testing.T) {
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	local := filepath.Join(spaces, "local")
	member := filepath.Join(spaces, "member")
	for _, d := range []string{local, member} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeAuthoredStack := func(dir, user string, infra bool) {
		if err := os.MkdirAll(filepath.Join(dir, "passports"), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "kind: stack\nidentity: {user: " + user + ", infra: " + fmt.Sprint(infra) + "}\n"
		if err := os.WriteFile(filepath.Join(dir, "passports", "stack.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeAuthoredStack(local, "local", true)
	writeAuthoredStack(member, "member", false)
	if got := InfraSpaceDir(member); got != local {
		t.Fatalf("member must resolve the infra sibling, got %s", got)
	}
	if got := InfraSpaceDir(local); got != local {
		t.Fatalf("infra space resolves itself, got %s", got)
	}
	otherInfra := filepath.Join(spaces, "other")
	if err := os.MkdirAll(otherInfra, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAuthoredStack(otherInfra, "other", true)
	if got := InfraSpaceDir(member); got != member {
		t.Fatalf("ambiguous infra owners selected another space: %s", got)
	}
	// Outside a spaces/ layout the space answers for itself.
	lone := filepath.Join(root, "lone")
	if err := os.MkdirAll(lone, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := InfraSpaceDir(lone); got != lone {
		t.Fatalf("standalone dir resolves itself, got %s", got)
	}
	// No infra sibling on record: fail back to the caller, never error out.
	root2 := t.TempDir()
	lonely := filepath.Join(root2, "spaces", "solo")
	if err := os.MkdirAll(lonely, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAuthoredStack(lonely, "solo", false)
	if got := InfraSpaceDir(lonely); got != lonely {
		t.Fatalf("no infra sibling falls back to caller, got %s", got)
	}
}
