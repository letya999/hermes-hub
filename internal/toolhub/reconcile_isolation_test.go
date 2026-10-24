package toolhub

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"path/filepath"
	"testing"
	"time"
)

func TestReconcilerOrphanFailuresDoNotStarveCredentials(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus}
	scanErr, purgeErr := errors.New("scan unavailable"), errors.New("purge unavailable")
	checked, purged := 0, 0
	r := &Reconciler{Store: s,
		Scan:      func(context.Context, map[string]string) ([]api.LabeledResource, error) { return nil, scanErr },
		Purge:     func(context.Context, string, string, map[string]string) error { purged++; return purgeErr },
		CredAlive: func(context.Context, string) (bool, error) { checked++; return true, nil },
	}
	if err := r.ScanOnce(t.Context()); !errors.Is(err, scanErr) || checked != 1 || purged != 0 {
		t.Fatalf("scan failure: err=%v checked=%d purged=%d", err, checked, purged)
	}
	r.Scan = func(context.Context, map[string]string) ([]api.LabeledResource, error) {
		return []api.LabeledResource{
			{Kind: "container", Name: "orphan-a", Labels: map[string]string{api.LabelConnection: "gone-a"}},
			{Kind: "container", Name: "orphan-b", Labels: map[string]string{api.LabelConnection: "gone-b"}},
			{Kind: "container", Name: "owned", Labels: map[string]string{api.LabelConnection: "live"}},
		}, nil
	}
	if err := r.ScanOnce(t.Context()); !errors.Is(err, purgeErr) || checked != 2 || purged != 2 {
		t.Fatalf("purge failure: err=%v checked=%d purged=%d", err, checked, purged)
	}
}

func TestReconcilerNeverPurgesUnlabelledScanResult(t *testing.T) {
	purged := 0
	r := &Reconciler{Store: NewStore(),
		Scan: func(context.Context, map[string]string) ([]api.LabeledResource, error) {
			return []api.LabeledResource{{Kind: "container", Name: "unlabelled"}}, nil
		},
		Purge: func(context.Context, string, string, map[string]string) error { purged++; return nil },
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Fatal("unlabelled scan result reached purge")
	}
}

func TestReconcilerCredentialResultCannotOverrideConcurrentRevoke(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus, Revision: 1}
	notified := false
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) {
			if err := s.MarkReauth("live"); err != nil {
				t.Fatal(err)
			}
			return false, nil
		},
		Notify: func(context.Context, string, string, map[string]any) error { notified = true; return nil },
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections["live"]
	if got.Status != api.ReauthStatus || notified {
		t.Fatalf("stale credential result overrode revoke: status=%s notified=%v", got.Status, notified)
	}
}

func TestReconcilerCredentialDriftCannotPurgeAfterConcurrentDelete(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus, Revision: 1, Metadata: map[string]string{api.LabelCredentialRecipe: "pinned"}}
	s.credentials["ref"] = api.CredentialReference{CredentialRefID: "ref", ConnectionID: "live", Status: api.ActiveStatus}
	purged, notified := false, false
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		CredResolve: func(context.Context, api.Connection) (string, error) {
			s.mu.Lock()
			connection := s.connections["live"]
			connection.Status = api.DeletingStatus
			connection.Revision++
			s.connections["live"] = connection
			s.mu.Unlock()
			return "changed", nil
		},
		CredPurge: func(context.Context, api.Connection, api.CredentialReference) error { purged = true; return nil },
		Notify:    func(context.Context, string, string, map[string]any) error { notified = true; return nil },
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections["live"]
	if got.Status != api.DeletingStatus || got.Metadata[api.LabelAcceptCredential] != "" || purged || notified {
		t.Fatalf("stale drift survived delete: status=%s drift=%q purged=%v notified=%v", got.Status, got.Metadata[api.LabelAcceptCredential], purged, notified)
	}
}

func TestReconcilerRecipeDriftCannotOverrideConcurrentDelete(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", Status: api.ActiveStatus, Revision: 1, Metadata: map[string]string{api.LabelRecipe: "pinned"}}
	notified := false
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Resolve: func(context.Context, api.Connection) (string, error) {
			s.mu.Lock()
			connection := s.connections["live"]
			connection.Status = api.DeletingStatus
			connection.Revision++
			s.connections["live"] = connection
			s.mu.Unlock()
			return "changed", nil
		},
		Notify: func(context.Context, string, string, map[string]any) error { notified = true; return nil },
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections["live"]
	if got.Status != api.DeletingStatus || got.Metadata[api.LabelAcceptRecipe] != "" || notified {
		t.Fatalf("stale recipe drift survived delete: status=%s drift=%q notified=%v", got.Status, got.Metadata[api.LabelAcceptRecipe], notified)
	}
}

