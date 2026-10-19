package toolhub

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

func TestLifecycleIdempotentEnableDisableRemoveAndRaces(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var released []string
	control := &ControlPlane{Store: store, WorkloadRoot: root, Now: time.Now, Release: func(_ context.Context, workloadID string) error {
		released = append(released, workloadID)
		return nil
	}}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "life-1"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); err != nil {
		t.Fatal(err)
	}
	first, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	bindingID, _ := first["binding_id"].(string)
	binding, err := store.binding(bindingID)
	if err != nil {
		t.Fatal(err)
	}
	workload, err := api.NewWorkloadInstance(binding, &api.OwnerRef{Type: api.ContextOwner, ID: auth.ContextID}, "", 1, time.Now().UTC(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status = api.RunningStatus
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	second, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil || second["binding_id"] != bindingID {
		t.Fatalf("idempotent enable=%v err=%v", second, err)
	}
	if got := store.WorkloadIDsForBinding(bindingID); len(got) != 1 {
		t.Fatalf("second enable created workloads=%v", got)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"], "effects": []any{"write"}}); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("effect escalation: %v", err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"], "cpu_millis": 999999}); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("budget escalation: %v", err)
	}
	effective, err := store.Resolve(auth, bindingID)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := api.OpenWorkloadWorkspace(root, effective, "")
	if err != nil || ws.Path == "" {
		t.Fatalf("workspace=%+v err=%v", ws, err)
	}
	if _, err := control.Invoke(context.Background(), auth, "disable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if tools, err := store.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("disabled projection=%+v err=%v", tools, err)
	}
	preview, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"], "remove_nonce": preview["remove_nonce"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatal("binding volume survived remove")
	}
	found := false
	for _, id := range released {
		if id == workload.WorkloadID {
			found = true
		}
	}
	if !found {
		t.Fatalf("remove did not release workload %s: %v", workload.WorkloadID, released)
	}
	if tools, err := store.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("removed projection=%+v err=%v", tools, err)
	}

	store2 := NewStore()
	grantTestControlOperations(t, store2, "alice", api.ControlOperations...)
	if err := store2.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store2, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control2 := &ControlPlane{Store: store2, WorkloadRoot: t.TempDir(), Now: time.Now}
	prepared2, err := control2.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "race-1"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := control2.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared2["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control2.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared2["onboarding_id"], "nonce": st["nonce"]}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"onboarding_id": prepared2["onboarding_id"]}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _, _ = control2.Invoke(context.Background(), auth, "enable", args) }()
		go func() { defer wg.Done(); _, _ = control2.Invoke(context.Background(), auth, "disable", args) }()
		go func() { defer wg.Done(); _, _ = control2.Invoke(context.Background(), auth, "revoke", args) }()
	}
	wg.Wait()
	onboarding, err := store2.OnboardingFor(auth, prepared2["onboarding_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if onboarding.BindingID == "" {
		return
	}
	err = store2.AuthorizeProjected(auth, api.ProjectedToolName(definition.DefinitionID, definition.Version, "search"), func(api.ProjectedTool, api.EffectiveBinding) error { return nil })
	if err == nil {
		effective, resolveErr := store2.Resolve(auth, onboarding.BindingID)
		if resolveErr != nil || effective.Binding.Status != api.ActiveStatus {
			t.Fatalf("stale authorization admitted: resolve=%v effective=%+v", resolveErr, effective)
		}
	}
}

func TestControlEnableUsesReadinessForInitialAndReenable(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", "prepare_source", "status", "confirm", "enable", "disable")
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	var admissions int
	control := &ControlPlane{Store: store, Ready: func(context.Context, api.EffectiveBinding) error {
		admissions++
		return nil
	}, Now: time.Now}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{
		"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "ready-reenable",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); err != nil {
		t.Fatal(err)
	}
	if admissions != 1 {
		t.Fatalf("initial confirm admissions=%d", admissions)
	}
	if _, err := control.Invoke(context.Background(), auth, "disable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if admissions != 2 {
		t.Fatalf("reenable admissions=%d", admissions)
	}
}

