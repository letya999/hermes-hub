package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"gopkg.in/yaml.v3"
)

// seedGovernance writes a valid governance document into the space's
// runtime/toolhub dir before the first Read, so no migration allow is
// recorded — the doc the test authors is the whole policy.
func seedGovernance(t *testing.T, dir string, mutate func(*toolhub.Governance)) *toolhub.Governance {
	t.Helper()
	doc := toolhub.NewGovernance()
	if mutate != nil {
		mutate(doc)
	}
	path := filepath.Join(dir, "runtime", "toolhub", "governance.json")
	if err := toolhub.SaveGovernance(path, doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func activeToolGrant(t *testing.T, doc *toolhub.Governance, principal, section string, ttl time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	grant := toolhub.NewToolGrant("grant-"+principal+"-"+strings.ReplaceAll(section, ":", "-"), principal, section, "test grant", "operator", now.Add(ttl), nil)
	if err := toolhub.Confirm(&grant, "operator", now); err != nil {
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
	seedGovernance(t, d, func(doc *toolhub.Governance) {
		activeToolGrant(t, doc, "me", toolhub.SectionUserMCP, time.Hour)
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
	// Default workspace selects `file: toolhub` and `web: native`; a global
	// deny on either section must fail the whole read, not silently filter.
	seedGovernance(t, d, func(doc *toolhub.Governance) {
		doc.Rules = append(doc.Rules, toolhub.PolicyRule{RuleID: "deny-files", Scope: toolhub.ScopeGlobal, Section: "hub:files", Effect: toolhub.RuleDeny, Reason: "test deny", GrantedBy: "operator", Status: toolhub.ActiveStatus, Revision: 1})
	})
	_, err := Read(d)
	if err == nil || !strings.Contains(err.Error(), "hub:files") {
		t.Fatalf("denied explicit tools entry admitted: %v", err)
	}
	seedGovernance(t, d, func(doc *toolhub.Governance) {
		doc.Rules = append(doc.Rules, toolhub.PolicyRule{RuleID: "deny-web", Scope: toolhub.ScopeGlobal, Section: "native:web", Effect: toolhub.RuleDeny, Reason: "test deny", GrantedBy: "operator", Status: toolhub.ActiveStatus, Revision: 1})
	})
	if _, err := Read(d); err == nil || !strings.Contains(err.Error(), "native:web") {
		t.Fatalf("denied native toolset admitted: %v", err)
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
	doc, err := toolhub.LoadGovernance(filepath.Join(d, "runtime", "toolhub", "governance.json"))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range doc.Rules {
		if r.Scope == toolhub.ScopeUser && r.Subject == "me" && r.Section == "native:terminal" && r.Effect == toolhub.RuleAllow {
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
	// cronjob is an implicit unmanaged emission — not in the default
	// workspace tools map — so its deny filters render output without failing
	// the read.
	seedGovernance(t, d, func(doc *toolhub.Governance) {
		doc.Rules = append(doc.Rules, toolhub.PolicyRule{RuleID: "deny-cronjob", Scope: toolhub.ScopeGlobal, Section: "native:cronjob", Effect: toolhub.RuleDeny, Reason: "test", GrantedBy: "operator", Status: toolhub.ActiveStatus, Revision: 1})
	})
	s, err := Read(d)
	if err != nil {
		t.Fatalf("implicit section deny must not fail the read: %v", err)
	}
	if s.govAllows("native:cronjob") {
		t.Fatal("denied implicit section still allowed")
	}
	cfg := Config(s)
	yamlBody, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var rendered map[string]any
	if err := yaml.Unmarshal(yamlBody, &rendered); err != nil {
		t.Fatal(err)
	}
	agent, _ := rendered["agent"].(map[string]any)
	enabled, _ := agent["enabled_toolsets"].([]any)
	for _, name := range enabled {
		if name == "cronjob" {
			t.Fatal("denied toolset still emitted")
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
	seedGovernance(t, d, func(doc *toolhub.Governance) {
		doc.Rules = append(doc.Rules,
			toolhub.PolicyRule{RuleID: "cap-hh", Scope: toolhub.ScopeGlobal, Section: "hub:hh", Effect: toolhub.RuleCapRead, Reason: "test", GrantedBy: "operator", Status: toolhub.ActiveStatus, Revision: 1},
			toolhub.PolicyRule{RuleID: "deny-terminal-family", Scope: toolhub.ScopeGlobal, Section: "hub:terminal", Effect: toolhub.RuleDeny, Reason: "test", GrantedBy: "operator", Status: toolhub.ActiveStatus, Revision: 1})
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
	var snapshot toolhub.ToolPolicySnapshot
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
	seedGovernance(t, d, func(doc *toolhub.Governance) {
		doc.Rules = append(doc.Rules, toolhub.PolicyRule{RuleID: "deny-orgmcp", Scope: toolhub.ScopeOrg, Subject: "acme", Section: toolhub.SectionOrgMCP, Effect: toolhub.RuleDeny, Reason: "org freeze", GrantedBy: "operator", Status: toolhub.ActiveStatus, Revision: 1})
	})
	if _, err := Read(d); err == nil || !strings.Contains(err.Error(), "org_mcp") {
		t.Fatalf("org catalog deny ignored: %v", err)
	}
	// The same deny for a different organization does not touch this space.
	seedGovernance(t, d, func(doc *toolhub.Governance) {
		doc.Rules = append(doc.Rules, toolhub.PolicyRule{RuleID: "deny-orgmcp-other", Scope: toolhub.ScopeOrg, Subject: "otherorg", Section: toolhub.SectionOrgMCP, Effect: toolhub.RuleDeny, Reason: "org freeze", GrantedBy: "operator", Status: toolhub.ActiveStatus, Revision: 1})
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
		{"gitea", ToolEntry{Via: ToolViaMCPRaw}, toolhub.SectionUserMCP},
		{"x", ToolEntry{Via: ToolViaOff}, ""},
		{"x", ToolEntry{}, ""},
	} {
		if got := s.sectionForEntry(tc.name, tc.entry); got != tc.want {
			t.Fatalf("%s via %q: got %q want %q", tc.name, tc.entry.Via, got, tc.want)
		}
	}
	// Org-scoped spaces route mcp-raw into the organization catalog section.
	org := Settings{User: "alice", Organization: "acme"}
	if got := org.sectionForEntry("gitea", ToolEntry{Via: ToolViaMCPRaw}); got != toolhub.SectionOrgMCP {
		t.Fatalf("org mcp-raw: %s", got)
	}
}

// denyDefaultSelections preserves only deny-default sections the space
// explicitly picked — never user_mcp, never implicit toolsets.
func TestDenyDefaultSelections(t *testing.T) {
	s := Settings{User: "alice", Tools: map[string]ToolEntry{
		"terminal": {Via: ToolViaNative},
		"file":     {Via: ToolViaNative},  // allow-default: not preserved
		"gitea":    {Via: ToolViaMCPRaw},  // user_mcp: never preserved
		"off":      {Via: ToolViaOff},     // disabled: skipped
		"ssh":      {Via: ToolViaToolHub}, // hub:ssh is deny-default exec
	}}
	got := s.denyDefaultSelections(toolhub.NewGovernance())
	want := []string{"native:terminal"}
	if len(got) != 1 || got[0] != want[0] {
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
	if !slices.Contains(plain, toolhub.SectionHubTools) || !slices.Contains(plain, "native:terminal") {
		t.Fatalf("unmanaged implicit: %v", plain)
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
