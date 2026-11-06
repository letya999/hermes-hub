package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type activationBackend struct{ calls atomic.Int32 }

func (b *activationBackend) Call(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
	b.calls.Add(1)
	return api.BackendResult{Text: "ok"}, nil
}

type activationBearer string

func (b activationBearer) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(copy)
}

func TestConcurrentReadersNeverMixRevisions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	writeManagedSpaceDocs(t, dir, "alice", map[string]string{
		"tools":  "kind: tools\nbinding: {profile_id: alice-default, generation: 1}\ncore:\n  files: {via: toolhub, tools: {include: [file_read]}}\n",
		"grants": "kind: grants\nallow: {tools: [use], workspace: [read]}\n",
	})
	for path, body := range map[string]string{
		filepath.Join(dir, "secrets.dev.env"):    "",
		filepath.Join(dir, "config", "SOUL.md"):  "# fixture\n",
		filepath.Join(root, "config", "SOUL.md"): "# fixture\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := passport.Revision{ID: "r-0123456789abcddd", Provenance: passport.Provenance{Space: "alice", Environment: "dev"}, Docs: map[string][]byte{}}
	for _, name := range passport.Names {
		body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", "r-0123456789abcdef", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		old.Docs[name] = []byte(strings.ReplaceAll(string(body), "r-0123456789abcdef", old.ID))
	}
	if err := old.Publish(dir); err != nil {
		t.Fatal(err)
	}
	next := passport.Revision{ID: "r-0123456789abcdee", Provenance: old.Provenance, Docs: map[string][]byte{}}
	for name, body := range old.Docs {
		next.Docs[name] = []byte(strings.ReplaceAll(string(body), old.ID, next.ID))
	}
	next.Docs["tools"] = []byte(strings.Replace(string(next.Docs["tools"]), "  files: {via: toolhub, tools: {include: [file_read]}}\n", "", 1))
	next.Docs["grants"] = []byte(strings.Replace(string(next.Docs["grants"]), "{tools: [use], workspace: [read]}", "{}", 1))
	docs, err := catalog.LoadPinned()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	definition := agenttools.HubToolsDefinition()
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	rule := api.CapabilityRule{CapabilityID: "files.read", ImplementationDigest: api.DefinitionDigest(definition), Action: "read", Resource: "files", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "policy-default", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus, Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule},
		RightCeiling: []api.RightRule{{Capability: "tools", Operation: "use"}, {Capability: "workspace", Operation: "read"}}}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: "dev", Generation: 1, PolicyVersion: auth.PolicyVersion, PassportRevision: old.ID, PolicyID: policy.PolicyID, PolicyRevision: 1,
		Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus,
		Rights: policy.RightCeiling, Selections: []api.CapabilitySelection{{CapabilityID: "files.read", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
			ImplementationDigest: api.DefinitionDigest(definition), ToolName: "file_read", Name: "file_read"}}}
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
	storePath := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	gov := toolhub.NewGovernanceStore(toolhub.GovernanceStorePath(storePath))
	for _, right := range profile.Rights {
		grant := api.NewRightGrant(api.DeterministicID("rightgrant", right.Capability, right.Operation), "alice", right.Capability, right.Operation, "", "fixture", "operator", now.Add(time.Hour))
		if err := api.Confirm(&grant, "operator", now); err != nil {
			t.Fatal(err)
		}
		if err := gov.GrantRight(grant, docs.Rights, now); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.RightsDoc, loaded.RightsLedger = docs.Rights, gov.Load
	passportRoot := filepath.Join(root, "passport-mount")
	if err := os.MkdirAll(filepath.Join(passportRoot, "alice"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "generated", "passports"), filepath.Join(passportRoot, "alice", "passports")); err != nil {
		t.Fatal(err)
	}
	backend := &activationBackend{}
	const token = "synthetic-activation-session-token-12345"
	gateway := &toolhub.Gateway{Store: loaded, PassportRoot: passportRoot, Tokens: map[string]identity.Envelope{token: auth}, Backend: backend,
		ControlToken: "synthetic-control-token-1234567890", SpacePrincipal: "alice",
		AuditWrite: func(string, map[string]string) error { return nil }}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "activation-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + toolhub.DefaultEndpointPath, HTTPClient: &http.Client{Transport: activationBearer(token)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	call := func(want bool) {
		t.Helper()
		before := backend.calls.Load()
		_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "file_read", Arguments: map[string]any{"path": "x"}})
		if (err == nil) != want || (backend.calls.Load() > before) != want {
			t.Fatalf("MCP call admitted=%v backend=%v want=%v", err == nil, backend.calls.Load() > before, want)
		}
	}
	list := func(wantTool, wantAvailable bool) {
		t.Helper()
		listed, err := session.ListTools(t.Context(), nil)
		if (err == nil) != wantAvailable {
			t.Fatalf("MCP list available=%t want=%t: %v", err == nil, wantAvailable, err)
		}
		if err == nil {
			found := false
			for _, tool := range listed.Tools {
				found = found || tool.Name == "file_read"
			}
			if found != wantTool {
				t.Fatalf("MCP list file_read=%t want=%t", found, wantTool)
			}
		}
	}
	decide := func(wantStatus int, wantAllow bool) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/rights/decide", bytes.NewBufferString(`{"principal_id":"alice","environment":"dev","capability":"tools","operation":"use"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+gateway.ControlToken)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != wantStatus {
			t.Fatalf("rights decision status=%d want=%d", response.StatusCode, wantStatus)
		}
		if wantStatus == http.StatusOK {
			var decision api.RightDecision
			if err := json.NewDecoder(response.Body).Decode(&decision); err != nil {
				t.Fatal(err)
			}
			if decision.Allow != wantAllow {
				t.Fatalf("rights decision allow=%t want=%t", decision.Allow, wantAllow)
			}
		}
	}
	call(true)
	list(true, true)
	decide(http.StatusOK, true)
	steps, release := make(chan string, 8), make(chan struct{}, 8)
	t.Cleanup(func() { close(release) })
	previousStep := applyStep
	applyStep = func(step string) { steps <- step; <-release }
	t.Cleanup(func() { applyStep = previousStep })
	done := make(chan error, 1)
	go func() {
		_, err := applyConfirm(context.Background(), dir, root, "dev", next, old.ID, nil, reconcile.Handlers{})
		done <- err
	}()
	for {
		var step string
		select {
		case step = <-steps:
		case err := <-done:
			t.Fatalf("apply ended before activation: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("apply did not reach the next publication step")
		}
		t.Logf("paused at %s", step)
		active, activeErr := passport.Current(dir)
		settings, settingsErr := stack.ReadEffective(dir, "dev")
		effective, effectiveErr := passport.LoadEffective(dir)
		switch step {
		case "bundle":
			if activeErr != nil || active != old.ID || settingsErr != nil || effectiveErr != nil || effective.Header.Metadata.Revision != old.ID {
				t.Fatalf("old snapshot changed during immutable staging: current=%q errors=%v %v %v", active, activeErr, settingsErr, effectiveErr)
			}
			call(false)
			list(false, false)
			decide(http.StatusServiceUnavailable, false)
		case "activate":
			if activeErr != nil || active != next.ID || settingsErr != nil || effectiveErr != nil || effective.Header.Metadata.Revision != next.ID || len(effective.Grants.Allow) != 0 {
				t.Fatalf("new snapshot is incomplete: current=%q errors=%v %v %v", active, activeErr, settingsErr, effectiveErr)
			}
			if _, selected := settings.Tools["files"]; selected {
				t.Fatal("new config retained the old selected tool")
			}
			call(false)
			list(false, false)
			decide(http.StatusServiceUnavailable, false)
		default:
			if activeErr == nil || settingsErr == nil || effectiveErr == nil {
				t.Fatalf("%s exposed a partially prepared revision: current=%q errors=%v %v %v", step, active, activeErr, settingsErr, effectiveErr)
			}
			call(false)
			list(false, false)
			decide(http.StatusServiceUnavailable, false)
		}
		release <- struct{}{}
		if step == "activate" {
			break
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	call(false)
	decide(http.StatusOK, false)
	settings, err := stack.ReadEffective(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	freshAuth := auth
	freshAuth.PolicyVersion = stack.PolicyVersion(settings)
	freshGateway := &toolhub.Gateway{Store: loaded, PassportRoot: passportRoot, Tokens: map[string]identity.Envelope{"fresh-activation-session-token-12345": freshAuth}, Backend: backend,
		AuditWrite: func(string, map[string]string) error { return nil }}
	freshHandler, err := freshGateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	freshServer := httptest.NewServer(freshHandler)
	t.Cleanup(freshServer.Close)
	freshClient := mcp.NewClient(&mcp.Implementation{Name: "activation-fresh", Version: "1"}, nil)
	freshSession, err := freshClient.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: freshServer.URL + toolhub.DefaultEndpointPath, HTTPClient: &http.Client{Transport: activationBearer("fresh-activation-session-token-12345")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = freshSession.Close() })
	freshList, err := freshSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("new revision MCP list: %v", err)
	}
	for _, tool := range freshList.Tools {
		if tool.Name == "file_read" {
			t.Fatal("new revision MCP list retained removed tool")
		}
	}
	if err := passport.PointCurrent(dir, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := freshSession.ListTools(t.Context(), nil); err == nil {
		t.Fatal("ToolHub accepted a profile bound to a different active revision")
	}
	if err := passport.PointCurrent(dir, next.ID); err != nil {
		t.Fatal(err)
	}
}