func TestReconcilerSurfaceDriftCannotOverrideConcurrentDelete(t *testing.T) {
	s, _, binding := seededStore(t)
	binding.ToolDigest = "sha256:pinned"
	s.bindings[binding.ToolBindingID] = binding
	connection := s.connections["google-work"]
	notified := false
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Surface: func(context.Context, api.Connection) ([]api.ToolSpec, error) {
			s.mu.Lock()
			current := s.connections[connection.ConnectionID]
			current.Status = api.DeletingStatus
			current.Revision++
			s.connections[connection.ConnectionID] = current
			s.mu.Unlock()
			return nil, nil
		},
		Notify: func(context.Context, string, string, map[string]any) error { notified = true; return nil },
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections[connection.ConnectionID]
	if got.Status != api.DeletingStatus || got.Metadata[api.LabelAcceptSurface] != "" || notified {
		t.Fatalf("stale surface drift survived delete: status=%s drift=%q notified=%v", got.Status, got.Metadata[api.LabelAcceptSurface], notified)
	}
}

func TestReconcilerCredentialPurgeFailureDoesNotStarveConnections(t *testing.T) {
	s := NewStore()
	for _, id := range []string{"first", "second"} {
		s.connections[id] = api.Connection{ConnectionID: id, CredentialRefID: "ref-" + id, Status: api.ActiveStatus, Metadata: map[string]string{api.LabelCredentialRecipe: "pinned"}}
		s.credentials["ref-"+id] = api.CredentialReference{CredentialRefID: "ref-" + id, ConnectionID: id, Status: api.ActiveStatus}
	}
	purgeErr := errors.New("first purge unavailable")
	purged := map[string]bool{}
	r := &Reconciler{Store: s,
		CredAlive:   func(context.Context, string) (bool, error) { return true, nil },
		CredResolve: func(context.Context, api.Connection) (string, error) { return "changed", nil },
		CredPurge: func(_ context.Context, connection api.Connection, _ api.CredentialReference) error {
			purged[connection.ConnectionID] = true
			if connection.ConnectionID == "first" {
				return purgeErr
			}
			return nil
		},
		Notify: func(context.Context, string, string, map[string]any) error { return nil },
	}
	err := r.ScanOnce(t.Context())
	if !errors.Is(err, purgeErr) || !purged["first"] || !purged["second"] {
		t.Fatalf("purge failure starved independent connection: err=%v purged=%v", err, purged)
	}
	for _, connection := range s.Connections() {
		if connection.Status != api.ReauthStatus {
			t.Errorf("%s remained %s", connection.ConnectionID, connection.Status)
		}
	}
}

func TestReconcilerStoreMutationFailureDoesNotStarveConnections(t *testing.T) {
	s := NewStore()
	s.path = t.TempDir() // A directory cannot be replaced by the store snapshot.
	for _, id := range []string{"first", "second"} {
		s.connections[id] = api.Connection{ConnectionID: id, CredentialRefID: id, Status: api.ActiveStatus}
	}
	checked := map[string]bool{}
	r := &Reconciler{Store: s,
		CredAlive: func(_ context.Context, ref string) (bool, error) { checked[ref] = true; return false, nil },
		Notify:    func(context.Context, string, string, map[string]any) error { return nil },
	}
	if err := r.ScanOnce(t.Context()); err == nil || !checked["first"] || !checked["second"] {
		t.Fatalf("mutation failure starved independent connection: err=%v checked=%v", err, checked)
	}
}

func TestReconcilerRecipeMutationFailureDoesNotStarveConnections(t *testing.T) {
	s := NewStore()
	s.path = t.TempDir()
	for _, id := range []string{"first", "second"} {
		s.connections[id] = api.Connection{ConnectionID: id, Status: api.ActiveStatus, Metadata: map[string]string{api.LabelRecipe: "pinned"}}
	}
	checked := map[string]bool{}
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Resolve: func(_ context.Context, connection api.Connection) (string, error) {
			checked[connection.ConnectionID] = true
			return "changed", nil
		},
		Notify: func(context.Context, string, string, map[string]any) error { return nil },
	}
	if err := r.ScanOnce(t.Context()); err == nil || !checked["first"] || !checked["second"] {
		t.Fatalf("recipe mutation failure starved independent connection: err=%v checked=%v", err, checked)
	}
}

