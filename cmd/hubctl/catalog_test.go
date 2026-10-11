package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestCatalogEnableHostCommand(t *testing.T) {
	p := filepath.Join(t.TempDir(), "store.json")
	if err := runConnector(context.Background(), []string{"catalog-cli", "--toolhub-store", p}); err != nil {
		t.Fatal(err)
	}
	args := []string{"catalog-enable", "--toolhub-store", p, "--definition", "cli-rg-search", "--version", "1.0.2", "--principal", "alice", "--policy-version", "policy-1"}
	for i := 0; i < 2; i++ {
		if err := runConnector(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	s, err := toolhub.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "alice", "policy-1")
	tools, err := s.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].DefinitionID != "cli-rg-search" {
		t.Fatalf("host command failed to bind catalog: %+v", tools)
	}
	for _, bad := range [][]string{nil, {"--toolhub-store", "relative", "--definition", "cli-rg-search", "--version", "1.0.2"}, {"--toolhub-store", p}, {"--bad"}, append(append([]string{}, args[1:]...), "extra"), {"--toolhub-store", filepath.Join(t.TempDir(), "missing"), "--definition", "x", "--version", "1.0.0"}} {
		if err := runCatalog(bad); err == nil {
			t.Fatalf("invalid catalog command accepted: %v", bad)
		}
	}
}
