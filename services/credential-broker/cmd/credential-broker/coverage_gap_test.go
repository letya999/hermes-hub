package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/credential-broker/app"
)

// Every maintenance subcommand fails loudly on a bad flag, a missing --config
// and an unreadable config path — before any store file is touched.
func TestCommandRefusals(t *testing.T) {
	ctx := context.Background()
	for _, args := range [][]string{
		{"store-migrate", "--bogus"},
		{"store-migrate"},
		{"store-migrate", "--config", "missing.json"},
		{"store-activate", "--bogus"},
		{"store-activate"},
		{"store-activate", "--config", "missing.json"},
		{"store-normalize", "--bogus"},
		{"store-normalize"},
		{"store-normalize", "--config", "missing.json"},
		{"store-retain", "--bogus"},
		{"store-retain"},
		{"store-retain", "--config", "missing.json"},
		{"store-sweep", "--bogus"},
		{"store-sweep"},
		{"store-sweep", "--config", "missing.json"},
		{"migrate-recovery", "--config", "missing.json"},
		{"migrate-head", "--config", "missing.json"},
		{"init", "--dir"},
		{"serve", "--config", "missing.json"},
		{"check-config", "--config", "missing.json"},
	} {
		if err := run(ctx, args, io.Discard); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

// A valid config whose store prerequisites are gone still fails the command —
// the error reaches the operator, not a silent skip.
func TestCommandFailuresOnBrokenStore(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "install")
	if err := app.Init(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	cfg, err := app.ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.MasterKeyFile); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"store-migrate", "--config", path},
		{"store-activate", "--config", path},
		{"store-normalize", "--config", path},
		{"store-retain", "--config", path},
		{"store-retain", "--config", path, "--records", "--keep-bytes", "1024"},
		{"store-sweep", "--config", path},
		{"migrate-head", "--config", path},
	} {
		if err := run(ctx, args, io.Discard); err == nil {
			t.Fatalf("%v accepted a broken store", args)
		}
	}
	cfg.RecoveryDir = filepath.Join(t.TempDir(), "recovery")
	cfg.RecoveryAnchorDir = filepath.Join(t.TempDir(), "anchor")
	cfg.RecoveryKeyFile = filepath.Join(t.TempDir(), "recovery.key")
	cfg.RecoveryIdentity = "installation-one/dev"
	migrationPath := filepath.Join(root, "migration.json")
	if err := app.WriteJSON(migrationPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"migrate-recovery", "--config", migrationPath}, io.Discard); err == nil {
		t.Fatal("migrate-recovery accepted a missing recovery key")
	}
}

// BROKER_DIRECT_FORM is honored on the serve path — the development form
// flag reaches the built service config.
func TestServeHonorsDirectFormEnv(t *testing.T) {
	root := filepath.Join(t.TempDir(), "install")
	if err := app.Init(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	t.Setenv("BROKER_DIRECT_FORM", "1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"serve", "--config", path}, io.Discard); err != nil {
		t.Fatalf("serve with direct form: %v", err)
	}
}
