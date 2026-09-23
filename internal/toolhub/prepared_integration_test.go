//go:build integration

package toolhub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPreparedLiveBuilds(t *testing.T) {
	if os.Getenv("HUB_PREPARED_LIVE_BUILD") != "1" {
		t.Skip("explicit isolated build opt-in required")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".local", "prepared-acceptance"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	seccomp, _ := filepath.Abs(filepath.Join("..", "..", "docker", "seccomp-buildkit-rootless.json"))
	entries, err := PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run(entry.ID, func(t *testing.T) {
			review, err := DefaultSourceReviewer(filepath.Join(root, "artifacts"), seccomp)(t.Context(), entry.Source)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.MarshalIndent(review, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, entry.ID+"-review.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("isolated build and MCP preflight: %s %s tools=%d", entry.ID, review.Definition.Source.Digest, len(review.Definition.Tools))
		})
	}
}

// This is live source/recipe evidence, not provider or build success evidence.
func TestPreparedLiveSourceRecipes(t *testing.T) {
	entries, err := PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run(entry.ID, func(t *testing.T) {
			data, err := FetchRepositoryRecipeContext(t.Context(), entry.Source, 64<<20)
			if err != nil {
				t.Fatal(err)
			}
			resolution, err := (RecipeResolver{}).Resolve(t.Context(), entry.Source, data)
			if err != nil {
				t.Fatal(err)
			}
			resolution.Connection = entry.overlay(resolution.Connection)
			t.Logf("source %s@%s; fields %+v", entry.Source.Repository, entry.Source.CommitSHA, resolution.Connection.CredentialInputs())
			buildData, err := FetchRepositoryArtifactContext(t.Context(), entry.Source, 64<<20)
			if err != nil {
				t.Fatal(err)
			}
			recipe, generated, err := GenerateArtifactRecipe(buildData, "", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(recipe.Entrypoint, entry.Entrypoint) {
				t.Fatalf("entrypoint drift: %v", recipe.Entrypoint)
			}
			if err := recipe.VerifyContext(generated); err != nil {
				t.Fatal(err)
			}
			t.Logf("generated recipe %s, entrypoint %v, files %d", recipe.Dockerfile, recipe.Entrypoint, len(recipe.Files))
		})
	}
}
