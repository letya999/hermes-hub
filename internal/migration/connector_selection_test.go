package migration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type selectionBearer string

func (b selectionBearer) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(copy)
}

type selectionBackend struct{ calls []string }

func (b *selectionBackend) Call(_ context.Context, _ api.EffectiveBinding, tool api.ToolSpec, _ map[string]any) (api.BackendResult, error) {
	b.calls = append(b.calls, tool.Name)
	return api.BackendResult{Text: "ok"}, nil
}

func TestPassportRemovalRevokesConnectorSelection(t *testing.T) {
	now := time.Now().UTC()
	docs, err := catalog.LoadPinned()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "alice")
	seed := filepath.Join(t.TempDir(), "alice")
	publishFixtureRevision(t, seed, "alice", "r-0123456789abcdef")
	base := map[string][]byte{}
	for _, name := range passport.Names {
		base[name], err = os.ReadFile(filepath.Join(seed, "generated", "passports", "r-0123456789abcdef", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
	}
	publish := func(id, selected, grants string) passport.Revision {
		t.Helper()
		rev := passport.Revision{ID: id, Provenance: passport.Provenance{Space: "alice", Environment: "dev", Revision: id}, Docs: map[string][]byte{}}
		for name, body := range base {
			rev.Docs[name] = []byte(strings.ReplaceAll(string(body), "r-0123456789abcdef", id))
		}
		tools := string(rev.Docs["tools"])
		rev.Docs["tools"] = []byte(tools[:strings.Index(tools, "kind: tools")] + "kind: tools\nbinding: {profile_id: alice-default, generation: 1}\ncore:\n" + selected)
		body := string(rev.Docs["grants"])
		rev.Docs["grants"] = []byte(body[:strings.Index(body, "kind: grants")] + "kind: grants\nallow: " + grants + "\n")
		return rev
	}
	storePath := filepath.Join(dir, "runtime", "toolhub", "store.json")
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(agenttools.HubToolsDefinition()); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "policy-default", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus, RightCeiling: []api.RightRule{{Capability: "tools", Operation: "use"}}}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: "alice", ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: "dev", Generation: 1, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: 1,
		Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus, Rights: []api.RightRule{{Capability: "tools", Operation: "use"}}}
	for _, row := range []struct{ id, name string }{{"github", "alpha_read"}, {"gitlab", "beta_read"}} {
		def := api.ToolDefinition{Schema: api.SchemaVersion, DefinitionID: row.id, Version: "1.0.0", Transport: api.RemoteMCP,
			Source:      api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
			Tools:       []api.ToolSpec{{Name: row.name, Effect: api.ReadEffect, CapabilityID: row.id + ".read", Executor: "connector:" + row.id, Requires: []string{"tools.use"}}},
			Credentials: []api.CredentialInput{{Name: "API_TOKEN", Required: true}},
			Workload:    api.WorkloadPolicy{Class: api.PerUser, Rationale: "fixture"},
			Execution:   api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 8192, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
			Health:      api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5}}
		if err := store.RegisterDefinition(def); err != nil {
			t.Fatal(err)
		}
		ref := api.CredentialReference{Schema: 1, CredentialRefID: api.CredentialReferenceID(row.id, 1), ConnectionID: row.id, Revision: 1, Backend: "local", Locator: "local://alice/fixture/1", Keys: []string{"API_TOKEN"}, Status: api.ActiveStatus}
		if err := store.PutCredentialReference(ref); err != nil {
			t.Fatal(err)
		}
		conn := api.Connection{Schema: 1, ConnectionID: row.id, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: row.id, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
		if err := store.PutConnection(conn); err != nil {
			t.Fatal(err)
		}
		grant := api.OperatorGrant(api.GrantDefinition, "alice", row.id, def.Version)
		if err := api.Confirm(&grant, "operator", now); err != nil {
			t.Fatal(err)
		}
		if err := store.PutGrant(grant); err != nil {
			t.Fatal(err)
		}
		binding := api.ToolBinding{Schema: 1, PrincipalID: "alice", ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: row.id, DefinitionVersion: def.Version,
			ConnectionID: conn.ConnectionID, ConnectionRevision: 1, CredentialRefID: ref.CredentialRefID, CredentialRevision: 1,
			PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
		if err := store.PutBinding(binding); err != nil {
			t.Fatal(err)
		}
		digest := api.DefinitionDigest(def)
		rule := api.CapabilityRule{CapabilityID: def.Tools[0].CapabilityID, ImplementationDigest: digest, Action: "read", Resource: "remote", ConnectionID: conn.ConnectionID,
			Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 30}}
		policy.Ceiling, policy.Defaults = append(policy.Ceiling, rule), append(policy.Defaults, rule)
		profile.Selections = append(profile.Selections, api.CapabilitySelection{CapabilityID: def.Tools[0].CapabilityID, DefinitionID: row.id, DefinitionVersion: def.Version,
			ImplementationDigest: digest, ToolName: row.name, Name: row.name, ConnectionID: conn.ConnectionID})
	}
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
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	gov := toolhub.NewGovernanceStore(toolhub.GovernanceStorePath(storePath))
	grant := api.NewRightGrant("rightgrant-alice-use", "alice", "tools", "use", "", "fixture", "operator", now.Add(time.Hour))
	if err := api.Confirm(&grant, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(grant, docs.Rights, now); err != nil {
		t.Fatal(err)
	}
	baselineConnections := store.Connections()
	slices.SortFunc(baselineConnections, func(a, b api.Connection) int { return strings.Compare(a.ConnectionID, b.ConnectionID) })
	baselineGrants, err := gov.RightGrants()
	if err != nil {
		t.Fatal(err)
	}
	baselineCredentials := map[string]api.CredentialReference{}
	for _, id := range []string{"github", "gitlab"} {
		_, ref, err := store.OwnedConnection(auth, id)
		if err != nil {
			t.Fatal(err)
		}
		baselineCredentials[id] = ref
	}
	backend := &selectionBackend{}
	sessionPolicyVersion := ""
	var gateway *toolhub.Gateway
	var session *mcp.ClientSession
	var sessionID string
	const token = "synthetic-connector-session-bearer-123456"
	var rev passport.Revision
	check := func(wantAlpha, wantBeta bool) {
		t.Helper()
		eff, err := rev.Effective()
		if err != nil {
			t.Fatal(err)
		}
		settings, err := stack.SettingsForEffective(dir, eff, "dev")
		if err != nil {
			t.Fatal(err)
		}
		currentPolicyVersion := stack.PolicyVersion(settings)
		if sessionPolicyVersion == "" {
			sessionPolicyVersion = currentPolicyVersion
		}
		if currentPolicyVersion != sessionPolicyVersion {
			t.Fatal("selection change invalidated the open session")
		}
		auth.PolicyVersion = sessionPolicyVersion
		if gateway == nil {
			current, err := toolhub.Load(storePath)
			if err != nil {
				t.Fatal(err)
			}
			current.RightsDoc, current.RightsLedger = docs.Rights, gov.Load
			gateway = &toolhub.Gateway{Store: current, Backend: backend, AuditWrite: func(string, map[string]string) error { return nil }, Tokens: map[string]identity.Envelope{token: auth}}
			handler, err := gateway.Handler()
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)
			client := mcp.NewClient(&mcp.Implementation{Name: "selection-test", Version: "1"}, nil)
			session, err = client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + toolhub.DefaultEndpointPath, HTTPClient: &http.Client{Transport: selectionBearer(token)}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			sessionID = session.ID()
			t.Cleanup(func() { _ = session.Close() })
		} else if err := gateway.RefreshProjection(); err != nil {
			t.Fatal(err)
		}
		if session.ID() != sessionID {
			t.Fatal("selection update replaced the MCP session")
		}
		current := gateway.Store
		listed, err := current.ListProjectedTools(auth)
		if err != nil {
			t.Fatal(err)
		}
		mcpListed, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range []struct {
			name string
			want bool
		}{{api.ProjectedToolName("github", "1.0.0", "alpha_read"), wantAlpha}, {api.ProjectedToolName("gitlab", "1.0.0", "beta_read"), wantBeta}} {
			found := false
			for _, tool := range listed {
				if tool.Name == row.name {
					found = true
				}
			}
			if found != row.want {
				t.Fatalf("list %s: got %v want %v", row.name, found, row.want)
			}
			mcpFound := false
			for _, tool := range mcpListed.Tools {
				if tool.Name == row.name {
					mcpFound = true
				}
			}
			if mcpFound != row.want {
				t.Fatalf("open MCP session listed %s=%v want=%v", row.name, mcpFound, row.want)
			}
			before := len(backend.calls)
			_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: row.name, Arguments: map[string]any{}})
			if (err == nil) != row.want || (len(backend.calls) > before) != row.want {
				t.Fatalf("call %s: admitted=%v backend=%v want=%v", row.name, err == nil, len(backend.calls) > before, row.want)
			}
		}
		connections := current.Connections()
		slices.SortFunc(connections, func(a, b api.Connection) int { return strings.Compare(a.ConnectionID, b.ConnectionID) })
		if !reflect.DeepEqual(connections, baselineConnections) {
			t.Fatal("connections changed on selection update")
		}
		for id, want := range baselineCredentials {
			_, got, err := current.OwnedConnection(auth, id)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("credential reference changed on selection update")
			}
		}
		gotGrants, err := gov.RightGrants()
		if err != nil || !reflect.DeepEqual(gotGrants, baselineGrants) {
			t.Fatal("issued grants changed on selection update")
		}
	}
	selected := "  github: {via: mcp}\n  gitlab: {via: mcp}\n"
	rev = publish("r-1111111111111111", selected, "{tools: [use]}")
	if err := DeriveSpaceRights(dir, "alice", "dev", rev, docs, now); err != nil {
		t.Fatal(err)
	}
	check(true, true)
	beforeBothRepeat, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeriveSpaceRights(dir, "alice", "dev", rev, docs, now); err != nil {
		t.Fatal(err)
	}
	afterBothRepeat, err := os.ReadFile(storePath)
	if err != nil || !reflect.DeepEqual(beforeBothRepeat, afterBothRepeat) {
		t.Fatal("repeated two-connector apply changed the store")
	}
	rev = publish("r-2222222222222222", "  gitlab: {via: mcp}\n", "{tools: [use]}")
	if err := DeriveSpaceRights(dir, "alice", "dev", rev, docs, now); err != nil {
		t.Fatal(err)
	}
	check(false, true)
	beforeRepeat, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeriveSpaceRights(dir, "alice", "dev", rev, docs, now); err != nil {
		t.Fatal(err)
	}
	check(false, true)
	afterRepeat, err := os.ReadFile(storePath)
	if err != nil || !reflect.DeepEqual(beforeRepeat, afterRepeat) {
		t.Fatal("repeated apply changed the store")
	}
	rev = publish("r-3333333333333333", selected, "{tools: [use]}")
	if err := DeriveSpaceRights(dir, "alice", "dev", rev, docs, now); err != nil {
		t.Fatal(err)
	}
	check(true, true)
	if err := gov.RevokeRight("rightgrant-alice-use", "operator", "fixture", now); err != nil {
		t.Fatal(err)
	}
	baselineGrants, err = gov.RightGrants()
	if err != nil {
		t.Fatal(err)
	}
	rev = publish("r-4444444444444444", selected, "{}")
	if err := DeriveSpaceRights(dir, "alice", "dev", rev, docs, now); err != nil {
		t.Fatal(err)
	}
	check(false, false)
}
