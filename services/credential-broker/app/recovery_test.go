package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/provider"
)

func TestBuildRestoresReplicatedJournalAfterPrimaryLoss(t *testing.T) {
	c := devConfig(t)
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryKeyFile = filepath.Join(filepath.Dir(c.MasterKeyFile), "recovery.key")
	c.RecoveryIdentity = "installation-one/dev"
	if err := os.WriteFile(c.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.RecoveryDir, "journal", "ledger.bin")); err != nil {
		t.Fatal("Build did not create encrypted recovery journal")
	}
	if err := os.RemoveAll(c.DataDir); err != nil {
		t.Fatal(err)
	}
	svc, err = Build(c)
	if err != nil {
		t.Fatalf("Build did not recover primary journal: %v", err)
	}
	defer svc.Broker.Close()
}

func TestAssembleProvidersRestoresLocalObject(t *testing.T) {
	c := devConfig(t)
	recovery := filepath.Join(t.TempDir(), "recovery")
	reg, err := assembleProviders(c.Providers, bytes.Repeat([]byte{1}, 32), recovery, bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := reg.Put(context.Background(), "local", provider.Bundle{"token": []byte("synthetic-provider-canary")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(c.Providers[0].Directory, ref.Locator)); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Resolve(context.Background(), ref)
	if err != nil || !bytes.Equal(got["token"], []byte("synthetic-provider-canary")) {
		t.Fatal("configured local provider did not recover original ref")
	}
	got.Wipe()
}

func TestAssembleProvidersRejectsMissingRecoveryKey(t *testing.T) {
	c := devConfig(t)
	if _, err := assembleProviders(c.Providers, bytes.Repeat([]byte{1}, 32), filepath.Join(t.TempDir(), "recovery"), nil); err == nil {
		t.Fatal("recoverable provider accepted missing recovery key")
	}
}

func TestBuildRecoveryPreservesOwnerAndRequiresFreshLease(t *testing.T) {
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
	bob := alice
	bob.PrincipalID = "bob"
	request, err := svc.Broker.CreateRequest(t.Context(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-1", OnboardingID: "onboarding-1", IdempotencyKey: "request-key-1", OwnerKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := svc.Broker.OpenSession(request.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Submit(t.Context(), request.ID, session, svc.Broker.CSRF(session, request.ID), provider.Bundle{"token": []byte("synthetic-provider-canary")}, nil); err != nil {
		t.Fatal(err)
	}
	ready, err := svc.Broker.GetRequest(alice, request.ID)
	if err != nil || ready.Status != "ready" {
		t.Fatal(err)
	}
	grant, err := svc.Broker.CreateGrant(alice, ready.CredentialID, v1.GrantRequest{ContractID: "github-pat", ContractRevision: 1, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime-alice", BindingID: "binding-1", WorkloadID: "workload-1", Execution: "dedicated", IdempotencyKey: "grant-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	alice.BindingID, alice.WorkloadID = "binding-1", "workload-1"
	oldLease, err := svc.Broker.Acquire(alice, v1.AcquireLease{GrantID: grant.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(c.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(c.Providers[0].Directory); err != nil {
		t.Fatal(err)
	}
	svc, err = Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Broker.GetCredential(bob, ready.CredentialID); err == nil {
		t.Fatal("other owner saw recovered credential")
	}
	if _, err := svc.Broker.InspectLease(alice, oldLease.ID); err == nil {
		t.Fatal("old lease survived restart")
	}
	newLease, err := svc.Broker.Acquire(alice, v1.AcquireLease{GrantID: grant.ID})
	if err != nil {
		t.Fatal(err)
	}
	use, err := svc.Broker.BeginUse(t.Context(), alice, newLease.ID)
	if err != nil {
		t.Fatal(err)
	}
	values, err := svc.Broker.Resolve(use.Context, use)
	if err != nil || !bytes.Equal(values["token"], []byte("synthetic-provider-canary")) {
		t.Fatal("fresh authorized lease could not read recovered provider object")
	}
	values.Wipe()
	use.Finish()
	if err := svc.Broker.RevokeCredential(alice, ready.CredentialID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(c.DataDir); err != nil {
		t.Fatal(err)
	}
	svc, err = Build(c)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := svc.Broker.GetCredential(alice, ready.CredentialID)
	if err != nil || credential.Status != "revoked" {
		t.Fatal("revocation was lost during recovery")
	}
	if _, err := svc.Broker.Acquire(alice, v1.AcquireLease{GrantID: grant.ID}); err == nil {
		t.Fatal("revoked authority acquired a new lease")
	}
	if err := svc.Broker.DeleteCredential(t.Context(), alice, ready.CredentialID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(c.DataDir); err != nil {
		t.Fatal(err)
	}
	svc, err = Build(c)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Broker.Close()
	credential, err = svc.Broker.GetCredential(alice, ready.CredentialID)
	if err != nil || credential.Status != "deleted" {
		t.Fatal("deletion was lost during recovery")
	}
	objects, err := os.ReadDir(filepath.Join(c.RecoveryDir, "providers", "local"))
	if err != nil || len(objects) != 0 {
		t.Fatal("deleted provider backup remains")
	}
}

func TestBuildRejectsRecoveryFromOtherEnvironment(t *testing.T) {
	first := devConfig(t)
	first.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	first.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	first.RecoveryKeyFile = filepath.Join(filepath.Dir(first.MasterKeyFile), "recovery.key")
	first.RecoveryIdentity = "installation-one/dev"
	key := bytes.Repeat([]byte{9}, 32)
	if err := os.WriteFile(first.RecoveryKeyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Build(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	second := devConfig(t)
	second.RecoveryDir = first.RecoveryDir
	second.RecoveryAnchorDir = first.RecoveryAnchorDir
	second.RecoveryKeyFile = filepath.Join(filepath.Dir(second.MasterKeyFile), "recovery.key")
	second.RecoveryIdentity = "installation-two/dev"
	if err := os.WriteFile(second.RecoveryKeyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	svc, err = Build(second)
	if err == nil {
		_ = svc.Broker.Close()
		t.Fatal("foreign recovery state opened under another environment")
	}
}

func TestBuildRejectsRecoveryWithoutIdentity(t *testing.T) {
	c := devConfig(t)
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryKeyFile = filepath.Join(filepath.Dir(c.MasterKeyFile), "recovery.key")
	if err := os.WriteFile(c.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Build(c)
	if err == nil {
		_ = svc.Broker.Close()
		t.Fatal("recovery accepted without deployment/environment binding")
	}
}

func TestBuildRejectsPrimaryKeyAsRecoveryKey(t *testing.T) {
	c := devConfig(t)
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryKeyFile = c.MasterKeyFile
	c.RecoveryIdentity = "installation-one/dev"
	svc, err := Build(c)
	if err == nil {
		_ = svc.Broker.Close()
		t.Fatal("primary key reused as recovery key")
	}
}

func TestBuildRejectsAnchorInsideRecoveryStore(t *testing.T) {
	c := devConfig(t)
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryAnchorDir = filepath.Join(c.RecoveryDir, "anchor")
	c.RecoveryKeyFile = filepath.Join(filepath.Dir(c.MasterKeyFile), "recovery.key")
	c.RecoveryIdentity = "installation-one/dev"
	if err := os.WriteFile(c.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Build(c)
	if err == nil {
		_ = svc.Broker.Close()
		t.Fatal("anchor shares rollback domain with recovery store")
	}
}

func TestMigrateRecoveryPreservesExistingCredential(t *testing.T) {
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
	if err := svc.Broker.Submit(t.Context(), request.ID, session, svc.Broker.CSRF(session, request.ID), provider.Bundle{"token": []byte("synthetic-migration-canary")}, nil); err != nil {
		t.Fatal(err)
	}
	ready, err := svc.Broker.GetRequest(alice, request.ID)
	if err != nil || ready.Status != "ready" {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryKeyFile = filepath.Join(filepath.Dir(c.MasterKeyFile), "recovery.key")
	c.RecoveryIdentity = "installation-one/dev"
	if err := os.WriteFile(c.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateRecovery(c); err != nil {
		t.Fatal(err)
	}
	if err := MigrateRecovery(c); err != nil {
		t.Fatalf("repeated migration failed: %v", err)
	}
	if err := os.RemoveAll(c.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(c.Providers[0].Directory); err != nil {
		t.Fatal(err)
	}
	svc, err = Build(c)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Broker.Close()
	credential, err := svc.Broker.GetCredential(alice, ready.CredentialID)
	if err != nil || credential.Status != "active" {
		t.Fatal("migrated credential metadata was lost")
	}
	grant, err := svc.Broker.CreateGrant(alice, ready.CredentialID, v1.GrantRequest{ContractID: "github-pat", ContractRevision: 1, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime-alice", BindingID: "binding-1", WorkloadID: "workload-1", Execution: "dedicated", IdempotencyKey: "grant-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	alice.BindingID, alice.WorkloadID = "binding-1", "workload-1"
	lease, err := svc.Broker.Acquire(alice, v1.AcquireLease{GrantID: grant.ID})
	if err != nil {
		t.Fatal(err)
	}
	use, err := svc.Broker.BeginUse(t.Context(), alice, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer use.Finish()
	values, err := svc.Broker.Resolve(use.Context, use)
	if err != nil || !bytes.Equal(values["token"], []byte("synthetic-migration-canary")) {
		t.Fatal("migrated provider version was not restored")
	}
	values.Wipe()
}

func TestRecoveryRejectsOverlappingStores(t *testing.T) {
	for _, kind := range []string{"backup-in-primary", "anchor-in-primary", "anchor-in-backup"} {
		t.Run(kind, func(t *testing.T) {
			c := devConfig(t)
			c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
			c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
			c.RecoveryKeyFile = filepath.Join(filepath.Dir(c.MasterKeyFile), "recovery.key")
			c.RecoveryIdentity = "installation-one/dev"
			if err := os.WriteFile(c.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "backup-in-primary":
				c.RecoveryDir = filepath.Join(c.DataDir, "recovery")
			case "anchor-in-primary":
				c.RecoveryAnchorDir = filepath.Join(c.DataDir, "anchor")
			case "anchor-in-backup":
				c.RecoveryAnchorDir = filepath.Join(c.RecoveryDir, "anchor")
			}
			if svc, err := Build(c); err == nil {
				_ = svc.Broker.Close()
				t.Error("Build accepted overlapping recovery storage")
			}
			if err := MigrateRecovery(c); err == nil {
				t.Error("migration accepted overlapping recovery storage")
			}
		})
	}
}

func TestBuildRejectsPartialRecoveryConfiguration(t *testing.T) {
	c := devConfig(t)
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	c.RecoveryIdentity = "installation-one/dev"
	svc, err := Build(c)
	if err == nil {
		_ = svc.Broker.Close()
		t.Fatal("partial recovery configuration silently disabled backup")
	}
}

func TestReadConfigRejectsPartialRecoveryConfiguration(t *testing.T) {
	c := devConfig(t)
	c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	path := filepath.Join(filepath.Dir(c.MasterKeyFile), "partial.json")
	if err := WriteJSON(path, c); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(path); err == nil {
		t.Fatal("check-config accepted incomplete recovery settings")
	}
	c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	c.RecoveryKeyFile = filepath.Join(filepath.Dir(c.MasterKeyFile), "recovery.key")
	if err := WriteJSON(filepath.Join(filepath.Dir(c.MasterKeyFile), "incomplete-recovery.json"), c); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(filepath.Join(filepath.Dir(c.MasterKeyFile), "incomplete-recovery.json")); err == nil {
		t.Fatal("check-config accepted recovery without identity")
	}
}

func TestRecoveryKeyMustSurvivePrimaryAndStayOutsideBackup(t *testing.T) {
	for _, placement := range []string{"primary", "backup", "anchor"} {
		t.Run(placement, func(t *testing.T) {
			c := devConfig(t)
			c.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
			c.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
			c.RecoveryIdentity = "installation-one/dev"
			if placement == "primary" {
				c.RecoveryKeyFile = filepath.Join(c.DataDir, "recovery.key")
			} else if placement == "backup" {
				c.RecoveryKeyFile = filepath.Join(c.RecoveryDir, "recovery.key")
			} else {
				c.RecoveryKeyFile = filepath.Join(c.RecoveryAnchorDir, "recovery.key")
			}
			if err := os.MkdirAll(filepath.Dir(c.RecoveryKeyFile), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
				t.Fatal(err)
			}
			if svc, err := Build(c); err == nil {
				_ = svc.Broker.Close()
				t.Fatal("recovery key shares primary, backup or anchor storage")
			}
		})
	}
}
