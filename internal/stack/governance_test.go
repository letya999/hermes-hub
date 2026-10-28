package stack

import (
	"encoding/json"
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

// appendWorkspaceKey injects a raw block into the space's workspace.yaml so
// the fixture arrives before the first Read — migration only preserves
// sections already selected at seed time.
func appendWorkspaceKey(t *testing.T, dir, block string) {
	t.Helper()
	path := filepath.Join(dir, "workspace.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, []byte(block)...), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestGovernanceUserMCPDeniedByDefault(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	appendWorkspaceKey(t, d, "\nmcp:\n  gitea: {url: \"https://mcp.example.com/gitea\"}\n")
	_, err := Read(d)
	if err == nil || !strings.Contains(err.Error(), "user_mcp") {
		t.Fatalf("user-declared MCP admitted without a grant: %v", err)
	}
}

func TestGovernanceUserMCPGrantUnlocks(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	appendWorkspaceKey(t, d, "\nmcp:\n  gitea: {url: \"https://mcp.example.com/gitea\"}\n")
	seedGovernance(t, d, func(doc *api.Governance) {
		activeToolGrant(t, doc, "me", api.SectionUserMCP, time.Hour)
	})
	s, err := Read(d)
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
	// Default workspace selects `file: toolhub`; a global deny on that section
	// must fail the whole read, not silently filter. `web: native` is an
	// explicit wish now (R3.1) — grant nothing and a deny must fail the read.
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-files", Scope: api.ScopeGlobal, Section: "hub:files", Effect: api.RuleDeny, Reason: "test deny", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	_, err := Read(d)
	if err == nil || !strings.Contains(err.Error(), "hub:files") {
		t.Fatalf("denied explicit tools entry admitted: %v", err)
	}
	wsPath := filepath.Join(d, "workspace.yaml")
	wsBody, _ := os.ReadFile(wsPath)
	wsBody = []byte(strings.Replace(string(wsBody), "  # web: native", "  web: {via: native}", 1))
	if err := os.WriteFile(wsPath, wsBody, 0600); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-web", Scope: api.ScopeGlobal, Section: "native:web", Effect: api.RuleDeny, Reason: "test deny", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	if _, err := Read(d); err == nil || !strings.Contains(err.Error(), "native:web") {
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
	wsPath := filepath.Join(d, "workspace.yaml")
	wsBody, err := os.ReadFile(wsPath)
	if err != nil {
		t.Fatal(err)
	}
	wsBody = []byte(strings.Replace(string(wsBody), "  images: toolhub", "  images: toolhub\n  assignments: toolhub", 1))
	if err := os.WriteFile(wsPath, wsBody, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(d); err != nil {
		t.Fatalf("assignments: toolhub must be a known section: %v", err)
	}
}

func TestGovernanceMigrationPreservesSelection(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	// terminal is deny-by-default; a space that selected it before governance
	// existed keeps it as an explicit recorded migration allow. The default
	// template ships the entry commented — uncomment it.
	wsPath := filepath.Join(d, "workspace.yaml")
	wsBody, err := os.ReadFile(wsPath)
	if err != nil {
		t.Fatal(err)
	}
	wsBody = []byte(strings.Replace(string(wsBody), "# terminal: native", "terminal: native", 1))
	if err := os.WriteFile(wsPath, wsBody, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(d); err != nil {
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
	// A native the space never wished for stays off even when a policy rule
	// names it — and evaluating it must never fail the read (R3.1/R3.2).
	// Unemitted sections are simply not on the render; govAllows reports the
	// recorded decision only for evaluated sections.
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-cronjob", Scope: api.ScopeGlobal, Section: "native:cronjob", Effect: api.RuleDeny, Reason: "test", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	s, err := Read(d)
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
	// cap:read on the hh family narrows it to read_only in the snapshot; an
	// explicit-entry deny would fail the read instead (proven above), so the
	// deny list is exercised through a default-scope attribute deny on a
	// family the space does not select.
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules,
			api.PolicyRule{RuleID: "cap-hh", Scope: api.ScopeGlobal, Section: "hub:hh", Effect: api.RuleCapRead, Reason: "test", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1},
			api.PolicyRule{RuleID: "deny-terminal-family", Scope: api.ScopeGlobal, Section: "hub:terminal", Effect: api.RuleDeny, Reason: "test", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	if _, err := Read(d); err != nil {
		t.Fatalf("hh deny: %v", err)
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
	if !snapshot.CappedReadOnly("hh") || snapshot.Denied("hh") || snapshot.Denied("files") {
		t.Fatalf("snapshot sets wrong: %+v", snapshot)
	}
	if snapshot.Principal != "me" {
		t.Fatalf("snapshot principal: %+v", snapshot)
	}
	// Compose mounts the snapshot read-only and pins the env.
	compose, err := os.ReadFile(filepath.Join(d, "generated", "compose.prod.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "tool-policy.prod.json") || !strings.Contains(string(compose), "/config/tool-policy.json") {
		t.Fatal("policy snapshot mount missing from compose")
	}
}

func TestGovernanceOrgScopedCatalog(t *testing.T) {
	d := t.TempDir()
	if err := initEnvironment(d, "me", "prod", "acme"); err != nil {
		t.Fatal(err)
	}
	// An org-scoped space treats its merged catalog as org_mcp; deny that
	// section and the read must fail.
	appendWorkspaceKey(t, d, "\nmcp:\n  orgwiki: {url: \"https://wiki.acme.example/mcp\"}\n")
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-orgmcp", Scope: api.ScopeOrg, Subject: "acme", Section: api.SectionOrgMCP, Effect: api.RuleDeny, Reason: "org freeze", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	if _, err := Read(d); err == nil || !strings.Contains(err.Error(), "org_mcp") {
		t.Fatalf("org catalog deny ignored: %v", err)
	}
	// The same deny for a different organization does not touch this space.
	seedGovernance(t, d, func(doc *api.Governance) {
		doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-orgmcp-other", Scope: api.ScopeOrg, Subject: "otherorg", Section: api.SectionOrgMCP, Effect: api.RuleDeny, Reason: "org freeze", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	})
	if _, err := Read(d); err != nil {
		t.Fatalf("foreign-org deny applied: %v", err)
	}
}

func TestGovernanceCorruptDocFailsClosed(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "runtime", "toolhub", "governance.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(d); err == nil {
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
