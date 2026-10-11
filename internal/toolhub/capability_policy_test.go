package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mustConfirm stamps a record's own issuer onto it: the same durable act the
// operator CLI performs with --confirm, pinned to a fixed time for tests.
func mustConfirm(t *testing.T, record any) {
	t.Helper()
	if err := Confirm(record, "", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
}

// The put* helpers re-stamp the record's own issuer before admission, matching
// what a confirmed operator write looks like; negative-path tests keep working
// because confirmation happens before the store's own checks run.
func putPolicy(t *testing.T, s *Store, p CapabilityPolicy) error {
	mustConfirm(t, &p)
	return s.PutCapabilityPolicy(p)
}

func putProfile(t *testing.T, s *Store, p CapabilityProfile) error {
	mustConfirm(t, &p)
	return s.PutCapabilityProfile(p)
}

func putGrant(t *testing.T, s *Store, g Grant) error {
	mustConfirm(t, &g)
	return s.PutGrant(g)
}

func managedStore(t *testing.T) (*Store, identity.Envelope, CapabilityPolicy, CapabilityProfile) {
	t.Helper()
	s, auth, binding := seededStore(t)
	definition := remoteDefinition()
	definition.Version = "2.0.0"
	definition.Tools[0].CapabilityID = "files.read"
	definition.Tools[0].Uses = []CapabilityUse{{Action: "read", Resource: "workspace", PathArgument: "path"}}
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
	rule := CapabilityRule{CapabilityID: "files.read", ImplementationDigest: DefinitionDigest(definition), Action: "read", Resource: "workspace", ConnectionID: binding.ConnectionID,
		PathPrefix: "docs", Limits: CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy := CapabilityPolicy{Schema: SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{"alice", "bob"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: ActiveStatus, Ceiling: []CapabilityRule{rule}, Defaults: []CapabilityRule{rule}}
	profile := CapabilityProfile{Schema: SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Reviewed synthetic fixture", Status: ActiveStatus,
		Selections: []CapabilitySelection{{CapabilityID: rule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: rule.ImplementationDigest,
			ToolName: "search", Name: "workspace_read", ConnectionID: binding.ConnectionID}}}
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	return s, auth, policy, profile
}

func TestManagedGroupSnapshotsRequireReviewedRevisions(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	rule := policy.Defaults[0]
	policy.Revision++
	policy.Defaults = nil
	policy.DefaultGroups = []CapabilityGroup{{GroupID: "workspace-reads", Revision: 1, Members: []CapabilityRule{rule}}}
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListProjectedTools(auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old profile survived group revision: %v", err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 1 {
		t.Fatalf("reviewed default group not projected: %v %v", tools, err)
	}

	changed := policy
	changed.DefaultGroups = []CapabilityGroup{{GroupID: "workspace-reads", Revision: 1, Members: []CapabilityRule{{CapabilityID: rule.CapabilityID, ImplementationDigest: rule.ImplementationDigest, Action: rule.Action, Resource: rule.Resource, ConnectionID: rule.ConnectionID, Limits: rule.Limits}}}}
	if err := putPolicy(t, s, changed); err == nil {
		t.Fatal("same policy revision changed group membership")
	}
	changed.Revision++
	if err := putPolicy(t, s, changed); err == nil {
		t.Fatal("group member outside ceiling admitted")
	}
	changed.DefaultGroups[0].Members[0] = rule
	changed.DefaultGroups[0].Members[0].Limits.OutputBytes--
	if err := putPolicy(t, s, changed); err == nil {
		t.Fatal("group membership changed without group revision")
	}

	policy.Revision++
	policy.DefaultGroups = nil
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	profile.AllowGroups = []CapabilityGroup{{GroupID: "personal-reads", Revision: 1, Members: []CapabilityRule{rule}}}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 1 {
		t.Fatalf("reviewed personal group not projected: %v %v", tools, err)
	}
	profile.Revision++
	profile.AllowGroups[0].Members[0].Limits.OutputBytes--
	if err := putProfile(t, s, profile); err == nil {
		t.Fatal("personal group membership changed without group revision")
	}
	profile.AllowGroups[0].Members[0].PathPrefix = ""
	if err := putProfile(t, s, profile); err == nil {
		t.Fatal("personal group outside ceiling admitted")
	}
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if tools, err := reloaded.ListProjectedTools(auth); err != nil || len(tools) != 1 {
		t.Fatalf("group snapshot lost on restart: %v %v", tools, err)
	}
	changed = policy
	changed.Revision++
	changed.DefaultGroups = []CapabilityGroup{{GroupID: "workspace-reads", Revision: 1, Members: []CapabilityRule{rule}}}
	changed.DefaultGroups[0].Members[0].Limits.OutputBytes--
	if err := putPolicy(t, reloaded, changed); err == nil {
		t.Fatal("removed group revision reused with changed members after restart")
	}
}

func TestManagedCapabilityDispatchAndNoLegacyFallback(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	var calls, injections, admissions int
	g := &Gateway{Store: s, AuditWrite: func(event string, fields map[string]string) error {
		if event == "admit" {
			admissions++
			if fields["capability_id"] != "files.read" || fields["generation"] != "1" || fields["capability_policy_revision"] != "1" {
				t.Fatalf("missing scope: %v", fields)
			}
		}
		return nil
	}, Injector: func(context.Context, EffectiveBinding) (CredentialInjection, error) {
		injections++
		return CredentialInjection{}, nil
	}, Backend: backendFunc(func(_ context.Context, e EffectiveBinding, tool ToolSpec, _ map[string]any) (BackendResult, error) {
		calls++
		if tool.Name != "search" || e.Definition.Execution.OutputBytes != 8192 || e.Definition.Execution.TimeoutSeconds != 10 {
			t.Fatalf("wrong implementation or limits: %+v", e)
		}
		return BackendResult{Text: "synthetic content"}, nil
	})}
	args := map[string]any{"path": "docs/readme.txt", "scope": "workspace"}
	if _, err := g.CallAuthorized(t.Context(), auth, "workspace_read", args); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"search", "send", "invoke", ProjectedToolName("google-work", "1.0.0", "search"), ProjectedToolName("google-work", "2.0.0", "search")} {
		if _, err := g.CallAuthorized(t.Context(), auth, name, args); err == nil {
			t.Fatalf("raw/legacy alias %s allowed", name)
		}
	}
	for _, args := range []map[string]any{
		nil, {"scope": "workspace"}, {"path": 12, "scope": "workspace"}, {"path": "docs/a", "scope": "archive"},
		{"path": "docs-other/a", "scope": "workspace"}, {"path": "docs/../secret", "scope": "workspace"},
		{"path": "docs\\secret", "scope": "workspace"}, {"path": "/docs/a", "scope": "workspace"},
		{"path": "docs/a", "scope": "workspace", "extra": "unreviewed"},
	} {
		if _, err := g.CallAuthorized(t.Context(), auth, "workspace_read", args); err == nil {
			t.Fatalf("bad scope admitted: %v", args)
		}
	}
	if calls != 1 || injections != 1 || admissions != 1 {
		t.Fatalf("denied call had effects: %d/%d/%d", calls, injections, admissions)
	}
	tools, err := s.ListProjectedTools(auth)
	if err != nil || len(tools) != 1 || tools[0].Name != "workspace_read" {
		t.Fatalf("projection: %v %v", tools, err)
	}
	catalog, err := s.Catalog(auth)
	if err != nil || len(catalog) != 1 || len(catalog[0].Tools) != 1 || catalog[0].Tools[0] != "workspace_read" {
		t.Fatalf("catalog leaked tools: %+v %v", catalog, err)
	}
	d, _ := s.Definition("google-work", "2.0.0")
	if err := s.RequireCatalogAccess(auth, d); err != nil {
		t.Fatal(err)
	}
	if err := s.RequireCatalogAccess(auth, remoteDefinition()); err == nil {
		t.Fatal("unselected version visible")
	}
	if _, err := s.Resolve(auth, tools[0].BindingID); err == nil {
		t.Fatal("bare binding bypassed capability")
	}
	if err := s.Authorize(auth, tools[0].BindingID, func(EffectiveBinding) error { calls++; return nil }); err == nil {
		t.Fatal("bare binding admitted")
	}
	for _, grant := range []Grant{OperatorGrant(GrantSelfInstall, "alice", "", ""), OperatorControlGrant("alice", "invoke")} {
		if err := putGrant(t, s, grant); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RequireSelfInstall(auth); err == nil {
		t.Fatal("legacy install grant leaked into managed profile")
	}
	if controls, err := s.AllowedControlOperations(auth); err != nil || len(controls) != 0 {
		t.Fatalf("legacy controls leaked: %v %v", controls, err)
	}
	profile.Revision++
	profile.Selections = nil
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("empty profile fallback: %v %v", tools, err)
	}
	if _, err := g.CallAuthorized(t.Context(), auth, "workspace_read", args); err == nil {
		t.Fatal("removed selection callable")
	}
	policy.Revision++
	policy.Status = RevokedStatus
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListProjectedTools(auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked policy: %v", err)
	}
}

func TestCapabilityCompleteTupleAndScopeIntersection(t *testing.T) {
	_, _, baseline, _ := managedStore(t)
	rule := baseline.Defaults[0]
	now := time.Now()
	for _, tc := range []struct {
		name    string
		edit    func(*CapabilityPolicy, *CapabilityProfile)
		path    string
		allowed bool
	}{
		{"default", func(*CapabilityPolicy, *CapabilityProfile) {}, "docs/a", true},
		{"zero", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Defaults = nil }, "docs/a", false},
		{"personal-add", func(p *CapabilityPolicy, u *CapabilityProfile) { u.Allows = p.Defaults; p.Defaults = nil }, "docs/a", true},
		{"no-ceiling", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Ceiling = nil }, "docs/a", false},
		{"allow-wider", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Defaults[0].PathPrefix = "" }, "outside", false},
		{"ceiling-wider", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Ceiling[0].PathPrefix = "" }, "docs/a", true},
		{"disjoint", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Ceiling[0].PathPrefix = "other" }, "docs/a", false},
		{"partial-deny", func(p *CapabilityPolicy, _ *CapabilityProfile) {
			d := rule
			d.PathPrefix = "docs/private"
			p.Denies = []CapabilityRule{d}
		}, "docs/public/a", true},
		{"org-deny", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Denies = []CapabilityRule{rule} }, "docs/a", false},
		{"user-deny", func(_ *CapabilityPolicy, u *CapabilityProfile) { u.Denies = []CapabilityRule{rule} }, "docs/a", false},
		{"expired-allow", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Defaults[0].ExpiresAt = now.Add(-time.Second) }, "docs/a", false},
		{"expired-ceiling", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Ceiling[0].ExpiresAt = now }, "docs/a", false},
		{"expired-deny", func(p *CapabilityPolicy, _ *CapabilityProfile) {
			d := rule
			d.ExpiresAt = now.Add(-time.Second)
			p.Denies = []CapabilityRule{d}
		}, "docs/a", true},
		{"separate-action-resource", func(p *CapabilityPolicy, _ *CapabilityProfile) {
			a, b := rule, rule
			a.Action = "edit"
			b.Resource = "archive"
			p.Defaults = []CapabilityRule{a, b}
		}, "docs/a", false},
		{"different-digest", func(p *CapabilityPolicy, _ *CapabilityProfile) {
			p.Defaults[0].ImplementationDigest = "sha256:" + strings.Repeat("0", 64)
		}, "docs/a", false},
		{"different-connection", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Defaults[0].ConnectionID = "other" }, "docs/a", false},
		{"ceiling-different-action", func(p *CapabilityPolicy, _ *CapabilityProfile) { p.Ceiling[0].Action = "edit" }, "docs/a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := CapabilityPolicy{Ceiling: []CapabilityRule{rule}, Defaults: []CapabilityRule{rule}}
			u := CapabilityProfile{}
			tc.edit(&p, &u)
			_, _, got := capabilityRuleDecision(u, p, rule, &tc.path, now)
			if got != tc.allowed {
				t.Fatalf("allowed=%v want=%v", got, tc.allowed)
			}
		})
	}
	p := CapabilityPolicy{Ceiling: []CapabilityRule{rule}, Defaults: []CapabilityRule{rule}}
	p.Defaults[0].Limits = CapabilityLimits{4096, 20}
	limits, _, allowed := capabilityRuleDecision(CapabilityProfile{}, p, rule, nil, now)
	if !allowed || limits != (CapabilityLimits{4096, 10}) {
		t.Fatalf("limits widened: %+v %v", limits, allowed)
	}
	for _, value := range []string{"../a", "a/../b", ".", "..", "/a", "a/", "a//b", "C:a", "a\x00b", "a\nb", "a\\b", strings.Repeat("a", 4097)} {
		if validCapabilityPath(value) {
			t.Fatalf("unsafe logical path %q", value)
		}
	}
}