func TestReconcilerSurfaceMutationFailureDoesNotStarveConnections(t *testing.T) {
	s := NewStore()
	s.path = t.TempDir()
	for _, id := range []string{"first", "second"} {
		s.connections[id] = api.Connection{ConnectionID: id, Status: api.ActiveStatus}
		s.bindings["bind-"+id] = api.ToolBinding{ToolBindingID: "bind-" + id, ConnectionID: id, Status: api.ActiveStatus, ToolDigest: "pinned"}
	}
	checked := map[string]bool{}
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Surface: func(_ context.Context, connection api.Connection) ([]api.ToolSpec, error) {
			checked[connection.ConnectionID] = true
			return nil, nil
		},
		Notify: func(context.Context, string, string, map[string]any) error { return nil },
	}
	if err := r.ScanOnce(t.Context()); err == nil || !checked["first"] || !checked["second"] {
		t.Fatalf("surface mutation failure starved independent connection: err=%v checked=%v", err, checked)
	}
}

func TestReconcilerCredentialDriftMutationFailureDoesNotStarveConnections(t *testing.T) {
	s := NewStore()
	s.path = t.TempDir()
	for _, id := range []string{"first", "second"} {
		s.connections[id] = api.Connection{ConnectionID: id, Status: api.ActiveStatus, Metadata: map[string]string{api.LabelCredentialRecipe: "pinned"}}
	}
	checked := map[string]bool{}
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		CredResolve: func(_ context.Context, connection api.Connection) (string, error) {
			checked[connection.ConnectionID] = true
			return "changed", nil
		},
		Notify: func(context.Context, string, string, map[string]any) error { return nil },
	}
	if err := r.ScanOnce(t.Context()); err == nil || !checked["first"] || !checked["second"] {
		t.Fatalf("credential drift mutation failure starved independent connection: err=%v checked=%v", err, checked)
	}
}

func TestReconcilerReauthObservationFailureDoesNotStarveConnections(t *testing.T) {
	s := NewStore()
	s.path = t.TempDir()
	for _, id := range []string{"first", "second"} {
		s.connections[id] = api.Connection{ConnectionID: id, Status: api.ReauthStatus, Metadata: map[string]string{api.LabelCredentialRecipe: "pinned"}}
	}
	checked := map[string]bool{}
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		CredResolve: func(_ context.Context, connection api.Connection) (string, error) {
			checked[connection.ConnectionID] = true
			return "changed", nil
		},
		Notify: func(context.Context, string, string, map[string]any) error { return nil },
	}
	if err := r.ScanOnce(t.Context()); err == nil || !checked["first"] || !checked["second"] {
		t.Fatalf("reauth observation failure starved independent connection: err=%v checked=%v", err, checked)
	}
}

func TestReconcilerReauthObservationCannotSurviveConcurrentDelete(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", Status: api.ReauthStatus, Revision: 1, Metadata: map[string]string{api.LabelCredentialRecipe: "pinned"}}
	notified := false
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		CredResolve: func(context.Context, api.Connection) (string, error) {
			s.mu.Lock()
			connection := s.connections["live"]
			connection.Status = api.DeletingStatus
			connection.Revision++
			s.connections["live"] = connection
			s.mu.Unlock()
			return "changed", nil
		},
		Notify: func(context.Context, string, string, map[string]any) error { notified = true; return nil },
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections["live"]
	if got.Status != api.DeletingStatus || got.Metadata[api.LabelAcceptCredential] != "" || notified {
		t.Fatalf("stale reauth observation survived delete: status=%s drift=%q notified=%v", got.Status, got.Metadata[api.LabelAcceptCredential], notified)
	}
}

func TestReconcilerRecordsUnknownBrokerVisibilityWithoutReauth(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus}
	notified := false
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return false, api.ErrCredentialVisibilityUnknown },
		Notify:    func(context.Context, string, string, map[string]any) error { notified = true; return nil },
	}
	if err := r.ScanOnce(t.Context()); !errors.Is(err, api.ErrCredentialVisibilityUnknown) {
		t.Fatalf("visibility error lost: %v", err)
	}
	got := s.connections["live"]
	if got.Status != api.ActiveStatus || got.CredentialVisibility != "unknown" || got.HealthCause != "credential_visibility_unknown" || got.CredentialCheckedAt.IsZero() || notified {
		t.Fatalf("unknown visibility presented as lost or unrecorded: %+v notified=%v", got, notified)
	}
}

