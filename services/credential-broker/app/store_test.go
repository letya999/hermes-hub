//go:build linux && cgo

package app

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/broker"
	"github.com/letya999/credential-broker/contract"
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

// records_auto_retain is the explicit destructive policy for automatic
// high-water rotation: Build wires it onto the store, the maintenance tick
// rotates without an operator and the file shrinks (SPEC-0087 R5.2).
func TestBuildWiresRecordsAutoRetain(t *testing.T) {
	c := devConfig(t)
	c.DirectForm = true
	c.MaxLedgerBytes = 2 << 20
	c.AuditArchiveDir = filepath.Join(t.TempDir(), "arch")
	c.AuditHotMaxBytes = 64 << 10
	c.AuditAutoRetain = true
	c.RecordsAutoRetain = true
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
	// Cross the audit high-water first: small records carrying ~4 KiB audit
	// bodies — the audit prefix must prune while records stay under water.
	auditPad := strings.Repeat("y", 4000)
	for i := 0; i < 14; i++ {
		if _, err := svc.ledger.AppendWith("mutation.v1", map[string]string{"op": "a"}, &journal.AuxWrites{
			Audit: &journal.AuditWrite{Kind: "k", Principal: "p", Context: "c", Body: []byte(auditPad)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if rep := svc.ledger.RetainMaintenance(); rep.Audit == nil || !rep.Audit.Pruned {
		t.Fatalf("auto-wired audit retention did not prune: %+v", rep)
	}
	pad := strings.Repeat("x", 4000)
	for i := 0; i < 440; i++ {
		if _, err := svc.ledger.AppendWith("mutation.v1", map[string]string{"pad": pad}, nil); err != nil {
			t.Fatal(err)
		}
	}
	rep := svc.ledger.RetainMaintenance()
	if rep.LastError != "" {
		t.Fatalf("maintenance error: %s", rep.LastError)
	}
	if rep.Records == nil || !rep.Records.Pruned {
		t.Fatalf("auto-wired rotation did not prune: %+v", rep)
	}
	floor, err := svc.ledger.RecordsFloor()
	if err != nil || floor <= 1 {
		t.Fatalf("floor = %d after auto rotation", floor)
	}
}

// The config-level archive byte budget must reach the serving journal:
// Build wires records_archive_max_bytes onto the retention policy so the
// maintenance tick permanently deletes the oldest verified archive pair.
func TestBuildWiresRecordsArchiveBudget(t *testing.T) {
	c := devConfig(t)
	c.DirectForm = true
	c.MaxLedgerBytes = 2 << 20
	c.RecordsArchiveDir = filepath.Join(t.TempDir(), "arch")
	c.RecordsArchiveMaxBytes = 1
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
	// Two rotations → two archives; budget=1 sweeps the oldest.
	pad := strings.Repeat("x", 4000)
	for i := 0; i < 40; i++ {
		if _, err := svc.ledger.AppendWith("mutation.v1", map[string]string{"pad": pad}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ledger.RetainRecords(32 << 10); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := svc.ledger.AppendWith("mutation.v1", map[string]string{"pad": pad}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ledger.RetainRecords(32 << 10); err != nil {
		t.Fatal(err)
	}
	if rep := svc.ledger.RetainMaintenance(); rep.LastError != "" {
		t.Fatalf("sweep error: %s", rep.LastError)
	}
	ents, err := os.ReadDir(c.RecordsArchiveDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 {
		t.Fatalf("archive budget not wired: %d files remain, want newest pair only", len(ents))
	}
}

// The service's own sweep tick drives high-water maintenance — no operator,
// no offline command: over-water history rotates inside normal operation.
func TestServiceTickRunsRetainMaintenance(t *testing.T) {
	c := devConfig(t)
	c.DirectForm = true
	c.DevHTTP = true
	c.MaxLedgerBytes = 2 << 20
	c.AuditArchiveDir = filepath.Join(t.TempDir(), "arch")
	c.AuditHotMaxBytes = 64 << 10
	c.RecordsAutoRetain = true
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
	pad := strings.Repeat("x", 4000)
	for i := 0; i < 440; i++ {
		if _, err := svc.ledger.AppendWith("mutation.v1", map[string]string{"pad": pad}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx, listener) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		floor, err := svc.ledger.RecordsFloor()
		if err == nil && floor > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("maintenance tick never rotated: floor=%d", floor)
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	<-done
}

// store-sweep runs the object worker offline: a stopped store still drains
// its durable pending-delete queue instead of needing the service process.
func TestSweepObjectsCommandDrainsRetiredObjects(t *testing.T) {
	c := devConfig(t)
	c.DirectForm = true
	svc, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.Actor{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime-alice", ExternalIdentityID: "tg-alice", ConversationID: "dm-alice", PolicyVersion: "policy-v1"}
	// A state-capable contract so checkpoints mint versions that retire.
	sc := contract.Contract{ID: "session", Revision: 1, Title: "session", Storage: "local",
		Fields:     []contract.Field{{ID: "token", Label: "token", Kind: "secret", Required: true, MaxBytes: 4096}},
		Deliveries: []contract.Delivery{{Type: "file", Field: "token", Target: "/run/secrets/token", EnvName: "TOKEN_FILE"}},
		State:      &contract.State{Target: "/home/mcp/.config/service", Files: []contract.StateFile{{Name: "runtime_session.json", JSON: true, MaxBytes: 1024}}}}
	if _, err := svc.Broker.RegisterContract(alice, sc); err != nil {
		t.Fatal(err)
	}
	req, err := svc.Broker.CreateRequest(t.Context(), alice, v1.CreateRequest{ContractID: "session", ContractRevision: 1, ConnectionID: "connection-1", OnboardingID: "onboarding-1", IdempotencyKey: "request-key-1", OwnerKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := svc.Broker.OpenSession(req.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Submit(t.Context(), req.ID, session, svc.Broker.CSRF(session, req.ID), provider.Bundle{"token": []byte("synthetic-sweep-canary")}, nil); err != nil {
		t.Fatal(err)
	}
	ready, err := svc.Broker.GetRequest(alice, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := alice
	a.BindingID, a.WorkloadID = "state_binding", "state_workload"
	g, err := svc.Broker.CreateGrant(a, ready.CredentialID, v1.GrantRequest{ContractID: "session", ContractRevision: 1, PrincipalID: a.PrincipalID, ContextID: a.ContextID, RuntimeID: a.RuntimeID, BindingID: a.BindingID, WorkloadID: a.WorkloadID, Execution: "dedicated", IdempotencyKey: "grant__state_binding"})
	if err != nil {
		t.Fatal(err)
	}
	// Two checkpoints with different content retire version 1.
	for _, sess := range []string{`{"session":"one"}`, `{"session":"two"}`} {
		l, err := svc.Broker.Acquire(a, v1.AcquireLease{GrantID: g.ID})
		if err != nil {
			t.Fatal(err)
		}
		m, err := svc.Broker.Materialize(t.Context(), a, l.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(m.Mounts[1].Source, "runtime_session.json"), []byte(sess), 0600); err != nil {
			t.Fatal(err)
		}
		if err := svc.Broker.Release(t.Context(), a, l.ID, v1.RuntimeRelease{Checkpoint: true, Quiesced: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	n, err := SweepObjects(c)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("offline sweep drained %d objects, want >= 1", n)
	}
}
