package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/credential-broker/app"
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
	for _, args := range [][]string{nil, {"unknown"}, {"init"}, {"init", "--unknown"}, {"serve"}, {"serve", "--unknown"}, {"migrate-recovery", "--unknown"}, {"check-config", "--config", "relative"}} {
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
