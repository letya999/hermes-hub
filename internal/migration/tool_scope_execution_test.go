package migration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestAuthoredToolScopeReachesExecutor(t *testing.T) {
	docs, err := catalog.LoadPinned()
	if err != nil {
		t.Fatal(err)
	}
	store := toolhub.NewStore()
	definition := catalog.HubToolsDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	selected, _, _, err := selectedHubTools(store, []byte("core:\n  files: {via: toolhub, tools: {include: [file_read]}, scope: {paths: [/workspace/docs]}, rules: {file_read: {scope: {paths: [/workspace/docs/a]}}}}\n"), docs, "")
	if err != nil || len(selected) != 1 || len(selected[0].PathScopes) != 1 || selected[0].PathScopes[0] != "/workspace/docs/a" {
		t.Fatalf("authored selection: %+v %v", selected, err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rule := api.CapabilityRule{CapabilityID: selected[0].CapabilityID, ImplementationDigest: selected[0].ImplementationDigest,
		Action: "read", Resource: "files", PathPrefix: "docs", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "policy-default", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus, Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule}}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus,
		Rights: []api.RightRule{{Capability: "tools", Operation: "use"}, {Capability: "workspace", Operation: "read"}}, Selections: selected}
	for _, record := range []any{&policy, &profile} {
		if err := api.Confirm(record, "operator", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	store.RightsDoc = docs.Rights
	gov := toolhub.NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	for _, right := range profile.Rights {
		grant := api.NewRightGrant(api.DeterministicID("rightgrant", right.Capability, right.Operation), "alice", right.Capability, right.Operation, "", "fixture", "operator", now.Add(time.Hour))
		if err := api.Confirm(&grant, "operator", now); err != nil {
			t.Fatal(err)
		}
		if err := gov.GrantRight(grant, docs.Rights, now); err != nil {
			t.Fatal(err)
		}
	}
	workspace := t.TempDir()
	for name, body := range map[string]string{"docs/a/ok.txt": "allowed", "docs/private/no.txt": "private"} {
		path := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	backendCalls := 0
	gateway := &toolhub.Gateway{Store: store, AuditWrite: func(string, map[string]string) error { return nil },
		Backend: api.RoutingBackend{Agent: api.AgentExecBackend{Exec: func(ctx context.Context, _ api.EffectiveBinding, request api.AgentExecRequest) (api.AgentExecResult, error) {
			backendCalls++
			// The executor itself is harness-runtime-internal (M40.4); the
			// plumbing under test ends where the admitted scopes reach the
			// backend verbatim. Scope enforcement is covered by the
			// agenttools package's own isolation tests.
			for _, scope := range request.Scopes {
				if scope.Resource == "files" && scope.PathArgument == "path" && scope.PathPrefix != "" {
					return api.AgentExecResult{Result: map[string]any{"ok": true}}, nil
				}
			}
			return api.AgentExecResult{Error: "admitted file scope missing"}, nil
		}}}}
	call := func(name string, args map[string]any, wantBackend bool) error {
		t.Helper()
		before := backendCalls
		_, err := gateway.CallAuthorized(t.Context(), auth, name, args)
		if (backendCalls > before) != wantBackend {
			t.Fatalf("backend calls %d -> %d for %q args %v: %v", before, backendCalls, name, args, err)
		}
		return err
	}
	if err := call("file_read", map[string]any{"path": "docs/a/ok.txt"}, true); err != nil {
		t.Fatalf("allowed path: %v", err)
	}
	for _, args := range []map[string]any{
		{"path": "docs/private/no.txt"}, {"path": "docs/a/../private/no.txt"},
		{"path": "docs/a/ok.txt", "root": "archive"}, {"path": 42},
	} {
		if err := call("file_read", args, false); err == nil {
			t.Fatalf("out-of-scope args admitted: %v", args)
		}
	}
	if err := call("project_create", map[string]any{"name": "x"}, false); err == nil {
		t.Fatal("unselected tool reached backend")
	}
	profile.Revision++
	profile.Selections[0].PathScopes = []string{"/workspace/docs/private"}
	if err := api.Confirm(&profile, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := call("file_read", map[string]any{"path": "docs/a/ok.txt"}, false); err == nil {
		t.Fatal("stale scope reached backend")
	}
}
