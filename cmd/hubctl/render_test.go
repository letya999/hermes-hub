package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestOperatorMigrationRejectsRenderedPolicyDrift(t *testing.T) {
	dir := t.TempDir()
	settings := stack.Settings{User: "alice", Environment: "dev"}
	store := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if err := toolhub.NewStore().Save(store); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := stack.ExpectedToolManifest(settings)
	manifest.PolicyVersion += "-stale"
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "tool-manifest.dev.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := migrateToolhubBindings(dir, settings); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("policy drift did not fence migration: %v", err)
	}
	after, err := os.ReadFile(store)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused migration changed ToolHub state")
	}
}

func TestOperatorRenderKeepsToolHubMigration(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	writeManagedSpaceFixture(t, dir, "alice")
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("synthetic persona"), 0600); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if err := os.MkdirAll(filepath.Dir(store), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("not a ToolHub store\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stack.RenderEnvironment(context.Background(), spacesRootReader{root: filepath.Dir(dir)}, dir, root, "dev"); err != nil {
		t.Fatalf("pure renderer failed: %v", err)
	}
	prev := renderClient
	renderClient = func(string) (spawnv1.EffectiveReader, error) { return spacesRootReader{root: filepath.Dir(dir)}, nil }
	t.Cleanup(func() { renderClient = prev })
	if err := renderEnvironment(context.Background(), dir, root, "dev"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("operator render skipped ToolHub migration: %v", err)
	}
}
