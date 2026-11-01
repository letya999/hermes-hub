//go:build linux && cgo

package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/broker"
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

// store-retain exports the hot audit prefix into a verified archive and, only
// under the explicit destructive flag, prunes the verified source on the live
// store's own command path (SPEC-0087 R5.4).
func TestRetainAuditsExportsAndPrunes(t *testing.T) {
	c := devConfig(t)
	c.DirectForm = true
	c.AuditHotMaxBytes = 64 << 10
	c.AuditArchiveDir = filepath.Join(t.TempDir(), "arch")
	svc, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.Actor{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime-alice", ExternalIdentityID: "tg-alice", ConversationID: "dm-alice", PolicyVersion: "policy-v1"}
	if _, err := svc.Broker.CreateRequest(t.Context(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-1", OnboardingID: "onboarding-1", IdempotencyKey: "request-key-1", OwnerKind: "user"}); err != nil {
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
		t.Fatal(err)
	}
	svc, err = Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Broker.CreateRequest(t.Context(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-2", OnboardingID: "onboarding-2", IdempotencyKey: "request-key-2", OwnerKind: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	rep, err := RetainAudits(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pruned || !rep.Exported || rep.Rows == 0 {
		t.Fatalf("export-only retention report %+v", rep)
	}
	rep, err = RetainAudits(c, true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pruned {
		t.Fatalf("destructive retention report %+v", rep)
	}
	master, err := os.ReadFile(c.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(c.DataDir, seal.Derive(master, "ledger"), c.MaxLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	floor, err := j.AuditFloor()
	_ = j.Close()
	if err != nil || floor != rep.SeqHi+1 {
		t.Fatalf("audit floor = %d %v, want %d", floor, err, rep.SeqHi+1)
	}
}

// A configured hot cap must gate live appends, not just the offline pass:
// Build wires the retention policy onto the serving journal so an audit write
// past the cap surfaces as a visible admission stop (SPEC-0087 R5.3).
func TestBuildAppliesAuditRetention(t *testing.T) {
	c := devConfig(t)
	c.DirectForm = true
	c.AuditHotMaxBytes = 4 << 10
	c.AuditArchiveDir = filepath.Join(t.TempDir(), "arch")
	svc, err := Build(c)
	if err != nil {
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
		t.Fatal(err)
	}
	svc, err = Build(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Broker.Close() }()
	alice := identity.Actor{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime-alice", ExternalIdentityID: "tg-alice", ConversationID: "dm-alice", PolicyVersion: "policy-v1"}
	var stop error
	for i := 0; i < 40 && stop == nil; i++ {
		_, stop = svc.Broker.CreateRequest(t.Context(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-" + strconv.Itoa(i), OnboardingID: "onboarding-" + strconv.Itoa(i), IdempotencyKey: "request-key-" + strconv.Itoa(i), OwnerKind: "user"})
	}
	if !errors.Is(stop, broker.ErrRetentionFull) {
		t.Fatalf("live audit appends passed the configured cap: %v", stop)
	}
}

// The replicated layout retains through the same command path: the pass
// opens the witnessed pair and prunes both legs in one transaction.
func TestRetainAuditsReplicatedStore(t *testing.T) {
	c := devConfig(t)
	c.DirectForm = true
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryKeyFile = filepath.Join(filepath.Dir(c.MasterKeyFile), "recovery.key")
	c.RecoveryIdentity = "installation-one/dev"
	c.AuditHotMaxBytes = 64 << 10
	c.AuditArchiveDir = filepath.Join(t.TempDir(), "arch")
	if err := os.WriteFile(c.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.Actor{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime-alice", ExternalIdentityID: "tg-alice", ConversationID: "dm-alice", PolicyVersion: "policy-v1"}
	if _, err := svc.Broker.CreateRequest(t.Context(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-1", OnboardingID: "onboarding-1", IdempotencyKey: "request-key-1", OwnerKind: "user"}); err != nil {
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
		t.Fatal(err)
	}
	svc, err = Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Broker.CreateRequest(t.Context(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-2", OnboardingID: "onboarding-2", IdempotencyKey: "request-key-2", OwnerKind: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	rep, err := RetainAudits(c, true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pruned || rep.Rows == 0 {
		t.Fatalf("replicated destructive retention report %+v", rep)
	}
	// Both legs pruned: the recovery audit table agrees with primary.
	pk, rk := storeKeys(t, c)
	j, err := journal.OpenReplicatedAnchored(c.DataDir, filepath.Join(c.RecoveryDir, "journal"), c.RecoveryAnchorDir, pk, rk, c.MaxLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	floor, err := j.AuditFloor()
	_ = j.Close()
	if err != nil || floor != rep.SeqHi+1 {
		t.Fatalf("replicated audit floor = %d %v, want %d", floor, err, rep.SeqHi+1)
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
