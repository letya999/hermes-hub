package toolhub

import (
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestConnectorReinstallAfterRemoval(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	args := map[string]any{"target": "google-work", "version": "2.0.0"}
	first, err := c.InvokeConnector(t.Context(), auth, "connector_install", args)
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.OperationFor(auth, first.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.OnboardingFor(auth, op.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	// Finalizers retain this tombstone after the resources are gone.
	old.Phase, old.RemoveReceipt = api.PhaseRemoved, "verified 0 remaining"
	if err := store.PutOnboarding(old); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan api.OperationResult, 8)
	for range 8 {
		wg.Go(func() {
			result, err := c.InvokeConnector(t.Context(), auth, "connector_install", args)
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		})
	}
	wg.Wait()
	close(results)
	var next string
	count := 0
	for result := range results {
		count++
		if result.OperationID == first.OperationID || result.State != api.OpStateAwaitingUser {
			t.Errorf("reinstall reused removed installation: %+v", result)
		}
		if next != "" && result.OperationID != next {
			t.Error("concurrent reinstall diverged")
		}
		next = result.OperationID
	}
	if count != 8 {
		t.Fatalf("completed installs=%d", count)
	}
	nextOp, err := store.OperationFor(auth, next)
	if err != nil {
		t.Fatal(err)
	}
	if nextOp.OnboardingID == old.OnboardingID {
		t.Error("removed onboarding reused")
	}
	preserved, err := store.OnboardingFor(auth, old.OnboardingID)
	if err != nil || preserved.Phase != api.PhaseRemoved {
		t.Errorf("removal tombstone overwritten: phase=%s error=%v", preserved.Phase, err)
	}
}

func TestConnectorRemoveAfterReinstall(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	old := m13P3EnabledOnboarding(t, store, auth)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, WorkloadRoot: t.TempDir()}
	first, err := c.InvokeConnector(t.Context(), auth, "connector_remove", map[string]any{"target": "google-work"})
	if err != nil {
		t.Fatal(err)
	}
	old, err = store.OnboardingFor(auth, old.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.remove(t.Context(), auth, map[string]any{"onboarding_id": old.OnboardingID, "remove_nonce": old.RemoveNonce}); err != nil {
		t.Fatal(err)
	}
	definition, err := store.Definition("google-work", "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	// Model a fresh admitted prepare; the fixture's old catalog grant was
	// connection-bound and intentionally cannot authorize another install.
	fresh, err := c.newOnboarding(auth, api.OnboardingCatalog, "fresh-install", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.InvokeConnector(t.Context(), auth, "connector_remove", map[string]any{"target": "google-work"})
	if err != nil {
		t.Fatal(err)
	}
	if second.OperationID == first.OperationID || second.State != api.OpStateAwaitingConfirm {
		t.Fatalf("new removal reused old receipt: %+v", second)
	}
	removal, err := store.OperationFor(auth, second.OperationID)
	if err != nil || removal.OnboardingID != fresh.OnboardingID {
		t.Fatalf("wrong removal generation: %s error=%v", removal.OnboardingID, err)
	}
	again, err := c.InvokeConnector(t.Context(), auth, "connector_remove", map[string]any{"target": "google-work"})
	if err != nil || again.OperationID != second.OperationID {
		t.Fatalf("remove retry diverged: %+v error=%v", again, err)
	}
}