func TestManagedIdentityAndImplementationPin(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	for name, mutate := range map[string]func(*identity.Envelope){
		"user": func(a *identity.Envelope) { a.PrincipalID = "bob" }, "context": func(a *identity.Envelope) { a.ContextID = "other" },
		"runtime": func(a *identity.Envelope) { a.RuntimeID = "other" }, "environment": func(a *identity.Envelope) { a.Environment = "prod" },
		"generation": func(a *identity.Envelope) { a.Generation++ }, "policy": func(a *identity.Envelope) { a.PolicyVersion = "policy-2" },
		"missing": func(a *identity.Envelope) { a.CapabilityProfile = "missing" }, "malformed": func(a *identity.Envelope) { a.Generation = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			other := auth
			mutate(&other)
			if _, err := s.ListProjectedTools(other); err == nil {
				t.Fatal("wrong identity admitted")
			}
		})
	}
	for _, mutate := range []func(*CapabilityProfile){
		func(p *CapabilityProfile) { p.Selections[0].ImplementationDigest = "sha256:" + strings.Repeat("0", 64) },
		func(p *CapabilityProfile) { p.Selections[0].ToolName = "send" },
		func(p *CapabilityProfile) { p.Selections[0].DefinitionVersion = "9.0.0" },
		func(p *CapabilityProfile) { p.PrincipalID = "mallory" },
		func(p *CapabilityProfile) { p.PolicyRevision++ },
	} {
		copy := profile
		copy.Selections = append([]CapabilitySelection(nil), profile.Selections...)
		copy.Revision++
		mutate(&copy)
		if err := putProfile(t, s, copy); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("unreviewed profile: %v", err)
		}
	}
	// New tools in a different immutable definition never inherit this selection.
	d := remoteDefinition()
	d.Version = "3.0.0"
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 1 {
		t.Fatalf("new schema inherited permission: %v %v", tools, err)
	}
	// Membership or ceiling changes fence the old profile immediately, even
	// before the operator republishes its next revision.
	policy.Revision++
	policy.Members = []string{"bob"}
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListProjectedTools(auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("removed membership admitted: %v", err)
	}
}

