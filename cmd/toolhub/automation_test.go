package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

type automationProjectionFixture struct{ principals []string }

func (f automationProjectionFixture) EnrolledPrincipals() []string { return f.principals }
func (f automationProjectionFixture) EnrolledAuth(principal string) (identity.Envelope, error) {
	return identity.TelegramEnvelope(principal, 11, principal, "policy-1"), nil
}
func (f automationProjectionFixture) CallReadTool(context.Context, string, string, map[string]any) ([]byte, error) {
	return nil, errors.New("no projected tool in fixture")
}

func TestAutomationStartupPreflightBeforeOwnerWrites(t *testing.T) {
	root := t.TempDir()
	owners := filepath.Join(root, "owners")
	legacy := filepath.Join(root, "legacy")
	alice := filepath.Join(owners, "alice")
	for _, dir := range []string{alice, filepath.Join(legacy, "schedules")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HUB_AUTOMATION_ROOT", filepath.Join(root, "host"))
	t.Setenv("HUB_AUTOMATION_OWNERS", owners)
	t.Setenv("HUB_AUTOMATION_LEGACY_SPOOL", legacy)
	routing := filepath.Join(root, "routing.json")
	if err := os.WriteFile(routing, []byte(`{"organization_id":"personal","users":[{"id":"alice","enabled":true},{"id":"bob","enabled":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_AUTOMATION_ROUTING", routing)
	t.Setenv("HUB_AUTOMATION_ENVIRONMENT", "dev")
	t.Setenv("HUB_AUTOMATION_JOB_URL", "http://127.0.0.1:1")
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", "http://127.0.0.1:1")
	t.Setenv("HUB_COMMUNICATION_AUTH", "control")
	projection := automationProjectionFixture{principals: []string{"alice", "bob"}}
	store := toolhub.NewStore()
	if _, err := newAutomationHost(context.Background(), projection, store, nil); err == nil {
		t.Fatal("missing enrolled owner mount passed preflight")
	}
	if _, err := os.Stat(filepath.Join(alice, "schedules")); !os.IsNotExist(err) {
		t.Fatalf("startup wrote before validating every owner: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(owners, "bob"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyRecord := filepath.Join(legacy, "schedules", "existing.json")
	if err := os.WriteFile(legacyRecord, []byte(`{"existing":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newAutomationHost(context.Background(), projection, store, nil); err == nil {
		t.Fatal("shared legacy automation record passed preflight")
	}
	if _, err := os.Stat(filepath.Join(alice, "schedules")); !os.IsNotExist(err) {
		t.Fatalf("legacy rejection wrote owner classes: %v", err)
	}
	if err := os.Remove(legacyRecord); err != nil {
		t.Fatal(err)
	}
	host, err := newAutomationHost(context.Background(), projection, store, nil)
	if err != nil {
		t.Fatalf("empty legacy spool and complete owners refused: %v", err)
	}
	defer host.Close()
	if info, err := os.Stat(filepath.Join(alice, "schedules")); err != nil || !info.IsDir() {
		t.Fatalf("accepted owner did not open its records: %v", err)
	}
}

func TestStaticAutomationRefusesMultipleOwners(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HUB_AUTOMATION_JOB_URL", "http://127.0.0.1:1")
	t.Setenv("HUB_AUTOMATION_JOB_PATH", "/v1/execute")
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", "http://127.0.0.1:1")
	t.Setenv("HUB_RUNTIME_AUTH", "control")
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	t.Setenv("HUB_AUTOMATION_ROOT", filepath.Join(root, "host"))
	projection := automationProjectionFixture{principals: []string{"alice", "bob"}}
	if _, err := newAutomationHost(context.Background(), projection, toolhub.NewStore(), nil); err == nil {
		t.Fatal("static runtime accepted sibling automation jobs on one runtime URL")
	}
	if _, err := os.Stat(filepath.Join(root, "host")); !os.IsNotExist(err) {
		t.Fatalf("static topology rejection created state: %v", err)
	}
}