func TestReconcilerRecordsTransportFailureAsUnavailable(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus}
	failure := errors.New("transport unavailable")
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { return false, failure }}
	if err := r.ScanOnce(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("transport error lost: %v", err)
	}
	got := s.connections["live"]
	if got.Status != api.ActiveStatus || got.CredentialVisibility != "unknown" || got.HealthCause != "credential_check_unavailable" {
		t.Fatalf("transport failure was misreported: %+v", got)
	}
}

func TestReconcilerCredentialRetryBackoffSurvivesRestart(t *testing.T) {
	s, _, _, _ := managedStore(t)
	path := filepath.Join(t.TempDir(), "store.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	probe := func(context.Context, string) (bool, error) {
		checked++
		return false, errors.New("broker unavailable")
	}
	r := &Reconciler{Store: s, CredAlive: probe}
	if err := r.ScanOnce(t.Context()); err == nil || checked != 1 {
		t.Fatalf("first check: err=%v checked=%d", err, checked)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r = &Reconciler{Store: reloaded, CredAlive: probe}
	if err := r.ScanOnce(t.Context()); err != nil || checked != 1 {
		t.Fatalf("retry ran before due time: err=%v checked=%d", err, checked)
	}
}

func TestReconcilerRecipeRetrySurvivesRestart(t *testing.T) {
	s, _, _, _ := managedStore(t)
	connection := s.connections["google-work"]
	connection.Metadata[api.LabelRecipe] = "pinned"
	s.connections[connection.ConnectionID] = connection
	for id, binding := range s.bindings {
		if binding.ConnectionID == connection.ConnectionID {
			binding.ToolDigest = api.ToolSurfaceDigest(remoteDefinition().Tools)
			s.bindings[id] = binding
		}
	}
	path := filepath.Join(t.TempDir(), "store.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	probe := func(context.Context, api.Connection) (string, error) {
		checks++
		return "", errors.New("recipe unavailable")
	}
	surfaceChecks := 0
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { return true, nil }, Resolve: probe,
		Surface: func(context.Context, api.Connection) ([]api.ToolSpec, error) {
			surfaceChecks++
			return remoteDefinition().Tools, nil
		}}
	if err := r.ScanOnce(t.Context()); err == nil || checks != 1 {
		t.Fatalf("first check: err=%v checks=%d", err, checks)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Store = reloaded
	if err := r.ScanOnce(t.Context()); err != nil || checks != 1 || surfaceChecks != 0 {
		t.Fatalf("dependent probe ran before recipe retry: err=%v recipe=%d surface=%d", err, checks, surfaceChecks)
	}
}

func TestReconcilerRecipeRetryEntersCooldownAfterFiveFailures(t *testing.T) {
	s, _, _, _ := managedStore(t)
	connection := s.connections["google-work"]
	connection.Metadata[api.LabelRecipe] = "pinned"
	s.connections[connection.ConnectionID] = connection
	checks := 0
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Resolve: func(context.Context, api.Connection) (string, error) {
			checks++
			return "", errors.New("recipe unavailable")
		}}
	for range 5 {
		connection = s.connections["google-work"]
		connection.RecipeRetryAt = time.Now().Add(-time.Minute)
		s.connections[connection.ConnectionID] = connection
		if err := r.ScanOnce(t.Context()); err == nil {
			t.Fatal("recipe failure lost")
		}
	}
	if checks != 5 || s.connections["google-work"].RecipeRetryAt.Before(time.Now().Add(30*time.Minute)) {
		t.Fatalf("recipe retry did not cool down: checks=%d", checks)
	}
}

func TestReconcilerClearsRecipeRetryAfterSuccessfulProbe(t *testing.T) {
	s, _, _, _ := managedStore(t)
	connection := s.connections["google-work"]
	connection.Metadata[api.LabelRecipe] = "pinned"
	connection.RecipeRetryCount = 5
	connection.RecipeRetryAt = time.Now().Add(-time.Minute)
	s.connections[connection.ConnectionID] = connection
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Resolve: func(context.Context, api.Connection) (string, error) { return "pinned", nil }}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections[connection.ConnectionID]
	if got.RecipeRetryCount != 0 || !got.RecipeRetryAt.IsZero() {
		t.Fatal("successful recipe probe retained retry state")
	}
}

func TestReconcilerClearsCredentialRetryAfterSuccessfulProbe(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus, CredentialRetryAt: time.Now().Add(-time.Minute), CredentialRetryCount: 4}
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { return true, nil }}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !s.connections["live"].CredentialRetryAt.IsZero() || s.connections["live"].CredentialRetryCount != 0 {
		t.Fatal("successful credential probe retained retry state")
	}
}

