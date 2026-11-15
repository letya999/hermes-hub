package toolhub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The toolsets/sources inventory blocks answer "which toolsets are selected"
// from the same caller-scoped projection that authorizes calls: authored set
// names, live admitted counts and definition grouping — never inferred groups.
func TestToolsListToolsetsAndSources(t *testing.T) {
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
	auth.Organization = "example"
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
			{CapabilityID: readRule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "search", Name: "workspace_read", ConnectionID: binding.ConnectionID, Toolset: "files"},
			{CapabilityID: writeRule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "send", Name: "workspace_send", ConnectionID: binding.ConnectionID, Toolset: "files"},
		},
		CoreTools: []string{"workspace_read"},
		Toolsets: []api.ProfileToolset{
			{Name: "files", Placement: "core", Via: "toolhub"},
			{Name: "browser", Placement: "extended", Via: "toolhub"},
		},
		Connectors: api.ConnectorsPersonal,
		Rights:     []api.RightRule{{Capability: "tools", Operation: "use"}},
	}
	policy.RightCeiling = profile.Rights
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	grantToolUse(t, store, auth)
	g := &Gateway{Store: store, AuditWrite: func(string, map[string]string) error { return nil }}

	listed, err := g.toolsList(auth, "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	sets, ok := listed["toolsets"].(map[string]any)
	if !ok {
		t.Fatalf("toolsets block = %v", listed["toolsets"])
	}
	core := sets["core"].([]map[string]any)
	if len(core) != 1 || core[0]["name"] != "files" || core[0]["tools_admitted"] != 2 || core[0]["tools_total"] != 2 {
		t.Fatalf("core toolsets = %v", core)
	}
	extended := sets["extended"].([]map[string]any)
	if len(extended) != 1 || extended[0]["name"] != "browser" || extended[0]["tools_admitted"] != 0 || extended[0]["tools_total"] != 0 {
		t.Fatalf("extended toolsets = %v", extended)
	}
	sources := listed["sources"].([]map[string]any)
	if len(sources) != 1 || sources[0]["definition_id"] != definition.DefinitionID || sources[0]["tool_count"] != 2 {
		t.Fatalf("sources = %v", sources)
	}
	for _, entry := range listed["tools"].([]map[string]any) {
		if entry["toolset"] != "files" {
			t.Fatalf("entry toolset = %v", entry)
		}
	}
	filtered, err := g.toolsList(auth, "", "", "files", 0)
	if err != nil || len(filtered["tools"].([]map[string]any)) != 2 {
		t.Fatalf("toolset=files filter = %v %v", filtered["tools"], err)
	}
	empty, err := g.toolsList(auth, "", "", "browser", 0)
	if err != nil || len(empty["tools"].([]map[string]any)) != 0 {
		t.Fatalf("toolset=browser filter = %v %v", empty["tools"], err)
	}
}
