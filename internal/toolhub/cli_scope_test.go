package toolhub

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

// scope: shared is an operator-only path: without the install_shared grant the
// spec denies before touching the definition; with it the definition is
// catalog-visible to every principal with catalog access.
func TestPrepareCLISharedScopeGate(t *testing.T) {
	spec := map[string]any{
		"name": "shared-search", "command": "rg", "scope": "shared",
		"tools": []any{map[string]any{"name": "search", "effect": "read"}},
	}
	fix := controlCLIFixture(t, true)
	// carol has no grants at all: the shared gate must deny before any
	// self-install or catalog logic runs.
	carol := identity.TelegramEnvelope("carol", 9, "runtime", "policy-1")
	if _, err := fix.control.prepareCLI(context.Background(), carol, spec, "s1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("shared install without install_shared admitted: %v", err)
	}
	// install_shared alone suffices — the operator path never needs the
	// self-install grant owners hold.
	if err := putGrant(t, fix.store, OperatorControlGrant("carol", "install_shared")); err != nil {
		t.Fatal(err)
	}
	body, err := fix.control.prepareCLI(context.Background(), carol, spec, "s1")
	if err != nil {
		t.Fatalf("operator shared install denied: %v", err)
	}
	if _, ok := fix.store.definitions[definitionKey("shared-search", "1.0.0")]; !ok {
		t.Fatal("shared definition not registered")
	}
	if pub := fix.store.publications[definitionKey("shared-search", "1.0.0")]; pub.Visibility != PublicationCatalog {
		t.Fatalf("publication=%+v", pub)
	}
	onboarding := mustOnboarding(t, fix.store, body["onboarding_id"].(string))
	if onboarding.Phase != PhaseAwaitingConfirm {
		t.Fatalf("phase=%s", onboarding.Phase)
	}
	// The shared definition is visible to another principal with catalog
	// access, unlike an owner-scoped install.
	if err := putGrant(t, fix.store, OperatorGrant(GrantCatalogDefault, "bob", "", "")); err != nil {
		t.Fatal(err)
	}
	if !fix.store.hasCatalogAccessLocked(bobAuth(), "shared-search", "1.0.0") {
		t.Fatal("shared definition not catalog-visible")
	}
}

func TestCLISpecScopeValues(t *testing.T) {
	for _, tc := range []struct {
		scope string
		want  bool
		ok    bool
	}{
		{"", false, true}, {"owner", false, true}, {"shared", true, true}, {"global", false, false},
	} {
		shared, err := cliSpecScope(map[string]any{"scope": tc.scope})
		if (err == nil) != tc.ok || shared != tc.want {
			t.Fatalf("scope=%q shared=%v err=%v", tc.scope, shared, err)
		}
	}
}

func TestCLISpecWorkloadFields(t *testing.T) {
	owner := cliSpecWorkload(map[string]any{"lifecycle": "binding", "workspace_scope": "binding", "workspace_access": "rw"}, false)
	if owner.Class != PerUser || owner.Lifecycle != CLILifecycleBinding || owner.WorkspaceScope != "binding" || owner.WorkspaceAccess != "rw" {
		t.Fatalf("owner workload=%+v", owner)
	}
	shared := cliSpecWorkload(map[string]any{"stateless": true, "lifecycle": "shared-pool"}, true)
	if shared.Class != Shared || !shared.Stateless || shared.Lifecycle != CLILifecycleSharedPool {
		t.Fatalf("shared workload=%+v", shared)
	}
	// Validate polices the combinations: owner+shared-pool and shared+binding
	// fail closed at definition compile.
	bad := cliDefinitionFromSpecMustFail(t, map[string]any{
		"name": "bad-pool", "command": "rg", "lifecycle": "shared-pool",
		"tools": []any{map[string]any{"name": "x", "effect": "read"}},
	}, false)
	if !strings.Contains(bad.Error(), "pool") {
		t.Fatalf("err=%v", bad)
	}
	bad = cliDefinitionFromSpecMustFail(t, map[string]any{
		"name": "bad-ws", "command": "rg", "workspace_scope": "binding",
		"tools": []any{map[string]any{"name": "x", "effect": "read"}},
	}, true)
	if !strings.Contains(bad.Error(), "binding workspace") {
		t.Fatalf("err=%v", bad)
	}
}

func cliDefinitionFromSpecMustFail(t *testing.T, spec map[string]any, shared bool) error {
	t.Helper()
	if _, err := cliDefinitionFromSpec(spec, shared); err == nil {
		t.Fatalf("spec admitted: %v", spec)
	} else {
		return err
	}
	return nil
}

// The definition records a release pin: resolved asset + verified sha256
// stand in for the commit chain.
func TestDefinitionSourceReleaseValidation(t *testing.T) {
	digest := func(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }
	definition := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "rg-release", Version: "1.0.0", Transport: BoundedCLI,
		Source: DefinitionSource{
			Image: "hermes-cli-artifact/rg", Digest: digest('9'), Command: "/usr/local/bin/rg",
			Repository: "https://github.com/BurntSushi/ripgrep",
			ReleaseTag: "14.1.1", ReleaseAsset: "ripgrep-14.1.1-x86_64.tar.gz", AssetDigest: digest('7'),
			ArchiveDigest: digest('1'), RecipeDigest: digest('2'), ReviewDigest: digest('3'),
		},
		Tools:     []ToolSpec{{Name: "run", Effect: ReadEffect}},
		Workload:  WorkloadPolicy{Class: PerUser, Rationale: "r"},
		Execution: ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: []string{"127.0.0.1"}},
		Health:    HealthProbe{Kind: "exec", Value: "/usr/local/bin/rg", TimeoutSeconds: 5},
	}
	if err := definition.Validate(); err != nil {
		t.Fatalf("release definition rejected: %v", err)
	}
	broken := definition
	broken.Source.AssetDigest = ""
	if err := broken.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutable release without recorded digest admitted: %v", err)
	}
	broken = definition
	broken.Source.CommitSHA = strings.Repeat("a", 40)
	if err := broken.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mixed commit+release provenance admitted: %v", err)
	}
	broken = definition
	broken.Source.ReleaseTag = ""
	if err := broken.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("release evidence without tag admitted: %v", err)
	}
	broken = definition
	broken.Source.ReleaseAsset = ""
	if err := broken.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("release tag without asset admitted: %v", err)
	}
}
