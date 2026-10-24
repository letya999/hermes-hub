//go:build manual

package api

import (
	"context"
	"encoding/json"
	"testing"
)

// Manual live check for issue #282 acceptance: real registries + real repo.
// Run: HUB_RECIPE_CATALOGS=mcp-registry,toolhive,docker-mcp,docker-hub,ghcr go test -tags manual -run TestLiveResolveGithubMCP -v ./internal/toolhub/api
func TestLiveResolveGithubMCP(t *testing.T) {
	ctx := context.Background()
	source, err := ResolveGitHubSource(ctx, "https://github.com/github/github-mcp-server")
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	t.Logf("pinned commit: %s", source.CommitSHA)
	if pin, ok, _ := PreparedForRepository(source.Repository, source.Subfolder); ok {
		source = pin.Source
		t.Logf("prepared pin selected: %s", source.CommitSHA)
	}
	catalogs, err := RecipeCatalogsFromEnv()
	if err != nil {
		t.Fatalf("catalogs: %v", err)
	}
	t.Logf("catalogs configured: %d", len(catalogs))
	contextBytes, err := FetchRepositoryRecipeContext(ctx, source, 64<<20)
	if err != nil {
		t.Fatalf("fetch context: %v", err)
	}
	resolution, err := (RecipeResolver{Catalogs: catalogs}).Resolve(ctx, source, contextBytes)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	t.Logf("state=%s trust=%s", resolution.State, resolution.Trust)
	t.Logf("launch: %+v", resolution.Launch)
	t.Logf("ledger entries: %d", len(resolution.Ledger))
	for _, entry := range resolution.Ledger {
		claim, _ := json.Marshal(entry.Claim)
		t.Logf("  rank=%s source=%s ref=%s ignored=%v verified=%s claim=%s", entry.Rank, entry.Source, entry.Ref, entry.Ignored, entry.Verified, string(claim))
	}
	for _, field := range resolution.Connection.Fields {
		t.Logf("  field %s required=%v secret=%v delivery=%s target=%s source=%s rank=%s", field.Name, field.Required, field.Secret, field.Delivery, field.Target, field.Source, field.Rank)
	}
	for _, w := range resolution.Warnings {
		t.Logf("  warning: %s", w)
	}
}
