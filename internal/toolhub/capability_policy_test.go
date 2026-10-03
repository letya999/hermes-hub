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
	if err := s.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := s.PutCapabilityProfile(profile); err != nil {
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
	if err := s.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListProjectedTools(auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old profile survived group revision: %v", err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	if err := s.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 1 {
		t.Fatalf("reviewed default group not projected: %v %v", tools, err)
	}

	changed := policy
	changed.DefaultGroups = []CapabilityGroup{{GroupID: "workspace-reads", Revision: 1, Members: []CapabilityRule{{CapabilityID: rule.CapabilityID, ImplementationDigest: rule.ImplementationDigest, Action: rule.Action, Resource: rule.Resource, ConnectionID: rule.ConnectionID, Limits: rule.Limits}}}}
	if err := s.PutCapabilityPolicy(changed); err == nil {
		t.Fatal("same policy revision changed group membership")
	}
	changed.Revision++
	if err := s.PutCapabilityPolicy(changed); err == nil {
		t.Fatal("group member outside ceiling admitted")
	}
	changed.DefaultGroups[0].Members[0] = rule
	changed.DefaultGroups[0].Members[0].Limits.OutputBytes--
	if err := s.PutCapabilityPolicy(changed); err == nil {
		t.Fatal("group membership changed without group revision")
	}

	policy.Revision++
	policy.DefaultGroups = nil
	if err := s.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	profile.AllowGroups = []CapabilityGroup{{GroupID: "personal-reads", Revision: 1, Members: []CapabilityRule{rule}}}
	if err := s.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 1 {
		t.Fatalf("reviewed personal group not projected: %v %v", tools, err)
	}
	profile.Revision++
	profile.AllowGroups[0].Members[0].Limits.OutputBytes--
	if err := s.PutCapabilityProfile(profile); err == nil {
		t.Fatal("personal group membership changed without group revision")
	}
	profile.AllowGroups[0].Members[0].PathPrefix = ""
	if err := s.PutCapabilityProfile(profile); err == nil {
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
	if err := reloaded.PutCapabilityPolicy(changed); err == nil {
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
		if err := s.PutGrant(grant); err != nil {
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
	if err := s.PutCapabilityProfile(profile); err != nil {
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
	if err := s.PutCapabilityPolicy(policy); err != nil {
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
			_, got := capabilityRuleDecision(u, p, rule, &tc.path, now)
			if got != tc.allowed {
				t.Fatalf("allowed=%v want=%v", got, tc.allowed)
			}
		})
	}
	p := CapabilityPolicy{Ceiling: []CapabilityRule{rule}, Defaults: []CapabilityRule{rule}}
	p.Defaults[0].Limits = CapabilityLimits{4096, 20}
	limits, allowed := capabilityRuleDecision(CapabilityProfile{}, p, rule, nil, now)
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
		if err := s.PutCapabilityProfile(copy); !errors.Is(err, ErrUnauthorized) {
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
	if err := s.PutCapabilityPolicy(policy); err != nil {
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
	if err := writer.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := reader.PutCapabilityProfile(reader.capabilityProfiles[profile.ProfileID]); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale identical retry reported successful publication: %v", err)
	}
	stale := profile
	stale.Revision++
	stale.Status = ActiveStatus
	if err := reader.PutCapabilityProfile(stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write resurrected profile: %v", err)
	}
	if _, err := reader.ListProjectedTools(auth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale reader admitted: %v", err)
	}
	if err := reader.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	old := profile
	old.Revision--
	old.Status = ActiveStatus
	if err := reader.PutCapabilityProfile(old); !errors.Is(err, ErrConflict) {
		t.Fatalf("old revision allowed: %v", err)
	}
	old.Revision = profile.Revision
	if err := reader.PutCapabilityProfile(old); !errors.Is(err, ErrConflict) {
		t.Fatalf("same revision changed: %v", err)
	}
	policy.Revision++
	if err := reader.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := reader.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	oldPolicy := policy
	oldPolicy.Status = DisabledStatus
	if err := reader.PutCapabilityPolicy(oldPolicy); !errors.Is(err, ErrConflict) {
		t.Fatalf("same policy revision changed: %v", err)
	}
	oldPolicy.Revision--
	if err := reader.PutCapabilityPolicy(oldPolicy); !errors.Is(err, ErrConflict) {
		t.Fatalf("old policy revision allowed: %v", err)
	}
	if err := os.Rename(path, path+".backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	policy.Revision++
	if err := reader.PutCapabilityPolicy(policy); err == nil {
		t.Fatal("unwritable policy store accepted")
	}
	if reader.capabilityPolicies[policy.PolicyID].Revision != 2 {
		t.Fatal("failed policy update changed memory")
	}
	policy.PolicyID = "new-policy"
	if err := reader.PutCapabilityPolicy(policy); err == nil {
		t.Fatal("unwritable policy insert accepted")
	}
	if _, ok := reader.capabilityPolicies[policy.PolicyID]; ok {
		t.Fatal("failed insert retained")
	}
	profile.PolicyRevision = 2
	profile.Revision++
	if err := reader.PutCapabilityProfile(profile); err == nil {
		t.Fatal("unwritable profile store accepted")
	}
	if reader.capabilityProfiles[profile.ProfileID].Revision != 2 {
		t.Fatal("failed profile update changed memory")
	}
	profile.ProfileID = "new-profile"
	if err := reader.PutCapabilityProfile(profile); err == nil {
		t.Fatal("unwritable profile insert accepted")
	}
	if _, ok := reader.capabilityProfiles[profile.ProfileID]; ok {
		t.Fatal("failed profile insert retained")
	}
	if _, err := reader.ListProjectedTools(auth); err == nil {
		t.Fatal("unavailable store admitted")
	}
	if len(reader.capabilityChanges) != 4 {
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
	if err := writer.PutCapabilityPolicy(policy); err != nil {
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
	if err := restarted.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	again, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if tools, err := again.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("history recovery widened profile: %v %v", tools, err)
	}
	if len(again.capabilityChanges) != 4 || again.capabilityChanges[0].Policy.Revision != 1 || again.capabilityChanges[2].Policy.Reason != "Remove default read" || again.capabilityChanges[2].Policy.IssuedBy != "operator" || again.capabilityChanges[3].Profile.PolicyRevision != 2 {
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
	state.CapabilityChanges[0], state.CapabilityChanges[1] = state.CapabilityChanges[1], state.CapabilityChanges[0]
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
	if err := writer.PutCapabilityPolicy(policy); err != nil {
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
	if err := s.PutCapabilityProfile(profile); err != nil {
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
			if err := NewStore().PutCapabilityPolicy(copy); err == nil {
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
			if err := NewStore().PutCapabilityProfile(copy); !errors.Is(err, ErrInvalid) {
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
		func(s *snapshot) { s.CapabilityChanges[0].Policy.Members = nil },
		func(s *snapshot) { s.CapabilityChanges[1].Profile.Generation = 0 },
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