func TestReconcilerStopsAutomaticCredentialRetryAfterFiveFailures(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus}
	checked := 0
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) {
		checked++
		return false, errors.New("broker unavailable")
	}}
	for range 5 {
		connection := s.connections["live"]
		connection.CredentialRetryAt = time.Now().Add(-time.Minute)
		s.connections["live"] = connection
		if err := r.ScanOnce(t.Context()); err == nil {
			t.Fatal("failed check was not reported")
		}
	}
	if err := r.ScanOnce(t.Context()); err != nil || checked != 5 {
		t.Fatalf("retry ran during cooldown: err=%v checked=%d", err, checked)
	}
	if s.connections["live"].CredentialRetryAt.Before(time.Now().Add(30 * time.Minute)) {
		t.Fatal("exhausted retries did not enter a long cooldown")
	}
}

func TestReconcilerRechecksCredentialAfterExhaustedCooldown(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.DegradedStatus,
		CredentialVisibility: "unknown", HealthCause: "credential_check_unavailable", CredentialCheckedAt: time.Now().Add(-time.Hour),
		CredentialRetryCount: 5, CredentialRetryAt: time.Now().Add(-time.Minute)}
	checked := 0
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { checked++; return true, nil }}
	if err := r.ScanOnce(t.Context()); err != nil || checked != 1 {
		t.Fatalf("credential was not rechecked after cooldown: err=%v checked=%d", err, checked)
	}
	got := s.connections["live"]
	if got.CredentialVisibility != "active" || got.CredentialRetryCount != 0 || got.Status != api.DegradedStatus {
		t.Fatalf("cooldown recovery evidence or safety state wrong: %+v", got)
	}
}

