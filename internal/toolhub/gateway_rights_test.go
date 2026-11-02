package toolhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// S6.4 live update: revoking a capability grant hides the rights-gated tool
// from the open MCP session — the same session receives
// notifications/tools/list_changed, the tool disappears from tools/list and
// a stale call no longer reaches the backend.
func TestRightsRevokeHidesToolFromOpenSession(t *testing.T) {
	store, auth, binding := seededStore(t)
	definition := remoteDefinition()
	definition.Version = "2.0.0"
	definition.Tools[0].CapabilityID = "files.read"
	definition.Tools[0].Requires = []string{"workspace.read"}
	definition.Tools[0].Uses = []api.CapabilityUse{{Action: "read", Resource: "workspace", PathArgument: "path"}}
	definition.Tools[0].ArgumentEquals = map[string]string{"scope": "workspace"}
	definition.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"scope":{"type":"string"}},"required":["path","scope"],"additionalProperties":false}`)
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID, binding.DefinitionVersion = "", definition.Version
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	store.RightsDoc = rightsVocabulary()
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load

	rule := api.CapabilityRule{CapabilityID: "files.read", ImplementationDigest: api.DefinitionDigest(definition), Action: "read", Resource: "workspace", ConnectionID: binding.ConnectionID,
		PathPrefix: "docs", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus, Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule}}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus,
		// The published ceiling: this principal holds workspace.read.
		Rights: []api.RightRule{{Capability: "workspace", Operation: "read"}},
		Selections: []api.CapabilitySelection{{CapabilityID: rule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: rule.ImplementationDigest,
			ToolName: "search", Name: api.ProjectedToolName(definition.DefinitionID, definition.Version, "search"), ConnectionID: binding.ConnectionID}}}
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	mintRight(t, gov, store.RightsDoc, api.NewRightGrant("rightgrant-alice-ws", auth.PrincipalID, "workspace", "read", "", "fixture", "operator", time.Now().Add(time.Hour)))

	gateway := &Gateway{
		Store: store, Tokens: map[string]identity.Envelope{aliceToken: auth},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		}),
	}
	attachGatewayAudit(t, gateway)
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	changed := make(chan struct{}, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "alice", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	})
	alice, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + DefaultEndpointPath,
		HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: aliceToken}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	name := api.ProjectedToolName(binding.DefinitionID, binding.DefinitionVersion, "search")
	if listed, err := alice.ListTools(context.Background(), nil); err != nil || !toolListed(listed, name) {
		t.Fatalf("rights-covered tool must project initially: listed=%v err=%v", listed, err)
	}
	initialSession := alice.ID()

	if err := gov.RevokeRight("rightgrant-alice-ws", "operator", "test revoke", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := gateway.RefreshProjection(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("open MCP session received no tools/list_changed after the grant revoke")
	}
	if listed, err := alice.ListTools(context.Background(), nil); err != nil || toolListed(listed, name) {
		t.Fatalf("revoked-right tool still listed: listed=%v err=%v", listed, err)
	}
	if _, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"path": "docs/a.txt", "scope": "workspace"}}); err == nil {
		t.Fatal("stale call reached the backend after the revoke")
	}
	if alice.ID() != initialSession {
		t.Fatal("rights refresh replaced the MCP session")
	}
}