func TestManagedProfilePersistenceAndRollback(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	path := filepath.Join(t.TempDir(), "store.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	reader, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.Status = RevokedStatus
	if err := putProfile(t, writer, profile); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, reader, reader.capabilityProfiles[profile.ProfileID]); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale identical retry reported successful publication: %v", err)
	}
	stale := profile
	stale.Revision++
	stale.Status = ActiveStatus
	if err := putProfile(t, reader, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write resurrected profile: %v", err)
	}
	if _, err := reader.ListProjectedTools(auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale reader admitted: %v", err)
	}
	if err := putProfile(t, reader, profile); err != nil {
		t.Fatal(err)
	}
	old := profile
	old.Revision--
	old.Status = ActiveStatus
	if err := putProfile(t, reader, old); !errors.Is(err, ErrConflict) {
		t.Fatalf("old revision allowed: %v", err)
	}
	old.Revision = profile.Revision
	if err := putProfile(t, reader, old); !errors.Is(err, ErrConflict) {
		t.Fatalf("same revision changed: %v", err)
	}
	policy.Revision++
	if err := putPolicy(t, reader, policy); err != nil {
		t.Fatal(err)
	}
	if err := putPolicy(t, reader, policy); err != nil {
		t.Fatal(err)
	}
	oldPolicy := policy
	oldPolicy.Status = DisabledStatus
	if err := putPolicy(t, reader, oldPolicy); !errors.Is(err, ErrConflict) {
		t.Fatalf("same policy revision changed: %v", err)
	}
	oldPolicy.Revision--
	if err := putPolicy(t, reader, oldPolicy); !errors.Is(err, ErrConflict) {
		t.Fatalf("old policy revision allowed: %v", err)
	}
	if err := os.Rename(path, path+".backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	policy.Revision++
	if err := putPolicy(t, reader, policy); err == nil {
		t.Fatal("unwritable policy store accepted")
	}
	if reader.capabilityPolicies[policy.PolicyID].Revision != 2 {
		t.Fatal("failed policy update changed memory")
	}
	policy.PolicyID = "new-policy"
	if err := putPolicy(t, reader, policy); err == nil {
		t.Fatal("unwritable policy insert accepted")
	}
	if _, ok := reader.capabilityPolicies[policy.PolicyID]; ok {
		t.Fatal("failed insert retained")
	}
	profile.PolicyRevision = 2
	profile.Revision++
	if err := putProfile(t, reader, profile); err == nil {
		t.Fatal("unwritable profile store accepted")
	}
	if reader.capabilityProfiles[profile.ProfileID].Revision != 2 {
		t.Fatal("failed profile update changed memory")
	}
	profile.ProfileID = "new-profile"
	if err := putProfile(t, reader, profile); err == nil {
		t.Fatal("unwritable profile insert accepted")
	}
	if _, ok := reader.capabilityProfiles[profile.ProfileID]; ok {
		t.Fatal("failed profile insert retained")
	}
	if _, err := reader.ListProjectedTools(auth); err == nil {
		t.Fatal("unavailable store admitted")
	}
	if len(reader.capabilityChanges) != 5 {
		t.Fatalf("failed/idempotent writes changed audit history: %d", len(reader.capabilityChanges))
	}
}

