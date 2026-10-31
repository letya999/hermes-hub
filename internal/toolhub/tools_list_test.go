package toolhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// putToolsListBinding materializes an active binding straight into the store,
// without any control-plane call — the same end state an enable performed in
// another session reaches. toolhubd shares one Store across sessions.
func putToolsListBinding(t *testing.T, store *Store, auth identity.Envelope, definition api.ToolDefinition) api.ToolBinding {
	t.Helper()
	keys := make([]string, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		keys = append(keys, input.Name)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(definition.DefinitionID, 1), ConnectionID: definition.DefinitionID, Revision: 1, Backend: "local", Locator: "local://" + auth.PrincipalID + "/cred/1", Keys: keys, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: definition.DefinitionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	return binding
}

func toolsListNames(t *testing.T, body map[string]any) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	tools, _ := body["tools"].([]any)
	for _, entry := range tools {
		if record, ok := entry.(map[string]any); ok {
			if name, ok := record["name"].(string); ok {
				names[name] = true
			}
		}
	}
	return names
}

func toolsListEntry(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()
	tools, _ := body["tools"].([]any)
	for _, entry := range tools {
		if record, ok := entry.(map[string]any); ok && record["name"] == name {
			return record
		}
	}
	return nil
}

func TestToolsListLiveInventoryAndIsolation(t *testing.T) {
	fix := newControlFixture(t, nil)
	definition := remoteDefinition()
	if err := fix.store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	alice := fix.session(t, aliceToken)
	bob := fix.session(t, bobToken)

	before, err := callControl(t, alice, "tools_list", nil)
	if err != nil {
		t.Fatalf("tools_list: %v", err)
	}
	if names := toolsListNames(t, before); len(names) != 0 {
		t.Fatalf("empty surface listed tools: %v", names)
	}
	controls, _ := before["controls"].([]any)
	if len(controls) == 0 {
		t.Fatalf("tools_list omitted control operations: %v", before)
	}

	// A binding that reaches the store without touching alice's session — the
	// end state of an enable done in a neighbouring session — must appear on
	// the next call, with no reconnect, respawn or tools/list refresh.
	binding := putToolsListBinding(t, fix.store, aliceAuth(), definition)
	search := api.ProjectedToolName(definition.DefinitionID, definition.Version, "search")
	send := api.ProjectedToolName(definition.DefinitionID, definition.Version, "send")

	after, err := callControl(t, alice, "tools_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	names := toolsListNames(t, after)
	if !names[search] || !names[send] {
		t.Fatalf("post-binding tools_list=%v", names)
	}
	entry := toolsListEntry(t, after, search)
	if entry["definition"] != definition.DefinitionID || entry["effect"] != "read" {
		t.Fatalf("compact entry=%v", entry)
	}
	bobList, err := callControl(t, bob, "tools_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := toolsListNames(t, bobList); names[search] || names[send] {
		t.Fatalf("bob listed alice binding: %v", names)
	}
	if _, err := bob.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": search, "arguments": map[string]any{}}}); err == nil {
		t.Fatal("bob invoked alice binding through invoke")
	}

	// Disabling must remove the tool on the next call — the list is not a
	// snapshot taken at session start.
	if err := fix.store.SetBindingStatus(binding.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	disabled, err := callControl(t, alice, "tools_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := toolsListNames(t, disabled); names[search] {
		t.Fatal("disabled binding still listed")
	}
	if _, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": search, "arguments": map[string]any{}}}); err == nil {
		t.Fatal("invoke called a tool that tools_list no longer reports")
	}
}

