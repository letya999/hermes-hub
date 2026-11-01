package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/app"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/internal/seal"
)

type failedWriter struct{}

func TestServeUsesExplicitIndependentStateWitnessDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "install")
	if err := app.Init(root); err != nil {
		t.Fatal(err)
	}
	cfg, err := app.ReadConfig(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = "127.0.0.1:0"
	cfg.StateWitnessDir = filepath.Join(root, "configured-witness")
	configPath := filepath.Join(root, "serve.json")
	if err := app.WriteJSON(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	witness := filepath.Join(t.TempDir(), "independent-witness")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"serve", "--config", configPath, "--state-witness-dir", witness}, io.Discard); err != nil {
		t.Fatalf("serve with independent witness: %v", err)
	}
	if _, err := os.Stat(filepath.Join(witness, "state-initialized")); err != nil {
		t.Fatalf("explicit witness directory unused: %v", err)
	}
	if _, err := os.Stat(cfg.StateWitnessDir); !os.IsNotExist(err) {
		t.Fatal("flag did not override config witness directory")
	}
}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("writer closed") }
func TestCommands(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	if e := run(ctx, []string{"version"}, &out); e != nil || out.String() != version+"\n" {
		t.Fatal(e, out.String())
	}
	if e := run(ctx, []string{"version"}, failedWriter{}); e == nil {
		t.Fatal("writer error")
	}
	for _, args := range [][]string{nil, {"unknown"}, {"init"}, {"init", "--unknown"}, {"serve"}, {"serve", "--unknown"}, {"migrate-recovery", "--unknown"}, {"migrate-head", "--unknown"}, {"migrate-head"}, {"check-config", "--config", "relative"}} {
		if e := run(ctx, args, io.Discard); e == nil {
			t.Fatal(args)
		}
	}
	root := filepath.Join(t.TempDir(), "install")
	if e := run(ctx, []string{"init", "--dir", root}, io.Discard); e != nil {
		t.Fatal(e)
	}
	if e := run(ctx, []string{"init", "--dir", root}, io.Discard); e == nil {
		t.Fatal("reinit")
	}
	path := filepath.Join(root, "config.json")
	if e := run(ctx, []string{"check-config", "--config", path}, io.Discard); e != nil {
		t.Fatal(e)
	}
	cfg, e := app.ReadConfig(path)
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	cfg.Listen = ln.Addr().String()
	cfg.PublicOrigin = "http://" + cfg.Listen
	path2 := filepath.Join(root, "serve.json")
	if e = app.WriteJSON(path2, cfg); e != nil {
		t.Fatal(e)
	}
	if e = run(ctx, []string{"serve", "--config", path2}, io.Discard); e == nil {
		t.Fatal("busy port")
	}
	_ = ln.Close()
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if e = run(stopped, []string{"serve", "--config", path2}, io.Discard); e != nil {
		t.Fatal(e)
	}
	_ = os.Remove(cfg.MasterKeyFile)
	if e = run(ctx, []string{"serve", "--config", path2}, io.Discard); e == nil {
		t.Fatal("missing master")
	}
}