func TestCredentialRotationResetsExhaustedProbeRetries(t *testing.T) {
	s, _, _ := seededStore(t)
	connection := s.connections["google-work"]
	connection.CredentialRetryCount = 5
	connection.CredentialRetryAt = time.Now().Add(time.Hour)
	connection.RecipeRetryCount = 5
	connection.RecipeRetryAt = time.Now().Add(time.Hour)
	connection.CredentialVisibility = "unknown"
	connection.CredentialCheckedAt = time.Now().Add(-time.Minute)
	connection.HealthCause = "credential_check_unavailable"
	connection.CredRecipeRetryCount = 5
	connection.CredRecipeRetryAt = time.Now().Add(time.Hour)
	connection.SurfaceRetryCount = 5
	connection.SurfaceRetryAt = time.Now().Add(time.Hour)
	s.connections[connection.ConnectionID] = connection
	if _, err := s.RotateCredential(connection.ConnectionID, "local", "local://alice/google/recovered", []string{"GOOGLE_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	got := s.connections[connection.ConnectionID]
	if got.CredentialRetryCount != 0 || !got.CredentialRetryAt.IsZero() || got.RecipeRetryCount != 0 || !got.RecipeRetryAt.IsZero() || got.CredRecipeRetryCount != 0 || !got.CredRecipeRetryAt.IsZero() || got.SurfaceRetryCount != 0 || !got.SurfaceRetryAt.IsZero() || got.CredentialVisibility != "" || !got.CredentialCheckedAt.IsZero() || got.HealthCause != "" {
		t.Fatalf("new credential inherited exhausted retry state: %+v", got)
	}
}

func TestReconcilerDefersFailedCredRecipeAndSurfaceChecks(t *testing.T) {
	s, _, binding := seededStore(t)
	binding.ToolDigest = api.ToolSurfaceDigest(remoteDefinition().Tools)
	s.bindings[binding.ToolBindingID] = binding
	connection := s.connections["google-work"]
	connection.Metadata[api.LabelCredentialRecipe] = "pinned"
	connection.CredRecipeRetryCount = 4
	s.connections[connection.ConnectionID] = connection
	credChecks, surfaceChecks, credFailing, surfaceFailing := 0, 0, true, true
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		CredResolve: func(context.Context, api.Connection) (string, error) {
			credChecks++
			if credFailing {
				return "", errors.New("backend unavailable")
			}
			return "pinned", nil
		},
		Surface: func(context.Context, api.Connection) ([]api.ToolSpec, error) {
			surfaceChecks++
			if surfaceFailing {
				return nil, errors.New("workload unavailable")
			}
			return remoteDefinition().Tools, nil
		},
	}
	if err := r.ScanOnce(t.Context()); err == nil || credChecks != 1 || surfaceChecks != 0 {
		t.Fatalf("failed cred-recipe probe did not isolate dependents: err=%v cred=%d surface=%d", err, credChecks, surfaceChecks)
	}
	got := s.connections[connection.ConnectionID]
	if got.CredRecipeRetryCount != 5 || got.CredRecipeRetryAt.Before(time.Now().Add(30*time.Minute)) {
		t.Fatalf("fifth cred-recipe failure did not enter cooldown: %+v", got)
	}
	path := filepath.Join(t.TempDir(), "store.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Store = reloaded
	if err := r.ScanOnce(t.Context()); err != nil || credChecks != 1 {
		t.Fatalf("cred-recipe probe ran during persisted cooldown: err=%v cred=%d", err, credChecks)
	}
	credFailing = false
	connection = reloaded.connections[connection.ConnectionID]
	connection.CredRecipeRetryAt = time.Now().Add(-time.Minute)
	reloaded.connections[connection.ConnectionID] = connection
	if err := r.ScanOnce(t.Context()); err == nil || credChecks != 2 || surfaceChecks != 1 {
		t.Fatalf("expired defer did not resume probes: err=%v cred=%d surface=%d", err, credChecks, surfaceChecks)
	}
	got = reloaded.connections[connection.ConnectionID]
	if got.CredRecipeRetryCount != 0 || !got.CredRecipeRetryAt.IsZero() {
		t.Fatalf("successful cred-recipe probe retained retry: %+v", got)
	}
	if got.SurfaceRetryCount != 1 || got.SurfaceRetryAt.IsZero() {
		t.Fatalf("surface failure did not persist retry: %+v", got)
	}
	if err := r.ScanOnce(t.Context()); err != nil || surfaceChecks != 1 {
		t.Fatalf("surface probe ran during retry window: err=%v surface=%d", err, surfaceChecks)
	}
	surfaceFailing = false
	connection = reloaded.connections[connection.ConnectionID]
	connection.SurfaceRetryAt = time.Now().Add(-time.Minute)
	reloaded.connections[connection.ConnectionID] = connection
	if err := r.ScanOnce(t.Context()); err != nil || surfaceChecks != 2 {
		t.Fatalf("expired surface defer did not resume probe: err=%v surface=%d", err, surfaceChecks)
	}
	got = reloaded.connections[connection.ConnectionID]
	if got.SurfaceRetryCount != 0 || !got.SurfaceRetryAt.IsZero() {
		t.Fatalf("successful surface probe retained retry: %+v", got)
	}
}

func TestReconcilerMapsUnrelatedProbeCodeToUnavailable(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus}
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { return false, api.ErrUnsupportedAuth }}
	if err := r.ScanOnce(t.Context()); !errors.Is(err, api.ErrUnsupportedAuth) {
		t.Fatal(err)
	}
	if got := s.connections["live"].HealthCause; got != "credential_check_unavailable" {
		t.Fatalf("unrelated error code became a health cause: %s", got)
	}
}

func TestReconcilerClearsUnknownVisibilityAfterSuccessfulCheck(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus, CredentialVisibility: "unknown", HealthCause: "credential_check_unavailable"}
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { return true, nil }}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections["live"]
	if got.CredentialVisibility != "active" || got.HealthCause != "" || got.CredentialCheckedAt.IsZero() {
		t.Fatalf("successful check did not clear unknown evidence: %+v", got)
	}
}

func TestReconcilerRecordsVisibleInactiveCredential(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.ActiveStatus}
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return false, nil },
		Notify:    func(context.Context, string, string, map[string]any) error { return nil },
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections["live"]
	if got.Status != api.DegradedStatus || got.CredentialVisibility != "inactive" || got.HealthCause != "credential_inactive" || got.CredentialCheckedAt.IsZero() {
		t.Fatalf("inactive credential reason missing: %+v", got)
	}
}

func TestReconcilerRechecksDegradedCredentialWithoutClaimingRecovery(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.DegradedStatus}
	checked := false
	r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { checked = true; return true, nil }}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections["live"]
	if !checked || got.Status != api.DegradedStatus || got.CredentialVisibility != "active" || got.CredentialCheckedAt.IsZero() {
		t.Fatalf("degraded credential was not rechecked safely: checked=%v state=%+v", checked, got)
	}
}

