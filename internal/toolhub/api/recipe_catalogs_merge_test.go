package api

import (
	"errors"
	"testing"
)

func TestRecipeCatalogsFromEnv(t *testing.T) {
	t.Setenv("HUB_RECIPE_CATALOGS", "")
	if catalogs, err := RecipeCatalogsFromEnv(); err != nil || len(catalogs) != 0 {
		t.Fatalf("empty env: %v %v", catalogs, err)
	}
	t.Setenv("HUB_RECIPE_CATALOGS", "mcp-registry, mcp-registry, docker-mcp")
	catalogs, err := RecipeCatalogsFromEnv()
	if err != nil || len(catalogs) != 2 {
		t.Fatalf("dedupe: %v %v", catalogs, err)
	}
	t.Setenv("HUB_RECIPE_CATALOGS", "bogus")
	if _, err := RecipeCatalogsFromEnv(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown: %v", err)
	}
	// smithery is opt-in and fails closed without a token env
	t.Setenv("HUB_RECIPE_CATALOGS", "smithery")
	t.Setenv("SMITHERY_API_KEY", "")
	if _, err := RecipeCatalogsFromEnv(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("smithery no token: %v", err)
	}
	t.Setenv("SMITHERY_API_KEY", "sk-test")
	if catalogs, err := RecipeCatalogsFromEnv(); err != nil || len(catalogs) != 1 {
		t.Fatalf("smithery: %v %v", catalogs, err)
	}
	// token env names must match the credential pattern
	t.Setenv("HUB_RECIPE_CATALOGS", "ghcr")
	t.Setenv("HUB_GHCR_TOKEN_ENV", "not a name!")
	if _, err := RecipeCatalogsFromEnv(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ghcr env name: %v", err)
	}
	t.Setenv("HUB_GHCR_TOKEN_ENV", "GHCR_TOKEN")
	t.Setenv("GHCR_TOKEN", "tok")
	if catalogs, err := RecipeCatalogsFromEnv(); err != nil || len(catalogs) != 1 {
		t.Fatalf("ghcr: %v %v", catalogs, err)
	}
}
