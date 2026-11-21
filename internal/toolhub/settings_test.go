package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/contracts/spawn/v1/fake"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestSettingsPublisherCanUpdateAdmissionRegistry(t *testing.T) {
	for _, extension := range []string{".json", ".db"} {
		t.Run(extension, func(t *testing.T) {
			store := NewStore()
			definition := agentDefinition()
			definition.Tools = []api.ToolSpec{{Name: "settings_edit", CapabilityID: "settings.edit", Requires: []string{"config.edit_own"}, Effect: api.WriteEffect, Audience: api.AudienceModel, Executor: "handler:settings", Description: "Publisher regression", Uses: []api.CapabilityUse{{Action: "update", Resource: "settings"}}}}
			if err := store.RegisterDefinition(definition); err != nil {
				t.Fatal(err)
			}
			auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
			auth.CapabilityProfile, auth.Environment, auth.Generation, auth.Organization = "alice-default", "dev", 1, "example"
			binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
			binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
			if err := store.PutBinding(binding); err != nil {
				t.Fatal(err)
			}
			digest := api.DefinitionDigest(definition)
			rule := api.CapabilityRule{CapabilityID: "settings.edit", ImplementationDigest: digest, Action: "update", Resource: "settings", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
			rights := []api.RightRule{{Capability: "tools", Operation: "use"}, {Capability: "config", Operation: "edit_own"}}
			policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Publisher regression", Status: api.ActiveStatus, Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule}, RightCeiling: rights}
			profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: policy.Reason, Status: api.ActiveStatus, Rights: rights, Selections: []api.CapabilitySelection{{CapabilityID: rule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "settings_edit", Name: "settings_edit"}}}
			if err := putPolicy(t, store, policy); err != nil {
				t.Fatal(err)
			}
			if err := putProfile(t, store, profile); err != nil {
				t.Fatal(err)
			}
			grantDefinitionRights(t, store, auth, definition)
			profile = store.capabilityProfiles[auth.CapabilityProfile]
			file := filepath.Join(t.TempDir(), "registry"+extension)
			if err := store.Save(file); err != nil {
				t.Fatal(err)
			}
			if err := store.CloseDB(); err != nil {
				t.Fatal(err)
			}
			reader, err := Load(file)
			if err != nil {
				t.Fatal(err)
			}
			reader.RightsDoc, reader.RightsLedger = store.RightsDoc, store.RightsLedger
			store = reader
			t.Cleanup(func() { _ = store.CloseDB() })
			writer, err := Load(file)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = writer.CloseDB() })
			called := false
			gateway := &Gateway{Store: store, AuditWrite: func(string, map[string]string) error { return nil }, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
				if !store.mu.TryLock() {
					return api.BackendResult{}, errors.New("publisher still holds admission mutex")
				}
				store.mu.Unlock()
				profile.Revision++
				mustConfirm(t, &profile)
				if err := writer.PutCapabilityProfile(profile); err != nil {
					return api.BackendResult{}, err
				}
				called = true
				return api.BackendResult{Structured: map[string]any{"changed": true}}, nil
			})}
			result, err := gateway.CallAuthorized(t.Context(), auth, "settings_edit", map[string]any{"key": "compression.enabled", "value": "false", "base_revision": "r-base"})
			if err != nil || result == nil || result.IsError || !called {
				t.Fatalf("publisher could not commit: %v", err)
			}
			profile.Revision++
			profile.Status = api.RevokedStatus
			if err := putProfile(t, writer, profile); err != nil {
				t.Fatal(err)
			}
			called = false
			if _, err := gateway.CallAuthorized(t.Context(), auth, "settings_edit", map[string]any{"key": "compression.enabled", "value": "false", "base_revision": "r-base"}); err == nil || called {
				t.Fatal("revoked publisher reached backend")
			}
		})
	}
}

// The settings tool family rides the spawn/v1 contract: spawn-controller
// owns the read and the config edit, and the effective digest travels in
// the schema's published_revision field. The respawn that env-carried
// config needs still goes to the authenticated supervisor route (S7b
// transition, removed in S7c).
func TestSettingsBackendUsesSpawnContractAndSupervisedRespawn(t *testing.T) {
	fakeServer := &fake.Server{}
	spawnClient := spawnv1.NewInProc(fakeServer)
	ctx := context.Background()
	if _, err := fakeServer.CreateSpace(ctx, spawnv1.CreateSpaceRequest{Org: "local", Owner: "alice", ID: "alice", Environment: "dev"}); err != nil {
		t.Fatal(err)
	}
	applied, err := spawnClient.Apply(ctx, "alice", spawnv1.ApplyRequest{
		Org: "local", Owner: "alice", IdempotencyKey: "settings-test-seed",
		Layers: map[string]map[string]map[string]any{
			"user": {"config": map[string]any{"runtime": map[string]any{"compression": map[string]any{"enabled": true}}}},
		},
	})
	if err != nil || applied.EffectiveDigest == "" {
		t.Fatalf("seed apply: %v", err)
	}
	var respawnCalls atomic.Int32
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respawnCalls.Add(1)
		if r.URL.Path != "/v1/respawn" || r.Header.Get("Authorization") != "Bearer control-secret" {
			t.Error("respawn left the authenticated supervisor route")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["principal_id"] != "alice" {
			t.Errorf("respawn request was not bound to the principal: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}))
	defer supervisor.Close()
	backend := api.AgentExecBackend{
		Exec: func(context.Context, api.EffectiveBinding, api.AgentExecRequest) (api.AgentExecResult, error) {
			t.Fatal("settings reached runtime executor")
			return api.AgentExecResult{}, nil
		},
		Settings: settingsBackend("dev", supervisor.URL, "control-secret", spawnClient),
	}
	effective := api.EffectiveBinding{Binding: api.ToolBinding{PrincipalID: "alice"}, Definition: api.ToolDefinition{Transport: api.AgentTools}}
	read := api.ToolSpec{Name: "settings_read", Executor: "handler:settings"}
	badIdentity := effective
	badIdentity.Binding.PrincipalID = "../alice"
	if _, err := backend.Call(context.Background(), badIdentity, read, nil); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("unvalidated principal reached settings path: %v", err)
	}
	out, err := backend.Call(context.Background(), effective, read, map[string]any{"key": "compression.enabled"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.Structured.(map[string]any)
	if result["published_revision"] != applied.EffectiveDigest || result["effective_revision"] != applied.EffectiveDigest ||
		result["published_values"].(map[string]any)["compression.enabled"] != true || result["secret_values_included"] != false {
		t.Fatalf("settings read did not report published state: %v", result)
	}
	edit := api.ToolSpec{Name: "settings_edit", Executor: "handler:settings"}
	if _, err := backend.Call(context.Background(), effective, edit, map[string]any{"key": "compression.enabled", "value": "false"}); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("edit without base revision: %v", err)
	}
	out, err = backend.Call(context.Background(), effective, edit, map[string]any{"key": "compression.enabled", "value": "false", "base_revision": "sha256:stale"})
	if err != nil || !out.IsError {
		t.Fatalf("stale base was accepted: %v %v", out, err)
	}
	out, err = backend.Call(context.Background(), effective, edit, map[string]any{"key": "compression.enabled", "value": "false", "base_revision": applied.EffectiveDigest})
	if err != nil {
		t.Fatal(err)
	}
	edited := out.Structured.(map[string]any)
	if edited["published_revision"] == applied.EffectiveDigest || edited["changed"] != true || respawnCalls.Load() != 1 {
		t.Fatalf("settings edit did not publish once and respawn: %v", out)
	}
}