func TestReconcilerDoesNotRenotifyUnchangedDegradedCredential(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.DegradedStatus, Revision: 3}
	notified := 0
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return false, nil },
		Notify:    func(context.Context, string, string, map[string]any) error { notified++; return nil },
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := s.connections["live"]
	if got.Status != api.DegradedStatus || got.Revision != 3 || notified != 0 || got.CredentialVisibility != "inactive" {
		t.Fatalf("unchanged degradation caused repeated mutation/notice: %+v notified=%d", got, notified)
	}
}

func TestReconcilerSkipsRecipeProbeAfterInactiveCredential(t *testing.T) {
	s := NewStore()
	s.connections["live"] = api.Connection{ConnectionID: "live", CredentialRefID: "ref", Status: api.DegradedStatus, Metadata: map[string]string{api.LabelRecipe: "pinned"}}
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { return false, nil },
		Resolve: func(context.Context, api.Connection) (string, error) {
			t.Fatal("recipe probed after credential was visibly inactive")
			return "", nil
		},
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcilerIsolatesCredentialCheckFailures(t *testing.T) {
	s := NewStore()
	failures := map[string]error{"ref-a": errors.New("backend a unavailable"), "ref-b": errors.New("backend b unavailable")}
	for _, id := range []string{"a", "b"} {
		s.connections[id] = api.Connection{ConnectionID: id, CredentialRefID: "ref-" + id, Status: api.ActiveStatus, Metadata: map[string]string{api.LabelRecipe: "pinned", api.LabelCredentialRecipe: "pinned"}}
	}
	checked := map[string]bool{}
	r := &Reconciler{Store: s, CredAlive: func(_ context.Context, ref string) (bool, error) {
		checked[ref] = true
		return false, failures[ref]
	}}
	r.Resolve = func(context.Context, api.Connection) (string, error) {
		t.Fatal("recipe checked after failed credential check")
		return "", nil
	}
	r.CredResolve = r.Resolve
	r.Surface = func(context.Context, api.Connection) ([]api.ToolSpec, error) {
		t.Fatal("surface checked after failed credential check")
		return nil, nil
	}
	r.Notify = func(context.Context, string, string, map[string]any) error {
		t.Fatal("unknown credential state notified as missing")
		return nil
	}
	err := r.ScanOnce(t.Context())
	for ref, failure := range failures {
		if !checked[ref] || !errors.Is(err, failure) {
			t.Errorf("independent check %s: checked=%v error preserved=%v", ref, checked[ref], errors.Is(err, failure))
		}
	}
	for _, conn := range s.Connections() {
		if conn.Status != api.ActiveStatus {
			t.Errorf("transport failure changed connection state: %s", conn.ConnectionID)
		}
	}
}

func TestReconcilerNotificationFailureDoesNotStarveConnections(t *testing.T) {
	s := NewStore()
	for _, id := range []string{"first", "second"} {
		s.connections[id] = api.Connection{ConnectionID: id, CredentialRefID: id, Status: api.ActiveStatus}
	}
	notifyErr := errors.New("notification unavailable")
	checked, notified := 0, 0
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { checked++; return false, nil },
		Notify:    func(context.Context, string, string, map[string]any) error { notified++; return notifyErr },
	}
	err := r.ScanOnce(t.Context())
	if !errors.Is(err, notifyErr) || checked != 2 || notified != 2 {
		t.Fatalf("notification failure aborted independent checks: err=%v checked=%d notified=%d", err, checked, notified)
	}
	for _, conn := range s.Connections() {
		if conn.Status != api.DegradedStatus {
			t.Errorf("missing credential not degraded for %s: %s", conn.ConnectionID, conn.Status)
		}
	}
}

