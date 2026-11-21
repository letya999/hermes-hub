package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	spawnfake "github.com/letya999/hermes-hub/contracts/spawn/v1/fake"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// memorySearchAdmit enrolls one principal end-to-end: binding, profile with
// the memory_search selection and issued rights — everything a rendered
// managed space carries. memoryRight=false models a revoked memory.search
// grant; rights=false models a revoked tools.use.
func memorySearchAdmit(t *testing.T, store *toolhub.Store, gov *toolhub.GovernanceStore, doc api.RightsDoc,
	definition api.ToolDefinition, digest, user string, memoryRight, toolCall bool) identity.Envelope {
	t.Helper()
	auth := identity.TelegramEnvelope(user, 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation, auth.Organization = user+"-default", "dev", 1, "example"
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	selections := []api.CapabilitySelection{{CapabilityID: "memory.search", DefinitionID: definition.DefinitionID,
		DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "memory_search", Name: "memory_search"}}
	if toolCall {
		selections = append(selections, api.CapabilitySelection{CapabilityID: "platform.call", DefinitionID: definition.DefinitionID,
			DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "tool_call", Name: "tool_call"})
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID,
		ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, Environment: auth.Environment, Generation: auth.Generation,
		PolicyVersion: auth.PolicyVersion, PolicyID: "org-default", PolicyRevision: 2, PassportRevision: "r-0123456789abcdef",
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus,
		Selections: selections}
	if err := api.Confirm(&profile, "", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	allow := map[string][]string{}
	grants := []struct{ capability, operation string }{{"tools", "use"}}
	if memoryRight {
		grants = append(grants, struct{ capability, operation string }{"memory", "search"})
	}
	for _, grant := range grants {
		right := api.NewRightGrant(api.DeterministicID("rightgrant", user, grant.capability, grant.operation),
			user, grant.capability, grant.operation, "", "fixture", "platform", time.Now().Add(time.Hour))
		if err := api.Confirm(&right, right.GrantedBy, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := gov.GrantRight(right, doc, time.Now()); err != nil {
			t.Fatal(err)
		}
		allow[grant.capability] = append(allow[grant.capability], grant.operation)
	}
	if err := toolhub.DeriveRights(store, gov, doc, user, allow, "dev", auth.CapabilityProfile, "policy-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	return auth
}

// publishSearchPassport submits and activates a six-passport revision for
// user through the spawn contract — the server-owned enrollment record the
// vault resolver reads via HUB_SPAWN_URL.
func publishSearchPassport(t *testing.T, client spawnv1.Client, user, environment string, enabled bool) {
	t.Helper()
	ctx := context.Background()
	// The enrolled capability profile pins this revision; the gateway
	// refuses calls whose active passport metadata does not match it.
	revision := "r-0123456789abcdef"
	tenant := spawnv1.Tenant{Org: "local", Owner: user}
	if _, err := client.CreateSpace(ctx, spawnv1.CreateSpaceRequest{Org: tenant.Org, Owner: tenant.Owner, ID: user, Environment: environment, IdempotencyKey: "mk-" + user}); err != nil && !errors.Is(err, spawnv1.ErrConflict) {
		t.Fatal(err)
	}
	docs := map[string]map[string]any{}
	for _, name := range spawnv1.Names {
		doc := map[string]any{
			"apiVersion": "six-passport/v2",
			"kind":       name,
			"metadata":   map[string]any{"space": user, "environment": environment, "revision": revision},
		}
		if name == "config" {
			doc["memory"] = map[string]any{"enabled": enabled}
		}
		docs[name] = doc
	}
	rev, err := client.SubmitRevision(ctx, user, "user", spawnv1.SubmitRevisionRequest{Org: tenant.Org, Owner: tenant.Owner, Documents: docs, IdempotencyKey: "sub-" + revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ValidateRevision(ctx, user, rev.ID, spawnv1.RevisionRequest{Org: tenant.Org, Owner: tenant.Owner, IdempotencyKey: "val-" + revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ActivateRevisions(ctx, user, spawnv1.ActivationRequest{Org: tenant.Org, Owner: tenant.Owner, Revisions: []string{rev.ID}, IdempotencyKey: "act-" + revision}); err != nil {
		t.Fatal(err)
	}
}

// searchTripwire fails the test if memory_search ever reaches the configured
// agent backend — production routing must execute it inside ToolHub.
type searchTripwire struct{ t *testing.T }

func (b searchTripwire) Call(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
	b.t.Error("memory_search reached the in-runtime agent backend — ToolHub must execute it")
	return api.BackendResult{}, api.ErrIsolation
}

// runCorrelationTransport carries the bearer plus the run-correlation headers
// the production runtime stamps onto every model-facing tools call.
type runCorrelationTransport struct {
	base  http.RoundTripper
	token string
	jobID string
	runID string
}

func (tr runCorrelationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+tr.token)
	if tr.jobID != "" {
		clone.Header.Set(api.HeaderJobID, tr.jobID)
	}
	if tr.runID != "" {
		clone.Header.Set(api.HeaderRunID, tr.runID)
	}
	return tr.base.RoundTrip(clone)
}

// TestMemorySearchRoundTripIsolated is the #515 contract: a public
// tools/call for memory_search must search the invoking user's vault
// through ToolHub's production routing — the real reviewed catalog, the
// #513 vault resolver picking the vault, the #514 exact-run gate admitting
// the call, and the in-repo BM25 index answering. Two synthetic users with
// real temporary vaults prove isolation; every denial lands before the
// vault resolves; an unbound vault reports an honest unavailable instead of
// a local fallback.
func TestMemorySearchRoundTripIsolated(t *testing.T) {
	store := toolhub.NewStore()
	definition := catalog.HubToolsDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	rule := api.CapabilityRule{CapabilityID: "memory.search", ImplementationDigest: digest,
		Action: "read", Resource: "memory", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Organization: "example",
		Members: []string{"alice", "bob", "carol", "dave", "erin", "frank"}, Revision: 2,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: api.ActiveStatus,
		Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule},
		RightCeiling: []api.RightRule{{Capability: "tools", Operation: "use"}, {Capability: "memory", Operation: "search"}}}
	if err := api.Confirm(&policy, "", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	doc := api.RightsDoc{Release: "fixture", DefinitionID: "hub-agent-rights", Capabilities: map[string]api.RightCapability{
		"tools":  {Operations: map[string]api.RightOperation{"use": {Subject: "self", Scope: "none", Issuers: []string{"platform"}, PEP: []string{"test"}, Effect: "read", Description: "fixture"}}},
		"memory": {Operations: map[string]api.RightOperation{"search": {Subject: "self", Scope: "none", Issuers: []string{"platform"}, PEP: []string{"test"}, Effect: "read", Description: "fixture"}}},
	}}
	store.RightsDoc = doc
	govPath := filepath.Join(t.TempDir(), "governance.json")
	gov := toolhub.NewGovernanceStore(govPath)
	store.RightsLedger = gov.Load

	spawnSrv := httptest.NewServer(spawnv1.Handler(spawnv1.NewInProc(&spawnfake.Server{}), "spawn-token"))
	defer spawnSrv.Close()
	spawnClient := spawnv1.NewHTTPClient(spawnSrv.URL, "spawn-token", spawnSrv.Client())
	vaults := t.TempDir()
	users := map[string]identity.Envelope{}
	admit := func(user string, memoryRight, toolCall, memoryEnabled bool) identity.Envelope {
		env := memorySearchAdmit(t, store, gov, doc, definition, digest, user, memoryRight, toolCall)
		publishSearchPassport(t, spawnClient, user, "dev", memoryEnabled)
		users[user] = env
		return env
	}
	// A real vault under the server-rendered binding: the indexer reads
	// memory/cards/* as private docs, so each user's marker is searchable.
	mkVault := func(user, marker string) {
		dir := filepath.Join(vaults, user)
		if err := vault.Ensure(dir); err != nil {
			t.Fatal(err)
		}
		cards := filepath.Join(dir, "memory", "cards")
		if err := os.MkdirAll(cards, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cards, "notes.md"), []byte(marker+" stays private to "+user), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	admit("alice", true, true, true)
	mkVault("alice", "vaultmarker-alice")
	admit("bob", true, true, true)
	mkVault("bob", "vaultmarker-bob")
	// carol: enrolled but no rendered binding — resolve must fail inside the
	// handler, surfacing honest unavailable with no fallback.
	admit("carol", true, false, true)
	// dave: memory.search right revoked — denied after projection, before vault.
	admit("dave", false, false, true)
	mkVault("dave", "vaultmarker-dave")
	// erin: tools.use revoked entirely — the call never reaches admission.
	admit("erin", false, false, true)
	mkVault("erin", "vaultmarker-erin")
	// frank: memory.enabled=false in his published passport.
	admit("frank", true, false, false)
	mkVault("frank", "vaultmarker-frank")

	records := map[string]api.RunContext{}
	liveRun := func(user string) api.RunContext {
		env := users[user]
		return api.RunContext{JobID: "job-" + user, RunID: "run-" + user + "-1", SessionID: "session-1",
			RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
			Environment: env.Environment, Status: "running",
			ScopeID: "user:" + env.PrincipalID, Trigger: "message", Channel: "telegram_bot",
			Envelope: identity.Envelope{Schema: env.Schema, PrincipalID: env.PrincipalID,
				ContextID: env.ContextID, RuntimeID: env.RuntimeID, PolicyVersion: env.PolicyVersion}}
	}
	for _, u := range []string{"alice", "bob", "carol", "dave", "erin", "frank"} {
		record := liveRun(u)
		records[record.JobID] = record
	}
	group := liveRun("alice")
	group.JobID, group.RunID, group.ScopeID = "job-alice-group", "run-alice-group", "group:alice"
	records[group.JobID] = group

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer comm-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
		record, ok := records[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(record)
	}))
	defer supervisor.Close()
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", supervisor.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "comm-token")
	t.Setenv("HUB_SPAWN_URL", spawnSrv.URL)
	t.Setenv("HUB_SPAWN_TOKEN", "spawn-token")
	t.Setenv("HUB_VAULTS_BY_PRINCIPAL", vaults)
	t.Setenv("HUB_TOOL_GOVERNANCE", govPath)
	t.Setenv("HUB_AUDIT_LEDGER", filepath.Join(t.TempDir(), "audit.jsonl"))

	// Each principal rides its own bearer: the tokens file maps it to the
	// verified envelope — headers never choose the principal.
	aliceToken := strings.Repeat("a", 32)
	tokens := map[string]string{"alice": aliceToken}
	envelopes := map[string]identity.Envelope{}
	for user, env := range users {
		if user == "alice" {
			continue
		}
		token := strings.Repeat(string(user[0]), 32)
		tokens[user], envelopes[token] = token, env
	}
	bobBody, err := json.Marshal(envelopes)
	if err != nil {
		t.Fatal(err)
	}
	tokensFile := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(tokensFile, bobBody, 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := toolhub.NewEndpointHandler(toolhub.EndpointConfig{Token: aliceToken, Auth: users["alice"], TokensFile: tokensFile,
		Backend: api.RoutingBackend{Agent: searchTripwire{t}}}, store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	callDirect := func(user, jobID, runID string, args map[string]any) (int, map[string]any) {
		body, _ := json.Marshal(map[string]any{"tool": "memory_search", "arguments": args})
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/tool-call", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		if jobID != "" {
			req.Header.Set(api.HeaderJobID, jobID)
		}
		if runID != "" {
			req.Header.Set(api.HeaderRunID, runID)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Text    string `json:"text"`
			IsError bool   `json:"is_error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		var parsed map[string]any
		if out.Text != "" {
			_ = json.Unmarshal([]byte(out.Text), &parsed)
		}
		return res.StatusCode, parsed
	}
	hitsText := func(out map[string]any) string {
		encoded, _ := json.Marshal(out)
		return string(encoded)
	}

	// Rebind check: the projected surface a managed space binds after the
	// catalog bump carries the reviewed contract — the definition version the
	// fixture bound and the runtime:toolhub executor that keeps memory tools
	// out of the runtime.
	projected, _, err := store.ResolveProjectedTool(users["alice"], "memory_search")
	if err != nil {
		t.Fatalf("memory_search not projected for alice: %v", err)
	}
	if projected.Version != definition.Version || projected.Tool.Executor != "runtime:toolhub" {
		t.Fatalf("stale contract projected: version=%s executor=%s", projected.Version, projected.Tool.Executor)
	}

	// Each caller searches only its own vault — the resolver key is the
	// verified envelope, never an argument or header.
	code, out := callDirect("alice", "job-alice", "run-alice-1", map[string]any{"query": "vaultmarker"})
	if code != http.StatusOK {
		t.Fatalf("alice denied on the real HTTP boundary: %d %v", code, out)
	}
	body := hitsText(out)
	if !strings.Contains(body, "vaultmarker-alice") || strings.Contains(body, "bob") {
		t.Fatalf("alice's search leaked or missed: %s", body)
	}
	if status, _ := out["status"].(string); status != "ok" && status != "degraded" {
		t.Fatalf("alice's search status: %v", out["status"])
	}
	code, out = callDirect("bob", "job-bob", "run-bob-1", map[string]any{"query": "vaultmarker"})
	if code != http.StatusOK {
		t.Fatalf("bob denied on the real HTTP boundary: %d %v", code, out)
	}
	body = hitsText(out)
	if !strings.Contains(body, "vaultmarker-bob") || strings.Contains(body, "alice") {
		t.Fatalf("bob's search leaked or missed: %s", body)
	}

	// The indirect path (cataloged tool_call) re-enters the same admission:
	// correlation rides the request headers, the nested call is authorized
	// again on the same g.call path.
	client := mcp.NewClient(&mcp.Implementation{Name: "memory-isolated", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + toolhub.DefaultEndpointPath,
		HTTPClient: &http.Client{Transport: runCorrelationTransport{base: http.DefaultTransport, token: aliceToken, jobID: "job-alice", runID: "run-alice-1"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "tool_call",
		Arguments: map[string]any{"tool": "memory_search", "arguments": map[string]any{"query": "vaultmarker"}}})
	if err != nil {
		t.Fatalf("indirect memory_search failed at transport: %v", err)
	}
	encoded, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(encoded), "vaultmarker-alice") || strings.Contains(string(encoded), "bob") {
		t.Fatalf("indirect call leaked or missed: %s", encoded)
	}

	// Every denial lands before the vault resolves — a refused call answers
	// with an HTTP error, never with a vault-derived result.
	for _, denied := range []struct {
		name, user, jobID, runID string
	}{
		{"unconfirmed run", "alice", "job-ghost", "run-x"},
		{"group run of the same owner", "alice", "job-alice-group", "run-alice-group"},
		{"memory.enabled=false", "frank", "job-frank", "run-frank-1"},
		{"revoked memory.search right", "dave", "job-dave", "run-dave-1"},
		{"revoked tools.use right", "erin", "job-erin", "run-erin-1"},
		{"no run claim", "alice", "", ""},
	} {
		code, out := callDirect(denied.user, denied.jobID, denied.runID, map[string]any{"query": "vaultmarker"})
		if code == http.StatusOK {
			t.Fatalf("%s reached memory_search: %v", denied.name, out)
		}
		if strings.Contains(hitsText(out), "vaultmarker") {
			t.Fatalf("%s still produced vault hits", denied.name)
		}
	}
	// Denials happened before vault open and indexer startup: the refused
	// principals' vaults carry no persisted index snapshot.
	for _, user := range []string{"dave", "erin", "frank"} {
		if _, err := os.Stat(filepath.Join(vaults, user, "runtime", "memory", "index")); !os.IsNotExist(err) {
			t.Fatalf("denied call ran the indexer inside %s's vault: %v", user, err)
		}
	}

	// An enrolled principal without a rendered binding gets an honest
	// unavailable — status, not a hard error, and never a local fallback.
	code, out = callDirect("carol", "job-carol", "run-carol-1", map[string]any{"query": "vaultmarker"})
	if code != http.StatusOK {
		t.Fatalf("unavailable vault must report, not error: %d %v", code, out)
	}
	if out["status"] != "unavailable" {
		t.Fatalf("unbound vault reported %v want unavailable", out["status"])
	}
	if _, err := os.Stat(filepath.Join(vaults, "carol")); !os.IsNotExist(err) {
		t.Fatalf("unavailable call materialized a vault fallback: %v", err)
	}
}
