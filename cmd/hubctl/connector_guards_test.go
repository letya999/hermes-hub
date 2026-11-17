package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The connector facade validates its contract before any store or network
// touch: usage errors, provider vocabulary and absolute-path requirements.
func TestConnectorDispatchGuards(t *testing.T) {
	ctx := context.Background()

	if err := runConnector(ctx, nil); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("bare connector must print usage: %v", err)
	}
	if err := runConnector(ctx, []string{"status", "extra-arg"}); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("positional args must be rejected: %v", err)
	}
	if err := runConnector(ctx, []string{"status", "--provider", "mastodon"}); err == nil || !strings.Contains(err.Error(), "unsupported connector provider") {
		t.Fatalf("foreign provider must be rejected: %v", err)
	}
	// Controller subcommands demand an absolute private config path.
	for _, sub := range []string{"local-controller", "generic-controller"} {
		if err := runConnector(ctx, []string{sub}); err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("%s without config: %v", sub, err)
		}
		if err := runConnector(ctx, []string{sub, "--config", "relative.json"}); err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("%s relative config: %v", sub, err)
		}
	}
	// catalog-cli refuses a relative store path.
	if err := runConnector(ctx, []string{"catalog-cli", "--toolhub-store", "rel.db"}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("catalog-cli relative store: %v", err)
	}
}

// catalog-cli lands the compiled definitions into a fresh protected store.
func TestConnectorCatalogCLIRegistersDefinitions(t *testing.T) {
	ctx := context.Background()
	store := filepath.Join(t.TempDir(), "registry.json")
	if err := runConnector(ctx, []string{"catalog-cli", "--toolhub-store", store}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store)
	if err != nil || info.Size() == 0 {
		t.Fatal("catalog store not written")
	}
	// Second run converges: the definitions re-register without duplication.
	if err := runConnector(ctx, []string{"catalog-cli", "--toolhub-store", store}); err != nil {
		t.Fatalf("re-run must converge: %v", err)
	}
}

// local-controller decodes strict JSON and rejects malformed config before
// binding the loopback admission socket.
func TestConnectorLocalControllerConfigValidation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "controller.json")
	if err := os.WriteFile(cfg, []byte("{bad json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runConnector(ctx, []string{"local-controller", "--config", cfg}); err == nil || !strings.Contains(err.Error(), "invalid local controller config") {
		t.Fatalf("malformed config: %v", err)
	}
	if err := os.WriteFile(cfg, []byte(`{"unknown_field": 1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runConnector(ctx, []string{"local-controller", "--config", cfg}); err == nil || !strings.Contains(err.Error(), "invalid local controller config") {
		t.Fatalf("unknown field config: %v", err)
	}
}

// runProvision validates usage before any filesystem mutation: a missing
// user, a foreign environment or stray arguments are rejected outright.
func TestProvisionDispatchGuards(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	if err := runProvision(ctx, nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("bare provision must print usage: %v", err)
	}
	if err := runProvision(ctx, []string{"--user", "alice", "extra"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("stray args must be rejected: %v", err)
	}
	if err := runProvision(ctx, []string{"--user", "alice", "--env", "staging", "--dir", dir}); err == nil || !strings.Contains(err.Error(), "dev or prod") {
		t.Fatalf("foreign env must be rejected: %v", err)
	}
}

// A fresh dir without passports scaffolds the profile, then preflight
// aggregates what's still missing instead of failing on the first finding.
func TestProvisionScaffoldsThenAggregatesPreflight(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "tenant-new")
	root := t.TempDir()
	provisionProfile(t, root, "ceiling:\n  - capability_id: cap.fs\n    action: read\n")

	err := runProvision(ctx, []string{"--user", "tenant-new", "--dir", dir, "--root", root, "--profile", "test-profile"})
	if err == nil {
		t.Fatal("incomplete space must fail preflight or bundle build")
	}
	// Scaffolding ran before the failure surfaced.
	if _, statErr := os.Lstat(filepath.Join(dir, "passports")); statErr != nil {
		t.Fatalf("scaffold did not materialize passports: %v", statErr)
	}
}
