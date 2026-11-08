package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestPrivateInboundFetchUsesCurrentOwnerGrant(t *testing.T) {
	store := NewStore()
	definition := agentDefinition()
	definition.Tools = append(definition.Tools, api.ToolSpec{
		Name: "inbox_fetch", CapabilityID: "inbox.fetch", Requires: []string{"workspace.read"},
		Effect: api.WriteEffect, Audience: api.AudienceInternal, Executor: "handler:files",
		Uses:      []api.CapabilityUse{{Action: "fetch", Resource: "inbound"}, {Action: "create", Resource: "files"}},
		Arguments: []api.CLIArgument{{Name: "id", Type: "string", Required: true}},
	})
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	rule := api.CapabilityRule{CapabilityID: "files", ImplementationDigest: digest, Action: "read", Resource: "files", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 30}}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "test", Status: api.ActiveStatus, Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule}}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "test", Status: api.ActiveStatus,
		Rights:     []api.RightRule{{Capability: "tools", Operation: "use"}, {Capability: "workspace", Operation: "read"}},
		Selections: []api.CapabilitySelection{{CapabilityID: "files", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "file_read", Name: "workspace_read"}}}
	policy.RightCeiling = profile.Rights
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	grantDefinitionRights(t, store, auth, definition)
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	communication := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-secret" || r.Header.Get("X-Hub-Principal") != "alice" {
			t.Error("wrong control identity reached communication")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound/in-90":
			calls++
			w.Header().Set("X-Hub-Source", "people")
			w.Header().Set("X-Hub-Name", "report.pdf")
			_, _ = w.Write([]byte("pdf"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/inbound/in-90/consume":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer communication.Close()
	inboxes := filepath.Join(t.TempDir(), "inboxes")
	if err := os.MkdirAll(filepath.Join(inboxes, "alice"), 0700); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: store, Tokens: map[string]identity.Envelope{"owner-token-012345678901234567890123": auth}, AuditWrite: LedgerAuditWriter(ledger), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		t.Fatal("internal pre-run fetch reached agent backend")
		return api.BackendResult{}, nil
	}), InboxRoots: inboxes, CommunicationURL: communication.URL, ControlToken: "control-secret"}
	if err := g.authorizeInboundFetch(auth, func(api.EffectiveBinding, api.ToolSpec) error { return nil }); err != nil {
		t.Fatalf("fixture admission: %v", err)
	}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, id string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"id": id})
		req := httptest.NewRequest(http.MethodPost, "/v1/inbound-fetch", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	if code := call("owner-token-012345678901234567890123", "in-90"); code != http.StatusOK || calls != 1 {
		t.Fatalf("owner fetch: code=%d calls=%d", code, calls)
	}
	if code := call("owner-token-012345678901234567890123", "in-90"); code != http.StatusOK || calls != 1 {
		t.Fatalf("repeated owner fetch: code=%d calls=%d", code, calls)
	}
	if body, err := os.ReadFile(filepath.Join(inboxes, "alice", "people", "in-90", "files", "report.pdf")); err != nil || string(body) != "pdf" {
		t.Fatalf("owner inbox materialization: %v", err)
	}
	if code := call("other-token-012345678901234567890123", "in-90"); code != http.StatusUnauthorized || calls != 1 {
		t.Fatalf("foreign bearer: code=%d calls=%d", code, calls)
	}
	if code := call("owner-token-012345678901234567890123", "../in-90"); code != http.StatusBadRequest || calls != 1 {
		t.Fatalf("unsafe id: code=%d calls=%d", code, calls)
	}
	profile.Revision++
	profile.Rights = profile.Rights[:1]
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	if code := call("owner-token-012345678901234567890123", "in-90"); code != http.StatusForbidden || calls != 1 {
		t.Fatalf("revoked workspace right: code=%d calls=%d", code, calls)
	}
}