func TestCapabilityHistoryRecoversPolicyAndIssuerTogether(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	file := filepath.Join(t.TempDir(), "registry.json")
	if err := s.Save(file); err != nil {
		t.Fatal(err)
	}
	writer, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	policy.Revision++
	policy.Defaults = nil
	policy.Reason = "Remove default read"
	if err := putPolicy(t, writer, policy); err != nil {
		t.Fatal(err)
	}
	restarted, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ListProjectedTools(auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale profile survived policy commit: %v", err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	if err := putProfile(t, restarted, profile); err != nil {
		t.Fatal(err)
	}
	again, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if tools, err := again.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("history recovery widened profile: %v %v", tools, err)
	}
	if len(again.capabilityChanges) != 5 || again.capabilityChanges[0].Grant == nil || again.capabilityChanges[1].Policy.Revision != 1 || again.capabilityChanges[3].Policy.Reason != "Remove default read" || again.capabilityChanges[3].Policy.IssuedBy != "operator" || again.capabilityChanges[4].Profile.PolicyRevision != 2 {
		t.Fatalf("lost authorization history: %+v", again.capabilityChanges)
	}
	// A profile cannot appear before the policy that authorized its creation.
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var state snapshot
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	state.CapabilityChanges[1], state.CapabilityChanges[2] = state.CapabilityChanges[2], state.CapabilityChanges[1]
	body, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid history order admitted: %v", err)
	}
}

func TestManagedAdmissionFencesOtherStoreInstances(t *testing.T) {
	s, auth, policy, _ := managedStore(t)
	file := filepath.Join(t.TempDir(), "registry.json")
	if err := s.Save(file); err != nil {
		t.Fatal(err)
	}
	reader, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"path": "docs/a", "scope": "workspace"}
	err = reader.AuthorizeProjectedCall(auth, "workspace_read", args, func(ProjectedTool, EffectiveBinding) error {
		// A separate file handle represents a different operator process. It
		// must not publish policy while admission uses this locked revision.
		lock := flock.New(file + ".lock")
		locked, err := lock.TryLock()
		if err != nil {
			t.Fatal(err)
		}
		if locked {
			_ = lock.Unlock()
			t.Fatal("another process could revoke across admission")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	policy.Revision++
	policy.Status = RevokedStatus
	if err := putPolicy(t, writer, policy); err != nil {
		t.Fatal(err)
	}
	// Model the race where a writer commits after Reload but before admission.
	reader.mu.RLock()
	release, err := reader.lockCapabilityAdmission()
	reader.mu.RUnlock()
	if release != nil {
		release()
	}
	if !errors.Is(err, ErrStale) {
		t.Fatalf("old snapshot passed admission fence: %v", err)
	}
	called := false
	if err := reader.AuthorizeProjectedCall(auth, "workspace_read", args, func(ProjectedTool, EffectiveBinding) error { called = true; return nil }); err == nil || called {
		t.Fatal("committed revoke admitted another call")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	reader.mu.RLock()
	release, err = reader.lockCapabilityAdmission()
	reader.mu.RUnlock()
	if release != nil {
		release()
	}
	if err == nil {
		t.Fatal("missing registry passed admission fence")
	}
}

func TestManagedPolicyOwnsPublishedData(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	policy.Members[0] = "mallory"
	policy.Defaults[0].Action = "delete"
	profile.Selections[0].Name = "evil"
	tools, err := s.ListProjectedTools(auth)
	if err != nil || len(tools) != 1 || tools[0].Name != "workspace_read" {
		t.Fatalf("caller mutated published grant: %v %v", tools, err)
	}
	d := remoteDefinition()
	d.Version = "4.0.0"
	d.Tools[0].InputSchema = json.RawMessage(`{"type":"object"}`)
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	d.Tools[0].Name = "evil"
	d.Tools[0].InputSchema[0] = '!'
	stored, err := s.Definition(d.DefinitionID, d.Version)
	if err != nil || stored.Tools[0].Name != "search" || !json.Valid(stored.Tools[0].InputSchema) {
		t.Fatalf("caller mutated immutable definition: %v", err)
	}
	if DefinitionDigest(d) != "" {
		t.Fatal("invalid definition received a digest")
	}
}

func TestManagedRevocationOnOpenMCPSession(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	var calls atomic.Int32
	g := &Gateway{Store: s, Tokens: map[string]identity.Envelope{aliceToken: auth}, AuditWrite: func(string, map[string]string) error { return nil },
		Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			calls.Add(1)
			return BackendResult{Text: "ok"}, nil
		})}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpConnect(t, server.URL, aliceToken)
	id := client.ID()
	if listed, err := client.ListTools(t.Context(), nil); err != nil || len(listed.Tools) != 1 {
		t.Fatalf("list: %+v %v", listed, err)
	}
	args := &mcp.CallToolParams{Name: "workspace_read", Arguments: map[string]any{"path": "docs/a", "scope": "workspace"}}
	if _, err := client.CallTool(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.Denies = []CapabilityRule{{CapabilityID: profile.Selections[0].CapabilityID, ImplementationDigest: profile.Selections[0].ImplementationDigest, Action: "read", Resource: "workspace", ConnectionID: profile.Selections[0].ConnectionID}}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(t.Context(), args); err == nil {
		t.Fatal("open session retained revoked capability")
	}
	if listed, err := client.ListTools(t.Context(), nil); err != nil || len(listed.Tools) != 0 || client.ID() != id {
		t.Fatalf("revoke projection/session: %+v %v", listed, err)
	}
	if calls.Load() != 1 {
		t.Fatal("revoked call reached backend")
	}
}

func TestManagedDispatchRequiresDurableAudit(t *testing.T) {
	s, auth, _, _ := managedStore(t)
	calls := 0
	g := &Gateway{Store: s, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		calls++
		return BackendResult{}, nil
	})}
	args := map[string]any{"path": "docs/a", "scope": "workspace"}
	if _, err := g.CallAuthorized(t.Context(), auth, "workspace_read", args); err == nil {
		t.Fatal("missing durable audit allowed")
	}
	g.AuditWrite = func(string, map[string]string) error { return errors.New("ledger unavailable") }
	if _, err := g.CallAuthorized(t.Context(), auth, "workspace_read", args); err == nil {
		t.Fatal("failed audit allowed")
	}
	if calls != 0 {
		t.Fatal("unaudited call reached backend")
	}
}