func TestCredentialReadinessFailureDoesNotPublishOrRotateOnRetry(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "refs.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	definition := catalogReadDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "TOKEN", Required: true}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	admit := false
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error {
		if !admit {
			return errors.New("admission denied")
		}
		return nil
	}}
	grantTestControlOperations(t, store, auth.PrincipalID, "prepare_source", "status", "confirm")
	prepared, err := control.Invoke(t.Context(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "retry-readiness"})
	if err != nil {
		t.Fatal(err)
	}
	id := prepared["onboarding_id"].(string)
	onboarding, err := store.onboarding(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.SubmitCredentials(id, onboarding.FormNonce, map[string]string{"TOKEN": "fixture-value"}); err != nil {
		t.Fatal(err)
	}
	status, err := control.Invoke(t.Context(), auth, "status", map[string]any{"onboarding_id": id})
	if err != nil {
		t.Fatal(err)
	}
	confirm := map[string]any{"onboarding_id": id, "nonce": status["nonce"]}
	if _, err := control.Invoke(t.Context(), auth, "confirm", confirm); err == nil {
		t.Fatal("failed admission was accepted")
	}
	if len(store.bindings) != 0 || len(store.connections) != 1 {
		t.Fatalf("failed admission published binding or duplicated connection: bindings=%d connections=%d", len(store.bindings), len(store.connections))
	}
	if err := store.AuthorizeProjected(auth, api.ProjectedToolName(definition.DefinitionID, definition.Version, "read"), func(api.ProjectedTool, api.EffectiveBinding) error { return nil }); err == nil {
		t.Fatal("failed admission projected a tool")
	}
	admit = true
	if _, err := control.Invoke(t.Context(), auth, "confirm", confirm); err != nil {
		t.Fatal(err)
	}
	if len(store.bindings) != 1 || len(store.connections) != 1 {
		t.Fatalf("retry did not reuse owner connection: bindings=%d connections=%d", len(store.bindings), len(store.connections))
	}
}

func TestConfirmProbeFailureReopensBrokerCredentialForm(t *testing.T) {
	store := NewStore()
	definition := catalogReadDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "GRAFANA_TOKEN", Required: true}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error {
		return fmt.Errorf("%w: provider read probe failed", api.ErrUnauthorized)
	}}
	grantTestControlOperations(t, store, auth.PrincipalID, "prepare_source", "status", "confirm")
	connectionID := api.DeterministicID("conn", auth.PrincipalID, definition.DefinitionID, "onboard-probe-reset")
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connectionID, 1), ConnectionID: connectionID, Revision: 1, Backend: "credential-broker", Locator: "broker-cred-1", Keys: []string{"GRAFANA_TOKEN"}, Status: api.ActiveStatus, BrokerContractID: "grafana-sat", BrokerContractRevision: 1, BrokerEnv: map[string]string{"GRAFANA_TOKEN": "GRAFANA_TOKEN"}}
	if err := store.PutCredentialReference(reference); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: connectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-probe-reset", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1, Mode: api.OnboardingSelfInstall, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, Phase: api.PhaseAwaitingConfirm, Locator: "broker-cred-1", BrokerCredentialID: "broker-cred-1", BrokerRequestID: "r-dead", BrokerAuthorizationURL: "https://broker/connect/r-dead", Required: []api.CredentialHint{{Name: "GRAFANA_TOKEN"}}, ConfirmationNonce: "nonce-1", ConfirmationExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(t.Context(), auth, "confirm", map[string]any{"onboarding_id": onboarding.OnboardingID, "nonce": "nonce-1"}); err == nil {
		t.Fatal("rejected probe passed confirm")
	}
	after, err := store.onboarding(onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Phase != api.PhaseAwaitingCreds || after.BrokerRotateCredentialID != "broker-cred-1" || after.BrokerCredentialID != "" || after.Locator != "" || after.BrokerRequestID != "" || after.BrokerAuthorizationURL != "" || after.ConfirmationNonce != "" {
		t.Fatalf("probe rejection did not reopen the credential form: %+v", after)
	}
	if after.Error == "" {
		t.Fatal("probe rejection dropped the failure record")
	}
}

