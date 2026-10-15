package api

import (
	"context"
	"errors"
	"testing"
)

type stubSearcher struct {
	found []DiscoveryCandidate
	err   error
}

func (s stubSearcher) Lookup(context.Context, RecipeLookup) ([]RecipeCandidate, error) {
	return nil, nil
}
func (s stubSearcher) SearchSources(context.Context, string) ([]DiscoveryCandidate, error) {
	return s.found, s.err
}

func TestSearchCatalogSourcesAndAppend(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	low := DiscoveryCandidate{Name: "low", Source: ArtifactSource{Repository: "https://github.com/a/low", CommitSHA: sha},
		Evidence: []RecipeEvidence{{Source: "smithery"}}}
	high := DiscoveryCandidate{Name: "high", Source: ArtifactSource{Repository: "https://github.com/a/high", CommitSHA: sha},
		Evidence: []RecipeEvidence{{Source: "mcp-registry"}}}

	found, warnings := SearchCatalogSources(context.Background(), []RecipeCatalog{
		stubSearcher{found: []DiscoveryCandidate{low, high}},
		stubSearcher{err: errors.New("offline")},
		stubCatalog{}, // not a SourceSearchCatalog — skipped
	}, "query", false)
	if len(found) != 2 || found[0].Name != "high" || len(warnings) != 1 {
		t.Fatalf("search: %v %v", found, warnings)
	}
	// fallback mode selects only GitHubSearchCatalog implementations
	found, _ = SearchCatalogSources(context.Background(), []RecipeCatalog{stubSearcher{found: []DiscoveryCandidate{high}}}, "q", true)
	if len(found) != 0 {
		t.Fatalf("non-fallback catalog searched in fallback mode")
	}

	// AppendDiscovered: dedupe by repo+subfolder, bound at 5, drop unsafe entries
	existing := []DiscoveryCandidate{{Name: "e", Source: ArtifactSource{Repository: "https://github.com/a/low", CommitSHA: sha}}}
	in := []DiscoveryCandidate{
		{Name: "merge", Source: ArtifactSource{Repository: "https://github.com/A/low", CommitSHA: sha}, Evidence: []RecipeEvidence{{Source: "x"}}},
		{Name: "", Source: ArtifactSource{Repository: "https://github.com/a/noname", CommitSHA: sha}},
		{Name: "badsha", Source: ArtifactSource{Repository: "https://github.com/a/bad", CommitSHA: "xyz"}},
		{Name: "ok1", Source: ArtifactSource{Repository: "https://github.com/a/ok1", CommitSHA: sha}},
	}
	out := AppendDiscovered(existing, in)
	if len(out) != 2 || out[1].Name != "ok1" || len(out[0].Evidence) != 1 {
		t.Fatalf("append: %+v", out)
	}
	// sha-less candidates need a parseable GitHub repository URL
	out = AppendDiscovered(nil, []DiscoveryCandidate{
		{Name: "nope", Source: ArtifactSource{Repository: "https://notgithub.example/x/y"}},
		{Name: "yes", Source: ArtifactSource{Repository: "https://github.com/a/b", Subfolder: "dir"}},
	})
	if len(out) != 1 || out[0].Name != "yes" {
		t.Fatalf("sha-less append: %+v", out)
	}
	// SourceURL rendering
	c := DiscoveryCandidate{Source: ArtifactSource{Repository: "https://github.com/a/b", CommitSHA: sha, Subfolder: "sub"}}
	if c.SourceURL() != "https://github.com/a/b/tree/"+sha+"/sub" {
		t.Fatalf("sourceURL: %s", c.SourceURL())
	}
	c.Source.Subfolder = ""
	if c.SourceURL() != "https://github.com/a/b/commit/"+sha {
		t.Fatalf("sourceURL: %s", c.SourceURL())
	}
	c.Source.CommitSHA = ""
	if c.SourceURL() != "https://github.com/a/b" {
		t.Fatalf("sourceURL: %s", c.SourceURL())
	}
}