func TestCapabilityPolicyRejectsMalformedAuthority(t *testing.T) {
	_, _, policy, profile := managedStore(t)
	for name, mutate := range map[string]func(*CapabilityPolicy){
		"schema":           func(p *CapabilityPolicy) { p.Schema++ },
		"revision":         func(p *CapabilityPolicy) { p.Revision = 0 },
		"issuer":           func(p *CapabilityPolicy) { p.IssuedBy = "model" },
		"issue-time":       func(p *CapabilityPolicy) { p.IssuedAt = time.Time{} },
		"reason":           func(p *CapabilityPolicy) { p.Reason = "\nforged" },
		"membership":       func(p *CapabilityPolicy) { p.Members = nil },
		"duplicate-member": func(p *CapabilityPolicy) { p.Members = []string{"alice", "alice"} },
		"bad-member":       func(p *CapabilityPolicy) { p.Members = []string{"*"} },
		"personal-members": func(p *CapabilityPolicy) { p.Organization = "" },
		"unbounded":        func(p *CapabilityPolicy) { p.Ceiling[0].Limits.OutputBytes = 0 },
		"digest":           func(p *CapabilityPolicy) { p.Defaults[0].ImplementationDigest = "latest" },
		"traversal":        func(p *CapabilityPolicy) { p.Defaults[0].PathPrefix = "../other" },
		"over-ceiling":     func(p *CapabilityPolicy) { p.Defaults[0].PathPrefix = "" },
		"limit-ceiling":    func(p *CapabilityPolicy) { p.Defaults[0].Limits.TimeoutSeconds++ },
		"expiry-ceiling":   func(p *CapabilityPolicy) { p.Ceiling[0].ExpiresAt = time.Now().Add(time.Hour) },
		"deny-limits":      func(p *CapabilityPolicy) { p.Denies = p.Defaults },
		"rule-count":       func(p *CapabilityPolicy) { p.Ceiling = make([]CapabilityRule, 4097) },
	} {
		t.Run(name, func(t *testing.T) {
			var copy CapabilityPolicy
			body, _ := json.Marshal(policy)
			if err := json.Unmarshal(body, &copy); err != nil {
				t.Fatal(err)
			}
			mutate(&copy)
			var err error
			if copy.IssuedBy == "model" || copy.IssuedBy == "hermes" {
				err = NewStore().PutCapabilityPolicy(copy)
			} else {
				err = putPolicy(t, NewStore(), copy)
			}
			if err == nil {
				t.Fatal("malformed policy accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*CapabilityProfile){
		"no-generation":  func(p *CapabilityProfile) { p.Generation = 0 },
		"no-environment": func(p *CapabilityProfile) { p.Environment = "" },
		"no-reason":      func(p *CapabilityProfile) { p.Reason = "" },
		"duplicate":      func(p *CapabilityProfile) { p.Selections = append(p.Selections, p.Selections[0]) },
		"same-capability": func(p *CapabilityProfile) {
			other := p.Selections[0]
			other.Name = "other_name"
			p.Selections = append(p.Selections, other)
		},
		"reserved":       func(p *CapabilityProfile) { p.Selections[0].Name = "invoke" },
		"unknown-action": func(p *CapabilityProfile) { p.Allows = []CapabilityRule{{}} },
		"bad-deny":       func(p *CapabilityProfile) { p.Denies = []CapabilityRule{{}} },
	} {
		t.Run(name, func(t *testing.T) {
			var copy CapabilityProfile
			body, _ := json.Marshal(profile)
			if err := json.Unmarshal(body, &copy); err != nil {
				t.Fatal(err)
			}
			mutate(&copy)
			if err := putProfile(t, NewStore(), copy); !errors.Is(err, ErrInvalid) {
				t.Fatalf("malformed profile accepted: %v", err)
			}
		})
	}
	for _, tool := range []ToolSpec{
		{CapabilityID: "files.read"},
		{CapabilityID: "files.read", Uses: []CapabilityUse{{Action: "*", Resource: "workspace"}}},
		{CapabilityID: "files.read", Uses: []CapabilityUse{{Action: "read", Resource: "workspace", PathArgument: "a/b"}}},
		{CapabilityID: "files.read", Uses: []CapabilityUse{{Action: "read", Resource: "workspace"}}, ArgumentEquals: map[string]string{"scope": "\n"}},
	} {
		d := remoteDefinition()
		tool.Name, tool.Effect = "read", ReadEffect
		d.Tools = []ToolSpec{tool}
		if err := NewStore().RegisterDefinition(d); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid tool contract accepted: %v", err)
		}
	}
}

func TestCapabilityStoreRejectsMalformedAndDuplicateRecords(t *testing.T) {
	s, _, _, _ := managedStore(t)
	storePath := filepath.Join(t.TempDir(), "registry.json")
	if err := s.Save(storePath); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*snapshot){
		func(s *snapshot) { s.CapabilityChanges = append(s.CapabilityChanges, s.CapabilityChanges[0]) },
		func(s *snapshot) { s.CapabilityChanges = append(s.CapabilityChanges, s.CapabilityChanges[1]) },
		func(s *snapshot) {
			for i := range s.CapabilityChanges {
				if s.CapabilityChanges[i].Policy != nil {
					s.CapabilityChanges[i].Policy.Members = nil
					return
				}
			}
		},
		func(s *snapshot) {
			for i := range s.CapabilityChanges {
				if s.CapabilityChanges[i].Profile != nil {
					s.CapabilityChanges[i].Profile.Generation = 0
					return
				}
			}
		},
		func(s *snapshot) { s.CapabilityChanges[0] = capabilityChange{} },
	} {
		var state snapshot
		if err := json.Unmarshal(body, &state); err != nil {
			t.Fatal(err)
		}
		mutate(&state)
		bad, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(storePath, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(storePath); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad store admitted: %v", err)
		}
	}
}

func TestCapabilityRecordsRequireHumanConfirmation(t *testing.T) {
	s, _, policy, profile := managedStore(t)
	// A record with no operator confirmation carries no authority at all.
	if err := NewStore().PutCapabilityPolicy(policy); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unconfirmed policy admitted: %v", err)
	}
	if err := NewStore().PutCapabilityProfile(profile); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unconfirmed profile admitted: %v", err)
	}
	grant := OperatorGrant(GrantSelfInstall, "alice", "", "")
	if err := s.PutGrant(grant); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unconfirmed grant admitted: %v", err)
	}
	// Confirmation is bound to the exact bytes reviewed: tampering voids it.
	body, _ := json.Marshal(profile)
	var tampered CapabilityProfile
	if err := json.Unmarshal(body, &tampered); err != nil {
		t.Fatal(err)
	}
	mustConfirm(t, &tampered)
	tampered.Selections[0].Name = "sneaked_in"
	if err := NewStore().PutCapabilityProfile(tampered); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("post-confirmation tamper admitted: %v", err)
	}
	// One operator identity cannot stand in for a record issued by another:
	// the stamp succeeds, but admission binds confirmation issuer to record
	// issuer and rejects the mismatch durably.
	crossed := OperatorGrant(GrantSelfInstall, "alice", "", "")
	if err := Confirm(&crossed, "mallory", time.Unix(1, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGrant(crossed); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-issuer confirmation admitted: %v", err)
	}
	// Model/agent issuers can never mint a confirmation.
	for _, issuer := range []string{"model", "hermes"} {
		if err := Confirm(&grant, issuer, time.Unix(1, 0).UTC()); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s confirmation minted: %v", issuer, err)
		}
	}
	// Grant mutations are durable lifecycle records, replayed like policies.
	storePath := filepath.Join(t.TempDir(), "registry.json")
	if err := putGrant(t, s, grant); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(storePath); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range loaded.capabilityChanges {
		if change.Grant != nil && change.Grant.GrantID == grant.GrantID && change.Grant.Confirmation != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("grant lifecycle record lost across restart")
	}
	if err := loaded.RequireSelfInstall(aliceAuth()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedAuditPersistsScopeWithoutArguments(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	ledgerPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("HUB_AUDIT_LEDGER", ledgerPath)
	config := EndpointConfig{Token: aliceToken, Auth: auth, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		return BackendResult{Text: "private result"}, nil
	})}
	handler, err := NewEndpointHandler(config, s)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcpConnect(t, server.URL, aliceToken)
	if _, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "workspace_read", Arguments: map[string]any{"path": "docs/private-name.txt", "scope": "workspace"}}); err != nil {
		t.Fatal(err)
	}
	ledger, err := audit.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := ledger.List("alice")
	if err != nil || len(events) != 2 {
		t.Fatalf("events: %v %v", events, err)
	}
	for _, e := range events {
		if e.CapabilityID != "files.read" || e.CapabilityProfile != profile.ProfileID || e.CapabilityPolicyRevision != 1 || e.CapabilityProfileRevision != 1 || e.Generation != 1 || e.Environment != "dev" || e.ImplementationDigest != profile.Selections[0].ImplementationDigest {
			t.Fatalf("audit lost authorization context: %+v", e)
		}
	}
	body, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "private-name") || strings.Contains(string(body), "private result") {
		t.Fatal("audit retained private arguments/output")
	}
	event := events[0]
	event.CapabilityID = "forged\nfield"
	if err := ledger.Append(event); err == nil {
		t.Fatal("audit accepted forged metadata")
	}
}

