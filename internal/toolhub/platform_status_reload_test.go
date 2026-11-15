package toolhub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestPlatformStatusReloadAfterAdmissionDoesNotBlockRights(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store := NewStore()
	t.Cleanup(func() { _ = store.CloseDB() })
	definition := r5ExecutorDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	if _, err := store.Enable(auth, definition.DefinitionID, definition.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	other, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.CloseDB() })
	gateway := &Gateway{Store: store}
	changed := false
	gateway.AuditWrite = func(event string, _ map[string]string) error {
		if event == "admit" && !changed {
			changed = true
			updated := r5ExecutorDefinition()
			updated.Version = "7.0.1"
			return other.RegisterDefinition(updated)
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := gateway.CallAuthorized(context.Background(), auth, api.ProjectedToolName(definition.DefinitionID, definition.Version, "platform_status"), map[string]any{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("platform status kept the admission lock while reloading the store")
	}
	if !changed {
		t.Fatal("the concurrent store update did not run")
	}
}
