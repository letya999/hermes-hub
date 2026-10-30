package api

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testPolicyRule(id, scope, subject, section, effect string) PolicyRule {
	return PolicyRule{
		RuleID: id, Scope: scope, Subject: subject, Section: section,
		Effect: effect, Reason: "test rule", GrantedBy: "operator",
		Status: ActiveStatus, Revision: 1,
	}
}

func TestGovernanceEvaluateOrder(t *testing.T) {
	now := time.Now()
	g := NewGovernance()
	if err := g.Validate(); err != nil {
		t.Fatalf("seeded governance invalid: %v", err)
	}
	if d := g.Evaluate("alice", "org1", "native:terminal", nil, now); d.Effect != RuleDeny || d.Via != "section" {
		t.Fatalf("restrictive section default: %+v", d)
	}
	if d := g.Evaluate("alice", "org1", "no:such", nil, now); d.Effect != RuleDeny || d.Via != "unknown" {
		t.Fatalf("unknown section: %+v", d)
	}
	if err := g.PutRule(testPolicyRule("u1", ScopeUser, "alice", "native:terminal", RuleAllow), now); err != nil {
		t.Fatal(err)
	}
	if d := g.Evaluate("alice", "org1", "native:terminal", nil, now); d.Effect != RuleAllow || d.Via != ScopeUser {
		t.Fatalf("user allow: %+v", d)
	}
	if d := g.Evaluate("bob", "org1", "native:terminal", nil, now); d.Effect != RuleDeny {
		t.Fatalf("user rule leaked to bob: %+v", d)
	}
	if err := g.PutRule(testPolicyRule("g1", ScopeGlobal, "", "native:terminal", RuleDeny), now); err != nil {
		t.Fatal(err)
	}
	if d := g.Evaluate("alice", "org1", "native:terminal", nil, now); d.Effect != RuleDeny || d.Via != "deny" {
		t.Fatalf("global deny over user allow: %+v", d)
	}
}

func TestGovernanceGrantLifecycle(t *testing.T) {
	now := time.Now()
	g := NewGovernance()
	grant := NewToolGrant("grant-1", "alice", "native:terminal", "need it", "operator", now.Add(time.Hour), nil)
	if err := g.PutToolGrant(grant, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unconfirmed grant: %v", err)
	}
	if err := Confirm(&grant, "operator", now); err != nil {
		t.Fatal(err)
	}
	grant.Status = GrantActive
	if err := g.PutToolGrant(grant, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("active grant must enter pending: %v", err)
	}
	grant.Status = GrantPending
	if err := g.PutToolGrant(grant, now); err != nil {
		t.Fatal(err)
	}
	pending := g.PendingToolGrants("alice", "native:terminal", now)
	if len(pending) != 1 || pending[0].GrantID != "grant-1" {
		t.Fatalf("pending: %+v", pending)
	}
	if _, err := g.AcknowledgeToolGrant("alice", "grant-1", "x", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ack without request: %v", err)
	}
	requested, err := g.RequestToolGrant("alice", "grant-1", now)
	if err != nil || requested.RequestNonce == "" {
		t.Fatalf("request: %v %q", err, requested.RequestNonce)
	}
	if _, err := g.AcknowledgeToolGrant("alice", "grant-1", "wrong", now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad nonce ack: %v", err)
	}
	acked, err := g.AcknowledgeToolGrant("alice", "grant-1", requested.RequestNonce, now)
	if err != nil || acked.Status != GrantActive {
		t.Fatalf("ack: %v %+v", err, acked)
	}
	if d := g.Evaluate("alice", "org1", "native:terminal", nil, now); d.Effect != RuleAllow || d.Via != "grant" {
		t.Fatalf("grant widened: %+v", d)
	}
	if err := g.RevokeToolGrant("grant-1", "model", "x", now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("model revoke: %v", err)
	}
	if err := g.RevokeToolGrant("grant-1", "operator", "done", now); err != nil {
		t.Fatal(err)
	}
	if d := g.Evaluate("alice", "org1", "native:terminal", nil, now); d.Effect != RuleDeny {
		t.Fatalf("revoked grant still allows: %+v", d)
	}
	if _, err := g.RequestToolGrant("alice", "grant-1", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("request revoked grant: %v", err)
	}
	if _, err := g.RequestToolGrant("alice", "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("request missing grant: %v", err)
	}
}

func TestGovernanceRuleAndDocValidation(t *testing.T) {
	now := time.Now()
	g := NewGovernance()
	if err := g.PutRule(testPolicyRule("u1", ScopeUser, "alice", "native:web", RuleAllow), now); err != nil {
		t.Fatal(err)
	}
	if err := g.PutRule(testPolicyRule("u1", ScopeUser, "alice", "native:web", RuleAllow), now); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	bad := []PolicyRule{
		testPolicyRule("", ScopeUser, "alice", "native:web", RuleAllow),
		testPolicyRule("x", ScopeGlobal, "alice", "native:web", RuleAllow),
		testPolicyRule("x", ScopeOrg, "", "native:web", RuleAllow),
		testPolicyRule("x", "weird", "", "native:web", RuleAllow),
		testPolicyRule("x", ScopeUser, "alice", "native:web", "maybe"),
		testPolicyRule("x", ScopeDefault, "", SectionUserMCP, RuleDeny),
	}
	for i, rule := range bad {
		if err := rule.Validate(); err == nil {
			t.Fatalf("bad rule %d accepted", i)
		}
	}
	issuer := testPolicyRule("x", ScopeUser, "alice", "native:web", RuleAllow)
	issuer.GrantedBy = "model"
	if err := issuer.Validate(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("model-issued rule: %v", err)
	}
	both := testPolicyRule("x", ScopeUser, "alice", "native:web", RuleAllow)
	both.Match = map[string]string{"owner": "user"}
	if err := both.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("section+match rule: %v", err)
	}
	g2 := NewGovernance()
	g2.Inventory = g2.Inventory[:len(g2.Inventory)-1]
	if err := g2.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("inventory missing section: %v", err)
	}
	g3 := NewGovernance()
	g3.Inventory = append(g3.Inventory, g3.Inventory[0])
	if err := g3.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate section: %v", err)
	}
}