// PreviewCapabilityProfile runs the same evaluator and admission checks as
// apply, so the diff an operator reviews is exactly what publish+dispatch
// will do — including denied selections staying visible.
// draftProfile deep-copies a profile so preview fixtures cannot mutate the
// stored fixture's shared slices.
func draftProfile(t *testing.T, p CapabilityProfile) CapabilityProfile {
	t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var out CapabilityProfile
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	out.Confirmation = nil
	return out
}

func TestPreviewCapabilityProfileDiff(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	_ = auth

	// Unconfirmed draft previews fine: confirmation gates apply, not review.
	draft := draftProfile(t, profile)
	draft.Revision = profile.Revision + 1
	draft.Selections[0].Name = "files_read"
	preview, err := s.PreviewCapabilityProfile(draft)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CurrentRevision != 1 || preview.CandidateRevision != 2 {
		t.Fatalf("preview revisions: %+v", preview)
	}
	if len(preview.Added) != 1 || preview.Added[0] != "files_read" || len(preview.Removed) != 1 || preview.Removed[0] != "workspace_read" || len(preview.Changed) != 0 {
		t.Fatalf("rename diff wrong: %+v", preview)
	}
	for _, side := range [][]ProfilePreviewEntry{preview.Current, preview.Candidate} {
		if len(side) != 1 || !side[0].Admitted || len(side[0].Scopes) != 1 || side[0].Scopes[0].PathPrefix != "docs" {
			t.Fatalf("admitted entry missing scope: %+v", side)
		}
	}

	// Identical content at a bumped revision produces an empty diff.
	same := draftProfile(t, profile)
	same.Revision++
	quiet, err := s.PreviewCapabilityProfile(same)
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet.Added)+len(quiet.Removed)+len(quiet.Changed) != 0 {
		t.Fatalf("identical profile produced a diff: %+v", quiet)
	}

	// Stale or replayed revisions are rejected exactly as apply rejects them.
	stale := draftProfile(t, profile)
	if _, err := s.PreviewCapabilityProfile(stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale preview admitted: %v", err)
	}

	// A deny in the draft quarantines the selection: visible, not admitted.
	denied := draftProfile(t, profile)
	denied.Revision++
	denyRule := policy.Defaults[0]
	denyRule.Limits = CapabilityLimits{}
	denied.Denies = []CapabilityRule{denyRule}
	deniedPreview, err := s.PreviewCapabilityProfile(denied)
	if err != nil {
		t.Fatal(err)
	}
	if len(deniedPreview.Candidate) != 1 || deniedPreview.Candidate[0].Admitted || deniedPreview.Candidate[0].Reason == "" {
		t.Fatalf("denied selection not quarantined: %+v", deniedPreview.Candidate)
	}
	if len(deniedPreview.Removed) != 1 || deniedPreview.Removed[0] != "workspace_read" {
		t.Fatalf("denied selection stayed admitted: %+v", deniedPreview)
	}

	// An allow outside the policy ceiling fails the same admission check
	// PutCapabilityProfile runs — preview cannot preview what apply rejects.
	overreach := draftProfile(t, profile)
	overreach.Revision++
	wider := policy.Defaults[0]
	wider.PathPrefix = ""
	overreach.Allows = []CapabilityRule{wider}
	if _, err := s.PreviewCapabilityProfile(overreach); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ceiling violation previewed: %v", err)
	}

	// A changed scope shows as Changed, not Removed+Added: publish a narrower
	// policy revision first, then preview the matching profile bump.
	narrower := policy
	narrower.Revision++
	rule := narrower.Ceiling[0]
	rule.PathPrefix = "docs/inbox"
	narrower.Ceiling = []CapabilityRule{rule}
	narrower.Defaults = []CapabilityRule{rule}
	if err := putPolicy(t, s, narrower); err != nil {
		t.Fatal(err)
	}
	migrated := draftProfile(t, profile)
	migrated.Revision++
	migrated.PolicyRevision = narrower.Revision
	migratedPreview, err := s.PreviewCapabilityProfile(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if len(migratedPreview.Changed) != 1 || migratedPreview.Changed[0] != "workspace_read" || len(migratedPreview.Added)+len(migratedPreview.Removed) != 0 {
		t.Fatalf("scope narrowing not reported as changed: %+v", migratedPreview)
	}
	if got := migratedPreview.Candidate[0].Scopes[0].PathPrefix; got != "docs/inbox" {
		t.Fatalf("candidate kept old scope: %q", got)
	}
}

