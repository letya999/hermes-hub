package toolhub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// R6.2/A6: a compiled revision's core selection limits the directly projected
// schemas; the rest of the admitted family stays live through tools_list and
// invoke, and invoke re-runs the identical admission a direct call would.
func TestToolsetCoreLimitAndExtendedInvokeRevoke(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Tools[0].CapabilityID = "files.read"
	definition.Tools[0].Uses = []api.CapabilityUse{{Action: "read", Resource: "workspace", PathArgument: "path"}}
	definition.Tools[0].ArgumentEquals = map[string]string{"scope": "workspace"}
	definition.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"scope":{"type":"string"}},"required":["path","scope"],"additionalProperties":false}`)
	definition.Tools[1].CapabilityID = "files.write"
	definition.Tools[1].Uses = []api.CapabilityUse{{Action: "write", Resource: "workspace", PathArgument: "path"}}
	definition.Tools[1].ArgumentEquals = map[string]string{"scope": "workspace"}
	definition.Tools[1].InputSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"scope":{"type":"string"}},"required":["path","scope"],"additionalProperties":false}`)
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	limits := api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}
	readRule := api.CapabilityRule{CapabilityID: "files.read", ImplementationDigest: digest, Action: "read", Resource: "workspace", ConnectionID: binding.ConnectionID, PathPrefix: "docs", Limits: limits}
	writeRule := api.CapabilityRule{CapabilityID: "files.write", ImplementationDigest: digest, Action: "write", Resource: "workspace", ConnectionID: binding.ConnectionID, PathPrefix: "docs", Limits: limits}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus, Ceiling: []api.CapabilityRule{readRule, writeRule}, Defaults: []api.CapabilityRule{readRule, writeRule}}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus,
		Selections: []api.CapabilitySelection{
			{CapabilityID: readRule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "search", Name: "workspace_read", ConnectionID: binding.ConnectionID},
			{CapabilityID: writeRule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "send", Name: "workspace_send", ConnectionID: binding.ConnectionID},
		},
		CoreTools:  []string{"workspace_read"},
		Connectors: api.ConnectorsPersonal,
	}
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}

	called := map[string]bool{}
	g := &Gateway{Store: store, AuditWrite: func(string, map[string]string) error { return nil },
		Injector: func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
			return api.CredentialInjection{}, nil
		},
		Backend: backendFunc(func(_ context.Context, effective api.EffectiveBinding, spec api.ToolSpec, args map[string]any) (api.BackendResult, error) {
			called[spec.Name] = true
			return api.BackendResult{Structured: map[string]any{"ok": spec.Name}}, nil
		})}

	// Only core names get a directly projected schema.
	_, direct, _, err := g.projectedServer(auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := direct["workspace_read"]; !ok || len(direct) != 1 {
		t.Fatalf("core projection = %v", direct)
	}

	// Discovery still reports the whole admitted surface, live.
	listed, err := g.toolsList(auth, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, entry := range listed["tools"].([]map[string]any) {
		names[entry["name"].(string)] = true
	}
	if !names["workspace_read"] || !names["workspace_send"] {
		t.Fatalf("extended tool missing from live inventory: %v", names)
	}
	// M31.9: the live-inventory guidance names only r5 wire tools — a retired
	// name here steers the model at a tool the surface no longer projects.
	note, _ := listed["note"].(string)
	if !strings.Contains(note, "tool_call") || !strings.Contains(note, "skill_search") {
		t.Fatalf("inventory note must name the r5 dispatch and skills tools: %q", note)
	}
	if strings.Contains(note, "invoke") || strings.Contains(note, "skills_list") {
		t.Fatalf("inventory note still names retired tools: %q", note)
	}

	// The extended name is admitted through the same dispatch a direct call
	// uses — the core boundary hides the schema, never the authority.
	if err := store.RequireControlOperation(auth, "invoke"); err != nil {
		t.Fatalf("invoke control: %v", err)
	}
	if _, err := g.mcpCall(t.Context(), auth, "workspace_send", map[string]any{"path": "docs/out.txt", "scope": "workspace"}); err != nil {
		t.Fatalf("extended invoke: %v", err)
	}
	if !called["send"] {
		t.Fatal("extended invoke never reached the executor")
	}

	// A revoked allow denies the next invoke exactly like a direct call —
	// admission is re-resolved per call, not cached at projection time.
	narrowed := policy
	narrowed.Revision++
	narrowed.Defaults = []api.CapabilityRule{readRule}
	if err := putPolicy(t, store, narrowed); err != nil {
		t.Fatal(err)
	}
	fresh := profile
	fresh.Revision++
	fresh.PolicyRevision = narrowed.Revision
	if err := putProfile(t, store, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := g.mcpCall(t.Context(), auth, "workspace_send", map[string]any{"path": "docs/out.txt", "scope": "workspace"}); err == nil {
		t.Fatal("revoked extended tool still invocable")
	}

	// A core list naming a tool the profile never selected fails closed —
	// a stale revision cannot project authority that was not granted.
	bad := profile
	bad.Revision += 2
	bad.CoreTools = []string{"workspace_teleport"}
	if err := putProfile(t, store, bad); err == nil {
		t.Fatal("core tool outside the admitted selections accepted")
	}
}

// R6.1 regression: the periodic projection refresh must apply the same core
// bound the initial build does — an extended selection re-admitted on refresh
// must not regain a direct schema.
func TestToolsetCoreBoundSurvivesRefresh(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Tools[0].CapabilityID = "files.read"
	definition.Tools[0].Uses = []api.CapabilityUse{{Action: "read", Resource: "workspace", PathArgument: "path"}}
	definition.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)
	definition.Tools[1].CapabilityID = "files.write"
	definition.Tools[1].Uses = []api.CapabilityUse{{Action: "write", Resource: "workspace", PathArgument: "path"}}
	definition.Tools[1].InputSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	limits := api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}
	readRule := api.CapabilityRule{CapabilityID: "files.read", ImplementationDigest: digest, Action: "read", Resource: "workspace", ConnectionID: binding.ConnectionID, Limits: limits}
	writeRule := api.CapabilityRule{CapabilityID: "files.write", ImplementationDigest: digest, Action: "write", Resource: "workspace", ConnectionID: binding.ConnectionID, Limits: limits}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus, Ceiling: []api.CapabilityRule{readRule, writeRule}, Defaults: []api.CapabilityRule{readRule, writeRule}}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus,
		Selections: []api.CapabilitySelection{
			{CapabilityID: readRule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: definition.Tools[0].Name, Name: "workspace_read", ConnectionID: binding.ConnectionID},
			{CapabilityID: writeRule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: definition.Tools[1].Name, Name: "workspace_send", ConnectionID: binding.ConnectionID},
		},
		CoreTools:  []string{"workspace_read"},
		Connectors: api.ConnectorsPersonal,
	}
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}

	g := &Gateway{Store: store, Tokens: map[string]identity.Envelope{"alice-principal-token-0000000000000": auth}, projections: map[string]*gatewayProjection{}}
	projection, err := g.projectionFor("alice-principal-token-0000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := projection.tools["workspace_read"]; !ok || len(projection.tools) != 1 {
		t.Fatalf("initial core projection = %v", projection.tools)
	}
	if err := g.refreshProjection(projection); err != nil {
		t.Fatal(err)
	}
	if _, ok := projection.tools["workspace_send"]; ok || len(projection.tools) != 1 {
		t.Fatalf("refresh leaked extended tool into direct projection: %v", projection.tools)
	}
}