func TestConfirmDeliveryFailureReopensBrokerCredentialForm(t *testing.T) {
	// A credential materialized without the required inputs — e.g. the owner
	// demoted a required field on the form — is a fixable credential failure,
	// not an infrastructure isolation fault: the onboarding must reopen the
	// credential step and mint a fresh request instead of pinning on
	// awaiting-confirm until the nonce dies.
	store := NewStore()
	definition := catalogReadDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "MCP_HTTP_AUTH_TOKEN", Required: true}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error {
		return fmt.Errorf("%w: %w: broker delivery does not satisfy %s", api.ErrIsolation, api.ErrCredentialDelivery, definition.DefinitionID)
	}}
	grantTestControlOperations(t, store, auth.PrincipalID, "prepare_source", "status", "confirm")
	connectionID := api.DeterministicID("conn", auth.PrincipalID, definition.DefinitionID, "onboard-delivery-reset")
	reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID(connectionID, 1), ConnectionID: connectionID, Revision: 1, Backend: "credential-broker", Locator: "broker-cred-1", Keys: []string{"MCP_HTTP_AUTH_TOKEN"}, Status: api.ActiveStatus, BrokerContractID: "marketplace-sat", BrokerContractRevision: 1, BrokerEnv: map[string]string{"MCP_HTTP_AUTH_TOKEN": "MCP_HTTP_AUTH_TOKEN"}}
	if err := store.PutCredentialReference(reference); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: connectionID, Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-delivery-reset", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1, Mode: api.OnboardingSelfInstall, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, Phase: api.PhaseAwaitingConfirm, Locator: "broker-cred-1", BrokerCredentialID: "broker-cred-1", BrokerRequestID: "r-dead", BrokerAuthorizationURL: "https://broker/connect/r-dead", Required: []api.CredentialHint{{Name: "MCP_HTTP_AUTH_TOKEN"}}, ConfirmationNonce: "nonce-1", ConfirmationExpires: time.Now().Add(time.Hour)}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(t.Context(), auth, "confirm", map[string]any{"onboarding_id": onboarding.OnboardingID, "nonce": "nonce-1"}); err == nil {
		t.Fatal("unsatisfiable delivery passed confirm")
	}
	after, err := store.onboarding(onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Phase != api.PhaseAwaitingCreds || after.BrokerRotateCredentialID != "broker-cred-1" || after.BrokerCredentialID != "" || after.Locator != "" || after.BrokerRequestID != "" || after.BrokerAuthorizationURL != "" || after.ConfirmationNonce != "" {
		t.Fatalf("delivery failure did not reopen the credential form: %+v", after)
	}
	if after.Error == "" {
		t.Fatal("delivery failure dropped the failure record")
	}
}

