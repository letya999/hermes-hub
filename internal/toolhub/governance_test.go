package toolhub

import (
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

var govRuleSeq int

func govRule(t *testing.T, scope, subject, section, effect string) api.PolicyRule {
	t.Helper()
	govRuleSeq++
	return api.PolicyRule{
		RuleID: "testrule-" + string(rune('a'+govRuleSeq/26)) + string(rune('a'+govRuleSeq%26)),
		Scope:  scope, Subject: subject, Section: section, Effect: effect,
		Reason: "test fixture", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1,
	}
}

func TestGovernanceEvaluateOrdering(t *testing.T) {
	now := time.Now().UTC()
	doc := api.NewGovernance()
	if err := doc.Validate(); err != nil {
		t.Fatalf("seeded document invalid: %v", err)
	}
	// Inventory defaults apply with no rules.
	if d := doc.Evaluate("alice", "", "user_mcp", nil, now); d.Effect != api.RuleDeny || d.Via != "section" {
		t.Fatalf("user_mcp default: %+v", d)
	}
	// SPEC-0083 R3.1: every native toolset defaults to deny — a wish needs an
	// active grant or an explicit allow rule to take effect.
	if d := doc.Evaluate("alice", "", "native:file", nil, now); d.Effect != api.RuleDeny || d.Via != "section" {
		t.Fatalf("native:file default must be deny: %+v", d)
	}
	if d := doc.Evaluate("alice", "", "imaginary:surface", nil, now); d.Effect != api.RuleDeny || d.Via != "unknown" {
		t.Fatalf("unknown section: %+v", d)
	}
	// Scope precedence: user allow beats org deny? No — deny at any scope wins.
	doc.Rules = append(doc.Rules,
		govRule(t, api.ScopeUser, "alice", "native:terminal", api.RuleAllow),
		govRule(t, api.ScopeOrg, "acme", "native:terminal", api.RuleDeny),
	)
	if d := doc.Evaluate("alice", "acme", "native:terminal", nil, now); d.Effect != api.RuleDeny || d.Via != "deny" {
		t.Fatalf("org deny must beat user allow: %+v", d)
	}
	// Without the org the user allow applies.
	if d := doc.Evaluate("alice", "", "native:terminal", nil, now); !d.Allowed() || d.Via != api.ScopeUser {
		t.Fatalf("user allow: %+v", d)
	}
	// Org allow beats global deny? No — deny wins at every real scope.
	doc.Rules = append(doc.Rules, govRule(t, api.ScopeGlobal, "", "native:web", api.RuleDeny), govRule(t, api.ScopeOrg, "acme", "native:web", api.RuleAllow))
	if d := doc.Evaluate("alice", "acme", "native:web", nil, now); d.Effect != api.RuleDeny || d.Via != "deny" {
		t.Fatalf("global deny must beat org allow: %+v", d)
	}
	// Org allow outranks global allow's scope precedence over the section floor.
	doc.Rules = append(doc.Rules, govRule(t, api.ScopeOrg, "acme", "native:terminal", api.RuleDeny), govRule(t, api.ScopeGlobal, "", "native:terminal", api.RuleCapRead))
	if d := doc.Evaluate("bob", "", "native:terminal", nil, now); d.Effect != api.RuleCapRead || d.Via != api.ScopeGlobal {
		t.Fatalf("global cap:read for unmatched org: %+v", d)
	}
	// Default-scope deny is the floor a grant can still lift — not absolute.
	if d := doc.Evaluate("alice", "", "toolhub", map[string]string{"owner": "external"}, now); d.Effect != api.RuleDeny || d.Via != api.ScopeDefault {
		t.Fatalf("external attr deny: %+v", d)
	}
	// Expired rules are inert.
	doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "expired-deny", Scope: api.ScopeGlobal, Section: "native:file", Effect: api.RuleDeny, Reason: "x", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1, ExpiresAt: now.Add(-time.Hour)})
	// Inert means no deny-via-rule — the inventory floor still applies.
	if d := doc.Evaluate("alice", "", "native:file", nil, now); d.Via == "deny" {
		t.Fatalf("expired deny applied: %+v", d)
	}
}

func TestGovernanceCapReadEffect(t *testing.T) {
	doc := api.NewGovernance()
	doc.Rules = append(doc.Rules, govRule(t, api.ScopeUser, "alice", "user_mcp", api.RuleCapRead))
	now := time.Now().UTC()
	d := doc.Evaluate("alice", "", "user_mcp", nil, now)
	if !d.Allowed() || d.AllowsToolEffect("write") || !d.AllowsToolEffect("read") {
		t.Fatalf("cap:read misapplied: %+v", d)
	}
}

