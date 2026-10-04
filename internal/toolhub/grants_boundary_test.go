package toolhub

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogGrantAbsentAndDenyPrecedence(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Catalog(aliceAuth())
	if err != nil || len(entries) != 0 {
		t.Fatalf("ungranted catalog disclosed: %+v, %v", entries, err)
	}
	if err := store.RequireCatalogAccess(aliceAuth(), definition); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ungranted access: %v", err)
	}
	if err := putGrant(t, store, OperatorGrant(GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	deny := OperatorGrant(GrantDefinition, "alice", definition.DefinitionID, definition.Version)
	deny.Status = RevokedStatus
	if err := putGrant(t, store, deny); err != nil {
		t.Fatal(err)
	}
	for range 32 {
		if err := store.RequireCatalogAccess(aliceAuth(), definition); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("catalog default overrode specific denial: %v", err)
		}
		entries, err := store.Catalog(aliceAuth())
		if err != nil || len(entries) != 0 {
			t.Fatalf("denied catalog disclosed: %+v, %v", entries, err)
		}
	}
	other := definition
	other.Version = "2.0.0"
	if err := store.RequireCatalogAccess(aliceAuth(), other); err != nil {
		t.Fatalf("specific denial leaked to another version: %v", err)
	}
}

func TestSelfInstallDenialWinsAcrossGrantIDs(t *testing.T) {
	store := NewStore()
	allow := OperatorGrant(GrantSelfInstall, "alice", "", "")
	if err := putGrant(t, store, allow); err != nil {
		t.Fatal(err)
	}
	deny := allow
	deny.GrantID, deny.Status = "explicit-denial", DisabledStatus
	if err := putGrant(t, store, deny); err != nil {
		t.Fatal(err)
	}
	for range 32 {
		if err := store.RequireSelfInstall(aliceAuth()); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("conflicting grants admitted self-install: %v", err)
		}
	}
}

func TestGrantRevisionCannotResurrectPermission(t *testing.T) {
	store := NewStore()
	allow := OperatorGrant(GrantSelfInstall, "alice", "", "")
	deny := allow
	deny.Status, deny.Revision = RevokedStatus, 2
	if err := putGrant(t, store, deny); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, allow); !errors.Is(err, ErrConflict) {
		t.Fatalf("older grant resurrected permission: %v", err)
	}
	if err := putGrant(t, store, deny); err != nil {
		t.Fatalf("identical retry rejected: %v", err)
	}
	allow.Revision = 2
	if err := putGrant(t, store, allow); !errors.Is(err, ErrConflict) {
		t.Fatalf("same revision changed permission: %v", err)
	}
	if err := store.RequireSelfInstall(aliceAuth()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("rejected update changed authorization: %v", err)
	}
}

func TestGrantRefreshAndFailedWriteDoNotAdmit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	initial := NewStore()
	grant := OperatorGrant(GrantSelfInstall, "alice", "", "")
	if err := putGrant(t, initial, grant); err != nil {
		t.Fatal(err)
	}
	if err := initial.Save(path); err != nil {
		t.Fatal(err)
	}
	reader, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	grant.Status, grant.Revision = RevokedStatus, 2
	if err := putGrant(t, writer, grant); err != nil {
		t.Fatal(err)
	}
	stale := grant
	stale.Status, stale.Revision = ActiveStatus, 3
	if err := putGrant(t, reader, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale store overwrote revoke: %v", err)
	}
	if err := reader.RequireSelfInstall(aliceAuth()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale reader admitted after revoke: %v", err)
	}
	if err := os.Rename(path, path+".backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, reader, stale); err == nil {
		t.Fatal("grant update succeeded on unusable store")
	}
	if reader.grants[grant.GrantID].Status != RevokedStatus {
		t.Fatal("failed durable update changed in-memory authorization")
	}
	bob := OperatorGrant(GrantSelfInstall, "bob", "", "")
	if err := putGrant(t, reader, bob); err == nil {
		t.Fatal("new grant succeeded on unusable store")
	}
	if _, exists := reader.grants[bob.GrantID]; exists {
		t.Fatal("failed durable insert left a grant")
	}
	if err := reader.RequireSelfInstall(aliceAuth()); err == nil {
		t.Fatal("unavailable store admitted self-install")
	}
	if err := reader.RequireCatalogAccess(aliceAuth(), remoteDefinition()); err == nil {
		t.Fatal("unavailable store admitted catalog access")
	}
}