func TestConfirmAndEnableFailureRecordedOnOnboarding(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "refs.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	definition := catalogReadDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "TOKEN", Required: true}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	admit := false
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error {
		if !admit {
			return errors.New("controller returned 503 Service Unavailable: workload enforcement unavailable: bridge-create: required runtime credential missing")
		}
		return nil
	}}
	grantTestControlOperations(t, store, auth.PrincipalID, "prepare_source", "status", "confirm", "enable", "disable")
	prepared, err := control.Invoke(t.Context(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "failure-visible"})
	if err != nil {
		t.Fatal(err)
	}
	id := prepared["onboarding_id"].(string)
	onboarding, err := store.onboarding(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.SubmitCredentials(id, onboarding.FormNonce, map[string]string{"TOKEN": "fixture-value"}); err != nil {
		t.Fatal(err)
	}
	status, err := control.Invoke(t.Context(), auth, "status", map[string]any{"onboarding_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(t.Context(), auth, "confirm", map[string]any{"onboarding_id": id, "nonce": status["nonce"]}); err == nil {
		t.Fatal("failed admission was confirmed")
	}
	stored, _ := store.onboarding(id)
	if !strings.Contains(stored.Error, "required runtime credential missing") || stored.Phase != api.PhaseAwaitingConfirm || stored.ConfirmationUsed {
		t.Fatalf("confirm failure not recorded: %+v", stored)
	}
	status, err = control.Invoke(t.Context(), auth, "status", map[string]any{"onboarding_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(status["error"]), "required runtime credential missing") || !strings.Contains(fmt.Sprint(status["instructions"]), "last confirm or enable failed") {
		t.Fatalf("status hides the recorded failure: %v", status)
	}
	admit = true
	if _, err := control.Invoke(t.Context(), auth, "confirm", map[string]any{"onboarding_id": id, "nonce": status["nonce"]}); err != nil {
		t.Fatal(err)
	}
	stored, _ = store.onboarding(id)
	if stored.Error != "" || stored.Phase != api.PhaseConfirmed {
		t.Fatalf("successful confirm kept stale error: %+v", stored)
	}
	if _, err := control.Invoke(t.Context(), auth, "disable", map[string]any{"onboarding_id": id}); err != nil {
		t.Fatal(err)
	}
	admit = false
	if _, err := control.Invoke(t.Context(), auth, "enable", map[string]any{"onboarding_id": id}); err == nil {
		t.Fatal("failed re-enable was accepted")
	}
	stored, _ = store.onboarding(id)
	if !strings.Contains(stored.Error, "required runtime credential missing") || stored.Phase != api.PhaseDisabled {
		t.Fatalf("enable failure not recorded: %+v", stored)
	}
	admit = true
	if _, err := control.Invoke(t.Context(), auth, "enable", map[string]any{"onboarding_id": id}); err != nil {
		t.Fatal(err)
	}
	stored, _ = store.onboarding(id)
	if stored.Error != "" || stored.Phase != api.PhaseEnabled {
		t.Fatalf("successful enable kept stale error: %+v", stored)
	}
}

func TestNinetyFiveUniqueAndFiveSharedCredentialRefs(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "refs.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	control := &ControlPlane{Store: store, Secrets: secrets, WorkloadRoot: t.TempDir(), Now: time.Now}
	grantTestControlOperations(t, store, "alice", "prepare_source", "status", "confirm", "enable")
	base := catalogReadDefinition()
	base.Credentials = []api.CredentialInput{{Name: "TOKEN", Required: true}}
	locators := map[string]int{}
	workloads := map[string]bool{}
	for i := 0; i < 95; i++ {
		definition := base
		definition.DefinitionID = fmt.Sprintf("mcp-ref-%02d", i)
		if err := store.RegisterDefinition(definition); err != nil {
			t.Fatal(err)
		}
		auth := aliceAuth()
		if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
			t.Fatal(err)
		}
		prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": fmt.Sprintf("u-%02d", i)})
		if err != nil {
			t.Fatal(err)
		}
		onboardingID := prepared["onboarding_id"].(string)
		onboarding, err := store.onboarding(onboardingID)
		if err != nil {
			t.Fatal(err)
		}
		if err := control.SubmitCredentials(onboardingID, onboarding.FormNonce, map[string]string{"TOKEN": fmt.Sprintf("value-%02d", i)}); err != nil {
			t.Fatal(err)
		}
		status, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": onboardingID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": onboardingID, "nonce": status["nonce"]}); err != nil {
			t.Fatal(err)
		}
		enabled, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": onboardingID})
		if err != nil {
			t.Fatal(err)
		}
		onboarding, _ = store.onboarding(onboardingID)
		locators[onboarding.Locator]++
		bindingID := enabled["binding_id"].(string)
		effective, err := store.Resolve(auth, bindingID)
		if err != nil {
			t.Fatal(err)
		}
		if workloads[effective.WorkloadID] {
			t.Fatalf("duplicate workload %s", effective.WorkloadID)
		}
		workloads[effective.WorkloadID] = true
		env, err := api.DecryptAuthorized(secrets, effective)
		if err != nil || env["TOKEN"] != fmt.Sprintf("value-%02d", i) {
			t.Fatalf("unique decrypt=%v err=%v", env, err)
		}
	}
	sharedDef := base
	sharedDef.DefinitionID = "mcp-shared"
	if err := store.RegisterDefinition(sharedDef); err != nil {
		t.Fatal(err)
	}
	sharedLocator, err := secrets.NewLocator()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(sharedLocator, "shared-owner", map[string]string{"TOKEN": "shared-value"}); err != nil {
		t.Fatal(err)
	}
	principals := []string{"u00", "u01", "u02", "u03", "u04"}
	if err := store.PutSharedCredentialPolicy(api.SharedCredentialPolicy{Schema: api.SchemaVersion, PolicyID: "policy-shared", DefinitionID: "mcp-shared", Locator: sharedLocator, StoreOwner: "shared-owner", Principals: principals, IssuedBy: "operator", Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	for _, principal := range principals {
		auth := identity.TelegramEnvelope(principal, 9, principal+"-runtime", "policy-1")
		if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, principal, "mcp-shared", "1.0.0")); err != nil {
			t.Fatal(err)
		}
		grantTestControlOperations(t, store, principal, "prepare_source", "status", "confirm", "enable")
		prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": "mcp-shared", "version": "1.0.0", "request_key": "shared-" + principal})
		if err != nil {
			t.Fatal(err)
		}
		onboardingID := prepared["onboarding_id"].(string)
		onboarding, err := store.onboarding(onboardingID)
		if err != nil {
			t.Fatal(err)
		}
		if err := control.SubmitCredentials(onboardingID, onboarding.FormNonce, map[string]string{"TOKEN": "ignored-unique"}); err != nil {
			t.Fatal(err)
		}
		status, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": onboardingID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": onboardingID, "nonce": status["nonce"]}); err != nil {
			t.Fatal(err)
		}
		enabled, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": onboardingID})
		if err != nil {
			t.Fatal(err)
		}
		onboarding, _ = store.onboarding(onboardingID)
		if onboarding.Locator != sharedLocator {
			t.Fatalf("shared locator=%s want=%s", onboarding.Locator, sharedLocator)
		}
		locators[onboarding.Locator]++
		effective, err := store.Resolve(auth, enabled["binding_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if workloads[effective.WorkloadID] {
			t.Fatalf("shared binding reused workload %s", effective.WorkloadID)
		}
		workloads[effective.WorkloadID] = true
		env, err := api.DecryptAuthorized(secrets, effective)
		if err != nil || env["TOKEN"] != "shared-value" {
			t.Fatalf("shared decrypt=%v err=%v", env, err)
		}
		if env["TOKEN"] == "ignored-unique" {
			t.Fatal("unique form value leaked into shared locator")
		}
	}
	if len(locators) != 96 {
		t.Fatalf("locator count=%d map=%d", len(locators), len(locators))
	}
	if locators[sharedLocator] != 5 {
		t.Fatalf("shared uses=%d", locators[sharedLocator])
	}
	if len(workloads) != 100 {
		t.Fatalf("workloads=%d", len(workloads))
	}
}

