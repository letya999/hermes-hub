package api

import (
	"context"
	"testing"
)

// ConnectionRecipe.Validate rejects malformed fields, unsupported deliveries,
// newline targets and bad alternatives.
func TestConnectionRecipeValidate(t *testing.T) {
	t.Parallel()
	ok := ConnectionRecipe{Fields: []ConnectionField{{Name: "FOO_TOKEN", Delivery: "env", Target: "FOO_TOKEN"}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid recipe: %v", err)
	}
	bad := []ConnectionRecipe{
		{Fields: []ConnectionField{{Name: "not a name"}}},
		{Fields: []ConnectionField{{Name: "FOO_TOKEN"}, {Name: "FOO_TOKEN"}}},
		{Fields: []ConnectionField{{Name: "FOO_TOKEN", Delivery: "carrier-pigeon"}}},
		{Fields: []ConnectionField{{Name: "FOO_TOKEN", Delivery: "file", Target: "a\nb"}}},
		{Alternatives: []ConnectionAlternative{{Name: ""}}},
		{Alternatives: []ConnectionAlternative{{Name: "a\nb"}}},
		{Alternatives: []ConnectionAlternative{{Name: "alt", URL: "http://insecure.example"}}},
		{Alternatives: []ConnectionAlternative{{Name: "alt", URL: "https://"}}},
		{Alternatives: []ConnectionAlternative{{Name: "alt", Fields: []ConnectionField{{Name: "!!"}}}}},
	}
	for i, recipe := range bad {
		if err := recipe.Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	// GitHub search is a registry hint only; it never fabricates candidates.
	candidates, err := (GitHubSearchCatalog{}).Lookup(context.Background(), RecipeLookup{})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("github search lookup must be empty: %v %v", candidates, err)
	}
}