// Applying a previewed candidate admits exactly the previewed projection.
func TestPreviewMatchesApply(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	_ = policy
	draft := draftProfile(t, profile)
	draft.Revision++
	draft.Selections[0].Name = "files_read"
	preview, err := s.PreviewCapabilityProfile(draft)
	if err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, s, draft); err != nil {
		t.Fatal(err)
	}
	tools, err := s.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != len(preview.Candidate) {
		t.Fatalf("applied surface %v diverged from preview %+v", tools, preview.Candidate)
	}
	for i, tool := range tools {
		if tool.Name != preview.Candidate[i].Name || !preview.Candidate[i].Admitted {
			t.Fatalf("applied entry %v diverged from preview %+v", tool, preview.Candidate[i])
		}
	}
}

// The connector manifest is a reviewed recommendation set: every entry is
// opt-in, HH stays a separately reviewed connector and nothing maps to a
// default grant.
func TestRecommendedConnectorManifest(t *testing.T) {
	manifest := RecommendedConnectorManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	var hh *ConnectorRecommendation
	for i, entry := range manifest.Entries {
		if entry.Admission != AdmissionOptIn {
			t.Fatalf("non-opt-in admission on %q", entry.Family)
		}
		if entry.Family == "hh" {
			hh = &manifest.Entries[i]
		}
	}
	if hh == nil || hh.Status != "implemented" {
		t.Fatal("hh connector missing from the reviewed manifest")
	}
	bad := manifest
	bad.Entries[0].Admission = "default"
	if err := bad.Validate(); err == nil {
		t.Fatal("default-admission manifest accepted")
	}
	dup := manifest
	dup.Entries = append(dup.Entries, manifest.Entries[0])
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate family accepted")
	}
}

// CP-10 rollback: republishing an earlier profile's content requires a new
// monotonic revision and restores only reviewed selections — no legacy
// always-on or self-install permission can come back with it.
func TestProfileRollbackRestoresOnlyReviewedSurface(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	_ = policy
	before, err := s.ListProjectedTools(auth)
	if err != nil || len(before) != 1 {
		t.Fatalf("seeded surface: %v %v", before, err)
	}

	// Rev 2 narrows the profile to nothing.
	narrowed := draftProfile(t, profile)
	narrowed.Revision++
	narrowed.Selections = nil
	if err := putProfile(t, s, narrowed); err != nil {
		t.Fatal(err)
	}
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("narrowed profile still projects: %v %v", tools, err)
	}

	// Rollback is republishing the rev-1 content at a new revision; replaying
	// the old revision number is rejected as stale.
	stale := draftProfile(t, profile)
	if err := putProfile(t, s, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale rollback accepted: %v", err)
	}
	rolledBack := draftProfile(t, profile)
	rolledBack.Revision = narrowed.Revision + 1
	if err := putProfile(t, s, rolledBack); err != nil {
		t.Fatal(err)
	}
	restored, err := s.ListProjectedTools(auth)
	if err != nil || len(restored) != 1 || restored[0].Name != before[0].Name {
		t.Fatalf("rollback did not restore the reviewed selection: %v %v", restored, err)
	}

	// Nothing outside the reviewed selection comes back: the store still has
	// no implicit grants, self-installs or unselected tools on the projection.
	for _, tool := range restored {
		if tool.Name != "workspace_read" {
			t.Fatalf("rollback resurrected an unreviewed tool: %v", tool)
		}
	}
}

