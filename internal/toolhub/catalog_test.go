package toolhub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestManifestCatalogAndEffectiveBindingLifecycle(t *testing.T) {
	store, auth, binding := seededStore(t)
	entries, err := store.Catalog(auth)
	if err != nil || len(entries) != 1 || !entries[0].Enabled || entries[0].BindingID != binding.ToolBindingID {
		t.Fatalf("catalog=%+v err=%v", entries, err)
	}
	if err := store.Disable(auth, binding.DefinitionID, binding.DefinitionVersion); err != nil {
		t.Fatal(err)
	}
	entries, err = store.Catalog(auth)
	if err != nil || entries[0].Enabled || entries[0].Status != string(DisabledStatus) {
		t.Fatalf("disabled catalog=%+v err=%v", entries, err)
	}
	if _, err := store.Enable(auth, binding.DefinitionID, binding.DefinitionVersion); err != nil {
		t.Fatal(err)
	}
	foreign := auth
	foreign.PrincipalID = "bob"
	if _, err := store.Enable(foreign, binding.DefinitionID, binding.DefinitionVersion); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("foreign owner accepted: %v", err)
	}
}

func TestProjectionRevisionReconnectIsMonotonicAndQuiet(t *testing.T) {
	store, auth, binding := seededStore(t)
	var callbacks atomic.Int32
	controller := &ReconnectController{Store: store, Auth: auth, OnChange: func(ProjectionChange) error { callbacks.Add(1); return nil }}
	if _, changed, err := controller.Reconcile(); err != nil || !changed {
		t.Fatalf("initial reconcile changed=%v err=%v", changed, err)
	}
	if _, changed, err := controller.Reconcile(); err != nil || changed {
		t.Fatalf("unchanged projection changed=%v err=%v", changed, err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, DisabledStatus); err != nil {
		t.Fatal(err)
	}
	change, changed, err := controller.Reconcile()
	if err != nil || !changed || change.Revision == 0 || callbacks.Load() != 2 {
		t.Fatalf("changed projection=%+v changed=%v callbacks=%d err=%v", change, changed, callbacks.Load(), err)
	}
	if _, changed, err := controller.Reconcile(); err != nil || changed || callbacks.Load() != 2 {
		t.Fatalf("reconnect storm changed=%v callbacks=%d err=%v", changed, callbacks.Load(), err)
	}
}

func TestReconnectControllerRetriesFailedNotification(t *testing.T) {
	store, auth, _ := seededStore(t)
	var calls atomic.Int32
	controller := &ReconnectController{Store: store, Auth: auth, OnChange: func(ProjectionChange) error {
		if calls.Add(1) == 1 {
			return errors.New("transport unavailable")
		}
		return nil
	}}
	if _, changed, err := controller.Reconcile(); !changed || err == nil {
		t.Fatalf("initial failure changed=%v err=%v", changed, err)
	}
	if _, changed, err := controller.Reconcile(); !changed || err != nil || calls.Load() != 2 {
		t.Fatalf("failed notification was not retried: changed=%v err=%v calls=%d", changed, err, calls.Load())
	}
}

func TestManifestCatalogReportsMissingCredentialsAndStaleConnection(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	entries, err := store.Catalog(auth)
	if err != nil || len(entries) != 1 || entries[0].Status != "missing-connection" || len(entries[0].MissingCredentials) != 1 {
		t.Fatalf("missing credential catalog=%+v err=%v", entries, err)
	}
	if _, err := store.Enable(auth, definition.DefinitionID, definition.Version); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing connection enabled: %v", err)
	}
	if _, err := store.Enable(auth, "missing", "1.0.0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing manifest enabled: %v", err)
	}
	store, auth, binding := seededStore(t)
	if err := store.SetConnectionStatus(binding.ConnectionID, DisabledStatus); err != nil {
		t.Fatal(err)
	}
	entries, err = store.Catalog(auth)
	if err != nil || len(entries) != 1 || entries[0].Status != "stale" || entries[0].Enabled {
		t.Fatalf("stale catalog=%+v err=%v", entries, err)
	}
}

