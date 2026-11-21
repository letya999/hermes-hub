//go:build integration

package devcheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestM32GroupFixtureUsesDedicatedGroupHome(t *testing.T) {
	f, err := NewM32GroupFixture(t.TempDir(), "prod", "http://127.0.0.1:18888/v1", "group")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.StopSpawn)
	for i, user := range f.Users {
		dir := filepath.Join(f.Root, "spaces", user)
		revision, err := SpawnEffectiveRevision(context.Background(), f.Spawn, spawnv1.Tenant{Org: "local", Owner: user}, user)
		if err != nil {
			t.Fatal(err)
		}
		eff, err := revision.Effective()
		if err != nil {
			t.Fatal(err)
		}
		want := "user"
		if i == 1 {
			want = "group"
		}
		if eff.Stack.Identity.Kind != want || eff.Stack.Identity.User != user || eff.Config.Memory.Enabled == nil || !*eff.Config.Memory.Enabled || len(eff.Tools.Core) == 0 {
			t.Fatal("group run fixture disabled memory instead of preserving its exact-run boundary")
		}
		entries, err := os.ReadDir(filepath.Join(dir, "passports"))
		if err != nil || len(entries) != 6 {
			t.Fatal("group fixture introduced extra authored inputs")
		}
	}
}

func TestM32FixtureUsesSixAuthoredPassports(t *testing.T) {
	f, err := NewM32Fixture(t.TempDir(), "prod", "http://host.docker.internal:18888/v1", "six")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.StopSpawn)
	store, err := toolhub.Load(f.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(filepath.Dir(f.StorePath), "governance.json"))
	store.RightsDoc, store.RightsLedger = docs.Rights, gov.Load
	for _, user := range f.Users {
		dir := filepath.Join(f.Root, "spaces", user)
		entries, err := os.ReadDir(filepath.Join(dir, "passports"))
		if err != nil || len(entries) != 6 {
			t.Fatalf("authored inputs: count=%d err=%v", len(entries), err)
		}
		effective, err := SpawnEffectiveRevision(context.Background(), f.Spawn, spawnv1.Tenant{Org: "local", Owner: user}, user)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := store.ListProjectedTools(f.Auth[user])
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, tool := range projected {
			names[tool.Name] = true
		}
		for _, name := range []string{"memory_search", "memory_card", "routine_create", "assignment_create", "watch_create"} {
			if !names[name] {
				t.Fatalf("authored selection not projected: %s", name)
			}
		}
		// These files must be inert even during re-render, not additional inputs.
		for name, body := range map[string]string{
			"agent.yaml":             "memory_vault: false\nmodel: poisoned\n",
			"settings.yaml":          "memory: {enabled: false}\n",
			"models.yaml":            "default: poisoned\n",
			"passport-src/user.yaml": "not: a-passport\n",
			"managed/prod/runtime/self-settings.json": `{"compression.enabled":false}`,
		} {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := stack.RenderEnvironment(context.Background(), f.Spawn, dir, f.Root, "prod"); err != nil {
			t.Fatal(err)
		}
		settings, eff, err := stack.ReadEffectiveSpaceBundle(context.Background(), f.Spawn, dir)
		if err != nil || settings.PassportRevision != effective.ID || settings.Model != "fixture" || !settings.Has("memory_vault") {
			t.Fatalf("retired configuration influenced effective state: %v", err)
		}
		registry, err := stack.LoadModelRegistryFor(dir, eff)
		if err != nil || !registry.Providers["fixture"].AllowPrivate {
			t.Fatalf("private fixture endpoint requires an output override: %v", err)
		}
	}
	if f.Auth[f.Users[0]].Equal(f.Auth[f.Users[1]]) {
		t.Fatal("fixture users share authority")
	}
}

func TestM32FixtureRejectsUncontainedIdentity(t *testing.T) {
	if _, err := NewM32Fixture(t.TempDir(), "prod", "http://host.docker.internal:18888/v1", "/../other"); err == nil {
		t.Fatal("unsafe fixture identity accepted")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "existing-owner-data")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewM32Fixture(root, "prod", "http://host.docker.internal:18888/v1", "safe"); err == nil {
		t.Fatal("nonempty directory accepted as an isolated fixture")
	}
	data, err := os.ReadFile(marker)
	entries, readErr := os.ReadDir(root)
	if err != nil || readErr != nil || string(data) != "preserve" || len(entries) != 1 {
		t.Fatal("fixture touched an existing directory")
	}
}
