package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPreparedCatalogAndGeneratedRecipes(t *testing.T) {
	entries, err := PreparedCatalog()
	if err != nil || len(entries) != 4 {
		t.Fatalf("catalog: %d %v", len(entries), err)
	}
	for _, entry := range entries {
		t.Run(entry.ID, func(t *testing.T) {
			files := map[string]string{"package.json": `{"bin":{"first":"build/index.js","alias":"./build/index.js"}}`, "package-lock.json": "{}"}
			if entry.ID == "notion" {
				files["package.json"] = `{"bin":{"notion":"bin/cli.mjs"}}`
			}
			if entry.Language == "go" {
				files = map[string]string{"go.mod": "module github.com/github/github-mcp-server\n\ngo 1.25\n", "cmd/github-mcp-server/main.go": "package main\nfunc main(){}", "cmd/helper/main.go": "package main\nfunc main(){}"}
			}
			for _, directive := range []string{"RUN --mount=type=cache,target=/tmp/cache echo unused", "RUN --mount=type=secret,id=key echo unused", "RUN --network=host echo unused", "ADD https://untrusted.invalid/code /app"} {
				files["Dockerfile"] = "FROM untrusted:latest\n" + directive + "\nENTRYPOINT [\"/server/github-mcp-server\"]\nCMD [\"stdio\"]\n"
				context := languageContext(t, files)
				if _, err := (RecipeResolver{}).Resolve(t.Context(), entry.Source, context); err != nil {
					t.Fatal(err)
				}
				recipe, generated, err := GenerateArtifactRecipe(context, "", "", nil)
				if err != nil || !slices.Equal(recipe.Entrypoint, entry.Entrypoint) {
					t.Fatalf("recipe %v %v", recipe.Entrypoint, err)
				}
				built, err := readRecipeContext(generated, 64<<20)
				if err != nil || strings.Contains(string(built[recipe.Dockerfile]), "untrusted") {
					t.Fatal("upstream Dockerfile became authority", err)
				}
			}
			if _, matched, err := preparedForSource(entry.Source); err != nil || !matched {
				t.Fatal("missing exact overlay", err)
			}
			other := entry.Source
			other.CommitSHA = strings.Repeat("0", 40)
			if _, matched, _ := preparedForSource(other); matched {
				t.Fatal("overlay accepted unreviewed revision")
			}
		})
	}
	for _, raw := range []string{"null trailing", `[{"unknown":true}]`, string(preparedCatalogJSON) + " {}"} {
		if _, err := parsePreparedCatalog([]byte(raw)); err == nil {
			t.Fatal("accepted malformed catalog")
		}
	}
	duplicate := append(entries, entries[0])
	encoded, _ := json.Marshal(duplicate)
	if _, err := parsePreparedCatalog(encoded); err == nil {
		t.Fatal("accepted duplicate")
	}
}