func TestEnableOnActiveBindingSupersedesSiblingVersions(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{
		"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "active-supersede",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); err != nil {
		t.Fatal(err)
	}
	enabled, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	bindingID, _ := enabled["binding_id"].(string)
	// Recreate the legacy dirty state: a sibling version stays active next to
	// the caller's binding, as stores written before supersession still carry.
	v2 := catalogReadDefinition()
	v2.Version = "1.0.1"
	if err := store.RegisterDefinition(v2); err != nil {
		t.Fatal(err)
	}
	newer := auth
	newer.PolicyVersion = "policy-newer"
	sibling, err := store.Enable(newer, v2.DefinitionID, v2.Version)
	if err != nil {
		t.Fatal(err)
	}
	stale := store.bindings[bindingID]
	stale.Status = api.ActiveStatus
	store.bindings[bindingID] = stale
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if store.bindings[bindingID].Status != api.ActiveStatus {
		t.Fatalf("kept binding status=%s", store.bindings[bindingID].Status)
	}
	if store.bindings[sibling.ToolBindingID].Status != api.DisabledStatus {
		t.Fatalf("sibling binding status=%s", store.bindings[sibling.ToolBindingID].Status)
	}
	tools, err := store.ListProjectedTools(auth)
	if err != nil || len(tools) != 1 {
		t.Fatalf("projected=%+v err=%v", tools, err)
	}
}
