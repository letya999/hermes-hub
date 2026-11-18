package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// catalog-cli registers the compiled CLI catalog into a fresh store and
// stays idempotent against an existing one.
func TestConnectorCatalogCLI(t *testing.T) {
	ctx := context.Background()
	// The JSON backend releases its file handle between verbs; a .db path
	// keeps the SQLite handle open for the life of the test process.
	store := filepath.Join(t.TempDir(), "registry.json")
	if err := runConnector(ctx, []string{"catalog-cli", "--toolhub-store", store}); err != nil {
		t.Fatalf("fresh catalog-cli: %v", err)
	}
	if info, err := os.Stat(store); err != nil || info.Size() == 0 {
		t.Fatalf("store not written: %v", err)
	}
	// Existing store: definitions already registered → no-op OK.
	if err := runConnector(ctx, []string{"catalog-cli", "--toolhub-store", store}); err != nil {
		t.Fatalf("idempotent catalog-cli: %v", err)
	}
	// A relative store path is rejected.
	if err := runConnector(ctx, []string{"catalog-cli", "--toolhub-store", "relative.db"}); err == nil {
		t.Fatal("relative store path accepted")
	}
}

// The controller verbs parse flags and decode the config file before ever
// serving — every rejection branch is part of the admission contract.
func TestConnectorControllerValidation(t *testing.T) {
	ctx := context.Background()
	for _, verb := range []string{"local-controller", "generic-controller"} {
		if err := runConnector(ctx, []string{verb}); err == nil {
			t.Fatalf("%s without config accepted", verb)
		}
		if err := runConnector(ctx, []string{verb, "--config", "relative.json"}); err == nil {
			t.Fatalf("%s relative config accepted", verb)
		}
		missing := filepath.Join(t.TempDir(), "missing.json")
		if err := runConnector(ctx, []string{verb, "--config", missing}); err == nil {
			t.Fatalf("%s missing config accepted", verb)
		}
		bad := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(bad, []byte("{not json"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runConnector(ctx, []string{verb, "--config", bad}); err == nil {
			t.Fatalf("%s bad config accepted", verb)
		}
		trailing := filepath.Join(t.TempDir(), "trailing.json")
		if err := os.WriteFile(trailing, []byte("{} {}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runConnector(ctx, []string{verb, "--config", trailing}); err == nil {
			t.Fatalf("%s trailing config accepted", verb)
		}
	}
	// A well-formed config reaches the serve path; cancellation is the
	// boundary, not a decode failure — the listener may still start, so only
	// assert the call returns (not a specific error).
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	cfg := filepath.Join(t.TempDir(), "ctl.json")
	if err := os.WriteFile(cfg, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	_ = runConnector(ctx, []string{"generic-controller", "--config", cfg})
}

// Unknown and empty invocations keep the dispatch contract.
func TestConnectorDispatchDefaults(t *testing.T) {
	err := runConnector(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "connector") {
		t.Fatalf("empty args: %v", err)
	}
}