// A managed profile opts its own principal into the reviewed control-op set
// and the self-install pipeline. Bindings materialized through that pipeline
// (provenance: the onboarding record under the same policy generation) project
// and dispatch like unmanaged installs; everything else still fails closed.
func TestManagedSelfInstallAndControlOperations(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	if err := s.RequireControlOperation(auth, "prepare_source"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("control op before profile grant: %v", err)
	}
	if err := s.RequireSelfInstall(auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("self-install before profile grant: %v", err)
	}

	// Operator publishes a new revision carrying the install surface.
	profile.Revision++
	profile.ControlOperations = []string{"discover", "prepare_source", "status", "required_credentials", "confirm", "enable", "disable", "diagnostics"}
	profile.SelfInstall = true
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	for _, op := range profile.ControlOperations {
		if err := s.RequireControlOperation(auth, op); err != nil {
			t.Fatalf("profile-granted op %s denied: %v", op, err)
		}
	}
	for _, op := range []string{"rotate", "revoke", "remove", "invoke", "grant"} {
		if err := s.RequireControlOperation(auth, op); err == nil {
			t.Fatalf("unlisted control op %s admitted", op)
		}
	}
	if err := s.RequireSelfInstall(auth); err != nil {
		t.Fatalf("profile-granted self-install denied: %v", err)
	}

	// The pipeline's durable result: a definition, an enabled self-install
	// onboarding and its binding under the managed policy generation.
	definition := remoteDefinition()
	definition.DefinitionID, definition.Version = "github-self", "0.1.0"
	definition.Credentials = nil
	definition.Tools = []ToolSpec{{Name: "repo_list", Effect: ReadEffect}}
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: definition.Workload.Class, Status: ActiveStatus, Revision: 1, ProjectionRevision: 2}
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	bindingID := DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	onboarding := Onboarding{Schema: SchemaVersion, OnboardingID: "onboard-self-1", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: OnboardingSelfInstall, Phase: PhaseEnabled,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, BindingID: bindingID,
		SourceURL: "https://github.com/example/repo", Revision: 1, CreatedAt: time.Now().UTC()}
	if err := s.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}

	projected, err := s.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	name := ProjectedToolName(definition.DefinitionID, definition.Version, "repo_list")
	found := false
	for _, tool := range projected {
		if tool.Name == name {
			found = true
		}
	}
	if !found || len(projected) != 2 {
		t.Fatalf("self-installed tool not projected: %v", projected)
	}
	dispatched := false
	g := &Gateway{Store: s, AuditWrite: func(string, map[string]string) error { return nil },
		Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			dispatched = true
			return BackendResult{Text: "ok"}, nil
		})}
	if _, err := g.CallAuthorized(t.Context(), auth, name, map[string]any{}); err != nil || !dispatched {
		t.Fatalf("self-installed call denied: %v", err)
	}

	// A catalog onboarding does not admit the binding; nor does a pre-managed
	// policy generation or another principal's pipeline.
	other := onboarding
	other.OnboardingID, other.Mode = "onboard-self-2", OnboardingCatalog
	if err := s.PutOnboarding(other); err != nil {
		t.Fatal(err)
	}
	staleDef := remoteDefinition()
	staleDef.DefinitionID, staleDef.Version, staleDef.Credentials = "stale-self", "0.0.1", nil
	staleDef.Tools = []ToolSpec{{Name: "peek", Effect: ReadEffect}}
	if err := s.RegisterDefinition(staleDef); err != nil {
		t.Fatal(err)
	}
	staleID := DeterministicBindingID(auth.PrincipalID, auth.ContextID, auth.RuntimeID, staleDef.DefinitionID, staleDef.Version, "", "")
	stale := onboarding
	stale.OnboardingID, stale.PolicyVersion, stale.BindingID = "onboard-self-3", "policy-0", staleID
	stale.DefinitionID, stale.DefinitionVersion = staleDef.DefinitionID, staleDef.Version
	if err := s.PutOnboarding(stale); err != nil {
		t.Fatal(err)
	}
	staleBinding := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, DefinitionID: staleDef.DefinitionID, DefinitionVersion: staleDef.Version, PolicyVersion: "policy-0",
		WorkloadClass: staleDef.Workload.Class, Status: ActiveStatus, Revision: 1, ProjectionRevision: 3}
	if err := s.PutBinding(staleBinding); err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{ProjectedToolName(staleDef.DefinitionID, staleDef.Version, "peek")} {
		if _, err := g.CallAuthorized(t.Context(), auth, denied, map[string]any{}); err == nil {
			t.Fatalf("non-self-install binding %s dispatched under managed", denied)
		}
	}

	// Dropping the profile grant fails closed: no tools, no ops, no install.
	profile.Revision++
	profile.SelfInstall = false
	profile.ControlOperations = nil
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 1 {
		t.Fatalf("self-install surface survived grant removal: %v %v", tools, err)
	}
	if err := s.RequireControlOperation(auth, "status"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("control op survived grant removal: %v", err)
	}
	if err := s.RequireSelfInstall(auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("self-install survived grant removal: %v", err)
	}

	// Structural validation: unlisted operations cannot be published at all.
	bad := profile
	bad.Revision++
	bad.ControlOperations = []string{"prepare_source", "nuke"}
	if err := putProfile(t, s, bad); err == nil {
		t.Fatal("unreviewed control operation published")
	}
}

func TestCapabilityPathArgumentNormalization(t *testing.T) {
	// Dispatch-time path arguments: the tool's default is the workspace
	// root, "." is its conventional spelling and both normalize to "" —
	// still bounded by the granted prefix, so a scoped grant keeps denying
	// root-level calls while a root grant admits them.
	digest := "sha256:" + strings.Repeat("7", 64)
	rule := CapabilityRule{CapabilityID: "files.list", ImplementationDigest: digest, Action: "list", Resource: "files", Limits: CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy := CapabilityPolicy{Ceiling: []CapabilityRule{rule}, Defaults: []CapabilityRule{rule}}
	profile := CapabilityProfile{}
	selection := CapabilitySelection{CapabilityID: "files.list", ImplementationDigest: digest, ToolName: "file_list"}
	tool := ToolSpec{Name: "file_list", CapabilityID: "files.list", Uses: []CapabilityUse{{Action: "list", Resource: "files", PathArgument: "path"}}}
	for _, tc := range []struct {
		name string
		args map[string]any
		want bool
	}{
		{"absent", map[string]any{}, true},
		{"empty", map[string]any{"path": ""}, true},
		{"dot", map[string]any{"path": "."}, true},
		{"subdir", map[string]any{"path": "docs/a"}, true},
		{"traversal", map[string]any{"path": "../x"}, false},
		{"absolute", map[string]any{"path": "/etc"}, false},
		{"backslash", map[string]any{"path": "a\\b"}, false},
		{"non-string", map[string]any{"path": float64(5)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, got := capabilityDecision(profile, policy, selection, tool, tc.args, false); got != tc.want {
				t.Fatalf("allowed=%v want=%v", got, tc.want)
			}
		})
	}
	// A prefix-scoped grant still denies the normalized root.
	scoped := rule
	scoped.PathPrefix = "docs"
	scopedPolicy := CapabilityPolicy{Ceiling: []CapabilityRule{scoped}, Defaults: []CapabilityRule{scoped}}
	for _, args := range []map[string]any{{}, {"path": "."}, {"path": ""}} {
		if _, _, got := capabilityDecision(profile, scopedPolicy, selection, tool, args, false); got {
			t.Fatalf("scoped grant admitted a root call: %v", args)
		}
	}
	if _, _, got := capabilityDecision(profile, scopedPolicy, selection, tool, map[string]any{"path": "docs/a"}, false); !got {
		t.Fatal("scoped grant denied an in-prefix call")
	}
}