func TestReconnectControllerRequiresStoreAndCallback(t *testing.T) {
	if _, _, err := (&ReconnectController{}).Reconcile(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid reconnect controller error=%v", err)
	}
	store, auth, binding := seededStore(t)
	if err := store.SetBindingStatus(binding.ToolBindingID, RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enable(auth, binding.DefinitionID, binding.DefinitionVersion); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked binding re-enabled: %v", err)
	}
	badAuth := auth
	badAuth.PolicyVersion = ""
	if _, err := store.ProjectionRevision(badAuth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid projection identity accepted: %v", err)
	}
	if err := store.Disable(auth, binding.DefinitionID, binding.DefinitionVersion); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked binding disabled: %v", err)
	}
	controller := &ReconnectController{Store: store, Auth: auth, OnChange: func(ProjectionChange) error { return errors.New("reconnect failed") }}
	if _, changed, err := controller.Reconcile(); !changed || err == nil {
		t.Fatalf("reconnect callback error changed=%v err=%v", changed, err)
	}
}

func TestEnableBuildsBindingFromExactOwnerConnection(t *testing.T) {
	store, auth, binding := seededStore(t)
	delete(store.bindings, binding.ToolBindingID)
	created, err := store.Enable(auth, binding.DefinitionID, binding.DefinitionVersion)
	if err != nil {
		t.Fatal(err)
	}
	if created.ConnectionID != binding.ConnectionID || created.CredentialRefID != binding.CredentialRefID || created.ToolBindingID != binding.ToolBindingID {
		t.Fatalf("binding=%+v want=%+v", created, binding)
	}
	foreign := auth
	foreign.PrincipalID = "bob"
	if _, err := store.Enable(foreign, binding.DefinitionID, binding.DefinitionVersion); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("foreign connection selected: %v", err)
	}
	badAuth := auth
	badAuth.ContextID = ""
	if _, err := store.Catalog(badAuth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid catalog identity accepted: %v", err)
	}
	if err := store.SetConnectionStatus(binding.ConnectionID, Status("invalid")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid connection status accepted: %v", err)
	}
}

func TestEnableReadyAdmitsBeforePublishingProjection(t *testing.T) {
	store := NewStore()
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	var admitted bool
	binding, err := store.EnableReady(context.Background(), auth, definition.DefinitionID, definition.Version, func(_ context.Context, effective EffectiveBinding) error {
		admitted = effective.Binding.Status == ActiveStatus && effective.WorkloadID != ""
		return nil
	})
	if err != nil || !admitted || binding.ToolBindingID == "" {
		t.Fatalf("ready binding=%+v admitted=%v err=%v", binding, admitted, err)
	}
	if revision, err := store.ProjectionRevision(auth); err != nil || revision == 0 {
		t.Fatalf("projection revision=%d err=%v", revision, err)
	}
	failed := NewStore()
	if err := failed.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if _, err := failed.EnableReady(context.Background(), auth, definition.DefinitionID, definition.Version, func(context.Context, EffectiveBinding) error { return errors.New("not ready") }); err == nil {
		t.Fatal("failed readiness published")
	}
	if revision, _ := failed.ProjectionRevision(auth); revision != 0 {
		t.Fatalf("failed readiness projection=%d", revision)
	}
	if _, err := failed.EnableReady(context.Background(), auth, definition.DefinitionID, definition.Version, nil); err != nil {
		t.Fatalf("nil readiness fallback=%v", err)
	}
}

func TestEnableReadyRechecksExistingBindingAndRollsBack(t *testing.T) {
	store, auth, binding := seededStore(t)
	called := false
	if _, err := store.EnableReady(context.Background(), auth, binding.DefinitionID, binding.DefinitionVersion, func(_ context.Context, effective EffectiveBinding) error {
		called = effective.Binding.Status == ActiveStatus
		return nil
	}); err != nil || !called {
		t.Fatalf("active readiness called=%v err=%v", called, err)
	}
	if err := store.Disable(auth, binding.DefinitionID, binding.DefinitionVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnableReady(context.Background(), auth, binding.DefinitionID, binding.DefinitionVersion, func(context.Context, EffectiveBinding) error { return errors.New("not ready") }); err == nil {
		t.Fatal("existing failed readiness accepted")
	}
	current, err := store.binding(binding.ToolBindingID)
	if err != nil || current.Status != DisabledStatus {
		t.Fatalf("failed readiness changed status=%+v err=%v", current, err)
	}
}

func TestDisableCannotFindForeignBinding(t *testing.T) {
	store, auth, binding := seededStore(t)
	foreign := auth
	foreign.PrincipalID = "bob"
	if err := store.Disable(foreign, binding.DefinitionID, binding.DefinitionVersion); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign binding disabled: %v", err)
	}
	if err := store.Disable(auth, "missing", "1.0.0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing manifest disabled: %v", err)
	}
}

