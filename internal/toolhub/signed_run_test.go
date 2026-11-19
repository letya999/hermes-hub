package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGatewayChecksSignedRunBeforeBackend(t *testing.T) {
	ctx := t.Context()
	store, auth, _, _ := managedStore(t)
	registry, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if _, err := registry.ImportLegacy(ctx, "fixture", auth.Organization, []commodel.User{{ID: auth.PrincipalID, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	bundle := contract.PolicyBundle{Space: auth.ContextID, Environment: auth.Environment, PassportRevision: "r-1", Passports: map[string]json.RawMessage{}}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		extra := ""
		switch name {
		case "config":
			extra = `,"memory":{"enabled":true}`
		case "grants":
			extra = `,"allow":{"memory":["read","search"]}`
		}
		bundle.Passports[name] = json.RawMessage(fmt.Sprintf(`{"apiVersion":"six-passport/v2","kind":%q,"metadata":{"space":"alice","environment":"dev","revision":"r-1"}%s}`, name, extra))
	}
	base, err := registry.StagePassports(ctx, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePassports(ctx, bundle.Space, bundle.Environment, "", base.Digest); err != nil {
		t.Fatal(err)
	}
	req := contract.TemplateRequest{Subject: auth.PrincipalID, Actor: "agent:" + auth.PrincipalID + ":default", Space: auth.ContextID}
	signed, policy, err := registry.BindPassports(ctx, req, auth.Environment)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := policy.Identity.Revision()
	if err != nil {
		t.Fatal(err)
	}
	record := privateRunRecord(auth, "job-1", "run-1")
	record.Envelope, record.Deadline, record.TaskID = auth, time.Now().Add(time.Minute), "default"
	record.ActingIdentity = task.ActingIdentity{Subject: req.Subject, Actor: req.Actor, Organization: policy.Identity.OrganizationID, TemplateRevision: revision}
	record.PolicyRevision, record.PassportDigest = signed.Digest, base.Digest
	token, err := registry.IssueRun(ctx, task.ClaimsForRun(record, contract.ToolHubAudience, policy.Identity.Restrictions.Mode), record.Deadline, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	const transportToken = "synthetic-boundary-token-0123456789"
	g := &Gateway{Store: store, RunIdentity: registry, Tokens: map[string]identity.Envelope{transportToken: auth}, Runs: runAuthorityFunc(func(context.Context, string) (api.RunContext, error) {
		return record, nil
	}), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		calls.Add(1)
		return api.BackendResult{Text: "ok"}, nil
	})}
	attachGatewayAudit(t, g)
	call := func(jobID, runID, bearer string) error {
		corr := api.HeaderCorrelation(jobID, runID)
		corr.Scoped, corr.RunToken = true, bearer
		_, err := g.CallAuthorized(api.WithCallCorrelation(ctx, corr), auth, "workspace_read", map[string]any{"path": "docs/readme.md", "scope": "workspace"})
		return err
	}
	if err := call(record.JobID, record.RunID, token); err != nil || calls.Load() != 1 {
		t.Fatalf("valid signed run did not reach the backend, calls=%d err=%v", calls.Load(), err)
	}
	prefix := token[:strings.LastIndexByte(token, '.')+1]
	for _, tc := range []struct{ job, run, token string }{
		{record.JobID, record.RunID, ""},
		{record.JobID, record.RunID, prefix + strings.Repeat("A", 86)},
		{record.JobID, "run-other", token},
		{"job-other", record.RunID, token},
	} {
		if err := call(tc.job, tc.run, tc.token); !errors.Is(err, api.ErrUnauthorized) || calls.Load() != 1 {
			t.Fatalf("invalid authority reached backend, calls=%d err=%v", calls.Load(), err)
		}
	}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, tc := range []struct{ path, token string }{
		{DefaultEndpointPath, ""},
		{RunEndpointPath, ""},
		{RunEndpointPath, token},
	} {
		transport := testBearerTransport{base: server.Client().Transport, token: transportToken, jobID: record.JobID, runID: record.RunID, runToken: tc.token}
		client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + tc.path, HTTPClient: &http.Client{Transport: transport}, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_read", Arguments: map[string]any{"path": "docs/readme.md", "scope": "workspace"}})
		_ = session.Close()
		allowed := err == nil && !result.IsError
		if allowed != (tc.token != "") {
			t.Fatal("MCP path failed to bind authorization to the signed run")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("MCP authorization dispatched %d total backend calls, want 2", calls.Load())
	}
	t.Run("memory requires its own audience", func(t *testing.T) {
		memoryToken, err := registry.IssueRun(ctx, task.ClaimsForRun(record, contract.MemoryAudience, policy.Identity.Restrictions.Mode), record.Deadline, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		corr := api.HeaderCorrelation(record.JobID, record.RunID)
		corr.Scoped, corr.RunToken = true, token
		for _, tc := range []struct {
			bearer  string
			allowed bool
		}{{"", false}, {token, false}, {memoryToken, true}} {
			corr.MemoryToken = tc.bearer
			if err := g.requireLiveMemoryRun(ctx, auth, corr); (err == nil) != tc.allowed {
				t.Fatalf("memory audience admission: allowed=%t, want=%t", err == nil, tc.allowed)
			}
		}
	})
	record.CurrentGeneration = "gen-4"
	if err := call(record.JobID, record.RunID, token); !errors.Is(err, api.ErrUnauthorized) || calls.Load() != 2 {
		t.Fatalf("stale generation reached backend, calls=%d err=%v", calls.Load(), err)
	}
	t.Run("memory bootstrap checks signed scopes before Hermes admission", func(t *testing.T) {
		gateway, _, records := memoryContextFixture(t)
		gateway.RunIdentity = registry
		admission := records["job-1"]
		admission.RunID = ""
		admission.Deadline = record.Deadline
		admission.ActingIdentity = record.ActingIdentity
		admission.PassportDigest = base.Digest
		claim := `{"job_id":"job-1","admission_id":"adm-1","text":"earl grey supplier"}`
		scope := contract.ServiceRequest{Space: auth.ContextID, Environment: auth.Environment, RequestDigest: contract.PolicyDigest([]byte(claim))}
		serviceToken, err := registry.IssueMemoryContextService(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		headers := map[string]string{"Authorization": "Bearer " + serviceToken}
		check := func(want int) {
			t.Helper()
			records[admission.JobID] = admission
			got := memoryContextCall(t, gateway, "", claim, headers)
			if got.Code != want || strings.Contains(got.Body.String(), "alice curated core") != (want == http.StatusOK) {
				t.Fatalf("bootstrap memory status=%d, want=%d", got.Code, want)
			}
		}
		check(http.StatusForbidden)
		admission.PolicyRevision = signed.Digest
		check(http.StatusOK)
		for _, denied := range []*httptest.ResponseRecorder{
			memoryContextCall(t, gateway, auth.PrincipalID, claim, headers),
			memoryContextCall(t, gateway, auth.PrincipalID, claim, nil),
			memoryContextCall(t, gateway, "", claim+" ", headers),
		} {
			if denied.Code != http.StatusUnauthorized || strings.Contains(denied.Body.String(), "alice curated core") {
				t.Fatalf("substituted memory service authority status=%d", denied.Code)
			}
		}
		admission.Revoked = true
		check(http.StatusForbidden)
		admission.Revoked = false
		if err := registry.SetLayer(ctx, auth.PrincipalID, "user:"+auth.PrincipalID, contract.Restrictions{MemoryScopes: []string{}}); err != nil {
			t.Fatal(err)
		}
		denied, limited, err := registry.BindPassports(ctx, req, auth.Environment)
		if err != nil {
			t.Fatal(err)
		}
		admission.ActingIdentity.TemplateRevision, err = limited.Identity.Revision()
		if err != nil {
			t.Fatal(err)
		}
		admission.PolicyRevision = denied.Digest
		check(http.StatusForbidden)
	})
}

func TestSignedToolPolicyNarrowsPassportRightsAndMode(t *testing.T) {
	policy := contract.PolicyBundle{Identity: &contract.AgentTemplate{Restrictions: contract.Restrictions{Mode: "read-only", Grants: []string{"workspace.read"}}},
		Passports: map[string]json.RawMessage{"grants": json.RawMessage(`{"apiVersion":"six-passport/v2","kind":"grants","metadata":{"revision":"r-1"},"allow":{"workspace":["read","write"]}}`)}}
	tool := api.ToolSpec{Effect: api.ReadEffect, Requires: []string{"workspace.read"}}
	if err := authorizeToolPolicy(&policy, tool); err != nil {
		t.Fatalf("allowed read refused: %v", err)
	}
	for _, right := range []string{"workspace.write", "tasks.delegate"} {
		tool.Requires = []string{right}
		if err := authorizeToolPolicy(&policy, tool); !errors.Is(err, api.ErrUnauthorized) {
			t.Fatalf("out-of-policy right admitted: %s", right)
		}
	}
	tool.Requires, tool.Effect = []string{"workspace.read"}, api.WriteEffect
	for _, mode := range []string{"read-only", "plan", "ask"} {
		policy.Identity.Restrictions.Mode = mode
		if err := authorizeToolPolicy(&policy, tool); !errors.Is(err, api.ErrUnauthorized) {
			t.Fatalf("mode %s admitted an unattended write", mode)
		}
	}
	policy.Identity.Restrictions.Mode = "full-access"
	policy.Identity.Restrictions.Guardrails = []string{"owner-policy"}
	if err := authorizeToolPolicy(&policy, tool); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatal("guardrail admitted an unattended write")
	}
}
