package toolhub

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// rightsVocabulary is a synthetic slice of the SPEC-0092 table covering the
// shapes Decide must distinguish: self ops, a cross op and a scoped op.
func rightsVocabulary() api.RightsDoc {
	selfIssuers := []string{"platform", "org", "operator"}
	crossIssuers := []string{"platform", "org"}
	op := func(subject, scope string, issuers []string) api.RightOperation {
		return api.RightOperation{Subject: subject, Scope: scope, Issuers: issuers, PEP: []string{"test"}, Effect: "read", Description: "fixture"}
	}
	return api.RightsDoc{Release: "test", DefinitionID: "hub-agent-rights", Capabilities: map[string]api.RightCapability{
		"workspace": {Description: "fixture", Operations: map[string]api.RightOperation{
			"read":       op("self", "path", selfIssuers),
			"write":      op("self", "path", selfIssuers),
			"read_other": op("cross", "path", crossIssuers),
		}},
		"models": {Description: "fixture", Operations: map[string]api.RightOperation{
			"switch": op("self", "model", selfIssuers),
		}},
		"observability": {Description: "fixture", Operations: map[string]api.RightOperation{
			"read_own_logs": op("self", "log", selfIssuers),
		}},
		"tools": {Description: "fixture", Operations: map[string]api.RightOperation{
			"use":       op("self", "none", selfIssuers),
			"configure": op("self", "none", selfIssuers),
		}},
	}}
}

// rightsFixture builds a store with the vocabulary wired and a governance
// ledger on a temp path, plus an unmanaged envelope.
func rightsFixture(t *testing.T) (*Store, *GovernanceStore, identity.Envelope) {
	t.Helper()
	store, auth, _ := seededStore(t)
	store.RightsDoc = rightsVocabulary()
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	return store, gov, auth
}

