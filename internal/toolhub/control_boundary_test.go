package toolhub

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestControlDefaultDenyBeforeAnySourceOrStateMutation(t *testing.T) {
	store := NewStore()
	var sourceCalls atomic.Int32
	control := &ControlPlane{Store: store, SourceResolver: func(context.Context, string) (api.ArtifactSource, error) {
		sourceCalls.Add(1)
		return api.ArtifactSource{}, errors.New("unexpected source resolution")
	}}
	// These grants cover different actions and must never implicitly grant
	// prepare_source, confirm, enable, diagnostics, or any other control tool.
	for _, kind := range []api.GrantKind{api.GrantCatalogDefault, api.GrantSelfInstall} {
		if err := putGrant(t, store, api.OperatorGrant(kind, "alice", "", "")); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range api.ControlOperations {
		if _, err := control.Invoke(t.Context(), aliceAuth(), operation, map[string]any{"source": "https://github.com/example/mcp"}); !errors.Is(err, api.ErrUnauthorized) {
			t.Fatalf("ungranted %s admitted: %v", operation, err)
		}
	}
	if sourceCalls.Load() != 0 || len(store.onboardings) != 0 || len(store.bindings) != 0 || len(store.definitions) != 0 {
		t.Fatal("denied operation reached source resolution or changed state")
	}
	for _, operation := range []string{"invoke", "prepare_source"} {
		if err := store.RequireControlOperation(aliceAuth(), operation); !errors.Is(err, api.ErrUnauthorized) {
			t.Fatalf("ungranted %s allowed: %v", operation, err)
		}
	}
}

func TestControlProjectionRevokeOnOpenMCPSession(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store}
	var backendCalls atomic.Int32
	gateway := &Gateway{Store: store, Control: control,
		Tokens: map[string]identity.Envelope{aliceToken: aliceAuth(), bobToken: bobAuth()},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			backendCalls.Add(1)
			return api.BackendResult{Text: "unexpected"}, nil
		}),
	}
	handler, err := gateway.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	alice := mcpConnect(t, server.URL, aliceToken)
	bob := mcpConnect(t, server.URL, bobToken)
	if listed, err := alice.ListTools(t.Context(), nil); err != nil || len(listed.Tools) != 0 {
		t.Fatalf("zero profile disclosed tools: %+v, %v", listed, err)
	}
	for _, name := range []string{"prepare_source", "diagnostics", "invoke"} {
		if _, err := alice.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"tool": "hidden"}}); err == nil {
			t.Fatalf("hidden tool %s was callable", name)
		}
	}
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-control", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: api.OnboardingSelfInstall, Phase: api.PhasePreparing, Revision: 1, CreatedAt: time.Now()}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	grant := api.OperatorControlGrant("alice", "status")
	if err := putGrant(t, store, grant); err != nil {
		t.Fatal(err)
	}
	if listed, err := alice.ListTools(t.Context(), nil); err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "status" {
		t.Fatalf("exact control projection: %+v, %v", listed, err)
	}
	if listed, err := bob.ListTools(t.Context(), nil); err != nil || len(listed.Tools) != 0 {
		t.Fatalf("cross-user control projection: %+v, %v", listed, err)
	}
	if body, err := callControl(t, alice, "status", map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil || body["phase"] != api.PhasePreparing {
		t.Fatalf("granted status failed: %+v, %v", body, err)
	}
	id := alice.ID()
	grant.Status, grant.Revision = api.RevokedStatus, 2
	if err := putGrant(t, store, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(t.Context(), aliceAuth(), "status", nil); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("stale handler admitted revoked operation: %v", err)
	}
	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": onboarding.OnboardingID}); err == nil {
		t.Fatal("open MCP session admitted revoked operation")
	}
	if listed, err := alice.ListTools(t.Context(), nil); err != nil || len(listed.Tools) != 0 || alice.ID() != id {
		t.Fatalf("revoked projection/session: %+v, %v", listed, err)
	}
	if backendCalls.Load() != 0 {
		t.Fatal("control projection reached connector backend")
	}
}

func TestControlGrantValidationAndStoreFailure(t *testing.T) {
	for _, operation := range []string{"", "*", "grant", "edit_config"} {
		if err := api.OperatorControlGrant("alice", operation).Validate(); !errors.Is(err, api.ErrInvalid) {
			t.Fatalf("unknown control grant accepted: %q, %v", operation, err)
		}
	}
	grant := api.OperatorControlGrant("alice", "status")
	grant.DefinitionID = "other"
	if err := grant.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("ambiguous control grant accepted: %v", err)
	}
	grant = api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")
	grant.Operation = "status"
	if err := grant.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("cross-kind operation accepted: %v", err)
	}
	store := NewStore()
	grantTestControlOperations(t, store, "alice", "status")
	deny := api.OperatorControlGrant("alice", "status")
	deny.GrantID, deny.Status = "control-deny", api.DisabledStatus
	if err := putGrant(t, store, deny); err != nil {
		t.Fatal(err)
	}
	if err := store.RequireControlOperation(aliceAuth(), "status"); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("deny did not win: %v", err)
	}
	if err := store.RequireControlOperation(aliceAuth(), "grant"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("agent authority administration accepted: %v", err)
	}
	if _, err := store.AllowedControlOperations(identity.Envelope{}); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("invalid identity accepted: %v", err)
	}
	path := filepath.Join(t.TempDir(), "store.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := loaded.RequireControlOperation(aliceAuth(), "status"); err == nil {
		t.Fatal("unavailable store admitted operation")
	}
}
