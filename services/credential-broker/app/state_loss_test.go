package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWitnessDirectoryMigrationPreservesInitializationEvidence(t *testing.T) {
	c := devConfig(t)
	service, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	c.StateWitnessDir = filepath.Join(t.TempDir(), "migration")
	service, err = Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.StateWitnessDir, "state-initialized")); err != nil {
		t.Fatalf("healthy migration did not persist independent witness: %v", err)
	}
	if err := os.RemoveAll(c.DataDir); err != nil {
		t.Fatal(err)
	}
	c.StateWitnessDir = filepath.Join(t.TempDir(), "empty-target")
	reopened, err := Build(c)
	if err == nil {
		_ = reopened.Broker.Close()
	}
	if !errors.Is(err, ErrStateRecoveryRequired) {
		t.Fatalf("new witness directory bypassed surviving key-directory evidence: %v", err)
	}
}

func TestCorruptStateWitnessFailsWithoutModifyingJournal(t *testing.T) {
	c := devConfig(t)
	service, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(c.DataDir, "ledger.bin")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	witnessPath := filepath.Join(filepath.Dir(c.MasterKeyFile), "state-initialized")
	if err := os.WriteFile(witnessPath, []byte("invalid witness"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Build(c)
	if err == nil {
		_ = reopened.Broker.Close()
	}
	if !errors.Is(err, ErrStateRecoveryRequired) {
		t.Fatalf("corrupt witness accepted: %v", err)
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed startup changed journal")
	}
	witness, err := os.ReadFile(witnessPath)
	if err != nil || string(witness) != "invalid witness" {
		t.Fatal("failed startup silently replaced witness")
	}
}

func TestIndependentStateWitnessSurvivesPrimaryInstallationReplacement(t *testing.T) {
	c := devConfig(t)
	root := filepath.Dir(filepath.Dir(c.MasterKeyFile))
	witness := filepath.Join(t.TempDir(), "independent-witness")
	configure := func() Config {
		t.Helper()
		path := filepath.Join(root, "config.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		config["state_witness_dir"] = witness
		path = filepath.Join(root, "witness-config.json")
		if err := WriteJSON(path, config); err != nil {
			t.Fatal(err)
		}
		configured, err := ReadConfig(path)
		if err != nil {
			t.Fatalf("independent state witness configuration rejected: %v", err)
		}
		return configured
	}
	service, err := Build(configure())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(witness, "state-initialized")); err != nil {
		t.Fatalf("independent witness was not persisted: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	reopened, err := Build(configure())
	if err == nil {
		_ = reopened.Broker.Close()
	}
	if !errors.Is(err, ErrStateRecoveryRequired) {
		t.Fatalf("replacement installation silently bootstrapped: %v", err)
	}
}

func TestBuildRejectsLostPreviouslyInitializedJournal(t *testing.T) {
	c := devConfig(t)
	service, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Broker.Close(); err != nil {
		t.Fatal(err)
	}
	// Only synthetic journal state is removed; the separately held key survives.
	if err := os.RemoveAll(c.DataDir); err != nil {
		t.Fatal(err)
	}
	reopened, err := Build(c)
	if err == nil {
		_ = reopened.Broker.Close()
		t.Fatal("lost state silently accepted as a fresh installation")
	}
}