func mintRight(t *testing.T, gov *GovernanceStore, doc api.RightsDoc, grant api.RightGrant) {
	t.Helper()
	if err := api.Confirm(&grant, grant.GrantedBy, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(grant, doc, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func grantToolUse(t *testing.T, store *Store, auth identity.Envelope) *GovernanceStore {
	t.Helper()
	store.RightsDoc = rightsVocabulary()
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	mintRight(t, gov, store.RightsDoc, api.NewRightGrant("rightgrant-"+auth.PrincipalID+"-use", auth.PrincipalID, "tools", "use", "", "fixture", "operator", time.Now().Add(time.Hour)))
	return gov
}

func grantDefinitionRights(t *testing.T, store *Store, auth identity.Envelope, definition api.ToolDefinition) {
	t.Helper()
	doc := api.RightsDoc{Release: "fixture", DefinitionID: "hub-agent-rights", Capabilities: map[string]api.RightCapability{}}
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	allow := map[string][]string{}
	for _, tool := range definition.Tools {
		for _, right := range append([]string{"tools.use"}, tool.Requires...) {
			capability, operation, ok := strings.Cut(right, ".")
			if !ok {
				t.Fatalf("invalid required right %q", right)
			}
			group := doc.Capabilities[capability]
			if group.Operations == nil {
				group.Operations = map[string]api.RightOperation{}
			}
			group.Operations[operation] = api.RightOperation{Subject: "self", Scope: "none", Issuers: []string{"platform"}, PEP: []string{"test"}, Effect: "read", Description: "fixture"}
			doc.Capabilities[capability] = group
			if !slices.Contains(allow[capability], operation) {
				allow[capability] = append(allow[capability], operation)
			}
		}
	}
	store.RightsDoc = doc
	for capability, operations := range allow {
		for _, operation := range operations {
			mintRight(t, gov, doc, api.NewRightGrant(api.DeterministicID("rightgrant", auth.PrincipalID, capability, operation), auth.PrincipalID, capability, operation, "", "fixture", "platform", time.Now().Add(time.Hour)))
		}
	}
	if err := DeriveRights(store, gov, doc, auth.PrincipalID, allow, auth.Environment, auth.CapabilityProfile, auth.PolicyVersion, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestDecideDeniesMissingOperation(t *testing.T) {
	store, _, auth := rightsFixture(t)
	if d, err := store.Decide(auth, "workspace", "delete_all", "", time.Now()); err != nil || d.Allow || d.Reason != "unknown capability.operation" {
		t.Fatalf("unknown operation must deny: %+v %v", d, err)
	}
	if d, err := store.Decide(auth, "nonsense", "read", "", time.Now()); err != nil || d.Allow {
		t.Fatalf("unknown domain must deny: %+v %v", d, err)
	}
	if d, err := store.Decide(auth, "workspace", "read", "", time.Now()); err != nil || d.Allow || d.Reason != "no active grant" {
		t.Fatalf("known right without a ledger grant must deny: %+v %v", d, err)
	}
}

func TestDecideAllowsActiveGrant(t *testing.T) {
	store, gov, auth := rightsFixture(t)
	doc := store.RightsDoc
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-read", auth.PrincipalID, "workspace", "read", "", "fixture grant", "operator", time.Now().Add(time.Hour)))
	d, err := store.Decide(auth, "workspace", "read", "/workspace/work", time.Now())
	if err != nil || !d.Allow {
		t.Fatalf("active grant must allow: %+v %v", d, err)
	}
	if d.Epoch == 0 {
		t.Fatal("decision must carry the ledger epoch")
	}
	// Scoped grants bind a path: a sibling path outside the grant denies.
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-scoped", auth.PrincipalID, "workspace", "write", "/workspace/work", "scoped fixture", "operator", time.Now().Add(time.Hour)))
	if d, _ := store.Decide(auth, "workspace", "write", "/workspace/work/file.txt", time.Now()); !d.Allow {
		t.Fatalf("scoped grant must cover its subtree: %+v", d)
	}
	if d, _ := store.Decide(auth, "workspace", "write", "/workspace/other/file.txt", time.Now()); d.Allow {
		t.Fatalf("scoped grant must not cover a sibling path: %+v", d)
	}
}

func TestDecideRevokeEpochImmediate(t *testing.T) {
	store, gov, auth := rightsFixture(t)
	doc := store.RightsDoc
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-switch", auth.PrincipalID, "models", "switch", "", "fixture", "operator", time.Now().Add(time.Hour)))
	d1, err := store.Decide(auth, "models", "switch", "", time.Now())
	if err != nil || !d1.Allow {
		t.Fatalf("grant must allow: %+v %v", d1, err)
	}
	if err := gov.RevokeRight("rightgrant-alice-switch", "operator", "test revoke", time.Now()); err != nil {
		t.Fatal(err)
	}
	d2, err := store.Decide(auth, "models", "switch", "", time.Now())
	if err != nil || d2.Allow {
		t.Fatalf("revoked grant must deny without republish: %+v %v", d2, err)
	}
	if d2.Epoch <= d1.Epoch {
		t.Fatalf("revoke must advance the rights epoch: %d -> %d", d1.Epoch, d2.Epoch)
	}
}

func TestCrossUserOperationIsSeparate(t *testing.T) {
	store, gov, auth := rightsFixture(t)
	doc := store.RightsDoc
	// Cross-subject rights are never operator-minted and always scope-bound.
	unscoped := api.NewRightGrant("rightgrant-cross-open", auth.PrincipalID, "workspace", "read_other", "", "fixture", "org", time.Now().Add(time.Hour))
	if err := api.Confirm(&unscoped, "org", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(unscoped, doc, time.Now()); err == nil {
		t.Fatal("cross-subject right without a scope target must be rejected")
	}
	operator := api.NewRightGrant("rightgrant-cross-op", auth.PrincipalID, "workspace", "read_other", "bob", "fixture", "operator", time.Now().Add(time.Hour))
	if err := api.Confirm(&operator, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(operator, doc, time.Now()); err == nil {
		t.Fatal("operator must not mint a cross-subject right")
	}
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-cross-bob", auth.PrincipalID, "workspace", "read_other", "bob", "fixture", "org", time.Now().Add(time.Hour)))
	if d, _ := store.Decide(auth, "workspace", "read_other", "", time.Now()); d.Allow {
		t.Fatalf("cross right must deny an unscoped call: %+v", d)
	}
	if d, _ := store.Decide(auth, "workspace", "read_other", "bob", time.Now()); !d.Allow {
		t.Fatalf("cross right must allow the named target: %+v", d)
	}
	if d, _ := store.Decide(auth, "workspace", "read_other", "carol", time.Now()); d.Allow {
		t.Fatalf("cross right must not leak to another principal: %+v", d)
	}
}

func TestDecideCeilingFromManagedProfile(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	store.RightsDoc = rightsVocabulary()
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	doc := store.RightsDoc
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-read", auth.PrincipalID, "workspace", "read", "", "fixture", "operator", time.Now().Add(time.Hour)))
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-write", auth.PrincipalID, "workspace", "write", "", "fixture", "operator", time.Now().Add(time.Hour)))
	// Profile ceiling carries only workspace.read: the ledger grant for
	// write exists but the published revision never admitted it.
	profile.Rights = []api.RightRule{{Capability: "workspace", Operation: "read"}}
	profile.Revision++
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	if d, _ := store.Decide(auth, "workspace", "read", "", time.Now()); !d.Allow {
		t.Fatalf("ceiling + ledger grant must allow: %+v", d)
	}
	if d, _ := store.Decide(auth, "workspace", "write", "", time.Now()); d.Allow || d.Reason != "outside the published ceiling" {
		t.Fatalf("ledger grant outside the ceiling must deny: %+v", d)
	}
}

// The envelope is the only principal source: a body carrying somebody
// else's principal is a forgery the endpoint denies (SPEC-0092 red-team).
func TestRightsDecideRejectsForeignPrincipal(t *testing.T) {
	store, gov, auth := rightsFixture(t)
	doc := store.RightsDoc
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-read", auth.PrincipalID, "workspace", "read", "", "fixture", "operator", time.Now().Add(time.Hour)))
	g := &Gateway{Store: store, Backend: api.RoutingBackend{}, Tokens: map[string]identity.Envelope{"alice-principal-token-0000000000000": auth},
		projections: map[string]*gatewayProjection{"alice-principal-token-0000000000000": {auth: auth}}}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/v1/rights/decide", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer alice-principal-token-0000000000000")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		var payload map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &payload)
		return w.Code, payload
	}
	code, payload := call(`{"capability":"workspace","operation":"read","principal_id":"mallory"}`)
	if code != http.StatusOK || payload["allow"] != false {
		t.Fatalf("foreign principal must deny: %d %v", code, payload)
	}
	code, payload = call(`{"capability":"workspace","operation":"read"}`)
	if code != http.StatusOK || payload["allow"] != true {
		t.Fatalf("envelope principal with a live grant must allow: %d %v", code, payload)
	}
}

// A published tools.use right admits the invoke control on a managed
// profile with no connectors bundle: the profile's control_operations allowlist
// cannot express it — without this derivation every extended selection on a
// connector-free space is listable but never callable through tool_call.
func TestManagedInvokeFollowsToolsUseRight(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	doc := rightsVocabulary()
	store.RightsDoc = doc
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	now := time.Now()
	allowed, err := store.AllowedControlOperations(auth)
	if err != nil {
		t.Fatal(err)
	}
	if allowed["invoke"] {
		t.Fatal("invoke admitted before the published right exists")
	}
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-use", auth.PrincipalID, "tools", "use", "", "fixture", "operator", now.Add(time.Hour)))
	if err := DeriveRights(store, gov, doc, auth.PrincipalID, map[string][]string{"tools": {"use"}}, auth.Environment, auth.CapabilityProfile, auth.PolicyVersion, now); err != nil {
		t.Fatal(err)
	}
	allowed, err = store.AllowedControlOperations(auth)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed["invoke"] {
		t.Fatal("tools.use right must admit invoke on a connector-free managed profile")
	}
	// Dropping the right from the published ceiling revokes the control.
	if err := DeriveRights(store, gov, doc, auth.PrincipalID, map[string][]string{"tools": {}}, auth.Environment, auth.CapabilityProfile, auth.PolicyVersion, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	allowed, err = store.AllowedControlOperations(auth)
	if err != nil {
		t.Fatal(err)
	}
	if allowed["invoke"] {
		t.Fatal("invoke must close when the revision drops tools.use")
	}
}

// DeriveRights is the only writer of managed rights state: the passport
// allow becomes the profile ceiling and the ledger's platform grants, and
// rows a later revision drops are revoked in place (SPEC-0092 R2.1).
func TestDeriveRightsStampsManagedState(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	doc := rightsVocabulary()
	store.RightsDoc = doc
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	now := time.Now()
	allow := map[string][]string{"workspace": {"read", "write"}, "models": {"switch"}}
	for capability, operations := range allow {
		for _, operation := range operations {
			mintRight(t, gov, doc, api.NewRightGrant(api.DeterministicID("rightgrant", auth.PrincipalID, capability, operation), auth.PrincipalID, capability, operation, "", "fixture", "operator", now.Add(time.Hour)))
		}
	}
	if err := DeriveRights(store, gov, doc, auth.PrincipalID, allow, auth.Environment, auth.CapabilityProfile, auth.PolicyVersion, now); err != nil {
		t.Fatal(err)
	}
	profile, _, err := store.capabilityProfileLocked(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Rights) != 3 {
		t.Fatalf("derived ceiling must carry every allow row: %+v", profile.Rights)
	}
	// Derived ledger grants let Decide allow what the revision carried.
	for _, op := range []string{"read", "write"} {
		if d, _ := store.Decide(auth, "workspace", op, "", now); !d.Allow {
			t.Fatalf("derived grant must allow workspace.%s: %+v", op, d)
		}
	}
	// Re-derive with a shrunk allow: the dropped right is revoked in place,
	// the surviving rows keep their grants (no revision churn).
	allow2 := map[string][]string{"workspace": {"read"}}
	if err := DeriveRights(store, gov, doc, auth.PrincipalID, allow2, auth.Environment, auth.CapabilityProfile, auth.PolicyVersion, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if d, _ := store.Decide(auth, "workspace", "write", "", now); d.Allow {
		t.Fatalf("dropped right must deny after re-derivation: %+v", d)
	}
	if d, _ := store.Decide(auth, "workspace", "read", "", now); !d.Allow {
		t.Fatalf("kept right must still allow: %+v", d)
	}
	profile, _, _ = store.capabilityProfileLocked(auth)
	if len(profile.Rights) != 1 {
		t.Fatalf("shrunk ceiling must carry only the kept right: %+v", profile.Rights)
	}
	// Idempotent: deriving the same allow twice changes nothing.
	grants, _ := gov.RightGrants()
	before := len(grants)
	if err := DeriveRights(store, gov, doc, auth.PrincipalID, allow2, auth.Environment, auth.CapabilityProfile, auth.PolicyVersion, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	grants, _ = gov.RightGrants()
	if len(grants) != before {
		t.Fatalf("re-derivation must not churn the ledger: %d -> %d", before, len(grants))
	}
}

// A derivation rewrites only the bound principal/environment/profile: a dev
// apply must never touch the prod profile's ceiling, and a revision naming
// tools.binding rewrites only that profile (SPEC-0095 R2).
func TestDeriveRightsScopesExactProfile(t *testing.T) {
	store, auth, _, devProfile := managedStore(t)
	doc := rightsVocabulary()
	store.RightsDoc = doc
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	prod := devProfile
	prod.ProfileID = "alice-prod"
	prod.Environment = "prod"
	prod.Generation = 2
	sibling := devProfile
	sibling.ProfileID = "alice-dev-other"
	sibling.Generation = 3
	sibling.Rights = nil
	if err := putProfile(t, store, prod); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, store, sibling); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-wread", auth.PrincipalID, "workspace", "read", "", "fixture", "operator", now.Add(time.Hour)))
	allow := map[string][]string{"workspace": {"read"}}
	if err := DeriveRights(store, gov, doc, auth.PrincipalID, allow, "dev", devProfile.ProfileID, auth.PolicyVersion, now); err != nil {
		t.Fatal(err)
	}
	if got := store.capabilityProfiles[devProfile.ProfileID].Rights; len(got) != 1 || got[0].Capability != "workspace" {
		t.Fatalf("bound dev profile must carry the derived ceiling: %+v", got)
	}
	if got := store.capabilityProfiles["alice-prod"].Rights; len(got) != 1 || got[0].Operation != "use" {
		t.Fatalf("prod profile must be untouched by a dev apply: %+v", got)
	}
	if got := store.capabilityProfiles["alice-dev-other"].Rights; len(got) != 0 {
		t.Fatalf("unbound dev profile must stay empty under a bound apply: %+v", got)
	}
	// A revision without tools.binding covers every same-environment
	// profile but still never crosses the environment boundary.
	if err := DeriveRights(store, gov, doc, auth.PrincipalID, allow, "dev", "", auth.PolicyVersion, now); err != nil {
		t.Fatal(err)
	}
	if got := store.capabilityProfiles["alice-dev-other"].Rights; len(got) != 1 {
		t.Fatalf("unbound dev apply must cover sibling dev profiles: %+v", got)
	}
	if got := store.capabilityProfiles["alice-prod"].Rights; len(got) != 1 || got[0].Operation != "use" {
		t.Fatalf("prod profile must stay untouched: %+v", got)
	}
}

func TestEmptyPublishedRightsDenyManagedCalls(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	doc := rightsVocabulary()
	store.RightsDoc = doc
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	if err := DeriveRights(store, gov, doc, auth.PrincipalID, map[string][]string{}, auth.Environment, auth.CapabilityProfile, auth.PolicyVersion, time.Now()); err != nil {
		t.Fatal(err)
	}
	profile, _, err := store.capabilityProfileLocked(auth)
	if err != nil {
		t.Fatal(err)
	}
	tool := api.ToolSpec{Requires: []string{"workspace.read"}}
	if missing := store.rightsMissingLocked(auth, profile, tool, time.Now()); len(missing) != 2 {
		t.Fatalf("empty ceiling admitted a required right: %v", missing)
	}
	if denied, err := store.RightsGated(auth, "workspace", "read", time.Now()); err != nil || !denied {
		t.Fatalf("empty ceiling bypassed PEP: denied=%v err=%v", denied, err)
	}
}

// A policy rotation must carry the space's agent-tools binding forward:
// the profile and token envelope follow the revision automatically, and a
// stale-pv binding silently zeroes the managed projection — the live
// incident behind CHG-0275 (artem_two saw 83 selections collapse to zero
// tools until the binding was repinned by hand).
func TestDeriveRightsRepinsManagedBinding(t *testing.T) {
	store := NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "hub-agent-tools", Version: "9.9.9",
		Transport: api.AgentTools, Workload: api.WorkloadPolicy{Class: api.PerUser, Rationale: "fixture catalog"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"communication-hub"}},
		Health:    api.HealthProbe{Kind: "exec", Value: "true", TimeoutSeconds: 5},
		Tools: []api.ToolSpec{{Name: "search", Effect: api.ReadEffect, CapabilityID: "files.read",
			Uses: []api.CapabilityUse{{Action: "read", Resource: "workspace"}}}},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice",
		ContextID: "alice", RuntimeID: "runtime", ConversationID: "test", DeliveryTargetID: "test",
		PolicyVersion: "policy-1", CapabilityProfile: "alice-default", Environment: "dev", Generation: 1}
	rule := api.CapabilityRule{CapabilityID: "files.read", ImplementationDigest: api.DefinitionDigest(definition), Action: "read", Resource: "workspace", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	if err := putPolicy(t, store, api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "fixture", Status: api.ActiveStatus, Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule}}); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, store, api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "org-default", PolicyRevision: 1, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "fixture", Status: api.ActiveStatus,
		Selections: []api.CapabilitySelection{{CapabilityID: rule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: rule.ImplementationDigest, ToolName: "search", Name: "agent_search"}}}); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: "policy-1",
		WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)

	doc := rightsVocabulary()
	store.RightsDoc = doc
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-read", "alice", "workspace", "read", "", "fixture", "operator", time.Now().Add(time.Hour)))
	mintRight(t, gov, doc, api.NewRightGrant("rightgrant-alice-use", "alice", "tools", "use", "", "fixture", "operator", time.Now().Add(time.Hour)))
	if err := DeriveRights(store, gov, doc, "alice", map[string][]string{"workspace": {"read"}, "tools": {"use"}}, "dev", "", "policy-2", time.Now()); err != nil {
		t.Fatal(err)
	}
	current, err := store.binding(binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	if current.PolicyVersion != "policy-2" {
		t.Fatalf("binding stayed on rotated pv: %s", current.PolicyVersion)
	}
	auth.PolicyVersion = "policy-2"
	tools, err := store.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "agent_search" {
		t.Fatalf("repinned selection must project: %+v", tools)
	}
}

func TestPublishedSelectionRevokesOpenSessionWithoutChangingGrants(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	mintRight(t, gov, store.RightsDoc, api.NewRightGrant("rightgrant-selection-use", "alice", "tools", "use", "", "fixture", "operator", time.Now().Add(time.Hour)))
	allow := map[string][]string{"tools": {"use"}}
	selected := profile.Selections
	check := func(want bool) {
		t.Helper()
		listed, err := store.ListProjectedTools(auth)
		if err != nil || (len(listed) == 1) != want {
			t.Fatalf("projection after selection change: count=%d err=%v want=%v", len(listed), err, want)
		}
		called := false
		err = store.AuthorizeProjectedCall(auth, "workspace_read", map[string]any{"path": "docs/a", "scope": "workspace"}, func(api.ProjectedTool, api.EffectiveBinding) error {
			called = true
			return nil
		})
		if (err == nil) != want || called != want {
			t.Fatalf("call after selection change: err=%v called=%v want=%v", err, called, want)
		}
	}
	check(true)
	for _, step := range []struct {
		selections []api.CapabilitySelection
		want       bool
	}{{nil, false}, {selected, true}} {
		if err := DeriveRightsAndSelections(store, gov, store.RightsDoc, "alice", allow, "dev", profile.ProfileID, "", selected[0].DefinitionID, step.selections, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
		core, err := store.CoreToolNames(auth)
		if err != nil || core == nil || len(core) != 0 {
			t.Fatalf("empty published core became unbounded: %v %v", core, err)
		}
		check(step.want)
	}
}

// A managed tool projects only while every declared Requires decides allow:
// the ceiling alone is not enough — the ledger grant must live too (R2.2).
func TestManagedAdmissionRequiresRights(t *testing.T) {
	s, auth, binding := seededStore(t)
	definition := remoteDefinition()
	definition.Version = "2.0.0"
	definition.Tools[0].CapabilityID = "files.read"
	definition.Tools[0].Requires = []string{"workspace.read"}
	definition.Tools[0].Uses = []api.CapabilityUse{{Action: "read", Resource: "workspace", PathArgument: "path"}}
	definition.Tools[0].ArgumentEquals = map[string]string{"scope": "workspace"}
	definition.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"scope":{"type":"string"}},"required":["path","scope"],"additionalProperties":false}`)
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID, binding.DefinitionVersion = "", definition.Version
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	auth.Organization = "example"
	rule := api.CapabilityRule{CapabilityID: "files.read", ImplementationDigest: api.DefinitionDigest(definition), Action: "read", Resource: "workspace", ConnectionID: binding.ConnectionID,
		PathPrefix: "docs", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{"alice", "bob"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus, Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule}}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus,
		Rights: []api.RightRule{{Capability: "workspace", Operation: "read"}, {Capability: "tools", Operation: "use"}},
		Selections: []api.CapabilitySelection{{CapabilityID: rule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: rule.ImplementationDigest,
			ToolName: "search", Name: "workspace_read", ConnectionID: binding.ConnectionID}}}
	policy.RightCeiling = profile.Rights
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	s.RightsDoc = rightsVocabulary()
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	s.RightsLedger = gov.Load
	mintRight(t, gov, s.RightsDoc, api.NewRightGrant("rightgrant-alice-use", auth.PrincipalID, "tools", "use", "", "fixture", "operator", time.Now().Add(time.Hour)))
	// Ceiling carries the right but the ledger grant does not exist: hidden.
	if tools, err := s.ListProjectedTools(auth); err == nil && len(tools) != 0 {
		t.Fatalf("tool must hide while its required right lacks a grant: %v", tools)
	}
	denials, err := s.RightsDenials(auth)
	if err != nil || len(denials) != 1 || denials[0].Missing != "workspace.read" {
		t.Fatalf("rights denial must name the missing right: %+v %v", denials, err)
	}
	mintRight(t, gov, s.RightsDoc, api.NewRightGrant("rightgrant-alice-read", auth.PrincipalID, "workspace", "read", "", "fixture", "operator", time.Now().Add(time.Hour)))
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 1 {
		t.Fatalf("granted right must admit the projection: %v %v", tools, err)
	}
	if denials, _ := s.RightsDenials(auth); len(denials) != 0 {
		t.Fatalf("granted right must clear the denial surface: %+v", denials)
	}
	// Revocation hides the tool again without a respawn.
	if err := gov.RevokeRight("rightgrant-alice-read", "operator", "test revoke", time.Now()); err != nil {
		t.Fatal(err)
	}
	if tools, err := s.ListProjectedTools(auth); err == nil && len(tools) != 0 {
		t.Fatalf("revoked right must hide the tool live: %v", tools)
	}
}

func TestOrganizationRightCeiling(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	if organization, err := store.ProfileOrganization(auth); err != nil || organization != "example" {
		t.Fatalf("protected profile organization: %q %v", organization, err)
	}
	wrongGeneration := auth
	wrongGeneration.Generation++
	if _, err := store.ProfileOrganization(wrongGeneration); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("wrong generation resolved organization: %v", err)
	}
	wrongOrg := auth
	wrongOrg.Organization = "other"
	if _, err := store.Decide(wrongOrg, "tools", "use", "", time.Now()); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("organization mismatch in authenticated envelope: %v", err)
	}
	if err := store.CheckOrganizationRights(auth.PrincipalID, "dev", "example", profile.ProfileID, profile.Generation,
		map[string][]string{"workspace": {"read"}}); err != nil {
		t.Fatalf("inside organization ceiling: %v", err)
	}
	for _, tc := range []struct {
		name, environment, organization string
		allow                           map[string][]string
	}{
		{"right", "dev", "example", map[string][]string{"users": {"delete"}}},
		{"environment", "prod", "example", map[string][]string{"workspace": {"read"}}},
		{"organization", "dev", "other", map[string][]string{"workspace": {"read"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.CheckOrganizationRights(auth.PrincipalID, tc.environment, tc.organization, profile.ProfileID, profile.Generation, tc.allow); !errors.Is(err, api.ErrUnauthorized) {
				t.Fatalf("outside organization ceiling: %v", err)
			}
		})
	}
	profile.Rights = append(profile.Rights, api.RightRule{Capability: "users", Operation: "delete"})
	profile.Revision++
	if err := putProfile(t, store, profile); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("profile publication outside organization ceiling: %v", err)
	}
	legacy := store.capabilityProfiles[profile.ProfileID]
	legacy.Rights = append(legacy.Rights, api.RightRule{Capability: "tools", Operation: "configure"})
	store.capabilityProfiles[profile.ProfileID] = legacy
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	mintRight(t, gov, store.RightsDoc, api.NewRightGrant("rightgrant-alice-configure", auth.PrincipalID, "tools", "configure", "", "fixture", "operator", time.Now().Add(time.Hour)))
	if decision, err := store.Decide(auth, "tools", "configure", "", time.Now()); err != nil || decision.Allow {
		t.Fatalf("legacy wider profile bypassed organization ceiling: %+v %v", decision, err)
	}
}

func TestAssertedRightsDenyAmbiguousGenerations(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	if decision, err := store.DecideAsserted(auth.PrincipalID, "dev", "tools", "use", "", time.Now()); err != nil || !decision.Allow {
		t.Fatalf("single active generation: %+v %v", decision, err)
	}
	profile.ProfileID = "alice-next"
	profile.Generation++
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	if decision, err := store.DecideAsserted(auth.PrincipalID, "dev", "tools", "use", "", time.Now()); err != nil || decision.Allow {
		t.Fatalf("ambiguous active generations must deny: %+v %v", decision, err)
	}
}

// The control-token path lets a trusted in-space service assert THIS
// space's principal; any other assertion refuses, and the ceiling still
// applies through the principal's active profile (SPEC-0092 R2.2).
func TestRightsDecideControlAssertedPrincipal(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	store.RightsDoc = rightsVocabulary()
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	profile.Rights = []api.RightRule{{Capability: "workspace", Operation: "read"}}
	profile.Revision++
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	mintRight(t, gov, store.RightsDoc, api.NewRightGrant("rightgrant-alice-read", auth.PrincipalID, "workspace", "read", "", "fixture", "operator", time.Now().Add(time.Hour)))
	g := &Gateway{Store: store, Backend: api.RoutingBackend{}, ControlToken: "control-token-000000000000000000", SpacePrincipal: auth.PrincipalID,
		Tokens:      map[string]identity.Envelope{"alice-principal-token-0000000000000": auth},
		projections: map[string]*gatewayProjection{"alice-principal-token-0000000000000": {auth: auth}}}
	attachGatewayAudit(t, g)
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/v1/rights/decide", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		var payload map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &payload)
		return w.Code, payload
	}
	// Asserted principal matches the space pin and holds ceiling+grant.
	code, payload := call("control-token-000000000000000000", `{"principal_id":"alice","environment":"dev","capability":"workspace","operation":"read"}`)
	if code != http.StatusOK || payload["allow"] != true {
		t.Fatalf("asserted space principal must decide allow: %d %v", code, payload)
	}
	// Ceiling binds the asserted path too: write has a ledger grant absent —
	// but even with one, a right outside profile.Rights must deny.
	code, payload = call("control-token-000000000000000000", `{"principal_id":"alice","environment":"dev","capability":"workspace","operation":"write"}`)
	if code != http.StatusOK || payload["allow"] != false {
		t.Fatalf("right outside the published ceiling must deny: %d %v", code, payload)
	}
	// A foreign principal under the control token is refused outright.
	code, _ = call("control-token-000000000000000000", `{"principal_id":"bob","environment":"dev","capability":"workspace","operation":"read"}`)
	if code != http.StatusForbidden {
		t.Fatalf("cross-space assertion must refuse: %d", code)
	}
	// A sibling space's principal — active profile in the same shared store —
	// is assertable: the profile set, not the pin, bounds shared deployments.
	sibling := profile
	sibling.ProfileID, sibling.PrincipalID = "bob-default", "bob"
	sibling.Rights = []api.RightRule{{Capability: "workspace", Operation: "read"}}
	sibling.Revision++
	if err := putProfile(t, store, sibling); err != nil {
		t.Fatal(err)
	}
	mintRight(t, gov, store.RightsDoc, api.NewRightGrant("rightgrant-bob-read", "bob", "workspace", "read", "", "fixture", "operator", time.Now().Add(time.Hour)))
	code, payload = call("control-token-000000000000000000", `{"principal_id":"bob","environment":"dev","capability":"workspace","operation":"read"}`)
	if code != http.StatusOK || payload["allow"] != true {
		t.Fatalf("sibling principal with an active profile must decide: %d %v", code, payload)
	}
	// An absent pin no longer closes the door by itself: principals the store
	// does not know still refuse, principals it serves still decide.
	g.SpacePrincipal = ""
	handler, _ = g.Handler()
	code, _ = call("control-token-000000000000000000", `{"principal_id":"carol","environment":"dev","capability":"workspace","operation":"read"}`)
	if code != http.StatusForbidden {
		t.Fatalf("unprofiled principal must refuse with no pin: %d", code)
	}
	code, payload = call("control-token-000000000000000000", `{"principal_id":"alice","environment":"dev","capability":"workspace","operation":"read"}`)
	if code != http.StatusOK || payload["allow"] != true {
		t.Fatalf("profiled principal must decide with no pin: %d %v", code, payload)
	}
	for _, body := range []string{
		`{"principal_id":"alice","capability":"workspace","operation":"read"}`,
		`{"principal_id":"alice","environment":"prod","capability":"workspace","operation":"read"}`,
	} {
		code, payload = call("control-token-000000000000000000", body)
		if code != http.StatusOK || payload["allow"] != false {
			t.Fatalf("missing or mismatched environment borrowed the dev ceiling: %d %v", code, payload)
		}
	}
}