func TestGovernanceGrantLifecycle(t *testing.T) {
	now := time.Now().UTC()
	doc := api.NewGovernance()
	grant := api.NewToolGrant("grant-1", "alice", "user_mcp", "host reviewed", "operator", now.Add(time.Hour), nil)
	if err := doc.PutToolGrant(grant, now); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("unconfirmed grant admitted: %v", err)
	}
	mustConfirm(t, &grant)
	if err := doc.PutToolGrant(grant, now); err != nil {
		t.Fatalf("confirmed grant refused: %v", err)
	}
	// Pending grants carry no authority.
	if d := doc.Evaluate("alice", "", "user_mcp", nil, now); d.Allowed() {
		t.Fatalf("pending grant admitted: %+v", d)
	}
	// Request stamps a nonce; acknowledge needs it.
	stamped, err := doc.RequestToolGrant("alice", "grant-1", now)
	if err != nil || stamped.RequestNonce == "" {
		t.Fatalf("request: %v %v", stamped, err)
	}
	if _, err := doc.RequestToolGrant("bob", "grant-1", now); err == nil {
		t.Fatal("cross-principal request")
	}
	if _, err := doc.AcknowledgeToolGrant("alice", "grant-1", "wrong", now); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("bad nonce: %v", err)
	}
	if _, err := doc.AcknowledgeToolGrant("alice", "grant-1", stamped.RequestNonce, now); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if d := doc.Evaluate("alice", "", "user_mcp", nil, now); d.Via != "grant" || !d.Allowed() {
		t.Fatalf("active grant: %+v", d)
	}
	if d := doc.Evaluate("bob", "", "user_mcp", nil, now); d.Allowed() {
		t.Fatalf("grant leaked to another principal: %+v", d)
	}
	// Expired grants deny again.
	if d := doc.Evaluate("alice", "", "user_mcp", nil, now.Add(2*time.Hour)); d.Allowed() {
		t.Fatalf("expired grant admitted: %+v", d)
	}
	// Revoke cuts immediately.
	if err := doc.RevokeToolGrant("grant-1", "operator", "test", now); err != nil {
		t.Fatal(err)
	}
	if d := doc.Evaluate("alice", "", "user_mcp", nil, now); d.Allowed() {
		t.Fatalf("revoked grant admitted: %+v", d)
	}
	if err := doc.RevokeToolGrant("grant-2", "model", "x", now); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("model revoke issuer: %v", err)
	}
}

func TestGovernanceRequestNonceRotation(t *testing.T) {
	now := time.Now().UTC()
	doc := api.NewGovernance()
	grant := api.NewToolGrant("grant-1", "alice", "user_mcp", "r", "operator", now.Add(time.Hour), nil)
	mustConfirm(t, &grant)
	if err := doc.PutToolGrant(grant, now); err != nil {
		t.Fatal(err)
	}
	first, _ := doc.RequestToolGrant("alice", "grant-1", now)
	second, _ := doc.RequestToolGrant("alice", "grant-1", now.Add(time.Minute))
	if first.RequestNonce == second.RequestNonce {
		t.Fatal("nonce reused")
	}
	if _, err := doc.AcknowledgeToolGrant("alice", "grant-1", first.RequestNonce, now.Add(2*time.Minute)); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("stale nonce activated: %v", err)
	}
	// Expired request cannot activate.
	doc.Grants[0].RequestExpires = now.Add(-time.Minute)
	if _, err := doc.AcknowledgeToolGrant("alice", "grant-1", second.RequestNonce, now); err == nil {
		t.Fatal("expired request acknowledged")
	}
}

func TestGovernanceStoreFailClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "governance.json")
	store := NewGovernanceStore(path)
	// Missing document loads the shipped default posture.
	doc, err := store.Load()
	if err != nil || doc == nil {
		t.Fatalf("missing doc: %v", err)
	}
	if d := doc.Evaluate("alice", "", "user_mcp", nil, time.Now()); d.Allowed() {
		t.Fatal("default posture allows user_mcp")
	}
	// Corrupt document is a hard error — callers deny closed.
	if err := os.WriteFile(path, []byte("{nope"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("corrupt doc loaded")
	}
	if err := store.Update(time.Now(), func(*api.Governance) error { return nil }); err == nil {
		t.Fatal("corrupt doc overwritten by Update")
	}
	// A schema-valid but invalid document fails validation on load.
	if err := os.WriteFile(path, []byte(`{"schema":1,"revision":1,"inventory":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("invalid doc loaded: %v", err)
	}
	if NewGovernanceStore("") != nil {
		t.Fatal("empty path must disable the store")
	}
}

func TestGovernanceStoreConcurrentUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "governance.json")
	store := NewGovernanceStore(path)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.Update(time.Now(), func(doc *api.Governance) error {
				doc.Rules = append(doc.Rules, govRule(t, api.ScopeGlobal, "", "native:file", api.RuleAllow))
				return nil
			})
		}()
	}
	wg.Wait()
	doc, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("concurrent writes corrupt: %v", err)
	}
	if len(doc.Rules) == 0 {
		t.Fatal("updates lost")
	}
}

func TestGovernanceValidationEdges(t *testing.T) {
	doc := api.NewGovernance()
	doc.Inventory = append(doc.Inventory, doc.Inventory[0]) // duplicate section
	if err := doc.Validate(); err == nil {
		t.Fatal("duplicate section accepted")
	}
	doc = api.NewGovernance()
	bad := govRule(t, api.ScopeUser, "alice", "user_mcp", api.RuleAllow)
	bad.GrantedBy = "model"
	if err := doc.PutRule(bad, time.Now()); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("model-issued rule: %v", err)
	}
	bad = govRule(t, api.ScopeDefault, "", "user_mcp", api.RuleDeny)
	if err := doc.PutRule(bad, time.Now()); err == nil {
		t.Fatal("user_mcp default-scope deny must stay a section default")
	}
	bad = govRule(t, api.ScopeUser, "", "user_mcp", api.RuleAllow)
	if err := doc.PutRule(bad, time.Now()); err == nil {
		t.Fatal("user rule without subject accepted")
	}
	bad = govRule(t, api.ScopeGlobal, "", "user_mcp", "bogus")
	if err := doc.PutRule(bad, time.Now()); err == nil {
		t.Fatal("unknown effect accepted")
	}
	bad = govRule(t, api.ScopeGlobal, "", "", api.RuleAllow)
	bad.Match = map[string]string{"owner": "external"}
	if err := doc.PutRule(bad, time.Now()); err != nil {
		t.Fatalf("attribute rule refused: %v", err)
	}
	// Revision regression on update rejected.
	bad.Revision = 2
	if err := doc.PutRule(bad, time.Now()); err != nil {
		t.Fatal(err)
	}
	bad.Revision = 2
	if err := doc.PutRule(bad, time.Now()); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("stale revision admitted: %v", err)
	}
}

// TestGovernanceGatewayFiltering exercises the list-time and call-time
// projection fences: the seeded store projects google-work under a
// principal-owned connection (owner=user attrs).
func TestGovernanceGatewayFiltering(t *testing.T) {
	s, auth, _ := seededStore(t)
	tools, err := s.ListProjectedTools(auth)
	if err != nil || len(tools) == 0 {
		t.Fatalf("fixture projection: %v %v", tools, err)
	}
	dir := t.TempDir()
	govPath := filepath.Join(dir, "governance.json")
	doc := api.NewGovernance()
	if err := api.SaveGovernance(govPath, doc); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: s, Governance: NewGovernanceStore(govPath)}
	filtered, err := g.filterProjectedTools(auth, tools)
	if err != nil || len(filtered) != len(tools) {
		t.Fatalf("default posture filtered user-owned tool: %v %v", filtered, err)
	}
	// Deny the owner=user attribute class at global scope.
	doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "deny-user-tools", Scope: api.ScopeGlobal, Match: map[string]string{"owner": "user"}, Effect: api.RuleDeny, Reason: "test", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	if err := api.SaveGovernance(govPath, doc); err != nil {
		t.Fatal(err)
	}
	filtered, err = g.filterProjectedTools(auth, tools)
	if err != nil || len(filtered) != 0 {
		t.Fatalf("owner=user deny not applied: %v %v", filtered, err)
	}
	// cap:read keeps read tools, drops write tools.
	doc.Rules = doc.Rules[:len(doc.Rules)-1]
	doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "cap-user", Scope: api.ScopeGlobal, Match: map[string]string{"owner": "user"}, Effect: api.RuleCapRead, Reason: "test", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	if err := api.SaveGovernance(govPath, doc); err != nil {
		t.Fatal(err)
	}
	filtered, err = g.filterProjectedTools(auth, tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range filtered {
		if tool.Tool.Effect != api.ReadEffect {
			t.Fatalf("write tool %s survived cap:read", tool.Name)
		}
	}
	if len(filtered) == 0 {
		t.Fatal("cap:read dropped read tools")
	}
	// admitGoverned denies what the list hides.
	admit := g.admitGoverned(auth, func(api.ProjectedTool, api.EffectiveBinding) error { return nil })
	for _, tool := range tools {
		_, effective, rerr := s.ResolveProjectedTool(auth, tool.Name)
		if rerr != nil {
			t.Fatal(rerr)
		}
		err := admit(tool, effective)
		if tool.Tool.Effect == api.ReadEffect && err != nil {
			t.Fatalf("read tool denied under cap:read: %v", err)
		}
		if tool.Tool.Effect != api.ReadEffect && !errors.Is(err, api.ErrUnauthorized) {
			t.Fatalf("write tool admitted under cap:read: %v", err)
		}
	}
	// Corrupt governance fails closed on both paths.
	if err := os.WriteFile(govPath, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := g.filterProjectedTools(auth, tools); err == nil {
		t.Fatal("corrupt governance filtered permissively")
	}
	if err := admit(tools[0], api.EffectiveBinding{}); err == nil {
		t.Fatal("corrupt governance admitted a call")
	}
}

func TestGovernanceNilStoreIsLegacyPermissive(t *testing.T) {
	s, auth, _ := seededStore(t)
	g := &Gateway{Store: s} // no Governance: pre-governance deployment
	tools, err := s.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := g.filterProjectedTools(auth, tools)
	if err != nil || len(filtered) != len(tools) {
		t.Fatalf("nil governance filtered: %v", err)
	}
	admit := g.admitGoverned(auth, func(api.ProjectedTool, api.EffectiveBinding) error { return nil })
	if err := admit(tools[0], api.EffectiveBinding{}); err != nil {
		t.Fatalf("nil governance denied: %v", err)
	}
}

func TestToolPolicySnapshot(t *testing.T) {
	p := &api.ToolPolicySnapshot{Schema: 1, Principal: "alice", Deny: []string{"terminal"}, ReadOnly: []string{"files"}}
	if !p.Denied("terminal") || p.Denied("files") || !p.CappedReadOnly("files") {
		t.Fatal("snapshot accessors")
	}
	p.DenyAll = true
	if !p.Denied("files") {
		t.Fatal("deny_all")
	}
	var nilSnap *api.ToolPolicySnapshot
	if nilSnap.Denied("x") || nilSnap.CappedReadOnly("x") {
		t.Fatal("nil snapshot must not deny")
	}
	path := filepath.Join(t.TempDir(), "tool-policy.json")
	if _, err := api.LoadToolPolicy(path); err == nil {
		t.Fatal("missing snapshot loaded")
	}
	if err := os.WriteFile(path, []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := api.LoadToolPolicy(path); err == nil {
		t.Fatal("corrupt snapshot loaded")
	}
	body, _ := json.Marshal(api.ToolPolicySnapshot{Schema: 1, Principal: "alice", Deny: []string{"ssh"}})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := api.LoadToolPolicy(path)
	if err != nil || !loaded.Denied("ssh") {
		t.Fatalf("snapshot load: %v", err)
	}
}

func TestGrantRequestControlOp(t *testing.T) {
	govPath := filepath.Join(t.TempDir(), "governance.json")
	govStore := NewGovernanceStore(govPath)
	now := time.Now().UTC()
	// Seed one pending grant for alice on user_mcp.
	grant := api.NewToolGrant("grant-1", "alice", "user_mcp", "host approved", "operator", now.Add(time.Hour), nil)
	mustConfirm(t, &grant)
	if err := govStore.Update(now, func(doc *api.Governance) error { return doc.PutToolGrant(grant, now) }); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: NewStore(), Governance: govStore, Listen: "127.0.0.1:8090", Now: time.Now}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	bob := identity.TelegramEnvelope("bob", 7, "runtime", "policy-1")
	for _, principal := range []string{auth.PrincipalID, bob.PrincipalID} {
		if err := putGrant(t, control.Store, api.OperatorControlGrant(principal, "grant_request")); err != nil {
			t.Fatal(err)
		}
	}
	out, err := control.Invoke(t.Context(), auth, "grant_request", map[string]any{"section": "user_mcp"})
	if err != nil {
		t.Fatalf("grant_request: %v", err)
	}
	ackURL, _ := out["acknowledge_url"].(string)
	if !strings.Contains(ackURL, "/grants/grant-1?nonce=") {
		t.Fatalf("ack url: %v", out)
	}
	// Bob sees no pending grant for the same document.
	if _, err := control.Invoke(t.Context(), bob, "grant_request", map[string]any{"section": "user_mcp"}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("cross-principal request: %v", err)
	}
	// A second request rotates the nonce.
	out2, err := control.Invoke(t.Context(), auth, "grant_request", map[string]any{"section": "user_mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if out2["acknowledge_url"] == ackURL {
		t.Fatal("nonce not rotated")
	}
	// No governance → op refuses.
	if _, err := (&ControlPlane{Store: NewStore()}).Invoke(t.Context(), auth, "grant_request", map[string]any{}); err == nil {
		t.Fatal("grant_request without governance store")
	}
}

func TestServeGrantAckForm(t *testing.T) {
	govPath := filepath.Join(t.TempDir(), "governance.json")
	govStore := NewGovernanceStore(govPath)
	now := time.Now().UTC()
	grant := api.NewToolGrant("grant-1", "alice", "user_mcp", "host approved", "operator", now.Add(time.Hour), nil)
	mustConfirm(t, &grant)
	if err := govStore.Update(now, func(doc *api.Governance) error { return doc.PutToolGrant(grant, now) }); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: NewStore(), Governance: govStore, Listen: "127.0.0.1:8090", Now: time.Now}
	g := &Gateway{Store: control.Store, Control: control, Governance: govStore}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	if err := putGrant(t, control.Store, api.OperatorControlGrant(auth.PrincipalID, "grant_request")); err != nil {
		t.Fatal(err)
	}
	out, err := control.Invoke(t.Context(), auth, "grant_request", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	ackURL, _ := out["acknowledge_url"].(string)
	parsed, err := url.Parse(ackURL)
	if err != nil {
		t.Fatal(err)
	}
	nonce := parsed.Query().Get("nonce")
	server := httptest.NewServer(http.HandlerFunc(g.serveGrantAck))
	defer server.Close()
	// GET with the nonce renders the grant form.
	req, _ := http.NewRequest(http.MethodGet, server.URL+parsed.Path+"?nonce="+url.QueryEscape(nonce), nil)
	req.Host = "127.0.0.1"
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 8192)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body[:n]), "user_mcp") {
		t.Fatalf("grant form: %d %s", resp.StatusCode, body[:n])
	}
	// GET with a bad nonce refuses.
	resp, err = server.Client().Get(server.URL + parsed.Path + "?nonce=deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad nonce GET: %d", resp.StatusCode)
	}
	// POST with the nonce activates the grant.
	resp, err = server.Client().PostForm(server.URL+parsed.Path, url.Values{"nonce": {nonce}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ack POST: %d", resp.StatusCode)
	}
	doc, _ := govStore.Load()
	var active bool
	for _, gr := range doc.Grants {
		if gr.GrantID == "grant-1" {
			active = gr.Status == api.GrantActive
		}
	}
	if !active {
		t.Fatal("acknowledge did not activate")
	}
	// Replaying the consumed nonce fails.
	resp, err = server.Client().PostForm(server.URL+parsed.Path, url.Values{"nonce": {nonce}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("replayed nonce accepted")
	}
}

// Section axes are facts; the trust tier is derived from them — a stored
// "trusted" label would let a tampered document launder authority.
func TestToolSectionTrustDerivation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		section api.ToolSection
		want    string
	}{
		{"platform native", api.ToolSection{Owner: api.OwnerHermes, Official: true}, "platform"},
		{"platform dynamic falls out", api.ToolSection{Owner: api.OwnerHermes, Official: true, Dynamic: true}, "external"},
		{"hub owned", api.ToolSection{Owner: api.OwnerHub, Official: true}, "hub"},
		{"unofficial hub falls out", api.ToolSection{Owner: api.OwnerHub}, "external"},
		{"prepared org", api.ToolSection{Owner: api.OwnerOrg, Prepared: true}, "prepared"},
		{"unprepared org falls out", api.ToolSection{Owner: api.OwnerOrg}, "external"},
		{"user owned", api.ToolSection{Owner: api.OwnerUser}, "user"},
		{"external", api.ToolSection{Owner: api.OwnerExternal}, "external"},
	} {
		if got := tc.section.Trust(); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	// Attributes are the match surface rules and grants see.
	attrs := api.ToolSection{Owner: api.OwnerHub, Official: true, Dynamic: true, Prepared: true, Effect: api.EffectExec}.SectionAttrs()
	for key, want := range map[string]string{"owner": api.OwnerHub, "official": "true", "dynamic": "true", "prepared": "true", "effect": api.EffectExec} {
		if attrs[key] != want {
			t.Fatalf("attr %s: %q", key, attrs[key])
		}
	}
}

// Section() resolves inventoried ids and nothing else; the seeded catalog
// itself must pass validation (a corrupt seed would fail every space closed).
func TestSeedInventoryIntegrity(t *testing.T) {
	doc := api.NewGovernance()
	if err := doc.Validate(); err != nil {
		t.Fatalf("seeded inventory invalid: %v", err)
	}
	for _, id := range []string{api.SectionUserMCP, api.SectionOrgMCP, api.SectionToolHub, api.SectionHubTools, api.SectionHubBrowser, "native:terminal"} {
		if _, ok := doc.Section(id); !ok {
			t.Fatalf("inventory missing %s", id)
		}
	}
	if _, ok := doc.Section("native:imaginary"); ok {
		t.Fatal("unknown id resolved")
	}
}

// SPEC-0083 R3.1: every native:* capability starts denied; only an explicit
// workspace wish plus a grant admits it. Hub-owned sections keep their own
// defaults.
func TestSeedInventoryNativeSectionsDefaultDeny(t *testing.T) {
	for _, sec := range api.SeedInventory() {
		if strings.HasPrefix(sec.ID, "native:") && sec.Default != api.RuleDeny {
			t.Fatalf("%s defaults to %s, want deny", sec.ID, sec.Default)
		}
	}
}

// migrationseed records preserved sections as user-scope allows attributed to
// the migration actor; the document must validate and evaluate them.
func TestMigrationSeedPreserves(t *testing.T) {
	now := time.Now().UTC()
	doc := api.MigrationSeed("alice", []string{"native:terminal", "native:code_execution"}, now)
	if err := doc.Validate(); err != nil {
		t.Fatalf("migration seed invalid: %v", err)
	}
	if d := doc.Evaluate("alice", "", "native:terminal", nil, now); !d.Allowed() || d.Via != api.ScopeUser {
		t.Fatalf("preserved section: %+v", d)
	}
	if d := doc.Evaluate("bob", "", "native:terminal", nil, now); d.Allowed() {
		t.Fatalf("preservation leaked to bob: %+v", d)
	}
	var migrates int
	for _, h := range doc.History {
		if h.Kind == "migrate" && h.By == "migration" {
			migrates++
		}
	}
	if migrates != 2 {
		t.Fatalf("migration history: %d entries", migrates)
	}
}

// LoadOrSeedGovernance: existing documents load; absent paths seed and
// persist; corrupt files fail closed; unreadable store still evaluates in
// memory.
func TestLoadOrSeedGovernance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "governance.json")
	now := time.Now().UTC()
	doc, err := api.LoadOrSeedGovernance(path, "alice", []string{"native:terminal"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("seed not persisted")
	}
	if d := doc.Evaluate("alice", "", "native:terminal", nil, now); !d.Allowed() {
		t.Fatalf("seeded preserve not evaluated: %+v", d)
	}
	// Second load returns the persisted document, not a fresh seed.
	again, err := api.LoadOrSeedGovernance(path, "alice", nil, now)
	if err != nil || again.Revision != doc.Revision {
		t.Fatalf("reload: %v %+v", err, again)
	}
	// Corrupt file fails closed.
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := api.LoadOrSeedGovernance(path, "alice", nil, now); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("corrupt doc: %v", err)
	}
	// A path the process cannot write still gets the in-memory seed.
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(blocked, 0500); err == nil {
		inner := filepath.Join(blocked, "governance.json")
		doc, err := api.LoadOrSeedGovernance(inner, "alice", nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if d := doc.Evaluate("alice", "", "user_mcp", nil, now); d.Allowed() {
			t.Fatal("in-memory seed allows user_mcp")
		}
		os.Chmod(blocked, 0700) // cleanup for windows tempdir removal
	}
}

// PendingToolGrants returns only this principal's live pending grants,
// newest first.
func TestPendingToolGrants(t *testing.T) {
	now := time.Now().UTC()
	doc := api.NewGovernance()
	mk := func(id, principal string, createdAt time.Time, status string) {
		grant := api.NewToolGrant(id, principal, "user_mcp", "r", "operator", now.Add(time.Hour), nil)
		grant.CreatedAt = createdAt
		grant.Status = status
		if status == api.GrantPending {
			mustConfirm(t, &grant)
		}
		doc.Grants = append(doc.Grants, grant)
	}
	mk("grant-1", "alice", now.Add(-time.Hour), api.GrantPending)
	mk("grant-2", "alice", now, api.GrantPending)
	mk("grant-3", "alice", now, api.GrantActive)
	mk("grant-4", "bob", now, api.GrantPending)
	expired := api.NewToolGrant("grant-5", "alice", "user_mcp", "r", "operator", now.Add(-time.Minute), nil)
	doc.Grants = append(doc.Grants, expired)
	pending := doc.PendingToolGrants("alice", "", now)
	if len(pending) != 2 || pending[0].GrantID != "grant-2" || pending[1].GrantID != "grant-1" {
		t.Fatalf("pending: %+v", pending)
	}
	if p := doc.PendingToolGrants("alice", "native:terminal", now); len(p) != 0 {
		t.Fatalf("section filter: %+v", p)
	}
}

// PutToolGrant enforces the lifecycle: pending-or-revoked entry, revision
// advance on re-registration, confirmed pending only.
func TestPutToolGrantBranches(t *testing.T) {
	now := time.Now().UTC()
	doc := api.NewGovernance()
	// Active grants cannot enter through PutToolGrant.
	active := api.NewToolGrant("grant-1", "alice", "user_mcp", "r", "operator", now.Add(time.Hour), nil)
	active.Status = api.GrantActive
	if err := doc.PutToolGrant(active, now); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("active entry: %v", err)
	}
	// Register pending, then re-register with a stale revision → conflict.
	grant := api.NewToolGrant("grant-1", "alice", "user_mcp", "r", "operator", now.Add(time.Hour), nil)
	mustConfirm(t, &grant)
	if err := doc.PutToolGrant(grant, now); err != nil {
		t.Fatal(err)
	}
	grant.Status = api.GrantRevoked
	grant.Revision = 1
	if err := doc.PutToolGrant(grant, now); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("stale re-registration: %v", err)
	}
	// Revocation via PutToolGrant with advanced revision succeeds.
	grant.Revision = 2
	if err := doc.PutToolGrant(grant, now); err != nil {
		t.Fatal(err)
	}
	if doc.Grants[0].Status != api.GrantRevoked {
		t.Fatal("re-registration lost")
	}
	// grant validation rejects malformed records inside PutToolGrant.
	bad := api.NewToolGrant("grant-2", "alice", "user_mcp", "r", "operator", time.Time{}, nil)
	if err := doc.PutToolGrant(bad, now); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("missing expiry: %v", err)
	}
	// RequestToolGrant on a missing id is a not-found.
	if _, err := doc.RequestToolGrant("alice", "grant-9", now); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("missing request: %v", err)
	}
	if _, err := doc.AcknowledgeToolGrant("alice", "grant-9", "n", now); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("missing acknowledge: %v", err)
	}
	if err := doc.RevokeToolGrant("grant-9", "operator", "x", now); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("missing revoke: %v", err)
	}
}

// The executor snapshot: Denied/CappedReadOnly semantics and the fail-closed
// loader (missing, oversized, malformed, wrong schema).
func TestToolPolicySnapshotContract(t *testing.T) {
	var nilSnapshot *api.ToolPolicySnapshot
	if nilSnapshot.Denied("file") || nilSnapshot.CappedReadOnly("file") {
		t.Fatal("nil snapshot decides")
	}
	p := &api.ToolPolicySnapshot{Schema: 1, Principal: "alice", Deny: []string{"ssh"}, ReadOnly: []string{"file"}}
	if !p.Denied("ssh") || p.Denied("file") || !p.CappedReadOnly("file") || p.CappedReadOnly("ssh") {
		t.Fatal("snapshot semantics")
	}
	all := &api.ToolPolicySnapshot{Schema: 1, Principal: "alice", DenyAll: true, ReadOnly: []string{"file"}}
	if !all.Denied("file") || all.CappedReadOnly("file") {
		t.Fatal("deny_all semantics")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	if _, err := api.LoadToolPolicy(path); err == nil {
		t.Fatal("missing snapshot loaded")
	}
	if err := os.WriteFile(path, []byte("{nope"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := api.LoadToolPolicy(path); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("corrupt snapshot: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":2,"principal":"alice"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := api.LoadToolPolicy(path); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("wrong schema: %v", err)
	}
	body, _ := json.Marshal(p)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := api.LoadToolPolicy(path)
	if err != nil || loaded.Principal != "alice" || !loaded.Denied("ssh") {
		t.Fatalf("load: %v %+v", err, loaded)
	}
}

// Rule and grant validation reject malformed records so a hand-edited
// document fails closed at load.
func TestGovernanceValidateEdges(t *testing.T) {
	now := time.Now().UTC()
	base := func() *api.Governance { return api.NewGovernance() }
	// Bad rule: both section and match, or neither.
	doc := base()
	doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "bad-1", Scope: api.ScopeGlobal, Section: "x", Match: map[string]string{"a": "b"}, Effect: api.RuleAllow, Reason: "r", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	if err := doc.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("section+match rule: %v", err)
	}
	doc = base()
	doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "bad-2", Scope: api.ScopeGlobal, Effect: api.RuleAllow, Reason: "r", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	if err := doc.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("targetless rule: %v", err)
	}
	// User-scope deny of user_mcp is allowed; default-scope is not.
	doc = base()
	doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "bad-3", Scope: api.ScopeDefault, Section: api.SectionUserMCP, Effect: api.RuleDeny, Reason: "r", GrantedBy: "operator", Status: api.ActiveStatus, Revision: 1})
	if err := doc.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("default user_mcp deny: %v", err)
	}
	// Model-issued rules never validate.
	doc = base()
	doc.Rules = append(doc.Rules, api.PolicyRule{RuleID: "bad-4", Scope: api.ScopeGlobal, Section: "native:file", Effect: api.RuleAllow, Reason: "r", GrantedBy: "model", Status: api.ActiveStatus, Revision: 1})
	if err := doc.Validate(); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("model rule: %v", err)
	}
	// grant without expiry, model issuer, oversized match map.
	doc = base()
	doc.Grants = append(doc.Grants, api.ToolGrant{Schema: api.SchemaVersion, GrantID: "g1", PrincipalID: "alice", Section: "user_mcp", Reason: "r", GrantedBy: "operator", Status: api.GrantPending, Revision: 1})
	if err := doc.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("expiry-less grant: %v", err)
	}
	doc = base()
	grant := api.NewToolGrant("g2", "alice", "user_mcp", "r", "model", now.Add(time.Hour), nil)
	doc.Grants = append(doc.Grants, grant)
	if err := doc.Validate(); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("model grant: %v", err)
	}
	doc = base()
	wide := api.NewToolGrant("g3", "alice", "user_mcp", "r", "operator", now.Add(time.Hour), map[string]string{})
	for i := 0; i < 9; i++ {
		wide.Match[string(rune('a'+i))] = "v"
	}
	doc.Grants = append(doc.Grants, wide)
	if err := doc.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("wide match grant: %v", err)
	}
}

// projectedToolAttrs assigns the trust axes each projection is judged under:
// hub executors are hub-owned, owner-typed connections inherit their owner's
// tier, reviewed/prepared artifacts read as org-authored, everything else is
// external.
func TestProjectedToolAttrs(t *testing.T) {
	binding := func(def api.ToolDefinition, conn *api.Connection) api.EffectiveBinding {
		return api.EffectiveBinding{Definition: def, Connection: conn}
	}
	// Hub executor → hub-owned and prepared.
	attrs := projectedToolAttrs(binding(api.ToolDefinition{DefinitionID: "d1", Transport: api.AgentTools}, nil), api.ToolSpec{Name: "x", Effect: api.ReadEffect})
	if attrs["owner"] != api.OwnerHub || attrs["prepared"] != "true" || attrs["official"] != "true" || attrs["transport"] != string(api.AgentTools) {
		t.Fatalf("hub attrs: %v", attrs)
	}
	// Principal-owned connection → user.
	conn := &api.Connection{Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}}
	attrs = projectedToolAttrs(binding(api.ToolDefinition{DefinitionID: "d2", Transport: "remote-mcp"}, conn), api.ToolSpec{Name: "x", Effect: api.WriteEffect})
	if attrs["owner"] != api.OwnerUser || attrs["prepared"] != "false" || attrs["official"] != "false" {
		t.Fatalf("user attrs: %v", attrs)
	}
	// Context-owned connection → org.
	conn = &api.Connection{Owner: api.OwnerRef{Type: api.ContextOwner, ID: "acme"}}
	attrs = projectedToolAttrs(binding(api.ToolDefinition{DefinitionID: "d3", Transport: "remote-mcp"}, conn), api.ToolSpec{Name: "x"})
	if attrs["owner"] != api.OwnerOrg {
		t.Fatalf("org attrs: %v", attrs)
	}
	// Reviewed artifact without a connection → org; unreviewed → external.
	attrs = projectedToolAttrs(binding(api.ToolDefinition{DefinitionID: "d4", Source: api.DefinitionSource{ReviewDigest: "sha256:x"}}, nil), api.ToolSpec{Name: "x"})
	if attrs["owner"] != api.OwnerOrg || attrs["official"] != "true" {
		t.Fatalf("reviewed attrs: %v", attrs)
	}
	attrs = projectedToolAttrs(binding(api.ToolDefinition{DefinitionID: "d5"}, nil), api.ToolSpec{Name: "x", Effect: api.WriteEffect})
	if attrs["owner"] != api.OwnerExternal {
		t.Fatalf("external attrs: %v", attrs)
	}
	// Definition id and per-tool effect ride into the attribute map.
	if attrs["definition_id"] != "d5" || attrs["effect"] != string(api.WriteEffect) {
		t.Fatalf("identity attrs: %v", attrs)
	}
}

// serveGrantAck refuses before touching the store: non-loopback hosts, bad
// grant ids, unknown grants, wrong methods, unreadable documents.
func TestServeGrantAckRefusals(t *testing.T) {
	govPath := filepath.Join(t.TempDir(), "governance.json")
	govStore := NewGovernanceStore(govPath)
	control := &ControlPlane{Store: NewStore(), Governance: govStore, Listen: "127.0.0.1:8090", Now: time.Now}
	g := &Gateway{Store: control.Store, Control: control, Governance: govStore}
	server := httptest.NewServer(http.HandlerFunc(g.serveGrantAck))
	defer server.Close()
	get := func(url string) int {
		resp, err := server.Client().Get(url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// Missing/malformed grant id → 404; unknown id → 404.
	if code := get(server.URL + "/grants/"); code != http.StatusNotFound {
		t.Fatalf("empty id: %d", code)
	}
	if code := get(server.URL + "/grants/../x"); code != http.StatusNotFound {
		t.Fatalf("path traversal id: %d", code)
	}
	if code := get(server.URL + "/grants/nonexistent?nonce=n"); code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", code)
	}
	// Wrong method on a valid id shape → 405 only when a grant exists; seed one.
	now := time.Now().UTC()
	grant := api.NewToolGrant("grant-1", "alice", "user_mcp", "r", "operator", now.Add(time.Hour), nil)
	mustConfirm(t, &grant)
	if err := govStore.Update(now, func(doc *api.Governance) error { return doc.PutToolGrant(grant, now) }); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, server.URL+"/grants/grant-1?nonce=n", nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	// Corrupt the doc → 503.
	if err := os.WriteFile(govPath, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := get(server.URL + "/grants/grant-1?nonce=n"); code != http.StatusServiceUnavailable {
		t.Fatalf("corrupt doc: %d", code)
	}
	// No governance configured → 404.
	bare := &Gateway{Store: NewStore(), Control: &ControlPlane{Store: NewStore()}}
	bareServer := httptest.NewServer(http.HandlerFunc(bare.serveGrantAck))
	defer bareServer.Close()
	if code := get(bareServer.URL + "/grants/grant-1?nonce=n"); code != http.StatusNotFound {
		t.Fatalf("no governance: %d", code)
	}
}

// grant_request rejects malformed sections, a grant_id owned by another
// principal, and an absent pending grant before it stamps anything.
func TestGrantRequestRefusals(t *testing.T) {
	govPath := filepath.Join(t.TempDir(), "governance.json")
	govStore := NewGovernanceStore(govPath)
	now := time.Now().UTC()
	control := &ControlPlane{Store: NewStore(), Governance: govStore, Listen: "127.0.0.1:8090", Now: time.Now}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	bob := identity.TelegramEnvelope("bob", 7, "runtime", "policy-1")
	for _, principal := range []string{auth.PrincipalID, bob.PrincipalID} {
		if err := putGrant(t, control.Store, api.OperatorControlGrant(principal, "grant_request")); err != nil {
			t.Fatal(err)
		}
	}
	ctx := t.Context()
	if _, err := control.Invoke(ctx, auth, "grant_request", map[string]any{"section": "Bad Section!!"}); err == nil {
		t.Fatal("bad section accepted")
	}
	// Bob's pending grant is invisible to alice via grant_id.
	grant := api.NewToolGrant("grant-b", "bob", "user_mcp", "r", "operator", now.Add(time.Hour), nil)
	mustConfirm(t, &grant)
	if err := govStore.Update(now, func(doc *api.Governance) error { return doc.PutToolGrant(grant, now) }); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(ctx, auth, "grant_request", map[string]any{"section": "user_mcp", "grant_id": "grant-b"}); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("cross-principal grant_id: %v", err)
	}
	// Alice has no pending grant at all.
	if _, err := control.Invoke(ctx, auth, "grant_request", map[string]any{"section": "org_mcp"}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("no pending: %v", err)
	}
}