func TestDiscoveryAlternativesAndSelectionUseGenericLifecycle(t *testing.T) {
	entries, _ := PreparedCatalog()
	source := entries[0].Source
	c := &ControlPlane{Store: NewStore(), Now: time.Now, RecipeCatalogs: []RecipeCatalog{
		testRecipeCatalogFunc(func(context.Context, RecipeLookup) ([]RecipeCandidate, error) {
			return nil, errors.New("registry unavailable")
		}),
		testRecipeCatalogFunc(func(context.Context, RecipeLookup) ([]RecipeCandidate, error) {
			var out []RecipeCandidate
			for i := 0; i < 7; i++ {
				out = append(out, RecipeCandidate{Repository: source.Repository, CommitSHA: source.CommitSHA, Launch: LaunchRecipe{Artifact: fmt.Sprintf("registry.example/mcp-%d", i), Digest: "sha256:" + strings.Repeat("a", 64), Entrypoint: []string{"mcp"}, Transport: ContainerMCP}})
			}
			return append(out, out[0], RecipeCandidate{Repository: "https://github.com/other/repo"}), nil
		}),
	}}
	body, err := c.Invoke(t.Context(), aliceAuth(), "discover", map[string]any{"query": "notion"})
	if err != nil {
		t.Fatal(err)
	}
	choices := body["candidates"].([]DiscoveryCandidate)
	if len(choices) != 5 || choices[0].PreparedID != "notion" || choices[1].Status != "requires-preflight" {
		t.Fatalf("bounded preference lost: %+v", choices)
	}
	if len(c.Store.onboardings) != 0 || len(c.Store.bindings) != 0 || len(c.Store.connections) != 0 {
		t.Fatal("discovery mutated install state")
	}
	if err := c.Store.PutGrant(OperatorGrant(GrantSelfInstall, aliceAuth().PrincipalID, "", "")); err != nil {
		t.Fatal(err)
	}
	resolved := false
	c.SourceResolver = func(_ context.Context, raw string) (ArtifactSource, error) {
		resolved = raw == source.Repository
		return source, nil
	}
	c.Reviewer = func(context.Context, ArtifactSource) (SourceReview, error) {
		return SourceReview{}, errors.New("test stops at generic reviewer")
	}
	if _, err := c.Invoke(t.Context(), aliceAuth(), "prepare_source", map[string]any{"candidate_id": choices[0].ID}); err == nil || !resolved {
		t.Fatal("selection bypassed generic resolver/reviewer", err)
	}
	if _, err := c.prepareSource(t.Context(), aliceAuth(), map[string]any{"candidate_id": choices[0].ID, "source": source.Repository}); !errors.Is(err, ErrInvalid) {
		t.Fatal("ambiguous selector accepted")
	}
	catalogs := c.RecipeCatalogs
	c.RecipeCatalogs = nil
	resolved = false
	if _, err := c.Invoke(t.Context(), aliceAuth(), "prepare_source", map[string]any{"source": source.Repository, "request_key": "direct-without-registries"}); err == nil || !resolved {
		t.Fatal("direct URL did not reach generic reviewer with all registries disabled", err)
	}
	c.RecipeCatalogs = catalogs[:1]
	if body, err := c.discover(t.Context(), aliceAuth(), "notion"); err != nil || len(body["warnings"].([]string)) != 1 {
		t.Fatal("unavailable registry hid bundle", err)
	}
	for len(c.discoveryChoices) < 512 {
		c.discoveryChoices[fmt.Sprint(len(c.discoveryChoices))] = discoverySelection{Expires: time.Now().Add(time.Hour)}
	}
	if _, err := c.discover(t.Context(), aliceAuth(), "notion"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded selection cache", err)
	}
}

func TestPreparedOverlayOverridesStaleAlias(t *testing.T) {
	entries, _ := PreparedCatalog()
	for _, entry := range entries {
		if entry.ID != "gitlab" {
			continue
		}
		connection := entry.overlay(ConnectionRecipe{Fields: []ConnectionField{{Name: "GITLAB_TOKEN", Required: true, Secret: true, Type: "token", Delivery: "env"}}})
		for _, field := range connection.Fields {
			if field.Name == "GITLAB_TOKEN" && field.Required {
				t.Fatal("stale alias still required")
			}
			if field.Name == "GITLAB_API_URL" && field.Secret {
				t.Fatal("API URL marked secret")
			}
		}
	}
}

func TestDiscoverySelectionIsBoundedReadOnlyAndOwnerScoped(t *testing.T) {
	now := time.Now()
	c := &ControlPlane{Now: func() time.Time { return now }}
	body, err := c.discover(t.Context(), aliceAuth(), "notion")
	if err != nil {
		t.Fatal(err)
	}
	choices := body["candidates"].([]DiscoveryCandidate)
	if len(choices) != 1 || choices[0].PreparedID != "notion" || choices[0].ID == "" {
		t.Fatalf("choices %+v", choices)
	}
	if repository, err := c.selectedRepository(aliceAuth(), choices[0].ID); err != nil || repository != choices[0].Source.Repository {
		t.Fatal(repository, err)
	}
	other := aliceAuth()
	other.PrincipalID = "bob"
	if _, err := c.selectedRepository(other, choices[0].ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("cross-owner selection", err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := c.selectedRepository(aliceAuth(), choices[0].ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired selection", err)
	}
	if _, err := c.discover(t.Context(), aliceAuth(), ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := c.discover(t.Context(), aliceAuth(), "no-such-mcp"); err != nil {
		t.Fatal(err)
	}
}