func TestReconcilerDriftNotificationsDoNotStarveConnections(t *testing.T) {
	for _, stage := range []string{"credential-recipe", "recipe", "surface"} {
		s, _, binding := seededStore(t)
		binding.ToolDigest = "sha256:pinned"
		s.bindings[binding.ToolBindingID] = binding
		first := s.connections["google-work"]
		first.Metadata = map[string]string{api.LabelRecipe: "pinned", api.LabelCredentialRecipe: "pinned"}
		s.connections[first.ConnectionID] = first
		second := first
		second.ConnectionID = "second"
		second.Metadata = map[string]string{api.LabelRecipe: "pinned", api.LabelCredentialRecipe: "pinned"}
		s.connections[second.ConnectionID] = second
		binding.ConnectionID, binding.ToolBindingID = second.ConnectionID, "second-binding"
		s.bindings[binding.ToolBindingID] = binding
		notifyErr := errors.New("notification unavailable")
		checked, notified := 0, 0
		r := &Reconciler{Store: s,
			CredAlive: func(context.Context, string) (bool, error) { return true, nil },
			Notify:    func(context.Context, string, string, map[string]any) error { notified++; return notifyErr },
		}
		resolve := func(context.Context, api.Connection) (string, error) { checked++; return "changed", nil }
		wantStatus, label := api.DegradedStatus, api.LabelAcceptRecipe
		switch stage {
		case "credential-recipe":
			r.CredResolve = resolve
			wantStatus, label = api.ReauthStatus, api.LabelAcceptCredential
		case "recipe":
			r.Resolve = resolve
		case "surface":
			label = api.LabelAcceptSurface
			r.Surface = func(context.Context, api.Connection) ([]api.ToolSpec, error) { checked++; return nil, nil }
		}
		err := r.ScanOnce(t.Context())
		if !errors.Is(err, notifyErr) || checked != 2 || notified != 2 {
			t.Errorf("%s: err=%v checked=%d notified=%d", stage, err, checked, notified)
		}
		for _, conn := range s.Connections() {
			if conn.Status != wantStatus || conn.Metadata[label] == "" {
				t.Errorf("%s: drift not fenced for %s: status=%s observed=%q", stage, conn.ConnectionID, conn.Status, conn.Metadata[label])
			}
		}
	}
}

func TestReconcilerRecipeDriftPreservesRestrictedStates(t *testing.T) {
	s := NewStore()
	want := map[string]api.Status{}
	for _, status := range []api.Status{api.RevokedStatus, api.DisabledStatus, api.DeletingStatus, api.ReauthStatus} {
		id := string(status)
		want[id] = status
		s.connections[id] = api.Connection{ConnectionID: id, Status: status, Metadata: map[string]string{api.LabelRecipe: "pinned"}}
	}
	resolved, notified := 0, 0
	r := &Reconciler{Store: s,
		CredAlive: func(context.Context, string) (bool, error) { t.Error("restricted credential probed"); return true, nil },
		Resolve:   func(context.Context, api.Connection) (string, error) { resolved++; return "changed", nil },
		Notify:    func(context.Context, string, string, map[string]any) error { notified++; return nil },
	}
	if err := r.ScanOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if resolved != 0 || notified != 0 {
		t.Errorf("restricted connections checked: resolved=%d notified=%d", resolved, notified)
	}
	for _, conn := range s.Connections() {
		if conn.Status != want[conn.ConnectionID] || conn.Metadata[api.LabelAcceptRecipe] != "" {
			t.Errorf("restricted state changed for %s: %s", conn.ConnectionID, conn.Status)
		}
	}
}

func TestReconcilerIsolatesRecipeAndSurfaceFailures(t *testing.T) {
	for _, stage := range []string{"credential-recipe", "recipe", "surface"} {
		t.Run(stage, func(t *testing.T) {
			s, _, binding := seededStore(t)
			binding.ToolDigest = "sha256:pinned"
			s.bindings[binding.ToolBindingID] = binding
			first := s.connections["google-work"]
			first.Metadata = map[string]string{api.LabelRecipe: "pinned", api.LabelCredentialRecipe: "pinned"}
			s.connections[first.ConnectionID] = first
			second := first
			second.ConnectionID = "second"
			s.connections[second.ConnectionID] = second
			binding.ConnectionID, binding.ToolBindingID = second.ConnectionID, "second-binding"
			s.bindings[binding.ToolBindingID] = binding
			failures := map[string]error{first.ConnectionID: errors.New("first backend unavailable"), second.ConnectionID: errors.New("second backend unavailable")}
			checked := map[string]bool{}
			check := func(_ context.Context, conn api.Connection) (string, error) {
				checked[conn.ConnectionID] = true
				return "", failures[conn.ConnectionID]
			}
			r := &Reconciler{Store: s, CredAlive: func(context.Context, string) (bool, error) { return true, nil }}
			switch stage {
			case "credential-recipe":
				r.CredResolve = check
			case "recipe":
				r.Resolve = check
			case "surface":
				r.Surface = func(ctx context.Context, conn api.Connection) ([]api.ToolSpec, error) {
					_, err := check(ctx, conn)
					return nil, err
				}
			}
			err := r.ScanOnce(t.Context())
			for id, failure := range failures {
				if !checked[id] || !errors.Is(err, failure) {
					t.Errorf("independent connection %s: checked=%v error preserved=%v", id, checked[id], errors.Is(err, failure))
				}
			}
		})
	}
}
