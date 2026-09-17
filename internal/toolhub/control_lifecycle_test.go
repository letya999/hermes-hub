package toolhub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

func TestLifecycleIdempotentEnableDisableRemoveAndRaces(t *testing.T) {
	store := NewStore()
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	control := &ControlPlane{Store: store, WorkloadRoot: root, Now: time.Now}
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
	workload, err := NewWorkloadInstance(binding, &OwnerRef{Type: ContextOwner, ID: auth.ContextID}, "", 1, time.Now().UTC(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status = RunningStatus
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
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"], "effects": []any{"write"}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("effect escalation: %v", err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"], "cpu_millis": 999999}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("budget escalation: %v", err)
	}
	effective, err := store.Resolve(auth, bindingID)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := OpenWorkloadWorkspace(root, effective, "")
	if err != nil || ws.Path == "" {
		t.Fatalf("workspace=%+v err=%v", ws, err)
	}
	if _, err := control.Invoke(context.Background(), auth, "disable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if tools, err := store.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("disabled projection=%+v err=%v", tools, err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatal("binding volume survived remove")
	}
	if tools, err := store.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("removed projection=%+v err=%v", tools, err)
	}

	store2 := NewStore()
	if err := store2.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := store2.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
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
	err = store2.AuthorizeProjected(auth, ProjectedToolName(definition.DefinitionID, definition.Version, "search"), func(ProjectedTool, EffectiveBinding) error { return nil })
	if err == nil {
		effective, resolveErr := store2.Resolve(auth, onboarding.BindingID)
		if resolveErr != nil || effective.Binding.Status != ActiveStatus {
			t.Fatalf("stale authorization admitted: resolve=%v effective=%+v", resolveErr, effective)
		}
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
	base := catalogReadDefinition()
	base.Credentials = []CredentialInput{{Name: "TOKEN", Required: true}}
	locators := map[string]int{}
	workloads := map[string]bool{}
	for i := 0; i < 95; i++ {
		definition := base
		definition.DefinitionID = fmt.Sprintf("mcp-ref-%02d", i)
		if err := store.RegisterDefinition(definition); err != nil {
			t.Fatal(err)
		}
		auth := aliceAuth()
		if err := store.PutGrant(OperatorGrant(GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
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
		env, err := DecryptAuthorized(secrets, effective)
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
	if err := store.PutSharedCredentialPolicy(SharedCredentialPolicy{Schema: SchemaVersion, PolicyID: "policy-shared", DefinitionID: "mcp-shared", Locator: sharedLocator, StoreOwner: "shared-owner", Principals: principals, IssuedBy: "operator", Status: ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	for _, principal := range principals {
		auth := identity.TelegramEnvelope(principal, 9, principal+"-runtime", "policy-1")
		if err := store.PutGrant(OperatorGrant(GrantDefinition, principal, "mcp-shared", "1.0.0")); err != nil {
			t.Fatal(err)
		}
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
		env, err := DecryptAuthorized(secrets, effective)
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
