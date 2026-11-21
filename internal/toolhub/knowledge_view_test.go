package toolhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestKnowledgeOpenLinkChecksRunOwnerExpiryAndCurrentRights(t *testing.T) {
	store, auth, policy, profile := managedStore(t)
	definition := agentDefinition()
	definition.Version = "18.0.2"
	definition.Tools = []api.ToolSpec{{Name: "memory_open", CapabilityID: "memory.open", Requires: []string{"memory.search"},
		Effect: api.ReadEffect, Audience: api.AudienceModel, Executor: "runtime:toolhub", Description: "Open private knowledge.",
		Uses: []api.CapabilityUse{{Action: "read", Resource: "memory"}}}}
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
	rule := api.CapabilityRule{CapabilityID: "memory.open", ImplementationDigest: digest, Action: "read", Resource: "memory",
		Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	policy.Revision++
	policy.Ceiling = append(policy.Ceiling, rule)
	policy.Defaults = append(policy.Defaults, rule)
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	profile.PassportRevision = "r-0123456789abcdef"
	profile.Selections = append(profile.Selections, api.CapabilitySelection{CapabilityID: "memory.open", DefinitionID: definition.DefinitionID,
		DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "memory_open", Name: "memory_open"})
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	grantDefinitionRights(t, store, auth, definition)
	vaults, passports := t.TempDir(), t.TempDir()
	searchVaultFixture(t, vaults, passports, auth.PrincipalID, auth.Environment)
	resolver, err := NewVaultResolver(vaults, dirTreeReader{root: passports})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	record := privateRunRecord(auth, "job-private", "run-private")
	g := &Gateway{Store: store, Vaults: resolver, Spawn: dirTreeReader{root: passports}, primaryToken: strings.Repeat("k", 32),
		Control: &ControlPlane{Store: store, FormOrigin: "http://127.0.0.1:8090", Now: func() time.Time { return now }},
		Runs:    runAuthorityFunc(func(context.Context, string) (api.RunContext, error) { return record, nil }), Backend: api.RoutingBackend{}}
	attachGatewayAudit(t, g)
	ctx := api.WithCallCorrelation(t.Context(), api.HeaderCorrelation("job-private", "run-private"))
	call, err := g.CallAuthorized(ctx, auth, "memory_open", nil)
	if err != nil || call.IsError {
		t.Fatalf("private memory_open: %+v %v", call, err)
	}
	body, ok := call.StructuredContent.(map[string]any)
	if !ok || body["status"] != "ok" {
		t.Fatalf("link result: %+v", call.StructuredContent)
	}
	link, _ := body["url"].(string)
	if link == "" {
		t.Fatal("missing knowledge link")
	}
	serve := func(raw string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		g.PublicMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, raw, nil))
		return rec
	}
	if rec := serve(link + "&q=earl+grey"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "earl grey supplier") {
		t.Fatalf("owner search: status=%d body=%q", rec.Code, rec.Body.String())
	}
	searched, err := g.memorySearch(t.Context(), auth, map[string]any{"query": "earl grey"})
	if err != nil {
		t.Fatal(err)
	}
	hits := searched["hits"].([]map[string]any)
	if len(hits) != 1 {
		t.Fatalf("search hits: %d", len(hits))
	}
	if rec := serve(link + "&id=" + url.QueryEscape(api.ArgString(hits[0], "id"))); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "we picked the earl grey supplier") {
		t.Fatalf("owner read: status=%d body=%q", rec.Code, rec.Body.String())
	}
	post := httptest.NewRecorder()
	g.PublicMux().ServeHTTP(post, httptest.NewRequest(http.MethodPost, link, strings.NewReader("text=changed")))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("knowledge write status=%d", post.Code)
	}
	parsed, _ := url.Parse(link)
	token := parsed.Query().Get("t")
	if rec := serve("http://127.0.0.1:8090/knowledge?t=" + url.QueryEscape(token+"x")); rec.Code != http.StatusForbidden {
		t.Fatalf("forged link status=%d", rec.Code)
	}
	issued := store.RightsLedger
	store.RightsLedger = func() (*api.Governance, error) { return &api.Governance{}, nil }
	if rec := serve(link + "&q=earl+grey"); rec.Code != http.StatusForbidden {
		t.Fatalf("revoked right status=%d", rec.Code)
	}
	store.RightsLedger = issued
	writeMemoryPassportEnv(t, passports, auth.PrincipalID, auth.Environment, false)
	if rec := serve(link + "&q=earl+grey"); rec.Code != http.StatusForbidden {
		t.Fatalf("disabled memory status=%d", rec.Code)
	}
	writeMemoryPassportEnv(t, passports, auth.PrincipalID, auth.Environment, true)
	record.ScopeID = "group:alice"
	if _, err := g.CallAuthorized(ctx, auth, "memory_open", nil); err == nil {
		t.Fatal("group run minted a private knowledge link")
	}
	record.ScopeID = "user:alice"
	record.Status = "completed"
	if _, err := g.CallAuthorized(ctx, auth, "memory_open", nil); err == nil {
		t.Fatal("completed run minted a knowledge link")
	}
	record.Status = "running"
	profile = store.capabilityProfiles[auth.CapabilityProfile]
	profile.Revision++
	profile.Selections = profile.Selections[:len(profile.Selections)-1]
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	if rec := serve(link + "&q=earl+grey"); rec.Code != http.StatusForbidden {
		t.Fatalf("revoked selection status=%d", rec.Code)
	}
	now = now.Add(knowledgeLinkTTL)
	if rec := serve(link); rec.Code != http.StatusGone {
		t.Fatalf("expired link status=%d", rec.Code)
	}
}

func TestKnowledgeOriginRequiresTLSOutsideLoopback(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://example.com/path", "https://user:pass@example.com", "javascript:alert(1)"} {
		if _, err := knowledgeOrigin(raw); err == nil {
			t.Fatalf("unsafe origin %q admitted", raw)
		}
	}
	for _, raw := range []string{"https://example.com", "http://127.0.0.1:8090"} {
		if _, err := knowledgeOrigin(raw); err != nil {
			t.Fatalf("safe origin %q denied: %v", raw, err)
		}
	}
}