func TestToolsListFiltersBoundsAndSecrets(t *testing.T) {
	fix := newControlFixture(t, nil)
	definition := remoteDefinition()
	if err := fix.store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	putToolsListBinding(t, fix.store, aliceAuth(), definition)
	alice := fix.session(t, aliceToken)
	search := api.ProjectedToolName(definition.DefinitionID, definition.Version, "search")

	compact, err := callControl(t, alice, "tools_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if entry := toolsListEntry(t, compact, search); entry["input_schema"] != nil {
		t.Fatalf("default listing leaked schemas: %v", entry)
	}

	filtered, err := callControl(t, alice, "tools_list", map[string]any{"query": "send"})
	if err != nil {
		t.Fatal(err)
	}
	if names := toolsListNames(t, filtered); len(names) != 1 {
		t.Fatalf("query filtered=%v", names)
	}
	entry := toolsListEntry(t, filtered, api.ProjectedToolName(definition.DefinitionID, definition.Version, "send"))
	if entry == nil || entry["input_schema"] == nil || entry["description"] == nil {
		t.Fatalf("query result lacks full spec: %v", entry)
	}

	empty, err := callControl(t, alice, "tools_list", map[string]any{"query": "zzz-no-match"})
	if err != nil || len(toolsListNames(t, empty)) != 0 {
		t.Fatalf("no-match query returned tools: %v", empty)
	}

	named, err := callControl(t, alice, "tools_list", map[string]any{"name": search})
	if err != nil || named["found"] != true {
		t.Fatalf("exact name=%v err=%v", named, err)
	}
	if entry := toolsListEntry(t, named, search); entry == nil || entry["input_schema"] == nil {
		t.Fatalf("exact name lacks full spec: %v", named)
	}
	missing, err := callControl(t, alice, "tools_list", map[string]any{"name": "hub-not-a-tool-000000"})
	if err != nil || missing["found"] != false || len(toolsListNames(t, missing)) != 0 {
		t.Fatalf("unknown name=%v err=%v", missing, err)
	}

	limited, err := callControl(t, alice, "tools_list", map[string]any{"limit": 1})
	if err != nil || limited["returned"].(float64) != 1 || limited["total"].(float64) != 2 {
		t.Fatalf("limit not honoured: %v", limited)
	}

	raw, _ := json.Marshal(compact)
	if strings.Contains(string(raw), "local://") || strings.Contains(string(raw), "GOOGLE_TOKEN=") {
		t.Fatalf("tools_list leaked locator or secret material: %s", raw)
	}
}

func TestManagedProfileAdmitsInvoke(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Connectors = api.ConnectorsPersonal
	profile.Revision++
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.AllowedControlOperations(auth)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed["invoke"] {
		t.Fatal("connectors bundle denied invoke")
	}
	if err := s.RequireControlOperation(auth, "invoke"); err != nil {
		t.Fatalf("invoke admission: %v", err)
	}
	// connectors:none admits no connector surface at all — invoke stays denied.
	profile.Connectors = api.ConnectorsNone
	profile.Revision++
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	allowed, err = s.AllowedControlOperations(auth)
	if err != nil {
		t.Fatal(err)
	}
	if allowed["invoke"] {
		t.Fatal("connectors:none admitted invoke")
	}
	if err := s.RequireControlOperation(auth, "invoke"); err == nil {
		t.Fatal("invoke allowed under connectors:none")
	}
}

func TestManagedInvokeCallsAdmittedTool(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Connectors = api.ConnectorsPersonal
	profile.Revision++
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	gateway := &Gateway{
		Store:  s,
		Tokens: map[string]identity.Envelope{aliceToken: auth},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			calls.Add(1)
			return api.BackendResult{Text: "ok"}, nil
		}),
		Control:    &ControlPlane{Store: s, Now: time.Now},
		Audit:      func(string, map[string]string) {},
		AuditWrite: func(string, map[string]string) error { return nil },
	}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "managed-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: aliceToken}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !toolListed(listed, "invoke") || !toolListed(listed, "tools_list") {
		t.Fatal("managed session missing invoke or tools_list")
	}

	args := map[string]any{"path": "docs/a", "scope": "workspace"}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": "workspace_read", "arguments": args}})
	if err != nil || result.IsError {
		t.Fatalf("managed invoke: %v %+v", err, result)
	}
	if calls.Load() != 1 {
		t.Fatalf("backend calls=%d", calls.Load())
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{"tool": "hub-foreign-000000", "arguments": map[string]any{}}}); err == nil {
		t.Fatal("invoke admitted a tool outside the managed profile")
	}
	// tools_list under the managed envelope reports the admitted selection.
	body, err := callControl(t, session, "tools_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := toolsListNames(t, body); !names["workspace_read"] {
		t.Fatalf("managed tools_list=%v", names)
	}
}
