package toolhub

import (
	"context"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func writeMemoryPassport(t *testing.T, root, user string, enabled bool) {
	t.Helper()
	revision := "r-0123456789abcdef"
	if enabled {
		revision = "r-0123456789abcdee"
	}
	writeMemoryPassportDecl(t, root, user, user, revision, "dev", enabled)
}

func TestMemoryCallChecksPublishedSwitchBeforeBackend(t *testing.T) {
	store, auth, policy, profile := managedStore(t)
	definition := agentDefinition()
	definition.Tools = []api.ToolSpec{{
		Name: "memory_search", CapabilityID: "memory.search", Requires: []string{"memory.search"},
		Effect: api.ReadEffect, Audience: api.AudienceModel, Executor: "handler:memory",
		Description: "Search a memory fixture.", Uses: []api.CapabilityUse{{Action: "read", Resource: "memory"}},
	}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	rule := api.CapabilityRule{CapabilityID: "memory.search", ImplementationDigest: digest,
		Action: "read", Resource: "memory", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy.Revision++
	policy.Ceiling = append(policy.Ceiling, rule)
	policy.Defaults = append(policy.Defaults, rule)
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	profile.Selections = append(profile.Selections, api.CapabilitySelection{CapabilityID: "memory.search", DefinitionID: definition.DefinitionID,
		DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "memory_search", Name: "memory_search"})
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	grantDefinitionRights(t, store, auth, definition)
	root := t.TempDir()
	writeMemoryPassport(t, root, "alice", false)
	calls := 0
	g := &Gateway{Store: store, PassportRoot: root, AuditWrite: func(string, map[string]string) error { return nil },
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			calls++
			return api.BackendResult{Text: "found"}, nil
		})}
	if _, err := g.CallAuthorized(t.Context(), auth, "memory_search", nil); err == nil || calls != 0 {
		t.Fatalf("disabled memory reached backend: calls=%d err=%v", calls, err)
	}
	writeMemoryPassport(t, root, "alice", true)
	if _, err := g.CallAuthorized(t.Context(), auth, "memory_search", nil); err != nil || calls != 1 {
		t.Fatalf("enabled memory with grant denied: calls=%d err=%v", calls, err)
	}
	otherEnv := auth
	otherEnv.Environment = "prod"
	if _, err := g.CallAuthorized(t.Context(), otherEnv, "memory_search", nil); err == nil || calls != 1 {
		t.Fatalf("memory call from a different environment reached backend: calls=%d err=%v", calls, err)
	}
}

func TestMemoryPassportGateUsesAuthenticatedPrincipal(t *testing.T) {
	root := t.TempDir()
	writeMemoryPassport(t, root, "alice", true)
	writeMemoryPassport(t, root, "bob", false)
	g := &Gateway{PassportRoot: root}
	if err := g.requireMemoryEnabled("alice", "dev"); err != nil {
		t.Fatalf("alice's enabled memory denied: %v", err)
	}
	if err := g.requireMemoryEnabled("alice", "prod"); err == nil {
		t.Fatal("prod call inherited dev memory switch")
	}
	if err := g.requireMemoryEnabled("bob", "dev"); err == nil {
		t.Fatal("bob inherited alice's enabled memory")
	}
	if err := g.requireMemoryEnabled("carol", "dev"); err == nil {
		t.Fatal("missing published passport enabled memory")
	}
	if err := g.requireMemoryEnabled("../alice", "dev"); err == nil {
		t.Fatal("invalid principal escaped passport root")
	}
	if err := (&Gateway{}).requireMemoryEnabled("alice", "dev"); err == nil {
		t.Fatal("unmounted passport root enabled memory")
	}
}
