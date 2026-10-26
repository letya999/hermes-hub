package toolhub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A shared stateless workload is consumed by every active binding on its
// definition; releasing one consumer's binding removes only that consumer's
// demand and the shared process stays alive. The controller release fires
// only when the last consumer is gone — and persistent retention opts the
// workload out of idle reaping entirely.
func TestLeaseSharedConsumerRelease(t *testing.T) {
	store := NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "shared-svc", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:    api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:     []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}},
		Workload:  api.WorkloadPolicy{Class: api.Shared, Rationale: "stateless connector serves all principals", Retention: "persistent"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	bind := func(principal string) api.ToolBinding {
		id := api.DeterministicBindingID(principal, principal, "runtime-"+principal, definition.DefinitionID, definition.Version, "", "")
		binding := api.ToolBinding{Schema: api.SchemaVersion, ToolBindingID: id, PrincipalID: principal, ContextID: principal, RuntimeID: "runtime-" + principal, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: "policy-1", WorkloadClass: api.Shared, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
		if err := store.PutBinding(binding); err != nil {
			t.Fatal(err)
		}
		return binding
	}
	alice := bind("alice")
	bob := bind("bob")
	var released []string
	c := &ControlPlane{Store: store, Now: time.Now, Release: func(_ context.Context, release api.ControllerRelease) error {
		released = append(released, release.WorkloadID)
		return nil
	}}
	sharedID := api.WorkloadInstanceID(definition.DefinitionID, api.Shared, "", "")
	releaseOne := func(binding api.ToolBinding) {
		t.Helper()
		// Real flow revokes the binding first (revokeAccess), so consumer
		// demand is the still-active binding set at release time.
		if err := store.SetBindingStatus(binding.ToolBindingID, api.RevokedStatus); err != nil {
			t.Fatal(err)
		}
		onboarding := api.Onboarding{OnboardingID: "onb-" + binding.ToolBindingID, PrincipalID: binding.PrincipalID, ContextID: binding.ContextID, BindingID: binding.ToolBindingID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, Definition: &definition}
		if err := c.releaseBindingWorkloads(onboarding, false); err != nil {
			t.Fatal(err)
		}
	}
	// Alice leaves: Bob's active binding still consumes the shared workload.
	releaseOne(alice)
	for _, id := range released {
		if id == sharedID {
			t.Fatalf("shared workload released while a consumer remains: %v", released)
		}
	}
	// Bob leaves too: no consumers remain and the shared workload releases.
	releaseOne(bob)
	if len(released) != 1 || released[0] != sharedID {
		t.Fatalf("last consumer did not release shared workload: %v", released)
	}
	// Retention policy validation: persistent demand belongs to shared
	// stateless workloads only.
	bad := definition
	bad.Workload = api.WorkloadPolicy{Class: api.PerUser, Rationale: "user state", Retention: "persistent"}
	if err := bad.Validate(); err == nil {
		t.Fatal("persistent retention accepted for a per-user workload")
	}
}

// Reconciliation purges only owned orphans: foreign objects are never
// touched, and a shared workload survives the loss of the connection that
// spawned it while another active binding still consumes its definition.
func TestReconcileNeverKillsUnmanaged(t *testing.T) {
	store := NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "shared-svc", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:    api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:     []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}},
		Workload:  api.WorkloadPolicy{Class: api.Shared, Rationale: "stateless connector serves all principals"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	bind := func(principal string) api.ToolBinding {
		id := api.DeterministicBindingID(principal, principal, "runtime-"+principal, definition.DefinitionID, definition.Version, "", "")
		binding := api.ToolBinding{Schema: api.SchemaVersion, ToolBindingID: id, PrincipalID: principal, ContextID: principal, RuntimeID: "runtime-" + principal, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: "policy-1", WorkloadClass: api.Shared, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
		if err := store.PutBinding(binding); err != nil {
			t.Fatal(err)
		}
		return binding
	}
	alice := bind("alice")
	bob := bind("bob")
	var purged []string
	r := &Reconciler{
		Store: store,
		Scan: func(_ context.Context, _ map[string]string) ([]api.LabeledResource, error) {
			return []api.LabeledResource{
				{Kind: "container", Name: "foreign"}, // no labels: unmanaged
				{Kind: "container", Name: "live-conn", Labels: map[string]string{api.LabelConnection: "conn-live"}},
				{Kind: "container", Name: "orphan", Labels: map[string]string{api.LabelConnection: "conn-gone", api.LabelBinding: "bind-gone"}},
				{Kind: "container", Name: "shared-svc", Labels: map[string]string{api.LabelConnection: "conn-gone", api.LabelBinding: alice.ToolBindingID}},
			}, nil
		},
		Purge: func(_ context.Context, _, name string, _ map[string]string) error {
			purged = append(purged, name)
			return nil
		},
	}
	if err := store.PutConnection(api.Connection{Schema: api.SchemaVersion, ConnectionID: "conn-live", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, Revision: 1, Status: api.ActiveStatus}); err != nil {
		t.Fatal(err)
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(purged) != 1 || purged[0] != "orphan" {
		t.Fatalf("purged = %v, want only the owned orphan", purged)
	}
	// Both consumers gone: the shared workload is an owned orphan now.
	for _, binding := range []api.ToolBinding{alice, bob} {
		if err := store.SetBindingStatus(binding.ToolBindingID, api.RevokedStatus); err != nil {
			t.Fatal(err)
		}
	}
	purged = nil
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range purged {
		if name == "shared-svc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("consumerless shared workload was not purged: %v", purged)
	}
}

// A persistent shared workload is pinned demand: idle reaping never touches
// it, so the one process serving many consumers cannot disappear under load.
func TestPersistentRetentionSkipsIdleReap(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	d.Workload.Class = api.Shared
	d.Workload.Retention = "persistent"
	budget, _ := api.NewWorkloadBudget(1, time.Minute)
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.Acquire(context.Background(), "generic-workload"); err != nil {
		t.Fatal(err)
	}
	var dockerCalls int
	c.command = func(context.Context, string, ...string) ([]byte, error) { dockerCalls++; return nil, nil }
	c.workloads["generic-workload"] = genericWorkload{plan: genericPlan(d)}
	if got := c.CleanupIdle(context.Background(), time.Now().Add(2*time.Minute)); len(got) != 0 || dockerCalls != 0 {
		t.Fatalf("persistent workload reaped: %v (docker calls %d)", got, dockerCalls)
	}
	d.Workload.Retention = ""
	c.workloads["generic-workload"] = genericWorkload{plan: genericPlan(d)}
	if got := c.CleanupIdle(context.Background(), time.Now().Add(4*time.Minute)); len(got) != 1 {
		t.Fatalf("demand workload not reaped: %v", got)
	}
}