func TestMigrateRecoveryCommand(t *testing.T) {
	root := filepath.Join(t.TempDir(), "install")
	if err := app.Init(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	cfg, err := app.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	cfg.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	cfg.RecoveryKeyFile = filepath.Join(root, "keys", "recovery.key")
	cfg.RecoveryIdentity = "installation-one/dev"
	if err := os.WriteFile(cfg.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	migrationConfig := filepath.Join(root, "migration.json")
	if err := app.WriteJSON(migrationConfig, cfg); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"migrate-recovery", "--config", migrationConfig}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Recovery migration complete.\n" {
		t.Fatal("migration output contains unexpected data")
	}
	for range 2 {
		if err := run(context.Background(), []string{"migrate-head", "--config", migrationConfig}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}

	if err := run(context.Background(), []string{"migrate-recovery"}, io.Discard); err == nil {
		t.Fatal("migration accepted missing config")
	}
	if err := run(context.Background(), []string{"migrate-recovery", "--config", migrationConfig}, failedWriter{}); err == nil {
		t.Fatal("migration ignored output failure")
	}
	svc, err = app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Broker.Close()
}

func TestStoreMigrateCommand(t *testing.T) {
	if _, err := journal.SQLCipherVersion(); err != nil {
		t.Skip("sqlcipher engine unavailable in this build")
	}
	root := filepath.Join(t.TempDir(), "install")
	if err := app.Init(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	cfg, err := app.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	cfg.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	cfg.RecoveryKeyFile = filepath.Join(root, "keys", "recovery.key")
	cfg.RecoveryIdentity = "installation-one/dev"
	if err := os.WriteFile(cfg.RecoveryKeyFile, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	migrationConfig := filepath.Join(root, "migration.json")
	if err := app.WriteJSON(migrationConfig, cfg); err != nil {
		t.Fatal(err)
	}
	// Seed the replicated recovery legs, then serve once on the full triad.
	if err := run(context.Background(), []string{"migrate-recovery", "--config", migrationConfig}, io.Discard); err != nil {
		t.Fatal(err)
	}
	svc, err = app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"store-migrate", "--config", migrationConfig}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Store migration staged.\n" {
		t.Fatal("unexpected output:", out.String())
	}
	out.Reset()
	if err := run(context.Background(), []string{"store-activate", "--config", migrationConfig}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Store activated.\n" {
		t.Fatal("unexpected output:", out.String())
	}
	if _, err := os.Lstat(filepath.Join(cfg.DataDir, "sqlcipher.activated")); err != nil {
		t.Fatal("activation marker missing")
	}
	if _, err := os.Lstat(filepath.Join(cfg.DataDir, "substrate-backup", "ledger.bin")); err != nil {
		t.Fatal("legacy ledger not retired")
	}
	out.Reset()
	if err := run(context.Background(), []string{"store-normalize", "--config", migrationConfig}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Store normalized.\n" {
		t.Fatal("unexpected output:", out.String())
	}
	master, err := os.ReadFile(cfg.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg.RecoveryKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.OpenReplicatedAnchored(cfg.DataDir, filepath.Join(cfg.RecoveryDir, "journal"), cfg.RecoveryAnchorDir,
		seal.Derive(master, "ledger"), seal.Derive(seal.Derive(raw, "identity/"+cfg.RecoveryIdentity), "ledger"), cfg.MaxLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Normalized() {
		_ = j.Close()
		t.Fatal("store-normalize did not arm the marker")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	// The service must come up on the SQLCipher backend.
	svc, err = app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Broker.Close()
}

func TestStoreMigrateSingleCommand(t *testing.T) {
	if _, err := journal.SQLCipherVersion(); err != nil {
		t.Skip("sqlcipher engine unavailable in this build")
	}
	root := filepath.Join(t.TempDir(), "install")
	if err := app.Init(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	cfg, err := app.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"store-migrate", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"store-activate", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(cfg.DataDir, "sqlcipher.activated")); err != nil {
		t.Fatal("activation marker missing")
	}
	svc, err = app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Broker.Close()
}

// store-retain exports the hot audit prefix into a verified archive and only
// prunes when the explicit --destroy flag is passed.
func TestStoreRetainCommand(t *testing.T) {
	if _, err := journal.SQLCipherVersion(); err != nil {
		t.Skip("sqlcipher engine unavailable in this build")
	}
	root := filepath.Join(t.TempDir(), "install")
	if err := app.Init(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	cfg, err := app.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.Actor{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime-alice", ExternalIdentityID: "tg-alice", ConversationID: "dm-alice", PolicyVersion: "policy-v1"}
	if _, err := svc.Broker.CreateRequest(context.Background(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-1", OnboardingID: "onboarding-1", IdempotencyKey: "request-key-1", OwnerKind: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"store-migrate", "--config", path}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"store-activate", "--config", path}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"store-normalize", "--config", path}, io.Discard); err != nil {
		t.Fatal(err)
	}
	cfg.AuditHotMaxBytes = 64 << 10
	cfg.AuditArchiveDir = filepath.Join(t.TempDir(), "arch")
	retentionConfig := filepath.Join(root, "retention.json")
	if err := app.WriteJSON(retentionConfig, cfg); err != nil {
		t.Fatal(err)
	}
	svc, err = app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Broker.CreateRequest(context.Background(), alice, v1.CreateRequest{ContractID: "github-pat", ContractRevision: 1, ConnectionID: "connection-2", OnboardingID: "onboarding-2", IdempotencyKey: "request-key-2", OwnerKind: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"store-retain", "--config", retentionConfig}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"pruned":false`) {
		t.Fatal("export-only pass pruned or malformed report:", out.String())
	}
	out.Reset()
	if err := run(context.Background(), []string{"store-retain", "--config", retentionConfig, "--destroy"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"pruned":true`) {
		t.Fatal("destructive pass did not prune or malformed report:", out.String())
	}
	master, err := os.ReadFile(cfg.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(cfg.DataDir, seal.Derive(master, "ledger"), cfg.MaxLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	floor, err := j.AuditFloor()
	_ = j.Close()
	if err != nil || floor < 2 {
		t.Fatalf("audit floor = %d %v, want >= 2 after prune", floor, err)
	}
}

// store-retain --records rotates the chained history prefix on a stopped
// store: sealed archive out, source rows pruned, file physically compacted.
func TestStoreRetainRecordsCommand(t *testing.T) {
	if _, err := journal.SQLCipherVersion(); err != nil {
		t.Skip("sqlcipher engine unavailable in this build")
	}
	root := filepath.Join(t.TempDir(), "install")
	if err := app.Init(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	cfg, err := app.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"store-migrate", "--config", path}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"store-activate", "--config", path}, io.Discard); err != nil {
		t.Fatal(err)
	}
	cfg.AuditArchiveDir = filepath.Join(t.TempDir(), "arch")
	cfg.AuditHotMaxBytes = 64 << 10
	retentionConfig := filepath.Join(root, "retention.json")
	if err := app.WriteJSON(retentionConfig, cfg); err != nil {
		t.Fatal(err)
	}
	master, err := os.ReadFile(cfg.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(cfg.DataDir, seal.Derive(master, "ledger"), cfg.MaxLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("x", 4000)
	for i := 0; i < 40; i++ {
		if _, err := j.AppendWith("mutation.v1", map[string]string{"pad": pad}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"store-retain", "--config", retentionConfig, "--records", "--keep-bytes", "32768", "--destroy"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"pruned":true`) {
		t.Fatal("records pass did not prune or malformed report:", out.String())
	}
	j, err = journal.Open(cfg.DataDir, seal.Derive(master, "ledger"), cfg.MaxLedgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	floor, err := j.RecordsFloor()
	if err != nil || floor < 2 {
		t.Fatalf("records floor = %d %v, want >= 2 after rotation", floor, err)
	}
}

// store-sweep drains the durable object queue on a stopped store — the
// offline twin of the service's in-process worker.
func TestStoreSweepCommand(t *testing.T) {
	root := filepath.Join(t.TempDir(), "install")
	if err := app.Init(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"store-sweep", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"swept"`) {
		t.Fatal("store-sweep report malformed:", out.String())
	}
}