func TestGovernanceIO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime", "toolhub", "governance.json")
	g, err := LoadOrSeedGovernance(path, "alice", []string{"native:terminal"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Section("native:terminal"); !ok {
		t.Fatal("seeded inventory lacks native:terminal")
	}
	loaded, err := LoadGovernance(path)
	if err != nil || loaded.Revision != g.Revision {
		t.Fatalf("load: %v %+v", err, loaded)
	}
	if d := loaded.Evaluate("alice", "", "native:terminal", nil, time.Now()); d.Effect != RuleAllow || d.Via != ScopeUser {
		t.Fatalf("migration preserve not applied: %+v", d)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrSeedGovernance(path, "alice", nil, time.Now()); err == nil {
		t.Fatal("corrupt governance reseeded")
	}
	pol := filepath.Join(dir, "tool-policy.json")
	if err := os.WriteFile(pol, []byte(`{"schema":1,"principal":"alice","deny":["ssh"],"read_only":["file"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	snap, err := LoadToolPolicy(pol)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Denied("ssh") || snap.Denied("file") || !snap.CappedReadOnly("file") {
		t.Fatalf("snapshot: %+v", snap)
	}
	if _, err := LoadToolPolicy(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing policy loaded")
	}
	if err := os.WriteFile(pol, []byte(`{"schema":2,"principal":"alice"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToolPolicy(pol); err == nil {
		t.Fatal("bad schema policy loaded")
	}
}

// TestGovernanceInventoryReconcile: a document seeded before a section shipped
// must not keep that section permanently denied as "unknown". Load reconciles
// the persisted inventory with the shipped catalog — new sections appear with
// shipped defaults and inventoried defaults track the shipped posture (section
// defaults are code-shipped, not operator-mutable; posture narrows only through
// rules). Extra sections the catalog no longer ships are kept, rules untouched.
func TestGovernanceInventoryReconcile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime", "toolhub", "governance.json")
	now := time.Now()
	// Simulate a pre-skills seed: strip the skill sections entirely and flip
	// an existing inventoried default back to a stale shipped posture.
	stale := NewGovernance()
	kept := stale.Inventory[:0]
	for _, section := range stale.Inventory {
		if section.ID == "hub:skills" || section.ID == "hub:skills_manage" {
			continue
		}
		if section.ID == "native:skills" {
			section.Default = RuleAllow
		}
		kept = append(kept, section)
	}
	stale.Inventory = kept
	stale.Inventory = append(stale.Inventory, ToolSection{ID: "hub:legacy", Owner: OwnerHermes, Effect: EffectRead, Default: RuleAllow})
	if err := SaveGovernance(path, stale); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadGovernance(path)
	if err != nil {
		t.Fatal(err)
	}
	section, ok := loaded.Section("hub:skills")
	if !ok {
		t.Fatal("new shipped section missing after reconcile")
	}
	if !section.Official || !section.Prepared {
		t.Fatalf("reconciled section lacks shipped trust flags: %+v", section)
	}
	if d := loaded.Evaluate("alice", "", "hub:skills", nil, now); !d.Allowed() {
		t.Fatalf("hub:skills must evaluate its shipped default, not unknown-deny: %+v", d)
	}
	if section, _ := loaded.Section("native:skills"); section.Default != RuleDeny {
		t.Fatalf("stale inventoried default not synced to shipped posture: %+v", section)
	}
	if _, ok := loaded.Section("hub:legacy"); !ok {
		t.Fatal("unshipped extra section dropped")
	}
}

func TestSectionTrustAndDecisionEffects(t *testing.T) {
	cases := []struct {
		s    ToolSection
		want string
	}{
		{ToolSection{Owner: OwnerHermes, Official: true}, "platform"},
		{ToolSection{Owner: OwnerHermes, Official: true, Dynamic: true}, "external"},
		{ToolSection{Owner: OwnerHub, Official: true}, "hub"},
		{ToolSection{Owner: OwnerOrg, Prepared: true}, "prepared"},
		{ToolSection{Owner: OwnerUser}, "user"},
		{ToolSection{Owner: OwnerExternal}, "external"},
	}
	for _, c := range cases {
		if got := c.s.Trust(); got != c.want {
			t.Fatalf("trust %+v = %q, want %q", c.s, got, c.want)
		}
	}
	allow := PolicyDecision{Effect: RuleAllow}
	capRead := PolicyDecision{Effect: RuleCapRead}
	deny := PolicyDecision{Effect: RuleDeny}
	if !allow.Allowed() || !capRead.Allowed() || deny.Allowed() {
		t.Fatal("Allowed")
	}
	if !allow.AllowsToolEffect(EffectExec) || capRead.AllowsToolEffect(EffectExec) || !capRead.AllowsToolEffect(EffectRead) {
		t.Fatal("AllowsToolEffect")
	}
	if (ToolSection{ID: "BAD ", Owner: OwnerUser, Effect: EffectRead, Default: RuleAllow}).Validate() == nil {
		t.Fatal("bad section id validated")
	}
	var nilGov *Governance
	if d := nilGov.Evaluate("a", "o", "native:web", nil, time.Now()); d.Effect != RuleDeny {
		t.Fatalf("nil governance allowed: %+v", d)
	}
}
