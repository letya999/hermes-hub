package toolhub

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestAssignmentCallbackDoesNotPinRegistryConnection(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	s, auth, policy, profile := managedStore(t)
	definition := r5ExecutorDefinition()
	definition.Tools = []api.ToolSpec{{Name: "assignment_list", CapabilityID: "assignments.list", Requires: []string{"tasks.delegate"},
		Effect: api.ReadEffect, Audience: api.AudienceModel, Executor: "handler:assignments", Description: "List owned assignments",
		Uses: []api.CapabilityUse{{Action: "list", Resource: "assignments"}}}}
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	rule := api.CapabilityRule{CapabilityID: "assignments.list", ImplementationDigest: digest, Action: "list", Resource: "assignments", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy.Revision++
	policy.Ceiling = append(policy.Ceiling, rule)
	policy.Defaults = append(policy.Defaults, rule)
	policy.RightCeiling = append(policy.RightCeiling, api.RightRule{Capability: "tasks", Operation: "delegate"})
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	profile.Rights = append(profile.Rights, api.RightRule{Capability: "tasks", Operation: "delegate"})
	profile.Selections = append(profile.Selections, api.CapabilitySelection{CapabilityID: "assignments.list", DefinitionID: definition.DefinitionID,
		DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "assignment_list", Name: "assignment_list"})
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	grantDefinitionRights(t, s, auth, definition)
	if err := s.Save(filepath.Join(t.TempDir(), "store.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.CloseDB() })
	called := false
	g := &Gateway{Store: s, Backend: backendFunc(func(_ context.Context, _ api.EffectiveBinding, _ api.ToolSpec, _ map[string]any) (api.BackendResult, error) {
		called = true
		if inUse := s.db.Stats().InUse; inUse != 0 {
			return api.BackendResult{}, fmt.Errorf("registry connection pinned during callback: %d", inUse)
		}
		decision, err := s.Decide(auth, "tasks", "delegate", "", time.Now())
		if err != nil || !decision.Allow {
			return api.BackendResult{}, fmt.Errorf("nested rights decision: %v", err)
		}
		return api.BackendResult{Text: "ok"}, nil
	})}
	g.acquireRuntime = func(context.Context, api.EffectiveBinding) (func(), error) { return func() {}, nil }
	attachGatewayAudit(t, g)
	result, err := g.call(context.Background(), auth, "assignment_list", map[string]any{})
	if err != nil || !called || result == nil || result.IsError {
		t.Fatalf("assignment callback: called=%t err=%v", called, err)
	}
	called = false
	g.acquireRuntime = func(context.Context, api.EffectiveBinding) (func(), error) {
		if err := s.Disable(auth, definition.DefinitionID, definition.Version); err != nil {
			return nil, err
		}
		return func() {}, nil
	}
	if _, err := g.call(context.Background(), auth, "assignment_list", map[string]any{}); err == nil || called {
		t.Fatalf("revoked assignment call reached backend: called=%t err=%v", called, err)
	}
}
