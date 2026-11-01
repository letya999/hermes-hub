//go:build linux && cgo

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/provider"
)

// storeKeys derives the journal keys exactly like the store commands do.
func storeKeys(t *testing.T, c Config) (primary, recovery []byte) {
	t.Helper()
	master, err := os.ReadFile(c.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	primary = seal.Derive(master, "ledger")
	raw, err := os.ReadFile(c.RecoveryKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	recovery = seal.Derive(seal.Derive(raw, "identity/"+c.RecoveryIdentity), "ledger")
	return primary, recovery
}

// An activated-but-not-normalized store opens through the legacy replay path;
// NormalizeStore arms the derived tables, and the next Build restores domain
// state from them.
func TestNormalizeStoreArmsNormalizedTables(t *testing.T) {
	c := devConfig(t)
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryKeyFile = filepath.Join(filepath.Dir(c.MasterKeyFile), "recovery.key")
	c.RecoveryIdentity = "installation-one/dev"
	c.DirectForm = true
	if err := os.WriteFile(c.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.Actor{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime-alice", ExternalIdentityID: "tg-alice", ConversationID: "dm-alice", PolicyVersion: "policy-v1"}
	request, err := svc.Broker.CreateRequest(t.Context(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-1", OnboardingID: "onboarding-1", IdempotencyKey: "request-key-1", OwnerKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := svc.Broker.OpenSession(request.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Submit(t.Context(), request.ID, session, svc.Broker.CSRF(session, request.ID), provider.Bundle{"token": []byte("synthetic-normalize-canary")}, nil); err != nil {
		t.Fatal(err)
	}
	ready, err := svc.Broker.GetRequest(alice, request.ID)
	if err != nil || ready.Status != "ready" {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStore(c); err != nil {
		t.Fatal(err)
	}
	if err := ActivateStore(c); err != nil {
		t.Fatal(err)
	}
	pk, rk := storeKeys(t, c)
	recoveryJournal := filepath.Join(c.RecoveryDir, "journal")
	j, err := journal.OpenReplicatedAnchored(c.DataDir, recoveryJournal, c.RecoveryAnchorDir, pk, rk, c.MaxLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	if j.Normalized() {
		_ = j.Close()
		t.Fatal("freshly activated store must not report normalized")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := NormalizeStore(c); err != nil {
		t.Fatal("NormalizeStore:", err)
	}
	j, err = journal.OpenReplicatedAnchored(c.DataDir, recoveryJournal, c.RecoveryAnchorDir, pk, rk, c.MaxLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Normalized() {
		_ = j.Close()
		t.Fatal("NormalizeStore did not arm the marker")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	svc, err = Build(c)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Broker.Close()
	if _, err := svc.Broker.GetCredential(alice, ready.CredentialID); err != nil {
		t.Fatal("normalized restore lost the credential:", err)
	}
}

// The single-directory layout (no recovery leg) normalizes through the same
// command path.
func TestNormalizeStoreSingleStore(t *testing.T) {
	c := devConfig(t)
	c.DirectForm = true
	svc, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.Actor{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime-alice", ExternalIdentityID: "tg-alice", ConversationID: "dm-alice", PolicyVersion: "policy-v1"}
	request, err := svc.Broker.CreateRequest(t.Context(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-1", OnboardingID: "onboarding-1", IdempotencyKey: "request-key-1", OwnerKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := svc.Broker.OpenSession(request.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Submit(t.Context(), request.ID, session, svc.Broker.CSRF(session, request.ID), provider.Bundle{"token": []byte("synthetic-normalize-canary")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStore(c); err != nil {
		t.Fatal(err)
	}
	if err := ActivateStore(c); err != nil {
		t.Fatal(err)
	}
	if err := NormalizeStore(c); err != nil {
		t.Fatal("NormalizeStore:", err)
	}
	master, err := os.ReadFile(c.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(c.DataDir, seal.Derive(master, "ledger"), c.MaxLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Normalized() {
		_ = j.Close()
		t.Fatal("single store did not arm the marker")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}