func TestEnableDisablePersistsAndReloadsFromDisk(t *testing.T) {
	store, auth, binding := seededStore(t)
	path := filepath.Join(t.TempDir(), "store.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	writer, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Disable(auth, binding.DefinitionID, binding.DefinitionVersion); err != nil {
		t.Fatal(err)
	}
	listed, err := reader.ListProjectedTools(auth)
	if err != nil || len(listed) != 0 {
		t.Fatalf("disable did not persist for reload: %+v err=%v", listed, err)
	}
	if _, err := writer.Enable(auth, binding.DefinitionID, binding.DefinitionVersion); err != nil {
		t.Fatal(err)
	}
	listed, err = reader.ListProjectedTools(auth)
	if err != nil || len(listed) == 0 {
		t.Fatalf("enable did not persist for reload: %+v err=%v", listed, err)
	}
}

func TestEnableRejectsAmbiguousOwnerConnections(t *testing.T) {
	store, auth, binding := seededStore(t)
	delete(store.bindings, binding.ToolBindingID)
	secondID := "google-alt"
	credential := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID(secondID, 1), ConnectionID: secondID, Revision: 1, Backend: "local", Locator: "local://alice/google/alt", Keys: []string{"GOOGLE_TOKEN"}, Status: ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := Connection{Schema: SchemaVersion, ConnectionID: secondID, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: "google-work", CredentialRefID: credential.CredentialRefID, Revision: 1, Status: ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enable(auth, binding.DefinitionID, binding.DefinitionVersion); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ambiguous owner connection accepted: %v", err)
	}
}

func TestReconnectMarkerIsWrittenOncePerRevision(t *testing.T) {
	dir := t.TempDir()
	store, auth, binding := seededStore(t)
	var calls atomic.Int32
	store.Reconnect = &ReconnectController{Store: store, Auth: auth, OnChange: func(change ProjectionChange) error {
		calls.Add(1)
		return WriteReconnectMarker(dir, change)
	}}
	if err := store.Disable(auth, binding.DefinitionID, binding.DefinitionVersion); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("reconnect callbacks=%d", calls.Load())
	}
	body, err := os.ReadFile(filepath.Join(dir, ReconnectMarkerName))
	if err != nil || !strings.Contains(string(body), `"revision"`) || strings.Contains(string(body), "GOOGLE_TOKEN") || strings.Contains(string(body), "local://") {
		t.Fatalf("reconnect marker leaked or missing: %s err=%v", body, err)
	}
	if err := WriteReconnectMarker("", ProjectionChange{Revision: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty marker directory accepted: %v", err)
	}
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteReconnectMarker(file, ProjectionChange{Revision: 1, Reason: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("file reconnect directory accepted: %v", err)
	}
}

func TestPersistAndNotifySurfacesReconnectError(t *testing.T) {
	store, auth, binding := seededStore(t)
	store.Reconnect = &ReconnectController{Store: store, Auth: auth, OnChange: func(ProjectionChange) error {
		return errors.New("reconnect failed")
	}}
	if err := store.Disable(auth, binding.DefinitionID, binding.DefinitionVersion); err == nil {
		t.Fatal("reconnect failure accepted")
	}
}

func TestReloadMissingStoreFailsClosed(t *testing.T) {
	store := NewStore()
	store.path = filepath.Join(t.TempDir(), "missing.json")
	if err := store.Reload(); err == nil {
		t.Fatal("missing store reload accepted")
	}
}

func TestAuthorizeProjectedRejectsNilAdmit(t *testing.T) {
	store, auth, _ := seededStore(t)
	if err := store.AuthorizeProjected(auth, ProjectedToolName("google-work", "1.0.0", "search"), nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil admit error=%v", err)
	}
}

func TestReloadUnchangedSnapshotPreservesPendingMutation(t *testing.T) {
	store, _, _ := seededStore(t)
	path := filepath.Join(t.TempDir(), "store.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	definition := remoteDefinition()
	definition.DefinitionID = "pending-install"
	if err := loaded.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.Definition(definition.DefinitionID, definition.Version); err != nil {
		t.Fatal("unchanged disk discarded pending install", err)
	}
	if err := loaded.persist(); err != nil {
		t.Fatal(err)
	}
	fresh, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Definition(definition.DefinitionID, definition.Version); err != nil {
		t.Fatal("pending install did not persist", err)
	}
}
